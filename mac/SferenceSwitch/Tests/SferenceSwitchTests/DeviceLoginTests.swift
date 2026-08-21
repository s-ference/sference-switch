import Foundation
import ServiceManagement
import XCTest
@testable import SferenceSwitch

private actor StubAdminReader: AdminStatusReading {
    private(set) var statusCalls = 0

    func fetchStatus() async throws -> AdminStatusSnapshot {
        statusCalls += 1
        return AdminStatusSnapshot(dict: [:])
    }

    func fetchStats(windowSeconds: Int,
                    bucketSeconds: Int) async throws -> StatsSnapshot {
        StatsSnapshot(dict: [:])
    }
}

private actor FakeDeviceLoginReader: DeviceLoginReading {
    private(set) var startCalls = 0
    private(set) var statusCalls = 0
    private(set) var cancelCalls = 0
    private var startResult: Result<DeviceLoginSnapshot, Error>
    private var statusQueue: [DeviceLoginSnapshot]
    /// Once the scripted queue drains the gateway keeps answering with
    /// the flow's current snapshot — repeat the last one rather than
    /// inventing an empty pending that would wipe the user code.
    private var lastStatus: DeviceLoginSnapshot?

    init(start: DeviceLoginSnapshot,
         statuses: [DeviceLoginSnapshot] = []) {
        startResult = .success(start)
        statusQueue = statuses
    }

    func failStart(with error: Error) {
        startResult = .failure(error)
    }

    func startDeviceLogin() async throws -> DeviceLoginSnapshot {
        startCalls += 1
        return try startResult.get()
    }

    func fetchDeviceLoginStatus() async throws -> DeviceLoginSnapshot {
        statusCalls += 1
        if statusQueue.isEmpty {
            return lastStatus ?? DeviceLoginSnapshot(dict: ["state": "pending"])
        }
        let next = statusQueue.removeFirst()
        lastStatus = next
        return next
    }

    func cancelDeviceLogin() async throws -> DeviceLoginSnapshot {
        cancelCalls += 1
        return DeviceLoginSnapshot(dict: ["state": "idle"])
    }
}

/// 1ms sleeps so the 2s poll cadence doesn't slow the suite.
private struct FastClock: RuntimeClock {
    var now: Date { Date() }

    func sleep(seconds: TimeInterval) async throws {
        try await Task.sleep(nanoseconds: 1_000_000)
    }
}

private actor FakeAuthSessionReader: AuthSessionReading {
    private(set) var logoutCalls = 0
    private(set) var authInfoCalls = 0
    private var result: Result<AuthLogoutResult, Error>
    var authInfo: AuthInfoSnapshot

    init(result: AuthLogoutResult = AuthLogoutResult(dict: ["ok": true]),
         authInfo: AuthInfoSnapshot = AuthInfoSnapshot(dict: [:])) {
        self.result = .success(result)
        self.authInfo = authInfo
    }

    func fail(with error: Error) {
        result = .failure(error)
    }

    func logout() async throws -> AuthLogoutResult {
        logoutCalls += 1
        return try result.get()
    }

    func fetchAuthInfo() async throws -> AuthInfoSnapshot {
        authInfoCalls += 1
        return authInfo
    }
}

@MainActor
private final class FakeLoginItemService: LoginItemServicing {
    var status: SMAppService.Status = .notRegistered
    func reconcileAtLaunch() {}
    func toggle() {}
    func openSystemSettings() {}
}

@MainActor
private final class URLRecorder {
    private(set) var urls: [URL] = []
    func record(_ url: URL) { urls.append(url) }
}

@MainActor
final class DeviceLoginTests: XCTestCase {
    private func stableVariant() -> AppVariant {
        AppVariant.resolve(
            infoDictionary: [
                "SferenceSwitchBuildChannel": "stable",
                "CFBundleDisplayName": "Sference Switch",
                "CFBundleExecutable": "SferenceSwitch",
            ],
            bundleIdentifier: "co.sference.switch",
            runningExecutableName: "SferenceSwitch",
            homeDirectory: "/tmp/sference-switch-test-home",
            environment: [:])
    }

