import Foundation

public enum ControlError: LocalizedError {
    case invalidURL(String)
    case status(Int)
    case api(Int, String)

    public var errorDescription: String? {
        switch self {
        case .invalidURL(let value):
            return "invalid control URL: \(value)"
        case .status(let code):
            return "control API returned HTTP \(code)"
        case .api(_, let message):
            return message
        }
    }

    public var isNotFound: Bool {
        switch self {
        case .status(404), .api(404, _):
            return true
        default:
            return false
        }
    }
}

/// ControlClient talks to the proxy's loopback control API. It discovers the
/// URL once at launch: the environment override wins, then a saved default,
/// then `server.control_addr` from the config file.
public struct ControlClient {
    public let baseURL: URL
    private let session: URLSession
    private let streamSession: URLSession

    public init() {
        let raw = Self.resolveControlURL()
        baseURL = URL(string: raw) ?? URL(string: "http://127.0.0.1:8788")!
        session = Self.makeSession(timeout: 15, resource: 30)
        streamSession = Self.makeSession(timeout: 604_800, resource: 604_800)
    }

    /// initForTesting lets tests point at a local fixture server.
    public init(baseURL: URL) {
        self.baseURL = baseURL
        session = Self.makeSession(timeout: 5, resource: 10)
        streamSession = Self.makeSession(timeout: 604_800, resource: 604_800)
    }

    /// init(baseURL:session:) is used by tests with a stubbed URLSession.
    init(baseURL: URL, session: URLSession) {
        self.baseURL = baseURL
        self.session = session
        self.streamSession = session
    }

