import Foundation

/// StatsSnapshot mirrors the JSON returned by `GET /api/stats`. Dates stay as
/// strings: the menu renders uptime from `uptimeSeconds` and never needs date
/// arithmetic.
public struct StatsSnapshot: Decodable {
    public let since: String?
    public let uptimeSeconds: Int64
    public let activeRequests: Int64
    public let totals: Totals
    public let models: [ModelTotals]
    public let throughput: Throughput
    public let activity: [ActivityBucket]
    public let last: LastRequest?
    public let persistError: String?
    public let halt: HaltStatus
}

public struct Totals: Decodable {
    public let requests: Int64
    public let promptTokens: Int64
    public let completionTokens: Int64
    public let totalTokens: Int64
    public let costUsd: Double
    public let errors: Int64
    public let blocked: Int64
}

public struct ModelTotals: Decodable {
    public let model: String
    public let requests: Int64
    public let promptTokens: Int64
    public let completionTokens: Int64
    public let totalTokens: Int64
    public let costUsd: Double
    public let errors: Int64
    public let lastUsed: String?
}

public struct Throughput: Decodable {
    public let windowSeconds: Int
    public let completionTokensPerSecond: Double
    public let totalTokensPerSecond: Double
    public let requestsPerMinute: Double
}

public struct ActivityBucket: Decodable {
    public let minute: String
    public let requests: Int64
    public let tokens: Int64
}

public struct LastRequest: Decodable {
    public let at: String
    public let model: String
    public let completionTokens: Int
    public let totalTokens: Int
    public let costUsd: Double
    public let durationMs: Int64
    public let tokensPerSecond: Double
}

public struct HaltStatus: Decodable {
    public let halted: Bool
    public let haltedAt: String?
    public let haltedFilter: String?
    public let refused: Int
}