    private func previewVariant() -> AppVariant {
        AppVariant.resolve(
            infoDictionary: [
                "SferenceSwitchBuildChannel": "preview",
                "CFBundleDisplayName": "Sference Switch Preview",
                "CFBundleExecutable": "SferenceSwitchPreview",
            ],
            bundleIdentifier: "co.sference.switch.preview",
            runningExecutableName: "SferenceSwitchPreview",
            homeDirectory: "/tmp/sference-switch-test-home",
            environment: ["SFERENCE_SWITCH_GATEWAY_BIN": "/usr/bin/true"])
    }

    private func makeState(
        variant: AppVariant? = nil,
        deviceReader: FakeDeviceLoginReader,
        authSessionReader: FakeAuthSessionReader = FakeAuthSessionReader(),
        adminReader: StubAdminReader = StubAdminReader(),
        recorder: URLRecorder? = nil
    ) -> SferenceSwitchState {
        // Default-parameter expressions evaluate in a nonisolated
        // context, so the MainActor recorder is built in the body.
        let recorder = recorder ?? URLRecorder()
        return SferenceSwitchState(
            variant: variant ?? stableVariant(),
            reader: adminReader,
            deviceLoginReader: deviceReader,
            authSessionReader: authSessionReader,
            clock: FastClock(),
            loginItemService: FakeLoginItemService(),
            previewRuntimeValidator: { _ in nil },
            browserOpener: { recorder.record($0) },
            startPolling: false)
    }

