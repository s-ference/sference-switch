import Foundation

enum AppChannel: String, Equatable {
    case stable
    case preview
}

enum RuntimeTrust: Equatable {
    case stable
    case previewDown
    case previewTrusted
    case previewMismatch(expected: String, reported: String)
    case identityMismatch(reason: String)
}

struct RuntimeProfile: Equatable {
    let adminBaseURL: URL
    let doorURLs: [URL]
    let expectedConfigPath: String?
    let environment: [String: String]

    static func stable(environment: [String: String] = ProcessInfo.processInfo.environment)
        -> RuntimeProfile {
        let binaryEnvironment = environment["SFERENCE_SWITCH_GATEWAY_BIN"]
            .flatMap { $0.isEmpty ? nil : ["SFERENCE_SWITCH_GATEWAY_BIN": $0] }
            ?? [:]
        return RuntimeProfile(
            adminBaseURL: endpointURL(
                url: environment["SFERENCE_SWITCH_ADMIN_URL"],
                address: environment["SFERENCE_SWITCH_ADMIN_ADDR"],
                defaultPort: 45273),
            doorURLs: doorEndpointURLs(
                urls: environment["SFERENCE_SWITCH_DOOR_URLS"],
                ports: environment["SFERENCE_SWITCH_DOOR_PORTS"],
                defaultPorts: [45271]),
            expectedConfigPath: nil,
            environment: binaryEnvironment)
    }

    static func preview(
        homeDirectory: String = NSHomeDirectory(),
        environment processEnvironment: [String: String] = ProcessInfo.processInfo.environment
    ) -> RuntimeProfile {
        let root = URL(fileURLWithPath: homeDirectory, isDirectory: true)
            .appendingPathComponent(".sference/switch-preview", isDirectory: true)
        let logs = root.appendingPathComponent("logs", isDirectory: true)
        let config = root.appendingPathComponent("gateway.yaml")

        var environment = [
            "SFERENCE_SWITCH_CONFIG_PATH": config.path,
            "SFERENCE_SWITCH_ADMIN_ADDR": "127.0.0.1:45373",
            "SFERENCE_SWITCH_ADMIN_URL": "http://127.0.0.1:45373",
            "SFERENCE_SWITCH_GATEWAY_ADMIN": "http://127.0.0.1:45373",
            "SFERENCE_SWITCH_GATEWAY_PORT": "45373",
            "SFERENCE_SWITCH_DOOR_PORTS": "45371",
            "SFERENCE_SWITCH_GATEWAY_PIDFILE": root.appendingPathComponent("gateway.pid").path,
            "SFERENCE_SWITCH_GATEWAY_PID": root.appendingPathComponent("gateway.pid").path,
            "SFERENCE_SWITCH_DOOR_PIDFILE": root.appendingPathComponent("door.pid").path,
            "SFERENCE_SWITCH_GATEWAY_LOG": logs.appendingPathComponent("router.log").path,
            "SFERENCE_SWITCH_DOOR_LOG": logs.appendingPathComponent("door.log").path,
            "SFERENCE_SWITCH_TELEMETRY_DIR": root.appendingPathComponent("telemetry").path,
            "SFERENCE_SWITCH_ROUTE_FILE": root.appendingPathComponent("route").path,
            "SFERENCE_SWITCH_ENV_FILE": root.appendingPathComponent("env").path,
            "SFERENCE_SWITCH_AUTH_FILE": root.appendingPathComponent("auth.json").path,
            "SFERENCE_CONFIG_DIR": root.appendingPathComponent("sference").path,
            "SFERENCE_SWITCH_AUTH_NO_KEYRING": "1",
            "SFERENCE_SWITCH_PRIVATE_RUNTIME": "1",
            "SFERENCE_SWITCH_OAUTH_PROFILE": "sference-switch-preview",
            "SFERENCE_SWITCH_CLAUDE_SETTINGS": root.appendingPathComponent("claude/settings.json").path,
            "SFERENCE_SWITCH_BACKUP_DIR": root.appendingPathComponent("backups").path,
            "SFERENCE_SWITCH_GATEWAY_TOKEN": "sference-switch-local-gateway-preview",
            "SFERENCE_SWITCH_LAUNCHD": "off",
            "SFERENCE_SWITCH_MENUBAR": "off",
            "SFERENCE_SWITCH_MENUBAR_APP": "",
            "SFERENCE_API_KEY": "",
            "SFERENCE_SWITCH_API_KEY": "",
            "SFERENCE_SWITCH_API_KEY_FALLBACK": "",
            "ANTHROPIC_API_KEY": "",
            "SFERENCE_SWITCH_ANTHROPIC_KEY": "",
            "ANTHROPIC_AUTH_TOKEN": "",
            "OPENAI_API_KEY": "",
            "CODEX_AUTH_TOKEN": "",
        ]
        if let binary = processEnvironment["SFERENCE_SWITCH_GATEWAY_BIN"], !binary.isEmpty {
            environment["SFERENCE_SWITCH_GATEWAY_BIN"] = binary
        }

        return RuntimeProfile(
            adminBaseURL: URL(string: "http://127.0.0.1:45373")!,
            doorURLs: [URL(string: "http://127.0.0.1:45371/doorz")!],
            expectedConfigPath: config.standardizedFileURL.path,
            environment: environment)
    }

