import SwiftUI
import MenuBarCore

struct OverviewView: View {
    @ObservedObject var stats: StatsModel
    @ObservedObject var model: ConsoleModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let snapshot = stats.snapshot {
                    hero(snapshot)
                    metrics(snapshot)
                    HStack(alignment: .top, spacing: 16) {
                        models(snapshot)
                        recentTraffic
                    }
                } else {
                    empty
                }
            }
            .padding(20)
        }
    }

    private func hero(_ snapshot: StatsSnapshot) -> some View {
        Card {
            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(String(format: "%.1f", snapshot.throughput.completionTokensPerSecond))
                        .font(.system(size: 54, weight: .semibold, design: .rounded))
                        .monospacedDigit()
                    Text("tok/s")
                        .font(.title3)
                        .foregroundStyle(.secondary)
                    Spacer()
                    VStack(alignment: .trailing, spacing: 3) {
                        Label("\(snapshot.activeRequests) active", systemImage: "waveform")
                        Text(String(format: "%.1f req/min", snapshot.throughput.requestsPerMinute))
                        Text("\(snapshot.throughput.windowSeconds)s window")
                    }
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                }
                Sparkline(values: snapshot.activity.suffix(60).map { Double($0.tokens) })
                    .frame(height: 84)
                HStack {
                    Text("completion throughput")
                    Spacer()
                    Text("activity · last 60 min")
                }
                .font(.caption2)
                .foregroundStyle(.tertiary)
            }
        }
    }

    private func metrics(_ snapshot: StatsSnapshot) -> some View {
        let avgCost = snapshot.totals.requests > 0
            ? snapshot.totals.costUsd / Double(snapshot.totals.requests)
            : 0
        return LazyVGrid(
            columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: 4),
            spacing: 10
        ) {
            MetricTile(
                label: "Total tokens",
                value: Presentation.compact(snapshot.totals.totalTokens),
                detail: "in \(Presentation.compact(snapshot.totals.promptTokens)) · out \(Presentation.compact(snapshot.totals.completionTokens))"
            )
            MetricTile(
                label: "Cost",
                value: Presentation.money(snapshot.totals.costUsd),
                detail: "avg \(Presentation.money(avgCost)) / request"
            )
            MetricTile(
                label: "Requests",
                value: Presentation.grouped(snapshot.totals.requests),
                detail: "errors \(Presentation.grouped(snapshot.totals.errors)) · blocked \(Presentation.grouped(snapshot.totals.blocked))"
            )
            MetricTile(
                label: "Last request",
                value: lastRate(snapshot),
                detail: snapshot.last?.model ?? "no usage yet"
            )
        }
    }

    private func models(_ snapshot: StatsSnapshot) -> some View {
        Card {
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("Models")
                        .font(.headline)
                    Spacer()
                    Text("\(snapshot.models.count)")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                if snapshot.models.isEmpty {
                    Text("No model traffic yet")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(Array(snapshot.models.prefix(8)), id: \.model) { item in
                        HStack(spacing: 10) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(item.model)
                                    .font(.system(size: 11, design: .monospaced))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                Text("\(Presentation.grouped(item.requests)) requests · \(Presentation.money(item.costUsd))")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            Text(Presentation.compact(item.totalTokens))
                                .font(.system(size: 11, weight: .medium).monospacedDigit())
                        }
                        .padding(.vertical, 3)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity)
        .fixedSize(horizontal: false, vertical: true)
    }

    private var recentTraffic: some View {
        Card {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Recent traffic")
                        .font(.headline)
                    Spacer()
                    Text("\(model.traffic.count)")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                if model.traffic.isEmpty {
                    Text("No requests recorded")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(model.traffic.prefix(8)) { entry in
                        HStack(spacing: 8) {
                            Text(ConsoleFormat.clockTime(entry.time))
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundStyle(.secondary)
                            Text(entry.model.isEmpty ? entry.path : entry.model)
                                .font(.system(size: 10, design: .monospaced))
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer()
                            Text("\(Presentation.compact(Int64(entry.tokens))) tok")
                                .font(.system(size: 10).monospacedDigit())
                                .foregroundStyle(.secondary)
                            Text("\(entry.durationMs)ms")
                                .font(.system(size: 10).monospacedDigit())
                                .foregroundStyle(.tertiary)
                        }
                    }
                }
            }
        }
        .frame(maxWidth: .infinity)
    }

    private var empty: some View {
        VStack(spacing: 10) {
            if model.loading || stats.snapshot == nil {
                ProgressView()
                Text("Loading metrics")
            } else {
                Image(systemName: "exclamationmark.triangle")
                    .font(.title)
                    .foregroundStyle(.orange)
                Text("Metrics unavailable")
            }
        }
        .frame(maxWidth: .infinity, minHeight: 360)
        .foregroundStyle(.secondary)
    }

    private func lastRate(_ snapshot: StatsSnapshot) -> String {
        guard let last = snapshot.last, last.tokensPerSecond > 0 else { return "—" }
        return String(format: "%.1f tok/s", last.tokensPerSecond)
    }
}
