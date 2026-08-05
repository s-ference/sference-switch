import Foundation

enum TrafficRange: Int, CaseIterable, Identifiable, Sendable {
    case day = 86_400
    case week = 604_800
    case month = 2_592_000
    /// Manual measurement: starts at an explicit reset instant rather than a
    /// rolling offset from now. The rawValue is a sentinel, not a duration.
    case manual = -1

    var id: Int { rawValue }

    var label: String {
        switch self {
        case .day: "24h"
        case .week: "7d"
        case .month: "30d"
        case .manual: "Manual"
        }
    }
}

/// The analytics endpoint rejects a window wider than 30 days.
let trafficMaxWindowSeconds = 2_592_000

enum TrafficGrouping: String, CaseIterable, Identifiable {
    case provider = "Provider"
    case model = "Model"

    var id: String { rawValue }
}

enum TrafficTab: String, CaseIterable, Identifiable {
    case cost = "Cost"
    case performance = "Performance"

    var id: String { rawValue }
}

struct TrafficAnalyticsSnapshot: Decodable, Equatable, Sendable {
    var generatedAt: Int64
    var window: TrafficAnalyticsWindow
    var coverage: TrafficCoverage
    var cost: TrafficCost
    var performance: TrafficPerformance

    enum CodingKeys: String, CodingKey {
        case generatedAt = "generated_at"
        case window
        case coverage
        case cost
        case performance
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        generatedAt = values.decodeDefault(Int64.self, forKey: .generatedAt)
        window = values.decodeDefault(TrafficAnalyticsWindow.self, forKey: .window)
        coverage = values.decodeDefault(TrafficCoverage.self, forKey: .coverage)
        cost = values.decodeDefault(TrafficCost.self, forKey: .cost)
        performance = values.decodeDefault(TrafficPerformance.self, forKey: .performance)
    }

    var isEmpty: Bool {
        cost.providers.isEmpty
            && cost.models.isEmpty
            && performance.providers.isEmpty
            && performance.models.isEmpty
    }
}

struct TrafficAnalyticsWindow: Decodable, Equatable, Sendable {
    var since: Int64 = 0
    var until: Int64 = 0
}

struct TrafficCoverage: Decodable, Equatable, Sendable {
    var requestRows: Int = 0
    var pricedActualCostRows: Int = 0
    var unpricedActualCostRows: Int = 0
    var savingsEligibleRows: Int = 0
    var savingsUnpricedRows: Int = 0
    var collectionEnabled = true
    var complete = true
    var reason = ""

    enum CodingKeys: String, CodingKey {
        case requestRows = "request_rows"
        case pricedActualCostRows = "priced_actual_cost_rows"
        case unpricedActualCostRows = "unpriced_actual_cost_rows"
        case savingsEligibleRows = "savings_eligible_rows"
        case savingsUnpricedRows = "savings_unpriced_rows"
        case collectionEnabled = "collection_enabled"
        case complete
        case reason
    }

    init() {}

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        requestRows = try values.decodeIfPresent(
            Int.self,
            forKey: .requestRows) ?? 0
        pricedActualCostRows = try values.decodeIfPresent(
            Int.self,
            forKey: .pricedActualCostRows) ?? 0
        unpricedActualCostRows = try values.decodeIfPresent(
            Int.self,
            forKey: .unpricedActualCostRows) ?? 0
        savingsEligibleRows = try values.decodeIfPresent(
            Int.self,
            forKey: .savingsEligibleRows) ?? 0
        savingsUnpricedRows = try values.decodeIfPresent(
            Int.self,
            forKey: .savingsUnpricedRows) ?? 0
        collectionEnabled = try values.decodeIfPresent(
            Bool.self,
            forKey: .collectionEnabled) ?? true
        complete = try values.decodeIfPresent(
            Bool.self,
            forKey: .complete) ?? true
        reason = try values.decodeIfPresent(
            String.self,
            forKey: .reason) ?? ""
    }
}

struct TrafficCost: Decodable, Equatable, Sendable {
    var summary = TrafficCostSummary()
    var providers: [TrafficCostRow] = []
    var models: [TrafficCostRow] = []
    var savings = TrafficSavings()
}