    private static func doorEndpointURLs(
        urls rawURLs: String?,
        ports rawPorts: String?,
        defaultPorts: [Int]
    ) -> [URL] {
        let candidates: [String]
        if let rawURLs, !rawURLs.trimmingCharacters(in: .whitespaces).isEmpty {
            candidates = rawURLs.split(separator: ",").map(String.init)
        } else if let rawPorts,
                  !rawPorts.trimmingCharacters(in: .whitespaces).isEmpty {
            candidates = rawPorts.split(separator: ",").map(String.init)
        } else {
            candidates = defaultPorts.map(String.init)
        }

        var seen = Set<URL>()
        return candidates.compactMap { raw -> URL? in
            let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { return nil }
            let candidate: String
            if Int(trimmed) != nil {
                candidate = "http://127.0.0.1:\(trimmed)"
            } else if trimmed.contains("://") {
                candidate = trimmed
            } else {
                candidate = "http://\(trimmed)"
            }
            guard var components = URLComponents(string: candidate),
                  components.scheme == "http",
                  let host = components.host?.lowercased(),
                  host == "127.0.0.1" || host == "localhost" || host == "::1",
                  components.port != nil else {
                return nil
            }
            components.path = "/doorz"
            components.query = nil
            components.fragment = nil
            guard let url = components.url, seen.insert(url).inserted else {
                return nil
            }
            return url
        }
    }

    private static func endpointURL(
        url rawURL: String?,
        address rawAddress: String?,
        defaultPort: Int
    ) -> URL {
        if let rawURL, !rawURL.isEmpty,
           let parsed = normalizedHTTPURL(rawURL, defaultPort: defaultPort) {
            return parsed
        }
        if let rawAddress, !rawAddress.isEmpty,
           let parsed = normalizedHTTPURL(rawAddress, defaultPort: defaultPort) {
            return parsed
        }
        return URL(string: "http://127.0.0.1:\(defaultPort)")!
    }

    private static func normalizedHTTPURL(_ raw: String, defaultPort: Int) -> URL? {
        let withScheme = raw.contains("://") ? raw : "http://\(raw)"
        guard var components = URLComponents(string: withScheme) else { return nil }
        if components.scheme == nil { components.scheme = "http" }
        if components.host == nil { components.host = "127.0.0.1" }
        if components.port == nil { components.port = defaultPort }
        if components.path.hasSuffix("/") {
            components.path = String(components.path.dropLast())
        }
        return components.url
    }
}

struct AppVariant: Equatable {
    let channel: AppChannel
    let bundleIdentifier: String
    let displayName: String
    let executableName: String
    let identityError: String?
    let statusItemAutosaveName: String
    let windowFrameAutosaveName: String
    let allowsLoginItem: Bool
    let runtime: RuntimeProfile

    static let stableBundleIdentifier = "co.sference.switch"
    static let previewBundleIdentifier = "co.sference.switch.preview"
    static let stableExecutableName = "SferenceSwitch"
    static let previewExecutableName = "SferenceSwitchPreview"
    static let stableDisplayName = "Sference Switch"
    static let previewDisplayName = "Sference Switch Preview"

