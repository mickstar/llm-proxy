import XCTest
@testable import MenuBarCore

final class PresentationTests: XCTestCase {
    private let fixture = """
    {
      "since": "2026-10-24T18:30:30.548196+11:00",
      "uptime_seconds": 3725,
      "active_requests": 1,
      "totals": {
        "requests": 1234, "prompt_tokens": 800000, "completion_tokens": 400000,
        "total_tokens": 1200000, "cost_usd": 0.00875,
        "errors": 2, "blocked": 0,
        "request_bytes": 100, "response_bytes": 200
      },
      "models": [{
        "model": "deepseek/deepseek-v4.1-flash", "requests": 42,
        "prompt_tokens": 100, "completion_tokens": 200, "total_tokens": 300,
        "cost_usd": 0.0085, "errors": 0, "blocked": 0,
        "last_used": "2026-10-24T18:30:30Z"
      }],
      "throughput": {
        "window_seconds": 60, "completion_tokens_per_second": 12.5,
        "total_tokens_per_second": 20, "requests_per_minute": 3
      },
      "activity": [
        {"minute": "2026-10-24T18:29:00Z", "requests": 0, "tokens": 0},
        {"minute": "2026-10-24T18:30:00Z", "requests": 1, "tokens": 3}
      ],
      "last": {
        "at": "2026-10-24T18:30:30Z", "model": "deepseek/deepseek-v4.1-flash",
        "completion_tokens": 128, "total_tokens": 200,
        "cost_usd": 0.00042, "duration_ms": 3100, "tokens_per_second": 41.29
      },
      "halt": {
        "halted": true, "halted_at": "2026-10-24T18:31:00Z",
        "halted_filter": "secret", "refused": 2
      }
    }
    """.data(using: .utf8)!

    func testDecodesControlResponse() throws {
        let s = try Presentation.decode(fixture)
        XCTAssertEqual(s.totals.requests, 1234)
        XCTAssertEqual(s.totals.totalTokens, 1_200_000)
        XCTAssertEqual(s.totals.costUsd, 0.00875, accuracy: 0.0000001)
        XCTAssertEqual(s.throughput.completionTokensPerSecond, 12.5, accuracy: 0.001)
        XCTAssertEqual(s.models.first?.model, "deepseek/deepseek-v4.1-flash")
        let last = try XCTUnwrap(s.last)
        XCTAssertEqual(last.durationMs, 3100)
        XCTAssertEqual(last.tokensPerSecond, 41.29, accuracy: 0.001)
        XCTAssertTrue(s.halt.halted)
        XCTAssertEqual(s.halt.haltedFilter, "secret")
        XCTAssertEqual(s.halt.refused, 2)
    }

    func testRendersMenuLabels() throws {
        let s = try Presentation.decode(fixture)
        XCTAssertEqual(Presentation.statusTitle(s), "12.5 t/s")
        XCTAssertEqual(Presentation.stateLine(s), "Halted — secret")
        XCTAssertEqual(Presentation.throughputLine(s), "12.5 tok/s · 3.0 req/min (last 60s)")
        XCTAssertEqual(Presentation.totalsLine(s), "Total 1.20M tok · $0.00875 · prompt 800.0k · completion 400.0k")
        XCTAssertEqual(Presentation.requestsLine(s), "Requests 1,234 · errors 2 · blocked 0")
        XCTAssertEqual(Presentation.uptimeLine(s), "Uptime 1h 2m · since 2026-10-24")
        XCTAssertEqual(Presentation.lastLine(s), "Last deepseek/deepseek-v4.1-flash · 128 tok in 3.1s (41.3 tok/s)")
        XCTAssertEqual(Presentation.modelLine(s.models[0]), "deepseek/deepseek-v4.1-flash — 300 tok · 42 req")
        XCTAssertEqual(Presentation.activitySparkline(s), "Activity (last 30m)  ▁█")
        XCTAssertEqual(Presentation.haltLine(s), "Proxy halted — secret (2 refused)")
    }

    func testCompactNumbers() {
        XCTAssertEqual(Presentation.compact(0), "0")
        XCTAssertEqual(Presentation.compact(999), "999")
        XCTAssertEqual(Presentation.compact(1_000), "1.00k")
        XCTAssertEqual(Presentation.compact(12_345), "12.3k")
        XCTAssertEqual(Presentation.compact(999_999), "1.00M")
        XCTAssertEqual(Presentation.compact(1_200_000), "1.20M")
        XCTAssertEqual(Presentation.compact(12_000_000), "12.0M")
        XCTAssertEqual(Presentation.compact(1_500_000_000), "1.50B")
    }

    func testMoney() {
        XCTAssertEqual(Presentation.money(0), "$0.000000")
        XCTAssertEqual(Presentation.money(0.0000123), "$0.000012")
        XCTAssertEqual(Presentation.money(0.000789), "$0.00079")
        XCTAssertEqual(Presentation.money(0.875), "$0.8750")
        XCTAssertEqual(Presentation.money(12.3456), "$12.346")
        XCTAssertEqual(Presentation.money(1234.5), "$1234.50")
    }

    func testDuration() {
        XCTAssertEqual(Presentation.duration(0), "0s")
        XCTAssertEqual(Presentation.duration(59), "59s")
        XCTAssertEqual(Presentation.duration(60), "1m 0s")
        XCTAssertEqual(Presentation.duration(3_725), "1h 2m")
        XCTAssertEqual(Presentation.duration(90_000), "1d 1h")
    }

    func testControlURLNormalization() {
        XCTAssertEqual(ControlClient.normalize("127.0.0.1:8788"), "http://127.0.0.1:8788")
        XCTAssertEqual(ControlClient.normalize(":8788"), "http://127.0.0.1:8788")
        XCTAssertEqual(ControlClient.normalize("0.0.0.0:9000"), "http://127.0.0.1:9000")
        XCTAssertEqual(ControlClient.normalize("[::]:9000"), "http://127.0.0.1:9000")
        XCTAssertEqual(ControlClient.normalize("http://localhost:8788"), "http://localhost:8788")
        XCTAssertNil(ControlClient.normalize("  "))
    }
}