    private static func makeSession(timeout: TimeInterval, resource: TimeInterval) -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = timeout
        config.timeoutIntervalForResource = resource
        config.waitsForConnectivity = false
        return URLSession(configuration: config)
    }

    // MARK: - Stats and halt

    public func fetchSnapshot() async throws -> (StatsSnapshot, Data) {
        let data = try await send("/api/stats", method: "GET")
        return (try Self.decode(StatsSnapshot.self, from: data), data)
    }

    public func resume() async throws {
        _ = try await send("/api/resume", method: "POST")
    }

    // MARK: - Configuration

    public func fetchConfig() async throws -> ProxyConfig {
        let data = try await send("/api/config", method: "GET")
        return try Self.decode(ProxyConfig.self, from: data)
    }

    public func saveSettings(server: ServerSettings, openrouter: OpenRouterSettings) async throws -> ProxyConfig {
        let payload = SettingsPayload(server: server, openrouter: openrouter)
        let body = try Self.encode(payload)
        let data = try await send("/api/settings", method: "PUT", body: body)
        return try Self.decode(ProxyConfig.self, from: data)
    }

    // MARK: - Filters

    public func createFilter(_ filter: FilterRule) async throws -> FilterRule {
        let data = try await send("/api/filters", method: "POST", body: try Self.encode(filter))
        return try Self.decode(FilterRule.self, from: data)
    }

    public func updateFilter(name: String, _ filter: FilterRule) async throws -> FilterRule {
        let data = try await send("/api/filters/\(Self.segment(name))", method: "PUT", body: try Self.encode(filter))
        return try Self.decode(FilterRule.self, from: data)
    }

    public func deleteFilter(name: String) async throws {
        _ = try await send("/api/filters/\(Self.segment(name))", method: "DELETE")
    }

    public func createRedaction(_ rule: RedactionRule) async throws -> RedactionRule {
        let data = try await send("/api/redactions/rules", method: "POST", body: try Self.encode(rule))
        return try Self.decode(RedactionRule.self, from: data)
    }

    public func updateRedaction(name: String, _ rule: RedactionRule) async throws -> RedactionRule {
        let data = try await send("/api/redactions/rules/\(Self.segment(name))", method: "PUT", body: try Self.encode(rule))
        return try Self.decode(RedactionRule.self, from: data)
    }

    public func deleteRedaction(name: String) async throws {
        _ = try await send("/api/redactions/rules/\(Self.segment(name))", method: "DELETE")
    }

    public func testFilter(pattern: String, patternFile: String, replacement: String, sample: String) async throws -> FilterTestResult {
        let payload = TestPayload(
            pattern: pattern,
            patternFile: patternFile,
            replacement: replacement,
            sample: sample
        )
        let data = try await send("/api/filters/test", method: "POST", body: try Self.encode(payload))
        return try Self.decode(FilterTestResult.self, from: data)
    }

    // MARK: - Models

    public func fetchModels() async throws -> [CatalogModel] {
        let data = try await send("/api/models", method: "GET")
        return try Self.decode([CatalogModel].self, from: data)
    }

    public func createModel(_ model: CatalogModel) async throws -> CatalogModel {
        let data = try await send("/api/models", method: "POST", body: try Self.encode(model))
        return try Self.decode(CatalogModel.self, from: data)
    }

    public func updateModel(id: String, _ model: CatalogModel) async throws -> CatalogModel {
        let data = try await send(Self.modelPath(id), method: "PUT", body: try Self.encode(model))
        return try Self.decode(CatalogModel.self, from: data)
    }

    public func deleteModel(id: String) async throws {
        _ = try await send(Self.modelPath(id), method: "DELETE")
    }

    public func fetchModel(id: String) async throws -> CatalogModel {
        let data = try await send("/api/models/fetch", method: "POST", body: try Self.encode(FetchModelPayload(id: id)))
        return try Self.decode(CatalogModel.self, from: data)
    }

    // MARK: - Redactions

    public func fetchRedactions() async throws -> [RedactionInstance] {
        let data = try await send("/api/redactions", method: "GET")
        return try Self.decode([RedactionInstance].self, from: data)
    }

    // MARK: - Traffic

    public func fetchTraffic(limit: Int = 500) async throws -> [TrafficEntry] {
        let data = try await send("/api/traffic?limit=\(limit)", method: "GET")
        return try Self.decode([TrafficEntry].self, from: data)
    }

    public func fetchTrafficEntry(id: UInt64) async throws -> TrafficEntry {
        let data = try await send("/api/traffic/\(id)", method: "GET")
        return try Self.decode(TrafficEntry.self, from: data)
    }

    /// trafficStream consumes the SSE traffic endpoint. The stream finishes
    /// when the connection ends; callers reconnect after a short delay.
    public func trafficStream() -> AsyncThrowingStream<TrafficEntry, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    var req = URLRequest(url: endpoint("/api/traffic/stream"))
                    req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                    let (bytes, response) = try await streamSession.bytes(for: req)
                    guard let http = response as? HTTPURLResponse else {
                        throw ControlError.status(0)
                    }
                    guard http.statusCode == 200 else {
                        throw ControlError.status(http.statusCode)
                    }
                    for try await line in bytes.lines {
                        guard line.hasPrefix("data:") else { continue }
                        let payload = line.dropFirst(5).trimmingCharacters(in: .whitespaces)
                        guard !payload.isEmpty, let data = payload.data(using: .utf8) else { continue }
                        if let entry = try? Self.decode(TrafficEntry.self, from: data) {
                            continuation.yield(entry)
                        }
                    }
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: - Transport

    private func endpoint(_ path: String) -> URL {
        URL(string: baseURL.absoluteString + path) ?? baseURL
    }

    private func send(_ path: String, method: String, body: Data? = nil) async throws -> Data {
        var req = URLRequest(url: endpoint(path))
        req.httpMethod = method
        req.cachePolicy = .reloadIgnoringLocalCacheData
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw ControlError.status(0)
        }
        guard (200..<300).contains(http.statusCode) else {
            throw Self.error(from: http.statusCode, data: data)
        }
        return data
    }

    private static func error(from status: Int, data: Data) -> ControlError {
        struct APIError: Decodable {
            let error: String
        }
        if let parsed = try? decode(APIError.self, from: data), !parsed.error.isEmpty {
            return .api(status, parsed.error)
        }
        return .status(status)
    }

    private static func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(type, from: data)
    }

    private static func encode<T: Encodable>(_ value: T) throws -> Data {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        return try encoder.encode(value)
    }

    /// segment escapes one path component, preserving no slashes.
    private static func segment(_ value: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }

    /// modelPath keeps the '/' separators in an author/slug id literal, because
    /// the server's {id...} route captures them.
    private static func modelPath(_ id: String) -> String {
        "/api/models/" + id.split(separator: "/", omittingEmptySubsequences: false)
            .map { segment(String($0)) }
            .joined(separator: "/")
    }

    /// resolveControlURL returns an http URL for the control API. An explicit
    /// `LLM_PROXY_CONTROL_URL` or `controlURL` default overrides discovery.
    public static func resolveControlURL() -> String {
        if let override = ProcessInfo.processInfo.environment["LLM_PROXY_CONTROL_URL"],
           let url = normalize(override) {
            return url
        }
        if let saved = UserDefaults.standard.string(forKey: "controlURL"),
           let url = normalize(saved) {
            return url
        }
        if let addr = configControlAddr(), let url = normalize(addr) {
            return url
        }
        return "http://127.0.0.1:8788"
    }

    /// configControlAddr reads server.control_addr from the standard config
    /// location. The tray must follow a non-default address without requiring
    /// duplicate settings.
    private static func configControlAddr() -> String? {
        guard let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            return nil
        }
        let path = support.appendingPathComponent("llm-proxy/config.json")
        guard let data = try? Data(contentsOf: path) else {
            return nil
        }
        struct ConfigFile: Decodable {
            struct Server: Decodable {
                let control_addr: String?
            }
            let server: Server?
        }
        guard let cfg = try? JSONDecoder().decode(ConfigFile.self, from: data) else {
            return nil
        }
        return cfg.server?.control_addr
    }

    /// normalize accepts a listen address (`127.0.0.1:8788`, `:8788`,
    /// `0.0.0.0:8788`) or a full URL and returns an http URL string.
    static func normalize(_ value: String) -> String? {
        var s = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return nil }
        if s.hasPrefix(":") {
            s = "127.0.0.1" + s
        } else if s.hasPrefix("0.0.0.0:") {
            s = "127.0.0.1:" + String(s.dropFirst("0.0.0.0:".count))
        } else if s.hasPrefix("[::]:") {
            s = "127.0.0.1:" + String(s.dropFirst("[::]:".count))
        }
        if !s.contains("://") {
            s = "http://" + s
        }
        guard let url = URL(string: s), url.host != nil else { return nil }
        return s
    }
}

private struct SettingsPayload: Encodable {
    let server: ServerSettings
    let openrouter: OpenRouterSettings
}

private struct TestPayload: Encodable {
    let pattern: String
    let patternFile: String
    let replacement: String
    let sample: String
}

private struct FetchModelPayload: Encodable {
    let id: String
}
