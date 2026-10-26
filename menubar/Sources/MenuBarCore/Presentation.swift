import Foundation

/// Presentation turns a stats snapshot into the short strings the menu bar
/// shows. Keeping it pure makes the labels testable without a window server.
public enum Presentation {
    /// decode parses the control API response. `duration_ms` maps to
    /// `durationMs` under the snake-case strategy, which is why the model uses
    /// that spelling.
    public static func decode(_ data: Data) throws -> StatsSnapshot {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(StatsSnapshot.self, from: data)
    }

    public static func statusTitle(_ s: StatsSnapshot) -> String {
        String(format: "%.1f t/s", s.throughput.completionTokensPerSecond)
    }

    public static func stateLine(_ s: StatsSnapshot) -> String {
        if s.halt.halted {
            return "Halted — \(s.halt.haltedFilter ?? "content filter")"
        }
        return "Running · \(s.activeRequests) active"
    }

    public static func throughputLine(_ s: StatsSnapshot) -> String {
        String(
            format: "%.1f tok/s · %.1f req/min (last %ds)",
            s.throughput.completionTokensPerSecond,
            s.throughput.requestsPerMinute,
            s.throughput.windowSeconds
        )
    }

    public static func totalsLine(_ s: StatsSnapshot) -> String {
        "Total \(compact(s.totals.totalTokens)) tok · \(money(s.totals.costUsd)) · prompt \(compact(s.totals.promptTokens)) · completion \(compact(s.totals.completionTokens))"
    }

    public static func requestsLine(_ s: StatsSnapshot) -> String {
        "Requests \(grouped(s.totals.requests)) · errors \(grouped(s.totals.errors)) · blocked \(grouped(s.totals.blocked))"
    }

    public static func uptimeLine(_ s: StatsSnapshot) -> String {
        if let err = s.persistError {
            return "Stats save failed: \(err)"
        }
        return "Uptime \(duration(s.uptimeSeconds))" + sinceText(s.since)
    }

    public static func lastLine(_ s: StatsSnapshot) -> String {
        guard let last = s.last, last.totalTokens > 0 else {
            return "No token usage recorded yet"
        }
        var text = "Last \(last.model) · \(grouped(Int64(last.completionTokens))) tok"
        if last.durationMs > 0 {
            text += String(format: " in %.1fs (%.1f tok/s)", Double(last.durationMs) / 1000, last.tokensPerSecond)
        }
        return text
    }

    public static func modelLine(_ m: ModelTotals) -> String {
        "\(m.model) — \(compact(m.totalTokens)) tok · \(grouped(m.requests)) req"
    }

    /// activitySparkline renders the last 30 minute buckets as block glyphs.
    /// With no traffic it returns a placeholder rather than an empty line.
    public static func activitySparkline(_ s: StatsSnapshot) -> String {
        let buckets = Array(s.activity.suffix(30))
        guard !buckets.isEmpty, buckets.contains(where: { $0.tokens > 0 }) else {
            return "Activity  —"
        }
        let maxTokens = buckets.map(\.tokens).max() ?? 0
        let blocks = Array("▁▂▃▄▅▆▇█")
        let glyphs = buckets.map { bucket -> Character in
            guard maxTokens > 0, bucket.tokens > 0 else { return blocks[0] }
            let scaled = Double(bucket.tokens) / Double(maxTokens) * Double(blocks.count - 1)
            return blocks[max(0, min(blocks.count - 1, Int(scaled.rounded())))]
        }
        return "Activity (last 30m)  " + String(glyphs)
    }

    public static func haltLine(_ s: StatsSnapshot) -> String {
        "Proxy halted — \(s.halt.haltedFilter ?? "filter") (\(s.halt.refused) refused)"
    }

    // MARK: - Number formatting

    public static func grouped(_ n: Int64) -> String {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        f.groupingSeparator = ","
        return f.string(from: NSNumber(value: n)) ?? "\(n)"
    }

    public static func money(_ value: Double) -> String {
        switch value {
        case ..<0.0001:
            return String(format: "$%.6f", value)
        case ..<0.01:
            return String(format: "$%.5f", value)
        case ..<1:
            return String(format: "$%.4f", value)
        case ..<100:
            return String(format: "$%.3f", value)
        default:
            return String(format: "$%.2f", value)
        }
    }

    public static func compact(_ n: Int64) -> String {
        let d = Double(n)
        switch n {
        case ..<1_000:
            return "\(n)"
        case ..<1_000_000:
            let k = d / 1_000
            if k >= 999.95 {
                return String(format: "%.2fM", d / 1_000_000)
            }
            return String(format: k < 10 ? "%.2fk" : "%.1fk", k)
        case ..<1_000_000_000:
            let m = d / 1_000_000
            if m >= 999.95 {
                return String(format: "%.2fB", d / 1_000_000_000)
            }
            return String(format: m < 10 ? "%.2fM" : "%.1fM", m)
        default:
            return String(format: "%.2fB", d / 1_000_000_000)
        }
    }

    public static func duration(_ seconds: Int64) -> String {
        let s = max(0, seconds)
        let d = s / 86_400
        let h = (s % 86_400) / 3_600
        let m = (s % 3_600) / 60
        if d > 0 { return "\(d)d \(h)h" }
        if h > 0 { return "\(h)h \(m)m" }
        if m > 0 { return "\(m)m \(s % 60)s" }
        return "\(s)s"
    }

    public static func sinceText(_ since: String?) -> String {
        guard let since, since.count >= 10 else { return "" }
        return " · since \(since.prefix(10))"
    }
}
