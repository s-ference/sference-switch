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
            return DeviceLoginSnapshot(dict: ["state": "pending"])
        }
        return statusQueue.removeFirst()
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
        adminReader: StubAdminReader = StubAdminReader(),
        recorder: URLRecorder = URLRecorder()
    ) -> SferenceSwitchState {
        SferenceSwitchState(
            variant: variant ?? stableVariant(),
            reader: adminReader,
            deviceLoginReader: deviceReader,
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
            "expires_at": "2026-08-19T12:00:00Z",
        ])
        XCTAssertEqual(snapshot.state, "pending")
        XCTAssertEqual(snapshot.userCode, "WXYZ-1234")
        XCTAssertEqual(
            snapshot.verificationURI, "https://app.sference.com/device")
        XCTAssertEqual(snapshot.error, "")

        let minimal = DeviceLoginSnapshot(dict: ["state": "idle"])
        XCTAssertEqual(minimal.state, "idle")
        XCTAssertEqual(minimal.userCode, "")
        XCTAssertEqual(minimal.verificationURI, "")
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