struct TrafficCostSummary: Decodable, Equatable, Sendable {
    var actualClaudeCostUSD: Double = 0
    var actualSferenceCostUSD: Double = 0
    var estimatedNativeCostForSferenceUSD: Double = 0
    var savedUSD: Double = 0
    var savedPercent: Double = 0

    enum CodingKeys: String, CodingKey {
        case actualClaudeCostUSD = "actual_claude_cost_usd"
        case actualSferenceCostUSD = "actual_sference_cost_usd"
        case estimatedNativeCostForSferenceUSD =
            "estimated_native_cost_for_sference_usd"
        case savedUSD = "saved_usd"
        case savedPercent = "saved_percent"
    }
}

struct TrafficCostRow: Decodable, Equatable, Identifiable, Sendable {
    var provider = ""
    var modelID: String?
    var displayName: String?
    var requests = 0
    var tokens: Int64 = 0
    var actualCostUSD: Double?
    var pricedRows = 0
    var unpricedRows = 0

    enum CodingKeys: String, CodingKey {
        case provider
        case modelID = "model_id"
        case displayName = "display_name"
        case requests
        case tokens
        case actualCostUSD = "actual_cost_usd"
        case pricedRows = "priced_rows"
        case unpricedRows = "unpriced_rows"
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        provider = try values.decodeIfPresent(String.self, forKey: .provider) ?? ""
        modelID = try values.decodeIfPresent(String.self, forKey: .modelID)
        displayName = try values.decodeIfPresent(
            String.self,
            forKey: .displayName)
        requests = try values.decodeIfPresent(Int.self, forKey: .requests) ?? 0
        tokens = try values.decodeIfPresent(Int64.self, forKey: .tokens) ?? 0
        actualCostUSD = try values.decodeIfPresent(
            Double.self,
            forKey: .actualCostUSD)
        pricedRows = try values.decodeIfPresent(
            Int.self,
            forKey: .pricedRows) ?? (actualCostUSD == nil ? 0 : requests)
        unpricedRows = try values.decodeIfPresent(
            Int.self,
            forKey: .unpricedRows) ?? (actualCostUSD == nil ? requests : 0)
    }

    var id: String { "\(provider):\(modelID ?? provider)" }
    var label: String {
        trafficModelLabel(
            displayName: displayName,
            modelID: modelID,
            fallback: provider)
    }
    var hasPartialCost: Bool {
        pricedRows > 0 && unpricedRows > 0
    }
}

struct TrafficSavings: Decodable, Equatable, Sendable {
    var bySferenceModel: [TrafficSavingsModelRow] = []
    var mappings: [TrafficSavingsMapping] = []

    enum CodingKeys: String, CodingKey {
        case bySferenceModel = "by_sference_model"
        case mappings
    }
}

struct TrafficSavingsModelRow: Decodable, Equatable, Identifiable, Sendable {
    var modelID = ""
    var displayName: String?
    var actualSferenceCostUSD: Double = 0
    var estimatedNativeCostUSD: Double = 0
    var savedUSD: Double = 0
    var savedPercent: Double = 0

    enum CodingKeys: String, CodingKey {
        case modelID = "model_id"
        case displayName = "display_name"
        case actualSferenceCostUSD = "actual_sference_cost_usd"
        case estimatedNativeCostUSD = "estimated_native_cost_usd"
        case savedUSD = "saved_usd"
        case savedPercent = "saved_percent"
    }

    var id: String { modelID }
    var label: String {
        trafficModelLabel(displayName: displayName, modelID: modelID)
    }
    var estimatedAdditionalClaudeCostUSD: Double {
        max(0, estimatedNativeCostUSD - actualSferenceCostUSD)
    }
    var hasNegativeSavings: Bool {
        estimatedNativeCostUSD < actualSferenceCostUSD
    }
}

struct TrafficSavingsMapping: Decodable, Equatable, Identifiable, Sendable {
    var sferenceModelID = ""
    var sferenceDisplayName: String?
    var requestedClaudeFamily = ""
    var actualSferenceCostUSD: Double = 0
    var estimatedNativeCostUSD: Double = 0

