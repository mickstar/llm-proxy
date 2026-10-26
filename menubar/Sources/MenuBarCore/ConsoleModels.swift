import Foundation

/// ProxyConfig mirrors `GET /api/config`.
public struct ProxyConfig: Decodable, Equatable {
    public var server: ServerSettings
    public var openrouter: OpenRouterSettings
    public var models: [CatalogModel]
    public var filters: [FilterRule]
    public var redactions: [RedactionRule]

    public init(server: ServerSettings, openrouter: OpenRouterSettings, models: [CatalogModel], filters: [FilterRule], redactions: [RedactionRule]) {
        self.server = server
        self.openrouter = openrouter
        self.models = models
        self.filters = filters
        self.redactions = redactions
    }
}

public struct ServerSettings: Codable, Equatable {
    public var proxyAddr: String
    public var controlAddr: String
    public var modelPolicy: String
    public var trafficLogCapacity: Int

    public init(proxyAddr: String, controlAddr: String, modelPolicy: String, trafficLogCapacity: Int) {
        self.proxyAddr = proxyAddr
        self.controlAddr = controlAddr
        self.modelPolicy = modelPolicy
        self.trafficLogCapacity = trafficLogCapacity
    }
}

public struct OpenRouterSettings: Codable, Equatable {
    public var baseUrl: String
    public var apiKey: String

    public init(baseUrl: String, apiKey: String) {
        self.baseUrl = baseUrl
        self.apiKey = apiKey
    }
}

public struct Pricing: Codable, Equatable {
    public var prompt: String
    /// Per-token rate for prompt tokens served from the provider's prompt
    /// cache. Empty means the prompt rate applies.
    public var cache: String
    public var completion: String

    public init(prompt: String, cache: String = "", completion: String) {
        self.prompt = prompt
        self.cache = cache
        self.completion = completion
    }

    private enum CodingKeys: String, CodingKey {
        case prompt, cache, completion
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        prompt = try container.decodeIfPresent(String.self, forKey: .prompt) ?? ""
        cache = try container.decodeIfPresent(String.self, forKey: .cache) ?? ""
        completion = try container.decodeIfPresent(String.self, forKey: .completion) ?? ""
    }
}

public struct CatalogModel: Codable, Equatable, Identifiable {
    public var id: String
    public var name: String
    public var contextLength: Int
    public var pricing: Pricing
    public var supportedParameters: [String]
    public var enabled: Bool

    public init(id: String, name: String, contextLength: Int, pricing: Pricing, supportedParameters: [String], enabled: Bool) {
        self.id = id
        self.name = name
        self.contextLength = contextLength
        self.pricing = pricing
        self.supportedParameters = supportedParameters
        self.enabled = enabled
    }
}

public struct FilterRule: Codable, Equatable, Identifiable {
    public var name: String
    public var pattern: String
    public var patternFile: String?
    public var action: String
    public var replacement: String?
    public var enabled: Bool

    public var id: String { name }

    public init(name: String, pattern: String, patternFile: String? = nil, action: String, replacement: String? = nil, enabled: Bool) {
        self.name = name
        self.pattern = pattern
        self.patternFile = patternFile
        self.action = action
        self.replacement = replacement
        self.enabled = enabled
    }
}

public struct RedactionRule: Codable, Equatable, Identifiable {
    public var name: String
    public var pattern: String
    public var template: String
    public var enabled: Bool

    public var id: String { name }

    public init(name: String, pattern: String, template: String, enabled: Bool) {
        self.name = name
        self.pattern = pattern
        self.template = template
        self.enabled = enabled
    }
}

/// RuleKind distinguishes a content filter (block/replace) from a reversible
/// redaction rule. Both appear in the console's rule list.
public enum RuleKind: String, CaseIterable {
    case filter
    case redaction
}

/// RedactionInstance is one recorded alias from `GET /api/redactions`.
public struct RedactionInstance: Decodable, Identifiable {
    public let alias: String
    public let original: String
    public let rule: String
    public let count: Int
    public let firstSeen: String
    public let conflict: Bool?

    public var id: String { alias }

    public init(alias: String, original: String, rule: String, count: Int, firstSeen: String, conflict: Bool? = nil) {
        self.alias = alias
        self.original = original
        self.rule = rule
        self.count = count
        self.firstSeen = firstSeen
        self.conflict = conflict
    }
}

public struct FilterTestResult: Decodable, Equatable {
    public let matches: Int
    public let matched: [String]
    public let result: String

    public init(matches: Int, matched: [String], result: String) {
        self.matches = matches
        self.matched = matched
        self.result = result
    }
}

