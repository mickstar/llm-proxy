import SwiftUI
import MenuBarCore

struct RedactionsView: View {
    @ObservedObject var model: ConsoleModel
    @State private var query = ""

    private var filtered: [RedactionInstance] {
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !needle.isEmpty else { return model.redactions }
        return model.redactions.filter {
            $0.original.lowercased().contains(needle)
                || $0.alias.lowercased().contains(needle)
                || $0.rule.lowercased().contains(needle)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text("\(filtered.count) alias\(filtered.count == 1 ? "" : "es")")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                Spacer()
                HStack(spacing: 5) {
                    Image(systemName: "magnifyingglass")
                        .foregroundStyle(.secondary)
                    TextField("Filter", text: $query)
                        .textFieldStyle(.plain)
                        .frame(width: 180)
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 7))
                Button {
                    Task { await model.refreshRedactions() }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .buttonStyle(.borderless)
                .help("Refresh redactions")
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            Divider()

            if filtered.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "eye.slash")
                        .font(.title2)
                        .foregroundStyle(.tertiary)
                    Text(model.redactions.isEmpty ? "No redactions recorded yet" : "No matching redactions")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    Text("Aliases appear here when a request matches a redaction rule.")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(filtered) {
                    TableColumn("Alias") { item in
                        Text(item.alias)
                            .font(.system(size: 10, design: .monospaced))
                            .textSelection(.enabled)
                    }
                    .width(min: 130, ideal: 190)

                    TableColumn("Original") { item in
                        Text(item.original)
                            .font(.system(size: 10, design: .monospaced))
                            .textSelection(.enabled)
                    }
                    .width(min: 160, ideal: 240)

                    TableColumn("Rule") { item in
                        HStack(spacing: 5) {
                            Text(item.rule)
                                .font(.system(size: 10, design: .monospaced))
                            if item.conflict == true {
                                Text("conflict")
                                    .font(.system(size: 8, weight: .semibold))
                                    .foregroundStyle(.orange)
                            }
                        }
                    }
                    .width(min: 100, ideal: 150)

                    TableColumn("Uses") { item in
                        Text("\(item.count)")
                            .font(.system(size: 10).monospacedDigit())
                    }
                    .width(min: 44, ideal: 54, max: 70)

                    TableColumn("First seen") { item in
                        Text(ConsoleFormat.dateTimeString(item.firstSeen))
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundStyle(.secondary)
                    }
                    .width(min: 100, ideal: 140)
                }
                .tableStyle(.inset(alternatesRowBackgrounds: false))
                HStack(spacing: 6) {
                    Image(systemName: "lock.shield")
                    Text("Originals live in memory only. They were never written to the config file or traffic log.")
                }
                .font(.caption2)
                .foregroundStyle(.tertiary)
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
            }
        }
    }
}