    static func current(
        bundle: Bundle = .main,
        homeDirectory: String = NSHomeDirectory(),
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> AppVariant {
        resolve(
            infoDictionary: bundle.infoDictionary ?? [:],
            bundleIdentifier: bundle.bundleIdentifier,
            runningExecutableName: bundle.executableURL?.lastPathComponent,
            homeDirectory: homeDirectory,
            environment: environment)
    }

    static func resolve(
        infoDictionary: [String: Any],
        bundleIdentifier: String? = nil,
        runningExecutableName: String? = nil,
        homeDirectory: String = NSHomeDirectory(),
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> AppVariant {
        let rawChannel = (infoDictionary["SferenceSwitchBuildChannel"] as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let declaredChannel = rawChannel.flatMap(AppChannel.init(rawValue:))
        let declaredExecutable = infoDictionary["CFBundleExecutable"] as? String
        let declaredName = (infoDictionary["CFBundleDisplayName"] as? String)
            ?? (infoDictionary["CFBundleName"] as? String)
        let declaredBundleID = bundleIdentifier
            ?? (infoDictionary["CFBundleIdentifier"] as? String)

        // Any Preview identity marker selects the isolated Preview runtime,
        // even when another metadata field is missing or contradictory. This
        // prevents malformed Preview packaging from ever falling back to the
        // Stable config, ports, or login item domain.
        let hasPreviewIdentity = declaredChannel == .preview
            || declaredBundleID == previewBundleIdentifier
            || declaredExecutable == previewExecutableName
            || runningExecutableName == previewExecutableName
            || declaredName == previewDisplayName
        let channel: AppChannel = hasPreviewIdentity
            ? .preview
            : (declaredChannel ?? .stable)
        let fallbackName = channel == .preview
            ? previewDisplayName
            : stableDisplayName
        let fallbackExecutable = channel == .preview
            ? previewExecutableName
            : stableExecutableName
        let fallbackBundleID = channel == .preview
            ? previewBundleIdentifier
            : stableBundleIdentifier
        let name = declaredName ?? fallbackName
        let executable = declaredExecutable ?? fallbackExecutable
        let resolvedBundleID = declaredBundleID ?? fallbackBundleID
        let identityError = packagedIdentityError(
            channel: channel,
            rawChannel: rawChannel,
            bundleIdentifier: declaredBundleID,
            displayName: declaredName,
            declaredExecutable: declaredExecutable,
            runningExecutable: runningExecutableName)

        return AppVariant(
            channel: channel,
            bundleIdentifier: resolvedBundleID,
            displayName: name,
            executableName: executable,
            identityError: identityError,
            statusItemAutosaveName: channel == .preview
                ? "sference-switch-toggle-preview"
                : "sference-switch-toggle",
            windowFrameAutosaveName: channel == .preview
                ? "sference-switch-router-window-preview"
                : "sference-switch-router-window",
            allowsLoginItem: channel == .stable && identityError == nil,
            runtime: channel == .preview
                ? .preview(homeDirectory: homeDirectory, environment: environment)
                : .stable(environment: environment))
    }

    /// Bare SwiftPM builds have no packaged identity fields and remain useful
    /// as Stable development builds. Once any packaged identity field exists,
    /// all fields must agree exactly.
    private static func packagedIdentityError(
        channel: AppChannel,
        rawChannel: String?,
        bundleIdentifier: String?,
        displayName: String?,
        declaredExecutable: String?,
        runningExecutable: String?
    ) -> String? {
        let hasPackagedIdentity = rawChannel != nil
            || bundleIdentifier != nil
            || displayName != nil
            || declaredExecutable != nil
            || runningExecutable != nil
        guard hasPackagedIdentity else { return nil }

        let expectedBundleID = channel == .preview
            ? previewBundleIdentifier
            : stableBundleIdentifier
        let expectedName = channel == .preview
            ? previewDisplayName
            : stableDisplayName
        let expectedExecutable = channel == .preview
            ? previewExecutableName
            : stableExecutableName
        var problems: [String] = []
        if rawChannel != channel.rawValue {
            problems.append("build channel")
        }
        if bundleIdentifier != expectedBundleID {
            problems.append("bundle identifier")
        }
        if displayName != expectedName {
            problems.append("display name")
        }
        if declaredExecutable != expectedExecutable {
            problems.append("declared executable")
        }
        if let runningExecutable,
           runningExecutable != expectedExecutable {
            problems.append("running executable")
        }
        guard !problems.isEmpty else { return nil }
        return "App identity mismatch: " + problems.joined(separator: ", ") + "."
    }
}