    private func waitForReauthToFinish(
        _ state: SferenceSwitchState
    ) async {
        for _ in 0..<400 where state.reauthenticating {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
    }

    func testDeviceLoginSnapshotParsesFields() {
        let snapshot = DeviceLoginSnapshot(dict: [
            "state": "pending",
            "user_code": "WXYZ-1234",
            "verification_uri": "https://app.sference.com/device",
            "verification_uri_complete":
                "https://app.sference.com/device?code=WXYZ1234",
            "expires_at": "2026-08-19T12:00:00Z",
        ])
        XCTAssertEqual(snapshot.state, "pending")
        XCTAssertEqual(snapshot.userCode, "WXYZ-1234")
        XCTAssertEqual(
            snapshot.verificationURI, "https://app.sference.com/device")
        XCTAssertEqual(
            snapshot.verificationURIComplete,
            "https://app.sference.com/device?code=WXYZ1234")
        XCTAssertEqual(snapshot.error, "")

        let minimal = DeviceLoginSnapshot(dict: ["state": "idle"])
        XCTAssertEqual(minimal.state, "idle")
        XCTAssertEqual(minimal.userCode, "")
        XCTAssertEqual(minimal.verificationURI, "")
        XCTAssertEqual(minimal.verificationURIComplete, "")
    }

    func testBeginDeviceLoginOpensBrowserAndCompletesOnApproval() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
            ]),
            statuses: [
                DeviceLoginSnapshot(dict: [
                    "state": "pending", "user_code": "WXYZ-1234",
                ]),
                DeviceLoginSnapshot(dict: ["state": "approved"]),
            ])
        let adminReader = StubAdminReader()
        let recorder = URLRecorder()
        let state = makeState(
            deviceReader: deviceReader,
            adminReader: adminReader,
            recorder: recorder)
        defer { state.stop() }

        await state.beginDeviceLogin()

        XCTAssertTrue(state.reauthenticating)
        XCTAssertEqual(state.deviceLogin?.state, "pending")
        XCTAssertEqual(state.deviceLogin?.userCode, "WXYZ-1234")
        XCTAssertEqual(
            recorder.urls, [URL(string: "https://app.sference.com/device")!])
        XCTAssertNil(state.lastError)

        await waitForReauthToFinish(state)

        XCTAssertFalse(state.reauthenticating)
        XCTAssertNil(state.deviceLogin)
        XCTAssertNil(state.lastError)
        // Approval triggers a refresh so the menu picks up the new
        // signed-in auth state immediately.
        let statusCalls = await adminReader.statusCalls
        XCTAssertGreaterThanOrEqual(statusCalls, 1)
    }

    func testBeginDeviceLoginRejoinsWithoutSecondStart() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
            ]))
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()
        await state.beginDeviceLogin()  // already in flight — no-op

        let startCalls = await deviceReader.startCalls
        XCTAssertEqual(startCalls, 1)
        XCTAssertTrue(state.reauthenticating)
    }

    func testBeginDeviceLoginStartFailureSurfacesError() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        await deviceReader.failStart(with: GatewayClientError.badResponse(503))
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()

        XCTAssertFalse(state.reauthenticating)
        XCTAssertNil(state.deviceLogin)
        XCTAssertEqual(
            state.lastError,
            "Sign-in could not be started. Is the router running?")
    }

    func testBeginDeviceLoginFailedStateCarriesServerError() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "failed", "error": "Unknown client_id",
            ]))
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()

        XCTAssertFalse(state.reauthenticating)
        XCTAssertEqual(state.deviceLogin?.state, "failed")
        XCTAssertEqual(state.lastError, "Unknown client_id")
    }

    func testPollFailureTransitionsToFailed() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
            ]),
            statuses: [
                DeviceLoginSnapshot(dict: [
                    "state": "failed", "error": "device code expired",
                ]),
            ])
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()
        await waitForReauthToFinish(state)

        XCTAssertFalse(state.reauthenticating)
        XCTAssertEqual(state.deviceLogin?.state, "failed")
        XCTAssertEqual(state.lastError, "device code expired")
    }

    func testCancelDeviceLoginClearsFlow() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
            ]))
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()
        XCTAssertTrue(state.reauthenticating)

        await state.cancelDeviceLogin()

        let cancelCalls = await deviceReader.cancelCalls
        XCTAssertEqual(cancelCalls, 1)
        XCTAssertFalse(state.reauthenticating)
        XCTAssertNil(state.deviceLogin)
    }

    func testPreviewVariantBlocksDeviceLogin() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "pending"]))
        let state = makeState(
            variant: previewVariant(),
            deviceReader: deviceReader)
        defer { state.stop() }

        await state.beginDeviceLogin()

        let startCalls = await deviceReader.startCalls
        XCTAssertEqual(startCalls, 0)
        XCTAssertFalse(state.reauthenticating)
        XCTAssertEqual(
            state.lastError,
            "Authentication changes are disabled in Sference Switch Preview.")
    }

    func testAdoptDeviceLoginPicksUpExternallyStartedFlow() async {
        // A flow started outside the app (CLI, earlier run) is pending
        // on the gateway; opening the menu adopts it WITHOUT opening
        // another browser tab.
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]),
            statuses: [
                DeviceLoginSnapshot(dict: [
                    "state": "pending",
                    "user_code": "ADOP-TED1",
                    "verification_uri": "https://app.sference.com/device",
                ]),
            ])
        let recorder = URLRecorder()
        let state = makeState(deviceReader: deviceReader, recorder: recorder)
        defer { state.stop() }

        await state.adoptDeviceLoginIfPending()

        XCTAssertTrue(state.reauthenticating)
        XCTAssertEqual(state.deviceLogin?.userCode, "ADOP-TED1")
        XCTAssertTrue(recorder.urls.isEmpty)  // adoption opens no browser
    }

    func testAdoptDeviceLoginIgnoresIdleGateway() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]),
            statuses: [DeviceLoginSnapshot(dict: ["state": "idle"])])
        let state = makeState(deviceReader: deviceReader)
        defer { state.stop() }

        await state.adoptDeviceLoginIfPending()

        XCTAssertFalse(state.reauthenticating)
        XCTAssertNil(state.deviceLogin)
    }

    func testOpenDeviceLoginVerificationPageUsesSnapshotURI() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
            ]))
        let recorder = URLRecorder()
        let state = makeState(deviceReader: deviceReader, recorder: recorder)
        defer { state.stop() }

        await state.beginDeviceLogin()
        XCTAssertEqual(recorder.urls.count, 1)  // opened by begin

        state.openDeviceLoginVerificationPage()
        XCTAssertEqual(
            recorder.urls,
            [URL(string: "https://app.sference.com/device")!,
             URL(string: "https://app.sference.com/device")!])
    }

    func testBeginDeviceLoginPrefersCompleteVerificationURI() async {
        // The gateway-built verification_uri_complete prefills the code
        // on the console's /device page — the browser must open it, not
        // the bare URI.
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: [
                "state": "pending",
                "user_code": "WXYZ-1234",
                "verification_uri": "https://app.sference.com/device",
                "verification_uri_complete":
                    "https://app.sference.com/device?code=WXYZ1234",
            ]))
        let recorder = URLRecorder()
        let state = makeState(deviceReader: deviceReader, recorder: recorder)
        defer { state.stop() }

        await state.beginDeviceLogin()

        XCTAssertEqual(
            recorder.urls,
            [URL(string: "https://app.sference.com/device?code=WXYZ1234")!])
        state.openDeviceLoginVerificationPage()
        XCTAssertEqual(recorder.urls.count, 2)
        XCTAssertEqual(
            recorder.urls[1],
            URL(string: "https://app.sference.com/device?code=WXYZ1234")!)
    }

    func testDeviceLoginBrowserURLFallsBackToPlainURI() {
        // Older gateways carry no verification_uri_complete.
        let legacy = DeviceLoginSnapshot(dict: [
            "state": "pending",
            "verification_uri": "https://app.sference.com/device",
        ])
        XCTAssertEqual(
            deviceLoginBrowserURL(legacy),
            URL(string: "https://app.sference.com/device")!)
        XCTAssertNil(deviceLoginBrowserURL(
            DeviceLoginSnapshot(dict: ["state": "pending"])))
    }

    // MARK: - Account card (overview)

    func testAuthInfoSnapshotParses() {
        let info = AuthInfoSnapshot(dict: [
            "signed_in": true,
            "health": "ok",
            "profile": "default",
            "email": "user@example.com",
            "expires_at": "2026-08-20T12:00:00Z",
            "fallback_in_use": false,
        ])
        XCTAssertTrue(info.signedIn)
        XCTAssertEqual(info.email, "user@example.com")
        XCTAssertEqual(info.expiresAt, "2026-08-20T12:00:00Z")

        let minimal = AuthInfoSnapshot(dict: [:])
        XCTAssertFalse(minimal.signedIn)
        XCTAssertEqual(minimal.email, "")
        XCTAssertEqual(minimal.expiresAt, "")
    }

    func testOverviewDidShowLoadsAuthInfoAndAdoptsPendingFlow() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]),
            statuses: [
                DeviceLoginSnapshot(dict: [
                    "state": "pending",
                    "user_code": "ADOP-TED1",
                    "verification_uri": "https://app.sference.com/device",
                ]),
            ])
        let sessionReader = FakeAuthSessionReader(
            authInfo: AuthInfoSnapshot(dict: [
                "signed_in": true, "email": "user@example.com",
            ]))
        let recorder = URLRecorder()
        let state = makeState(
            deviceReader: deviceReader,
            authSessionReader: sessionReader,
            recorder: recorder)
        defer { state.stop() }

        state.overviewDidShow()
        // overviewDidShow kicks two unstructured tasks; give them a turn.
        try? await Task.sleep(nanoseconds: 50_000_000)

        let infoCalls = await sessionReader.authInfoCalls
        XCTAssertGreaterThanOrEqual(infoCalls, 1)
        XCTAssertEqual(state.authInfo?.email, "user@example.com")
        XCTAssertTrue(state.reauthenticating)
        XCTAssertEqual(state.deviceLogin?.userCode, "ADOP-TED1")
        XCTAssertTrue(recorder.urls.isEmpty)  // adoption opens no browser
    }

    func testSignOutRefreshesAuthInfo() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        let sessionReader = FakeAuthSessionReader(
            result: AuthLogoutResult(dict: ["ok": true]),
            authInfo: AuthInfoSnapshot(dict: ["signed_in": false]))
        let state = makeState(
            deviceReader: deviceReader,
            authSessionReader: sessionReader)
        defer { state.stop() }

        await state.signOut()

        let infoCalls = await sessionReader.authInfoCalls
        XCTAssertGreaterThanOrEqual(infoCalls, 1)
        XCTAssertEqual(state.authInfo?.signedIn, false)
    }

    func testAccountCardPresentation() {
        let signedInAuth = AuthStatus(dict: [
            "signed_in": true, "health": "ok",
        ])
        let signedOutAuth = AuthStatus(dict: [
            "signed_in": false, "health": "signed_out",
        ])
        let deadAuth = AuthStatus(dict: [
            "signed_in": true, "health": "refresh_failed",
        ])
        let pending = DeviceLoginSnapshot(dict: [
            "state": "pending", "user_code": "WXYZ-1234",
        ])
        let info = AuthInfoSnapshot(dict: [
            "email": "user@example.com", "expires_at": "2026-08-20T12:00:00Z",
        ])

        // No status read yet renders no actions.
        XCTAssertEqual(
            accountCardPresentation(
                auth: nil, authInfo: nil, deviceLogin: nil),
            .unavailable)
        XCTAssertEqual(
            accountCardPresentation(
                auth: signedOutAuth, authInfo: nil, deviceLogin: nil),
            .signedOut)
        XCTAssertEqual(
            accountCardPresentation(
                auth: deadAuth, authInfo: nil, deviceLogin: nil),
            .reauthRequired)
        XCTAssertEqual(
            accountCardPresentation(
                auth: signedInAuth, authInfo: info, deviceLogin: nil),
            .signedIn(
                email: "user@example.com",
                expiresAt: "2026-08-20T12:00:00Z"))
        // A pending flow wins over every auth state.
        XCTAssertEqual(
            accountCardPresentation(
                auth: signedInAuth, authInfo: info, deviceLogin: pending),
            .pending(code: "WXYZ-1234"))
        XCTAssertEqual(
            accountCardPresentation(
                auth: nil, authInfo: nil, deviceLogin: pending),
            .pending(code: "WXYZ-1234"))
    }

    func testAccountSignedInCaption() {
        XCTAssertEqual(
            accountSignedInCaption(email: "", expiresAt: ""),
            "Signed in with an API key.")
        XCTAssertEqual(
            accountSignedInCaption(email: "user@example.com", expiresAt: ""),
            "Signed in as user@example.com.")
        XCTAssertEqual(
            accountSignedInCaption(
                email: "user@example.com",
                expiresAt: "not-a-date"),
            "Signed in as user@example.com.")
        let expiring = accountSignedInCaption(
            email: "user@example.com",
            expiresAt: "2099-01-01T00:00:00Z")
        XCTAssertTrue(expiring.hasPrefix("Signed in as user@example.com · session expires "))
    }

    // MARK: - Sign out

    func testSignOutSuccessClearsErrorAndRefreshes() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        let sessionReader = FakeAuthSessionReader(
            result: AuthLogoutResult(dict: ["ok": true]))
        let adminReader = StubAdminReader()
        let state = makeState(
            deviceReader: deviceReader,
            authSessionReader: sessionReader,
            adminReader: adminReader)
        defer { state.stop() }

        await state.signOut()

        let logoutCalls = await sessionReader.logoutCalls
        XCTAssertEqual(logoutCalls, 1)
        XCTAssertNil(state.lastError)
        let statusCalls = await adminReader.statusCalls
        XCTAssertGreaterThanOrEqual(statusCalls, 1)
    }

    func testSignOutRefusalSurfacesReason() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        let sessionReader = FakeAuthSessionReader(
            result: AuthLogoutResult(dict: [
                "ok": false,
                "error": "credential comes from SFERENCE_API_KEY — unset the environment variable",
            ]))
        let state = makeState(
            deviceReader: deviceReader,
            authSessionReader: sessionReader)
        defer { state.stop() }

        await state.signOut()

        XCTAssertEqual(
            state.lastError,
            "credential comes from SFERENCE_API_KEY — unset the environment variable")
    }

    func testSignOutTransportFailure() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        let sessionReader = FakeAuthSessionReader()
        await sessionReader.fail(with: GatewayClientError.badResponse(503))
        let state = makeState(
            deviceReader: deviceReader,
            authSessionReader: sessionReader)
        defer { state.stop() }

        await state.signOut()

        XCTAssertEqual(
            state.lastError, "Sign-out failed. Is the router running?")
    }

    func testPreviewVariantBlocksSignOut() async {
        let deviceReader = FakeDeviceLoginReader(
            start: DeviceLoginSnapshot(dict: ["state": "idle"]))
        let sessionReader = FakeAuthSessionReader()
        let state = makeState(
            variant: previewVariant(),
            deviceReader: deviceReader,
            authSessionReader: sessionReader)
        defer { state.stop() }

        await state.signOut()

        let logoutCalls = await sessionReader.logoutCalls
        XCTAssertEqual(logoutCalls, 0)
        XCTAssertEqual(
            state.lastError,
            "Authentication changes are disabled in Sference Switch Preview.")
    }

    func testAuthLogoutResultParses() {
        let ok = AuthLogoutResult(dict: ["ok": true, "state": "signed_out"])
        XCTAssertTrue(ok.ok)
        XCTAssertEqual(ok.error, "")
        let refused = AuthLogoutResult(dict: [
            "ok": false, "error": "reason",
        ])
        XCTAssertFalse(refused.ok)
        XCTAssertEqual(refused.error, "reason")
    }

    // MARK: - Display helpers

    func testDeviceLoginMenuTitle() {
        XCTAssertEqual(
            deviceLoginMenuTitle(DeviceLoginSnapshot(dict: [
                "state": "pending", "user_code": "WXYZ-1234",
            ])),
            "Approve in Browser: WXYZ-1234")
        XCTAssertEqual(
            deviceLoginMenuTitle(DeviceLoginSnapshot(dict: [
                "state": "pending",
            ])),
            "Sign-In: Waiting for Code…")
        XCTAssertEqual(
            deviceLoginMenuTitle(DeviceLoginSnapshot(dict: [
                "state": "approved",
            ])),
            "Sign-In Approved")
        XCTAssertEqual(
            deviceLoginMenuTitle(DeviceLoginSnapshot(dict: [
                "state": "failed",
            ])),
            "Sign-In Failed")
        XCTAssertEqual(
            deviceLoginMenuTitle(DeviceLoginSnapshot(dict: [
                "state": "idle",
            ])),
            "Sign-In")
    }

    func testAuthIsSignedOut() {
        let signedIn = AuthStatus(dict: [
            "signed_in": true, "health": "ok",
        ])
        XCTAssertFalse(authIsSignedOut(auth: signedIn))

        let signedOut = AuthStatus(dict: [
            "signed_in": false, "health": "signed_out",
        ])
        XCTAssertTrue(authIsSignedOut(auth: signedOut))

        // Store presence but the gateway reports signed_out health.
        let staleStore = AuthStatus(dict: [
            "signed_in": true, "health": "signed_out",
        ])
        XCTAssertTrue(authIsSignedOut(auth: staleStore))

        // refresh_failed is the reauth state, not plain signed-out.
        let dead = AuthStatus(dict: [
            "signed_in": true, "health": "refresh_failed",
        ])
        XCTAssertFalse(authIsSignedOut(auth: dead))

        // Unknown auth (no status read yet) offers nothing.
        XCTAssertFalse(authIsSignedOut(auth: nil))
    }
}
