import AppKit
import SwiftUI
import MenuBarCore

struct TrafficView: View {
    @ObservedObject var model: ConsoleModel
    @State private var selectedID: UInt64?
    @State private var detail: TrafficEntry?
    @State private var detailError: String?
    @State private var detailLoading = false
    @State private var query = ""

    private var filtered: [TrafficEntry] {
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !needle.isEmpty else { return model.traffic }
        return model.traffic.filter {
            $0.model.lowercased().contains(needle)
                || $0.path.lowercased().contains(needle)
                || $0.method.lowercased().contains(needle)
        }
    }

    var body: some View {
        HSplitView {
            VStack(spacing: 0) {
                header
                Divider()
                Table(filtered, selection: $selectedID) {
                    TableColumn("Time") { entry in
                        Text(ConsoleFormat.clockTime(entry.time))
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundStyle(.secondary)
                    }
                    .width(min: 58, ideal: 64, max: 74)

                    TableColumn("Model") { entry in
                        Text(entry.model)
                            .font(.system(size: 10, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    .width(min: 150, ideal: 210)

                    TableColumn("Status") { entry in
                        Text("\(entry.status)")
                            .font(.system(size: 10, weight: .semibold).monospacedDigit())
                            .foregroundStyle(statusColor(entry.status))
                    }
                    .width(min: 44, ideal: 50, max: 56)

                    TableColumn("Tokens") { entry in
                        Text(entry.tokens == 0 ? "—" : Presentation.grouped(Int64(entry.tokens)))
                            .font(.system(size: 10).monospacedDigit())
                    }
                    .width(min: 50, ideal: 64, max: 80)

                    TableColumn("Cost") { entry in
                        Text(entry.cost > 0 ? Presentation.money(entry.cost) : "—")
                            .font(.system(size: 10).monospacedDigit())
                            .foregroundStyle(.secondary)
                    }
                    .width(min: 54, ideal: 70, max: 85)
                }
                .tableStyle(.inset(alternatesRowBackgrounds: true))
            }
            .frame(minWidth: 560)

            inspector
                .frame(minWidth: 360, idealWidth: 430, maxWidth: 620)
        }
        .task(id: selectedID) {
            await loadDetail()
        }
        .task {
            if selectedID == nil {
                selectedID = model.traffic.first?.id
            }
        }
        .onChange(of: model.traffic.map(\.id)) { ids in
            if let selectedID, ids.contains(selectedID) { return }
            selectedID = ids.first
            detail = nil
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
            if model.streaming {
                HStack(spacing: 4) {
                    Circle().fill(.green).frame(width: 6, height: 6)
                    Text("live")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(.green)
                }
            }
            Spacer()
            HStack(spacing: 5) {
                Image(systemName: "magnifyingglass")
                    .foregroundStyle(.secondary)
                TextField("Filter", text: $query)
                    .textFieldStyle(.plain)
                    .frame(width: 150)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 7))
            Button {
                Task { await model.refreshTraffic() }
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.borderless)
            .help("Refresh traffic")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
    }

    @ViewBuilder
    private var inspector: some View {
        if detailLoading && detail == nil {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let error = detailError {
            VStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.title2)
                    .foregroundStyle(.orange)
                Text(error)
                    .font(.caption)
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let entry = detail {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(entry.method)
                            .font(.system(size: 11, weight: .bold, design: .monospaced))
                        Text(entry.path)
                            .font(.system(size: 11, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer()
                        Text("\(entry.status)")
                            .font(.system(size: 11, weight: .bold).monospacedDigit())
                            .foregroundStyle(statusColor(entry.status))
                    }
                    metadata(entry)
                    bodySection(
                        title: "REQUEST",
                        body: entry.requestText,
                        truncated: entry.requestTruncated == true
                    )
                    bodySection(
                        title: "RESPONSE",
                        body: entry.responseText,
                        truncated: entry.responseTruncated == true
                    )
                }
                .padding(16)
            }
        } else {
            VStack(spacing: 8) {
                Image(systemName: "text.magnifyingglass")
                    .font(.title2)
                    .foregroundStyle(.tertiary)
                Text("Select an exchange to inspect it")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func metadata(_ entry: TrafficEntry) -> some View {
        Card(padding: 12) {
            Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 6) {
                GridRow {
                    label("Model")
                    Text(entry.model.isEmpty ? "—" : entry.model)
                        .font(.system(size: 10, design: .monospaced))
                        .textSelection(.enabled)
                }
                GridRow {
                    label("Duration")
                    Text("\(entry.durationMs) ms")
                }
                GridRow {
                    label("Size")
                    Text("\(entry.requestSize) B in · \(entry.responseSize) B out")
                }
                GridRow {
                    label("Tokens")
                    Text("\(Presentation.grouped(Int64(entry.tokens))) · in \(Presentation.grouped(Int64(entry.promptTokens ?? 0))) · out \(Presentation.grouped(Int64(entry.completionTokens ?? 0)))")
                }
                GridRow {
                    label("Cost")
                    Text(Presentation.money(entry.cost))
                }
                if let filter = entry.filterSummary, !filter.isEmpty {
                    GridRow {
                        label("Filter")
                        Text(filter).textSelection(.enabled)
                    }
                }
                if let redaction = entry.redactionSummary, !redaction.isEmpty {
                    GridRow {
                        label("Redaction")
                        Text(redaction).textSelection(.enabled)
                    }
                }
            }
            .font(.system(size: 10))
        }
    }

    private func label(_ text: String) -> some View {
        Text(text.uppercased())
            .font(.system(size: 8, weight: .semibold))
            .tracking(0.6)
            .foregroundStyle(.secondary)
    }

    private func bodySection(title: String, body: String, truncated: Bool) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(title)
                    .font(.system(size: 9, weight: .semibold))
                    .tracking(0.7)
                    .foregroundStyle(.secondary)
                Spacer()
                if truncated {
                    Text("truncated")
                        .font(.system(size: 9))
                        .foregroundStyle(.orange)
                }
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(body, forType: .string)
                } label: {
                    Image(systemName: "doc.on.doc")
                        .font(.system(size: 9))
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .help("Copy \(title.lowercased()) body")
            }
            let limit = 32_000
            let clipped = body.count > limit ? String(body.prefix(limit)) : body
            VStack(alignment: .leading, spacing: 3) {
                if body.count > limit {
                    Text("showing first \(limit / 1000) KB")
                        .font(.system(size: 8))
                        .foregroundStyle(.tertiary)
                }
                ScrollView(.vertical) {
                    Text(body.isEmpty ? "(none captured)" : ConsoleFormat.prettyBody(clipped))
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(body.isEmpty ? .tertiary : .primary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(height: title == "REQUEST" ? 150 : 210)
            }
            .padding(9)
            .background(Color.black.opacity(0.10), in: RoundedRectangle(cornerRadius: 8))
        }
    }

    private func statusColor(_ status: Int) -> Color {
        switch status {
        case 200..<300: return .green
        case 300..<400: return .secondary
        case 400..<500: return .orange
        default: return .red
        }
    }

    private func loadDetail() async {
        guard let id = selectedID else {
            detail = nil
            detailError = nil
            return
        }
        detailLoading = true
        detailError = nil
        defer { detailLoading = false }
        do {
            detail = try await model.trafficDetail(id: id)
        } catch {
            detail = nil
            detailError = error.localizedDescription
        }
    }
}