/// TrafficEntry mirrors one control-API traffic record. Token, cost, body, and
/// summary fields are optional because the server omits zero values.
public struct TrafficEntry: Decodable, Identifiable, Equatable {
    public let id: UInt64
    public let time: String
    public let method: String
    public let path: String
    public let model: String
    public let status: Int
    public let durationMs: Int64
    public let requestSize: Int
    public let responseSize: Int
    public let promptTokens: Int?
    public let completionTokens: Int?
    public let totalTokens: Int?
    public let costUsd: Double?
    public let filterSummary: String?
    public let redactionSummary: String?
    public let notContentFiltered: Bool?
    public let blocked: Bool?
    public let requestBody: String?
    public let requestTruncated: Bool?
    public let responseBody: String?
    public let responseTruncated: Bool?

    public var tokens: Int { totalTokens ?? 0 }
    public var cost: Double { costUsd ?? 0 }
    public var requestText: String { requestBody ?? "" }
    public var responseText: String { responseBody ?? "" }

    public init(
        id: UInt64,
        time: String,
        method: String,
        path: String,
        model: String,
        status: Int,
        durationMs: Int64,
        requestSize: Int,
        responseSize: Int,
        promptTokens: Int? = nil,
        completionTokens: Int? = nil,
        totalTokens: Int? = nil,
        costUsd: Double? = nil,
        filterSummary: String? = nil,
        redactionSummary: String? = nil,
        notContentFiltered: Bool? = nil,
        blocked: Bool? = nil,
        requestBody: String? = nil,
        requestTruncated: Bool? = nil,
        responseBody: String? = nil,
        responseTruncated: Bool? = nil
    ) {
        self.id = id
        self.time = time
        self.method = method
        self.path = path
        self.model = model
        self.status = status
        self.durationMs = durationMs
        self.requestSize = requestSize
        self.responseSize = responseSize
        self.promptTokens = promptTokens
        self.completionTokens = completionTokens
        self.totalTokens = totalTokens
        self.costUsd = costUsd
        self.filterSummary = filterSummary
        self.redactionSummary = redactionSummary
        self.notContentFiltered = notContentFiltered
        self.blocked = blocked
        self.requestBody = requestBody
        self.requestTruncated = requestTruncated
        self.responseBody = responseBody
        self.responseTruncated = responseTruncated
    }
}

/// FilterDraft is the editable form state for a content filter, with the same
/// validation the server applies before saving.
public struct FilterDraft: Equatable {
    public var name = ""
    public var pattern = ""
    public var patternFile = ""
    public var action = "block"
    public var replacement = ""
    public var enabled = true

    public init() {}

    public init(rule: FilterRule) {
        name = rule.name
        pattern = rule.pattern
        patternFile = rule.patternFile ?? ""
        action = rule.action
        replacement = rule.replacement ?? ""
        enabled = rule.enabled
    }

    public init(redaction: RedactionRule) {
        name = redaction.name
        pattern = redaction.pattern
        patternFile = ""
        action = FilterDraft.redactAction
        replacement = redaction.template
        enabled = redaction.enabled
    }

    /// redactAction selects a reversible redaction rule instead of a filter.
    public static let redactAction = "redact"

    /// kind reports which config section a draft belongs to.
    public var kind: RuleKind {
        action == FilterDraft.redactAction ? .redaction : .filter
    }

    public func validationError() -> String? {
        if name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Name is required."
        }
        let hasPattern = !pattern.isEmpty
        let hasFile = !patternFile.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        if !hasPattern && !hasFile {
            return "Enter a pattern or a pattern file."
        }
        if hasPattern && hasFile {
            return "Pattern and pattern file are mutually exclusive."
        }
        switch action {
        case "block":
            if !replacement.isEmpty {
                return "A block filter cannot define a replacement."
            }
        case "replace":
            if replacement.isEmpty {
                return "A replace filter requires a replacement."
            }
        case FilterDraft.redactAction:
            if hasFile {
                return "A redaction cannot use a pattern file."
            }
            if replacement.isEmpty {
                return "A redaction requires a template."
            }
        default:
            return "Choose block, replace, or redact."
        }
        return nil
    }

    public func rule() -> FilterRule {
        FilterRule(
            name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            pattern: pattern,
            patternFile: patternFile.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : patternFile.trimmingCharacters(in: .whitespacesAndNewlines),
            action: action,
            replacement: action == "replace" ? replacement : nil,
            enabled: enabled
        )
    }

    public func redactionRule() -> RedactionRule {
        RedactionRule(
            name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            pattern: pattern,
            template: replacement,
            enabled: enabled
        )
    }

    /// suggestedReplacement mirrors the TUI's $R templates.
    public func suggestedReplacement() -> String {
        let source = pattern.isEmpty ? patternFile : pattern
        if source.contains("@") {
            return "redacted$R@gmail.com"
        }
        return "[REDACTED-$R]"
    }
}
