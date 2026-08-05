import Foundation

protocol TrafficAnalyticsReading: Sendable {
    func fetchAnalytics(since: Date, until: Date) async throws
        -> TrafficAnalyticsSnapshot
}

final class TrafficAPIClient: TrafficAnalyticsReading, @unchecked Sendable {
    private let runtime: RuntimeProfile
    private let session: URLSession
    private let decoder: JSONDecoder

    init(runtime: RuntimeProfile, session: URLSession? = nil) {
        self.runtime = runtime
        if let session {
            self.session = session
        } else {
            let configuration = URLSessionConfiguration.ephemeral
            configuration.timeoutIntervalForRequest = 5
            configuration.timeoutIntervalForResource = 10
            configuration.waitsForConnectivity = false
            configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
            configuration.urlCache = nil
            self.session = URLSession(configuration: configuration)
        }
        decoder = JSONDecoder()
    }

    func fetchAnalytics(since: Date, until: Date) async throws
        -> TrafficAnalyticsSnapshot {
        var url = adminBaseURL(runtime: runtime)
        url.appendPathComponent("v1/admin/analytics")
        guard var components = URLComponents(
            url: url,
            resolvingAgainstBaseURL: false
        ) else {
            throw GatewayClientError.invalidPayload
        }
        components.queryItems = [
            URLQueryItem(
                name: "since",
                value: String(Int64(since.timeIntervalSince1970))),
            URLQueryItem(
                name: "until",
                value: String(Int64(until.timeIntervalSince1970))),
        ]
        guard let requestURL = components.url else {
            throw GatewayClientError.invalidPayload
        }

        let (data, response) = try await session.data(from: requestURL)
        guard let http = response as? HTTPURLResponse,
              (200..<300).contains(http.statusCode) else {
            throw GatewayClientError.badResponse(
                (response as? HTTPURLResponse)?.statusCode ?? -1)
        }
        do {
            return try decoder.decode(TrafficAnalyticsSnapshot.self, from: data)
        } catch {
            throw GatewayClientError.invalidPayload
        }
    }
}

actor TrafficFetchCoordinator {
    private let reader: any TrafficAnalyticsReading

    init(reader: any TrafficAnalyticsReading) {
        self.reader = reader
    }

    func fetch(since: Date, until: Date) async throws
        -> TrafficAnalyticsSnapshot {
        try Task.checkCancellation()
        return try await reader.fetchAnalytics(since: since, until: until)
    }
}

@MainActor
final class TrafficStore: ObservableObject {
    @Published private(set) var snapshot: TrafficAnalyticsSnapshot?
    @Published private(set) var isLoading = false
    @Published private(set) var isRefreshing = false
    @Published private(set) var isStale = false
    @Published private(set) var errorMessage: String?
    @Published private(set) var lastUpdated: Date?
    @Published var range: TrafficRange = .week {
        didSet {
            guard range != oldValue, isVisible else { return }
            requestRefresh()
        }
    }

    /// Instant the manual measurement was last reset. Persisted so it
    /// survives a menubar restart, which would otherwise silently restart the
    /// measurement while the label still says "Manual".
    @Published private(set) var manualStart: Date

    static let manualStartDefaultsKey = "traffic.manualStart"

    private let coordinator: TrafficFetchCoordinator
    private let now: @Sendable () -> Date
    private let refreshInterval: TimeInterval
    private let defaults: UserDefaults
    private var refreshTask: Task<Void, Never>?
    private var pollingTask: Task<Void, Never>?
    private var refreshPending = false
    private var generation: UInt64 = 0
    private(set) var isVisible = false

    init(
        reader: any TrafficAnalyticsReading,
        refreshInterval: TimeInterval = 60,
        now: @escaping @Sendable () -> Date = { Date() },
        defaults: UserDefaults = .standard
    ) {
        coordinator = TrafficFetchCoordinator(reader: reader)
        self.refreshInterval = refreshInterval
        self.now = now
        self.defaults = defaults
        let stored = defaults.double(forKey: Self.manualStartDefaultsKey)
        manualStart = stored > 0
            ? Date(timeIntervalSince1970: stored)
            : now()
    }