    enum CodingKeys: String, CodingKey {
        case sferenceModelID = "sference_model_id"
        case sferenceDisplayName = "sference_display_name"
        case requestedClaudeFamily = "requested_claude_family"
        case actualSferenceCostUSD = "actual_sference_cost_usd"
        case estimatedNativeCostUSD = "estimated_native_cost_usd"
    }

    var id: String { "\(sferenceModelID):\(requestedClaudeFamily)" }
    var label: String {
        trafficModelLabel(
            displayName: sferenceDisplayName,
            modelID: sferenceModelID)
    }
    var savedUSD: Double {
        estimatedNativeCostUSD - actualSferenceCostUSD
    }
}

struct TrafficPerformance: Decodable, Equatable, Sendable {
    var providers: [TrafficPerformanceRow] = []
    var models: [TrafficPerformanceRow] = []
}

struct TrafficPerformanceRow: Decodable, Equatable, Identifiable, Sendable {
    var provider = ""
    var modelID: String?
    var displayName: String?
    var requests = 0
    var tokens: Int64 = 0
    var ttftSamples = 0
    var medianTTFTMS: Double?
    var outputTPSSamples = 0
    var medianOutputTokensPerSecond: Double?

    enum CodingKeys: String, CodingKey {
        case provider
        case modelID = "model_id"
        case displayName = "display_name"
        case requests
        case tokens
        case ttftSamples = "ttft_samples"
        case medianTTFTMS = "median_ttft_ms"
        case outputTPSSamples = "output_tps_samples"
        case medianOutputTokensPerSecond =
            "median_output_tokens_per_second"
    }

    var id: String { "\(provider):\(modelID ?? provider)" }
    var label: String {
        trafficModelLabel(
            displayName: displayName,
            modelID: modelID,
            fallback: provider)
    }
    var measuredMedianTTFTMS: Double? {
        ttftSamples > 0 ? medianTTFTMS : nil
    }
    var measuredMedianOutputTokensPerSecond: Double? {
        outputTPSSamples > 0 ? medianOutputTokensPerSecond : nil
    }
}

private func trafficModelLabel(
    displayName: String?,
    modelID: String?,
    fallback: String = ""
) -> String {
    if let displayName, !displayName.isEmpty {
        return displayName
    }
    if let modelID, !modelID.isEmpty {
        return modelID
    }
    return fallback
}

struct TrafficSavingsChartGroup: Identifiable, Equatable {
    var id: String
    var label: String
    var actualSferenceCostUSD: Double
    var estimatedNativeCostUSD: Double

    var estimatedAdditionalClaudeCostUSD: Double {
        max(0, estimatedNativeCostUSD - actualSferenceCostUSD)
    }
    var hasNegativeSavings: Bool {
        estimatedNativeCostUSD < actualSferenceCostUSD
    }
}

func trafficSavingsEligibleSferenceCost(
    _ summary: TrafficCostSummary
) -> Double {
    max(
        0,
        summary.estimatedNativeCostForSferenceUSD - summary.savedUSD)
}

extension TrafficAnalyticsWindow {
    static let empty = TrafficAnalyticsWindow()
}

extension TrafficCoverage {
    static let empty = TrafficCoverage()
}

extension TrafficCost {
    static let empty = TrafficCost()
}

extension TrafficPerformance {
    static let empty = TrafficPerformance()
}

private extension KeyedDecodingContainer {
    func decodeDefault<T: Decodable>(
        _ type: T.Type,
        forKey key: Key
    ) -> T where T: TrafficDefaultValue {
        (try? decodeIfPresent(type, forKey: key)) ?? T.defaultValue
    }
}

private protocol TrafficDefaultValue {
    static var defaultValue: Self { get }
}

extension Int64: TrafficDefaultValue {
    fileprivate static var defaultValue: Int64 { 0 }
}

extension TrafficAnalyticsWindow: TrafficDefaultValue {
    fileprivate static var defaultValue: TrafficAnalyticsWindow { .empty }
}

extension TrafficCoverage: TrafficDefaultValue {
    fileprivate static var defaultValue: TrafficCoverage { .empty }
}

extension TrafficCost: TrafficDefaultValue {
    fileprivate static var defaultValue: TrafficCost { .empty }
}

extension TrafficPerformance: TrafficDefaultValue {
    fileprivate static var defaultValue: TrafficPerformance { .empty }
}
