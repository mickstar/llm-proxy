import SwiftUI
import MenuBarCore

// MARK: - Cards and tiles

struct Card<Content: View>: View {
    var padding: CGFloat = 16
    @ViewBuilder var content: Content

    init(padding: CGFloat = 16, @ViewBuilder content: () -> Content) {
        self.padding = padding
        self.content = content()
    }

    var body: some View {
        content
            .padding(padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                Color.primary.opacity(0.045),
                in: RoundedRectangle(cornerRadius: 14, style: .continuous)
            )
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .strokeBorder(Color.primary.opacity(0.07), lineWidth: 0.5)
            )
    }
}

struct MetricTile: View {
    let label: String
    let value: String
    var detail: String? = nil
    var tint: Color = .accentColor

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label.uppercased())
                .font(.system(size: 9, weight: .semibold))
                .tracking(0.7)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.system(size: 24, weight: .semibold, design: .rounded))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.6)
            if let detail {
                Text(detail)
                    .font(.system(size: 10))
                    .foregroundStyle(.tertiary)
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(12)
        .background(tint.opacity(0.065), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .strokeBorder(tint.opacity(0.12), lineWidth: 0.5)
        )
    }
}

struct BannerView: View {
    let banner: ConsoleModel.Banner
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: banner.isError ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
            Text(banner.text)
                .font(.caption)
                .textSelection(.enabled)
            Spacer(minLength: 8)
            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.system(size: 9, weight: .bold))
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
        }
        .foregroundStyle(banner.isError ? Color.red : Color.green)
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .background(
            (banner.isError ? Color.red : Color.green).opacity(0.10),
            in: RoundedRectangle(cornerRadius: 9, style: .continuous)
        )
    }
}

// MARK: - Sparkline

struct Sparkline: View {
    let values: [Double]
    var tint: Color = .accentColor

    var body: some View {
        GeometryReader { geo in
            let maxValue = max(values.max() ?? 0, 1)
            let width = geo.size.width
            let height = geo.size.height
            let step = values.count > 1 ? width / CGFloat(values.count - 1) : width
            let points = values.enumerated().map { index, value in
                CGPoint(
                    x: CGFloat(index) * step,
                    y: height - 2 - CGFloat(value / maxValue) * (height - 4)
                )
            }
            ZStack {
                if points.count > 1 {
                    area(points: points, height: height)
                        .fill(
                            LinearGradient(
                                colors: [tint.opacity(0.32), tint.opacity(0.02)],
                                startPoint: .top,
                                endPoint: .bottom
                            )
                        )
                    line(points: points)
                        .stroke(tint.opacity(0.9), style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
                }
            }
        }
    }

    private func line(points: [CGPoint]) -> Path {
        Path { path in
            path.move(to: points[0])
            for point in points.dropFirst() {
                path.addLine(to: point)
            }
        }
    }

    private func area(points: [CGPoint], height: CGFloat) -> Path {
        Path { path in
            path.move(to: CGPoint(x: points[0].x, y: height))
            path.addLine(to: points[0])
            for point in points.dropFirst() {
                path.addLine(to: point)
            }
            path.addLine(to: CGPoint(x: points[points.count - 1].x, y: height))
            path.closeSubpath()
        }
    }
}

// MARK: - Formatting

enum ConsoleFormat {
    private static let isoFractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let iso: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    private static let clock: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f
    }()

    private static let dateTime: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "MMM d HH:mm:ss"
        return f
    }()

    static func date(_ isoString: String) -> Date? {
        isoFractional.date(from: isoString) ?? iso.date(from: isoString)
    }

    static func clockTime(_ isoString: String) -> String {
        guard let date = date(isoString) else { return isoString }
        return clock.string(from: date)
    }

    static func dateTimeString(_ isoString: String) -> String {
        guard let date = date(isoString) else { return isoString }
        return dateTime.string(from: date)
    }

    /// prettyBody re-indents JSON bodies and each JSON payload of an SSE body.
    static func prettyBody(_ body: String) -> String {
        guard !body.isEmpty else { return body }
        let trimmed = body.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.hasPrefix("{") || trimmed.hasPrefix("[") {
            if let pretty = prettyJSON(body) { return pretty }
        }
        if body.contains("data:") {
            return body
                .split(separator: "\n", omittingEmptySubsequences: false)
                .map { line -> String in
                    guard line.hasPrefix("data: ") else { return String(line) }
                    let payload = String(line.dropFirst(6))
                    if let pretty = prettyJSON(payload) {
                        return "data: " + pretty
                    }
                    return String(line)
                }
                .joined(separator: "\n")
        }
        return body
    }

    static func prettyJSON(_ text: String) -> String? {
        guard let data = text.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data),
              let pretty = try? JSONSerialization.data(
                withJSONObject: object,
                options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
              ) else {
            return nil
        }
        return String(data: pretty, encoding: .utf8)
    }
}