    /// Starts a new measurement from this instant.
    func resetManual() {
        manualStart = now()
        defaults.set(
            manualStart.timeIntervalSince1970,
            forKey: Self.manualStartDefaultsKey)
        if range == .manual {
            requestRefresh()
        }
    }

    /// Resolves the query window. Rolling ranges are an offset from `until`;
    /// the manual window is anchored at its reset instant, clamped at both
    /// ends because the endpoint rejects `since >= until` (which a
    /// just-reset measurement would produce) and spans wider than 30 days.
    private func windowStart(until: Date, for range: TrafficRange) -> Date {
        guard range == .manual else {
            return until.addingTimeInterval(-TimeInterval(range.rawValue))
        }
        let earliest = until.addingTimeInterval(
            -TimeInterval(trafficMaxWindowSeconds))
        return min(
            max(manualStart, earliest),
            until.addingTimeInterval(-1))
    }

    func start() {
        guard !isVisible else { return }
        isVisible = true
        requestRefresh()
        pollingTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                do {
                    try await Task.sleep(
                        nanoseconds: UInt64(
                            max(self.refreshInterval, 0.1)
                                * 1_000_000_000))
                } catch {
                    return
                }
                guard !Task.isCancelled else { return }
                self.requestRefresh()
            }
        }
    }

    func stop() {
        isVisible = false
        pollingTask?.cancel()
        pollingTask = nil
        refreshTask?.cancel()
        refreshTask = nil
        refreshPending = false
        isLoading = false
        isRefreshing = false
    }

    func requestRefresh() {
        guard isVisible else { return }
        guard refreshTask == nil else {
            refreshPending = true
            return
        }
        generation &+= 1
        let requestGeneration = generation
        let requestRange = range
        let until = now()
        let since = windowStart(until: until, for: requestRange)
        isLoading = snapshot == nil
        isRefreshing = snapshot != nil

        refreshTask = Task { [weak self] in
            guard let self else { return }
            let result: Result<TrafficAnalyticsSnapshot, Error>
            do {
                result = .success(
                    try await self.coordinator.fetch(
                        since: since,
                        until: until))
            } catch {
                result = .failure(error)
            }
            guard !Task.isCancelled else { return }
            self.complete(
                result,
                generation: requestGeneration,
                requestedRange: requestRange)
        }
    }

    private func complete(
        _ result: Result<TrafficAnalyticsSnapshot, Error>,
        generation requestGeneration: UInt64,
        requestedRange: TrafficRange
    ) {
        guard requestGeneration == generation else { return }
        refreshTask = nil
        isLoading = false
        isRefreshing = false

        if requestedRange == range {
            switch result {
            case .success(let value):
                if snapshot != value {
                    snapshot = value
                }
                lastUpdated = Date(
                    timeIntervalSince1970:
                        TimeInterval(value.generatedAt))
                isStale = false
                errorMessage = nil
            case .failure(let error):
                isStale = snapshot != nil
                errorMessage = trafficErrorMessage(error)
            }
        } else {
            refreshPending = true
        }

        if refreshPending, isVisible {
            refreshPending = false
            requestRefresh()
        }
    }
}

func trafficErrorMessage(_ error: Error) -> String {
    if let gatewayError = error as? GatewayClientError {
        switch gatewayError {
        case .badResponse(let status):
            if status == 404 {
                return "Traffic analytics are not available in this gateway build."
            }
            return "The gateway returned HTTP \(status)."
        case .invalidPayload:
            return "The gateway returned analytics data the app could not read."
        }
    }
    if error is CancellationError {
        return ""
    }
    return "Traffic data could not be refreshed."
}
