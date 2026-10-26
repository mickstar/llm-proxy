import SwiftUI
import MenuBarCore

/// ControlCenterView is the content of the status-item panel: a compact
/// dashboard rather than a list of menu rows.
struct ControlCenterView: View {
    @ObservedObject var model: StatsModel
    let controlURL: String
    let onCopy: () -> Void
    let onQuit: () -> Void
    let onExpand: () -> Void

    private let accent = Color.accentColor

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            header
            if let reason = model.offlineReason {
                offline(reason)
            } else if let snapshot = model.snapshot {
                hero(snapshot)
                activity(snapshot)
                metrics(snapshot)
                models(snapshot)
                if snapshot.halt.halted {
                    halt(snapshot)
                }
                footer
            } else {
                loading
            }
        }
        .padding(18)
        .frame(width: 360)
        .modifier(GlassPanel())
        .overlay(
            RoundedRectangle(cornerRadius: 26, style: .continuous)
                .strokeBorder(Color.white.opacity(0.16), lineWidth: 0.5)
        )
    }

    // MARK: - Header

    private var header: some View {
        HStack(spacing: 10) {
            ZStack {
                Circle()
                    .fill(accent.opacity(0.18))
                    .frame(width: 32, height: 32)
                Image(systemName: "bolt.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(accent)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text("llm-proxy")
                    .font(.system(size: 14, weight: .semibold, design: .rounded))
                statusLine
            }
            Spacer()
            if let snapshot = model.snapshot {
                Text("up \(Presentation.duration(snapshot.uptimeSeconds))")
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.tertiary)
            }
        }
    }

    private var statusLine: some View {
        HStack(spacing: 5) {
            Circle()
                .fill(statusColor)
                .frame(width: 6, height: 6)
            Text(statusText)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var statusColor: Color {
        if model.offlineReason != nil { return .orange }
        if model.snapshot?.halt.halted == true { return .red }
        return .green
    }

    private var statusText: String {
        if model.offlineReason != nil { return "Offline" }
        if let snapshot = model.snapshot {
            return snapshot.halt.halted ? "Halted — \(snapshot.halt.haltedFilter ?? "filter")" : "Running"
        }
        return "Connecting"
    }

    // MARK: - Hero

    private func hero(_ snapshot: StatsSnapshot) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 7) {
                Text(String(format: "%.1f", snapshot.throughput.completionTokensPerSecond))
                    .font(.system(size: 42, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                Text("tok/s")
                    .font(.headline)
                    .foregroundStyle(.secondary)
                Spacer()
                VStack(alignment: .trailing, spacing: 2) {
                    Label("\(snapshot.activeRequests) active", systemImage: "waveform")
                    Text(String(format: "%.1f req/min", snapshot.throughput.requestsPerMinute))
                }
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
            }
            Text("completion throughput · last \(snapshot.throughput.windowSeconds)s")
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(accent.opacity(0.10), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .strokeBorder(accent.opacity(0.18), lineWidth: 0.5)
        )
    }

    // MARK: - Activity

    private func activity(_ snapshot: StatsSnapshot) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack {
                Text("ACTIVITY")
                    .font(.system(size: 9, weight: .semibold))
                    .tracking(0.7)
                    .foregroundStyle(.secondary)
                Spacer()
                Text("30 min")
                    .font(.system(size: 9).monospacedDigit())
                    .foregroundStyle(.tertiary)
            }
            Sparkline(values: snapshot.activity.suffix(30).map { Double($0.tokens) })
                .frame(height: 42)
        }
        .padding(12)
        .background(Color.primary.opacity(0.045), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    // MARK: - Metrics

    private func metrics(_ snapshot: StatsSnapshot) -> some View {
        let avgCost = snapshot.totals.requests > 0
            ? snapshot.totals.costUsd / Double(snapshot.totals.requests)
            : 0
        return Grid(horizontalSpacing: 8, verticalSpacing: 8) {
            GridRow {
                metric(
                    "TOTAL TOKENS",
                    Presentation.compact(snapshot.totals.totalTokens),
                    detail: "in \(Presentation.compact(snapshot.totals.promptTokens)) · out \(Presentation.compact(snapshot.totals.completionTokens))"
                )
                metric(
                    "COST",
                    Presentation.money(snapshot.totals.costUsd),
                    detail: "avg \(Presentation.money(avgCost)) / request"
                )
            }
            GridRow {
                metric(
                    "REQUESTS",
                    Presentation.grouped(snapshot.totals.requests),
                    detail: "errors \(Presentation.grouped(snapshot.totals.errors)) · blocked \(Presentation.grouped(snapshot.totals.blocked))"
                )
                metric(
                    "LAST REQUEST",
                    lastRate(snapshot),
                    detail: lastDetail(snapshot)
                )
            }
        }
    }

    private func metric(_ label: String, _ value: String, detail: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.system(size: 9, weight: .semibold))
                .tracking(0.7)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Text(value)
                .font(.system(size: 19, weight: .semibold, design: .rounded))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            Text(detail)
                .font(.system(size: 9))
                .foregroundStyle(.tertiary)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(10)
        .background(Color.primary.opacity(0.045), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    private func lastRate(_ snapshot: StatsSnapshot) -> String {
        guard let last = snapshot.last, last.tokensPerSecond > 0 else { return "—" }
        return String(format: "%.1f tok/s", last.tokensPerSecond)
    }

    private func lastDetail(_ snapshot: StatsSnapshot) -> String {
        guard let last = snapshot.last, last.completionTokens > 0 else { return "no usage yet" }
        return "\(Presentation.compact(Int64(last.completionTokens))) completion tokens"
    }

    // MARK: - Models

    private func models(_ snapshot: StatsSnapshot) -> some View {
        let top = snapshot.models.prefix(4)
        let maxTokens = max(snapshot.models.first?.totalTokens ?? 0, 1)
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("MODELS")
                    .font(.system(size: 9, weight: .semibold))
                    .tracking(0.7)
                    .foregroundStyle(.secondary)
                Spacer()
                Text("\(snapshot.models.count)")
                    .font(.system(size: 9).monospacedDigit())
                    .foregroundStyle(.tertiary)
            }
            if top.isEmpty {
                Text("No model traffic yet")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.vertical, 2)
            }
            ForEach(Array(top), id: \.model) { model in
                VStack(spacing: 4) {
                    HStack(spacing: 8) {
                        Text(model.model)
                            .font(.system(size: 10, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer(minLength: 6)
                        Text(Presentation.compact(model.totalTokens))
                            .font(.system(size: 10, weight: .medium).monospacedDigit())
                        Text(Presentation.money(model.costUsd))
                            .font(.system(size: 9).monospacedDigit())
                            .foregroundStyle(.secondary)
                        Text("\(Presentation.grouped(model.requests)) req")
                            .font(.system(size: 9).monospacedDigit())
                            .foregroundStyle(.tertiary)
                    }
                    GeometryReader { geo in
                        ZStack(alignment: .leading) {
                            Capsule().fill(Color.primary.opacity(0.07))
                            Capsule()
                                .fill(accent.opacity(0.65))
                                .frame(width: geo.size.width * CGFloat(Double(model.totalTokens) / Double(maxTokens)))
                        }
                    }
                    .frame(height: 3)
                }
            }
        }
        .padding(12)
        .background(Color.primary.opacity(0.045), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    // MARK: - Halt

    private func halt(_ snapshot: StatsSnapshot) -> some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.octagon.fill")
                .foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 1) {
                Text("Proxy halted")
                    .font(.caption.weight(.semibold))
                Text("\(snapshot.halt.haltedFilter ?? "filter") · \(snapshot.halt.refused) refused")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button("Resume") {
                Task { await model.resume() }
            }
            .buttonStyle(PillButtonStyle(tint: .red))
        }
        .padding(10)
        .background(Color.red.opacity(0.13), in: RoundedRectangle(cornerRadius: 13, style: .continuous))
    }

    // MARK: - Footer and states

    private var footer: some View {
        HStack(spacing: 8) {
            Button(action: onExpand) {
                Label("Expand", systemImage: "rectangle.expand.vertical")
            }
            Button(action: onCopy) {
                Label("Copy", systemImage: "doc.on.doc")
            }
            Button(action: onQuit) {
                Label("Quit", systemImage: "power")
            }
            Spacer()
            Text(controlURL)
                .font(.system(size: 8, design: .monospaced))
                .foregroundStyle(.tertiary)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .buttonStyle(PillButtonStyle(tint: accent))
    }

    private func offline(_ reason: String) -> some View {
        VStack(spacing: 9) {
            Image(systemName: "bolt.slash.fill")
                .font(.system(size: 26))
                .foregroundStyle(.orange)
            Text("Can't reach llm-proxy")
                .font(.headline)
            Text(reason)
                .font(.caption)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Button("Retry") {
                Task { await model.refresh() }
            }
            .buttonStyle(PillButtonStyle(tint: .orange))
            .padding(.top, 2)
            footer
                .padding(.top, 8)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 26)
    }

    private var loading: some View {
        VStack(spacing: 8) {
            ProgressView()
            Text("Connecting to llm-proxy")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 50)
    }
}

// MARK: - Glass and controls

private struct GlassPanel: ViewModifier {
    private let shape = RoundedRectangle(cornerRadius: 26, style: .continuous)

    @ViewBuilder
    func body(content: Content) -> some View {
        if #available(macOS 26.0, *) {
            content.glassEffect(.regular, in: shape)
        } else {
            content.background(.ultraThinMaterial, in: shape)
        }
    }
}

private struct PillButtonStyle: ButtonStyle {
    var tint: Color = .accentColor

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 10, weight: .medium))
            .foregroundStyle(tint)
            .padding(.horizontal, 9)
            .padding(.vertical, 4)
            .background(tint.opacity(configuration.isPressed ? 0.22 : 0.12), in: Capsule())
    }
}
