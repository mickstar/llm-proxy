import XCTest
@testable import MenuBarCore

final class ConsoleClientTests: XCTestCase {
    private var client: ControlClient!

    override func setUp() {
        super.setUp()
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: config)
        client = ControlClient(baseURL: URL(string: "http://127.0.0.1:8788")!, session: session)
    }

    override func tearDown() {
        StubURLProtocol.handler = nil
        super.tearDown()
    }

    func testCreateFilterSendsSnakeCaseAndDecodes() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/filters")
            let body = try XCTUnwrap(StubURLProtocol.bodyData(request))
            let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(object["name"] as? String, "my email")
            XCTAssertEqual(object["pattern"] as? String, "\\S+@\\S+")
            XCTAssertEqual(object["action"] as? String, "replace")
            XCTAssertEqual(object["enabled"] as? Bool, true)
            XCTAssertNil(object["pattern_file"])
            return (201, """
            {"name":"my email","pattern":"\\\\S+@\\\\S+","action":"replace","replacement":"x$R","enabled":true}
            """.data(using: .utf8)!)
        }
        let filter = FilterRule(
            name: "my email",
            pattern: "\\S+@\\S+",
            action: "replace",
            replacement: "x$R",
            enabled: true
        )
        let saved = try await client.createFilter(filter)
        XCTAssertEqual(saved.name, "my email")
        XCTAssertEqual(saved.replacement, "x$R")
        XCTAssertTrue(saved.enabled)
    }

    func testFetchConfigDecodesAllSections() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.url?.path, "/api/config")
            return (200, """
            {
              "server":{"proxy_addr":"127.0.0.1:8787","control_addr":"127.0.0.1:8788","model_policy":"allowlist","traffic_log_capacity":250},
              "openrouter":{"base_url":"https://openrouter.ai/api/v1","api_key":"sk-or-test"},
              "models":[{"id":"a/b","name":"AB","context_length":123,"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":["temperature"],"enabled":true}],
              "filters":[],
              "redactions":[{"name":"email","pattern":"x","template":"y","enabled":true}]
            }
            """.data(using: .utf8)!)
        }
        let config = try await client.fetchConfig()
        XCTAssertEqual(config.server.modelPolicy, "allowlist")
        XCTAssertEqual(config.server.trafficLogCapacity, 250)
        XCTAssertEqual(config.openrouter.apiKey, "sk-or-test")
        XCTAssertEqual(config.models.first?.contextLength, 123)
        XCTAssertEqual(config.redactions.first?.template, "y")
    }

    func testUpdateModelKeepsSlashSeparators() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/api/models/deepseek/deepseek-v4.1-flash")
            return (200, """
            {"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek","context_length":10,"pricing":{"prompt":"1","completion":"2"},"supported_parameters":[],"enabled":false}
            """.data(using: .utf8)!)
        }
        let model = CatalogModel(
            id: "deepseek/deepseek-v4.1-flash",
            name: "DeepSeek",
            contextLength: 10,
            pricing: Pricing(prompt: "1", completion: "2"),
            supportedParameters: [],
            enabled: false
        )
        let saved = try await client.updateModel(id: model.id, model)
        XCTAssertEqual(saved.id, model.id)
        XCTAssertFalse(saved.enabled)
    }

    func testFilterTestPayloadAndResult() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.url?.path, "/api/filters/test")
            let body = try XCTUnwrap(StubURLProtocol.bodyData(request))
            let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(object["pattern"] as? String, "sk-\\w+")
            XCTAssertEqual(object["replacement"] as? String, "[$R]")
            XCTAssertEqual(object["sample"] as? String, "sk-live-abc")
            return (200, """
            {"matches":1,"matched":["sk-live-abc"],"result":"[sk-live-abc]"}
            """.data(using: .utf8)!)
        }
        let result = try await client.testFilter(
            pattern: "sk-\\w+",
            patternFile: "",
            replacement: "[$R]",
            sample: "sk-live-abc"
        )
        XCTAssertEqual(result.matches, 1)
        XCTAssertEqual(result.matched, ["sk-live-abc"])
        XCTAssertEqual(result.result, "[sk-live-abc]")
    }

    func testSaveSettingsSendsBothSections() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/api/settings")
            let body = try XCTUnwrap(StubURLProtocol.bodyData(request))
            let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            let server = try XCTUnwrap(object["server"] as? [String: Any])
            let openrouter = try XCTUnwrap(object["openrouter"] as? [String: Any])
            XCTAssertEqual(server["model_policy"] as? String, "allowlist")
            XCTAssertEqual(server["traffic_log_capacity"] as? Int, 300)
            XCTAssertEqual(openrouter["api_key"] as? String, "sk-or-new")
            return (200, """
            {"server":{"proxy_addr":"127.0.0.1:8787","control_addr":"127.0.0.1:8788","model_policy":"allowlist","traffic_log_capacity":300},
             "openrouter":{"base_url":"https://openrouter.ai/api/v1","api_key":"sk-or-new"},
             "models":[],"filters":[],"redactions":[]}
            """.data(using: .utf8)!)
        }
        let config = try await client.saveSettings(
            server: ServerSettings(proxyAddr: "127.0.0.1:8787", controlAddr: "127.0.0.1:8788", modelPolicy: "allowlist", trafficLogCapacity: 300),
            openrouter: OpenRouterSettings(baseUrl: "https://openrouter.ai/api/v1", apiKey: "sk-or-new")
        )
        XCTAssertEqual(config.server.modelPolicy, "allowlist")
        XCTAssertEqual(config.openrouter.apiKey, "sk-or-new")
    }

    func testFetchModelPostsID() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/models/fetch")
            let body = try XCTUnwrap(StubURLProtocol.bodyData(request))
            let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(object["id"] as? String, "a/b")
            return (200, """
            {"id":"a/b","name":"AB","context_length":1,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":[],"enabled":true}
            """.data(using: .utf8)!)
        }
        let model = try await client.fetchModel(id: "a/b")
        XCTAssertEqual(model.id, "a/b")
    }

    func testDeleteFilterEscapesOnePathSegment() async throws {
        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url?.path, "/api/filters/my email")
            XCTAssertTrue(request.url?.absoluteString.contains("my%20email") == true)
            return (204, Data())
        }
        try await client.deleteFilter(name: "my email")
    }

    func testAPIErrorCarriesServerMessage() async {
        StubURLProtocol.handler = { _ in
            (400, #"{"error":"filter \"x\": pattern: invalid"}"#.data(using: .utf8)!)
        }
        do {
            _ = try await client.createFilter(FilterRule(name: "x", pattern: "(", action: "block", enabled: true))
            XCTFail("expected an error")
        } catch let error as ControlError {
            XCTAssertTrue((error.errorDescription ?? "").contains("invalid"))
            XCTAssertFalse(error.isNotFound)
        } catch {
            XCTFail("wrong error: \(error)")
        }
    }

    func testTrafficEntryDefaultsMissingOptionalFields() throws {
        let data = #"{"id":7,"time":"2026-10-24T19:00:00+11:00","method":"POST","path":"/v1/chat/completions","model":"m","status":200,"duration_ms":12,"request_size":10,"response_size":20}"#.data(using: .utf8)!
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        let entry = try decoder.decode(TrafficEntry.self, from: data)
        XCTAssertEqual(entry.id, 7)
        XCTAssertEqual(entry.tokens, 0)
        XCTAssertEqual(entry.cost, 0)
        XCTAssertEqual(entry.requestText, "")
    }

    func testFilterDraftValidationAndSuggestion() {
        var draft = FilterDraft()
        XCTAssertEqual(draft.validationError(), "Name is required.")
        draft.name = "email"
        XCTAssertEqual(draft.validationError(), "Enter a pattern or a pattern file.")
        draft.pattern = "\\S+@\\S+"
        XCTAssertNil(draft.validationError())
        draft.action = "replace"
        XCTAssertEqual(draft.validationError(), "A replace filter requires a replacement.")
        draft.replacement = draft.suggestedReplacement()
        XCTAssertEqual(draft.replacement, "redacted$R@gmail.com")
        draft.patternFile = "/tmp/x"
        XCTAssertEqual(draft.validationError(), "Pattern and pattern file are mutually exclusive.")
    }

    func testRedactionDraftRoundTrip() {
        let rule = RedactionRule(name: "userpath", pattern: "/Users/jane.doe", template: "/Users/john.smith", enabled: true)
        var draft = FilterDraft(redaction: rule)
        XCTAssertEqual(draft.action, "redact")
        XCTAssertEqual(draft.kind, .redaction)
        XCTAssertEqual(draft.patternFile, "")
        XCTAssertNil(draft.validationError())
        XCTAssertEqual(draft.redactionRule(), rule)

        draft.replacement = ""
        XCTAssertEqual(draft.validationError(), "A redaction requires a template.")
        draft.replacement = "/Users/john.smith"
        draft.patternFile = "/tmp/key"
        XCTAssertEqual(draft.validationError(), "Pattern and pattern file are mutually exclusive.")
        draft.pattern = ""
        XCTAssertEqual(draft.validationError(), "A redaction cannot use a pattern file.")
    }

    func testRedactionRuleRequestsUseDedicatedPath() async throws {
        let rule = RedactionRule(name: "userpath", pattern: "/Users/jane.doe", template: "/Users/john.smith", enabled: true)

        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/api/redactions/rules")
            let body = try XCTUnwrap(StubURLProtocol.bodyData(request))
            let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(object["name"] as? String, "userpath")
            XCTAssertEqual(object["pattern"] as? String, "/Users/jane.doe")
            XCTAssertEqual(object["template"] as? String, "/Users/john.smith")
            XCTAssertEqual(object["enabled"] as? Bool, true)
            return (201, """
            {"name":"userpath","pattern":"/Users/jane.doe","template":"/Users/john.smith","enabled":true}
            """.data(using: .utf8)!)
        }
        let created = try await client.createRedaction(rule)
        XCTAssertEqual(created, rule)

        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/api/redactions/rules/userpath")
            return (200, """
            {"name":"userpath","pattern":"/home/michael","template":"/home/john.smith","enabled":false}
            """.data(using: .utf8)!)
        }
        var changed = rule
        changed.pattern = "/home/michael"
        changed.template = "/home/john.smith"
        changed.enabled = false
        let updated = try await client.updateRedaction(name: "userpath", changed)
        XCTAssertEqual(updated, changed)

        StubURLProtocol.handler = { request in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url?.path, "/api/redactions/rules/userpath")
            return (204, Data())
        }
        try await client.deleteRedaction(name: "userpath")
    }
}

// MARK: - Stub transport

final class StubURLProtocol: URLProtocol {
    static var handler: ((URLRequest) throws -> (Int, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (status, data) = try handler(request)
            let response = HTTPURLResponse(
                url: request.url!,
                statusCode: status,
                httpVersion: "HTTP/1.1",
                headerFields: ["Content-Type": "application/json"]
            )!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if !data.isEmpty {
                client?.urlProtocol(self, didLoad: data)
            }
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}

    static func bodyData(_ request: URLRequest) -> Data? {
        if let body = request.httpBody {
            return body
        }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        let bufferSize = 4096
        var buffer = [UInt8](repeating: 0, count: bufferSize)
        while stream.hasBytesAvailable {
            let read = stream.read(&buffer, maxLength: bufferSize)
            if read <= 0 { break }
            data.append(buffer, count: read)
        }
        return data
    }
}
