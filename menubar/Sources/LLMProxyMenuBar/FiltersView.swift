import AppKit
import MenuBarCore
import SwiftUI
import UniformTypeIdentifiers

struct FiltersView: View {
    @ObservedObject var model: ConsoleModel
    @State private var selectedID: String?
    @State private var editingOriginal: String?
    @State private var editingKind: RuleKind?
    @State private var draft = FilterDraft()
    @State private var sample = "Contact john.smith@example.com or use sk-live-123."
    @State private var test: FilterTestResult?
    @State private var testError: String?
    @State private var testRunning = false
    @State private var validationError: String?
    @State private var patternDropTargeted = false
    @State private var saving = false
    @State private var confirmDelete = false

    private struct RuleRow: Identifiable {
        let id: String
        let name: String
        let action: String
        let source: String
        let enabled: Bool
        let kind: RuleKind
    }

    private var filters: [FilterRule] { model.config?.filters ?? [] }
    private var redactions: [RedactionRule] { model.config?.redactions ?? [] }

    private var rows: [RuleRow] {
        var out = filters.map {
            RuleRow(id: "filter:\($0.name)", name: $0.name, action: $0.action, source: matchSource($0), enabled: $0.enabled, kind: .filter)
        }
        out += redactions.map {
            RuleRow(id: "redaction:\($0.name)", name: $0.name, action: FilterDraft.redactAction, source: $0.pattern, enabled: $0.enabled, kind: .redaction)
        }
        return out
    }

    private var rowIDs: [String] { rows.map(\.id) }
    private var editingID: String? {
        guard let editingOriginal, let editingKind else { return nil }
        return "\(editingKind.rawValue):\(editingOriginal)"
    }

    var body: some View {
        HSplitView {
            listPane
                .frame(minWidth: 230, idealWidth: 260, maxWidth: 320)
            editorPane
                .frame(minWidth: 480)
        }
        .onChange(of: rowIDs) { ids in
            if let selectedID, ids.contains(selectedID) { return }
            if let first = rows.first {
                select(first)
            } else {
                newFilter()
            }
        }
        .onChange(of: selectedID) { id in
            guard let id, id != editingID, let row = rows.first(where: { $0.id == id }) else { return }
            select(row)
        }
        .onChange(of: draft.action) { action in
            switch action {
            case "replace":
                if draft.replacement.isEmpty {
                    draft.replacement = draft.suggestedReplacement()
                }
            case FilterDraft.redactAction:
                if draft.replacement.isEmpty {
                    draft.replacement = draft.suggestedReplacement()
                }
                draft.patternFile = ""
            default:
                break
            }
        }
        .confirmationDialog(
            "Delete rule \"\(draft.name)\"?",
            isPresented: $confirmDelete,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                let name = editingOriginal ?? draft.name
                let kind = editingKind ?? draft.kind
                Task {
                    if await model.deleteRule(kind: kind, name: name) {
                        if let first = rows.first {
                            select(first)
                        } else {
                            newFilter()
                        }
                    }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The rule stops applying immediately. This cannot be undone.")
        }
        .task { primeSelection() }
    }

    private var listPane: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text("\(rows.count) rule\(rows.count == 1 ? "" : "s")")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    newFilter()
                } label: {
                    Image(systemName: "plus")
                }
                .buttonStyle(.borderless)
                .help("New rule")
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
            Divider()
            List(selection: $selectedID) {
                ForEach(rows) { row in
                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(row.name)
                                .font(.system(size: 12, weight: .medium))
                                .lineLimit(1)
                            HStack(spacing: 5) {
                                Text(row.action)
                                    .font(.system(size: 8, weight: .semibold))
                                    .foregroundStyle(actionColor(row.action))
                                Text(row.source)
                                    .font(.system(size: 9, design: .monospaced))
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                        }
                        Spacer(minLength: 4)
                        Toggle("", isOn: Binding(
                            get: { row.enabled },
                            set: { value in
                                Task {
                                    switch row.kind {
                                    case .filter:
                                        if let filter = filters.first(where: { $0.name == row.name }) {
                                            await model.setFilterEnabled(filter, enabled: value)
                                        }
                                    case .redaction:
                                        if let rule = redactions.first(where: { $0.name == row.name }) {
                                            await model.setRedactionEnabled(rule, enabled: value)
                                        }
                                    }
                                }
                            }
                        ))
                        .toggleStyle(.switch)
                        .controlSize(.mini)
                        .labelsHidden()
                        .help(row.enabled ? "Disable rule" : "Enable rule")
                    }
                    .padding(.vertical, 2)
                    .tag(row.id)
                }
            }
            .listStyle(.inset)
        }
    }

    private func actionColor(_ action: String) -> Color {
        switch action {
        case "block":
            return .red
        case FilterDraft.redactAction:
            return .purple
        default:
            return .blue
        }
    }

    private var editorPane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(alignment: .firstTextBaseline) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(editingOriginal == nil ? "New rule" : "Edit rule")
                            .font(.title3.weight(.semibold))
                        Text(editingOriginal == nil ? "Rules apply to outbound request bodies." : draft.name)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    if editingOriginal != nil {
                        Button(role: .destructive) {
                            confirmDelete = true
                        } label: {
                            Image(systemName: "trash")
                        }
                        .help("Delete rule")
                    }
                    Button("Save") {
                        save()
                    }
                    .keyboardShortcut("s", modifiers: .command)
                    .disabled(saving)
                }

                if let validationError {
                    Text(validationError)
                        .font(.caption)
                        .foregroundStyle(.red)
                }

                Card {
                    VStack(alignment: .leading, spacing: 12) {
                        field("Name") {
                            TextField("rule-name", text: $draft.name)
                                .textFieldStyle(.roundedBorder)
                        }
                        field("Action") {
                            Picker("Action", selection: $draft.action) {
                                Text("Block").tag("block")
                                Text("Replace").tag("replace")
                                Text("Redact").tag(FilterDraft.redactAction)
                            }
                            .labelsHidden()
                            .pickerStyle(.segmented)
                            .frame(maxWidth: 320)
                        }
                        field("Pattern") {
                            TextField("RE2 pattern, case-insensitive unless (?-i)", text: $draft.pattern)
                                .textFieldStyle(.roundedBorder)
                                .font(.system(.body, design: .monospaced))
                            Text(draft.action == FilterDraft.redactAction
                                ? "Matches become a reversible alias, restored in responses and tool calls."
                                : "For a secret file, choose it below so its contents are matched verbatim.")
                                .font(.caption2)
                                .foregroundStyle(.tertiary)
                        }
                        if draft.action != FilterDraft.redactAction {
                            field("Pattern file") {
                                HStack(spacing: 8) {
                                    TextField("path or glob whose verbatim contents are matched", text: $draft.patternFile)
                                        .textFieldStyle(.roundedBorder)
                                        .font(.system(.body, design: .monospaced))
                                    Button("Choose…") {
                                        choosePatternFile()
                                    }
                                    .help("Pick a secret file such as ~/.ssh/id_ed25519; its contents never enter the config")
                                }
                                .padding(4)
                                .background(
                                    patternDropTargeted ? Color.accentColor.opacity(0.12) : Color.clear,
                                    in: RoundedRectangle(cornerRadius: 7)
                                )
                                .onDrop(of: [.fileURL], isTargeted: $patternDropTargeted) { providers in
                                    acceptPatternFileDrop(providers)
                                }
                                Text("The file's contents are matched verbatim (LF or CRLF) and never enter the config. Drop a key file here to use it.")
                                    .font(.caption2)
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        if draft.action != "block" {
                            field(draft.action == FilterDraft.redactAction ? "Template" : "Replacement") {
                                HStack(spacing: 8) {
                                    TextField(
                                        draft.action == FilterDraft.redactAction
                                            ? "template ($R keeps the alias unique per match)"
                                            : "replacement ($R = per-match token)",
                                        text: $draft.replacement
                                    )
                                    .textFieldStyle(.roundedBorder)
                                    .font(.system(.body, design: .monospaced))
                                    Button("Suggest") {
                                        draft.replacement = draft.suggestedReplacement()
                                    }
                                }
                                Text(draft.action == FilterDraft.redactAction
                                    ? "The alias is restored to the original on the way back, like a redaction rule."
                                    : "$R expands to a recorded token so the original can be restored in responses.")
                                    .font(.caption2)
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        Toggle("Enabled", isOn: $draft.enabled)
                            .toggleStyle(.switch)
                    }
                }

                testCard
            }
            .padding(18)
            .frame(maxWidth: 820, alignment: .leading)
        }
    }

    private var testCard: some View {
        Card {
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("Live test")
                        .font(.headline)
                    Spacer()
                    if testRunning {
                        ProgressView().controlSize(.small)
                    } else if let test {
                        Text("\(test.matches) match\(test.matches == 1 ? "" : "es")")
                            .font(.caption.monospacedDigit())
                            .foregroundStyle(test.matches > 0 ? Color.orange : Color.secondary)
                    }
                }
                Text("Sample text")
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.secondary)
                TextEditor(text: $sample)
                    .font(.system(size: 11, design: .monospaced))
                    .frame(minHeight: 64)
                    .padding(6)
                    .background(Color.black.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
                    .overlay(
                        RoundedRectangle(cornerRadius: 8)
                            .strokeBorder(Color.primary.opacity(0.10), lineWidth: 0.5)
                    )
                if let testError {
                    Text(testError)
                        .font(.caption)
                        .foregroundStyle(.red)
                } else if let test {
                    if !test.matched.isEmpty {
                        VStack(alignment: .leading, spacing: 3) {
                            Text("Matched")
                                .font(.system(size: 9, weight: .semibold))
                                .tracking(0.6)
                                .foregroundStyle(.secondary)
                            ForEach(Array(test.matched.prefix(8).enumerated()), id: \.offset) { _, value in
                                Text(value)
                                    .font(.system(size: 10, design: .monospaced))
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2)
                                    .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 4))
                            }
                        }
                    }
                    VStack(alignment: .leading, spacing: 3) {
                        Text("Result")
                            .font(.system(size: 9, weight: .semibold))
                            .tracking(0.6)
                            .foregroundStyle(.secondary)
                        Text(test.result)
                            .font(.system(size: 10, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                            .background(Color.black.opacity(0.08), in: RoundedRectangle(cornerRadius: 6))
                    }
                } else if !hasSingleSource {
                    Text("Enter a pattern or a pattern file to test.")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
            }
        }
        .task(id: testKey) {
            await runTest()
        }
    }

    private var hasSingleSource: Bool {
        let hasPattern = !draft.pattern.isEmpty
        let hasFile = !draft.patternFile.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return hasPattern != hasFile
    }

    private func choosePatternFile() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.showsHiddenFiles = true
        panel.treatsFilePackagesAsDirectories = false
        panel.message = "Choose a secret file to block from outbound requests."
        panel.prompt = "Use File"
        let home = FileManager.default.homeDirectoryForCurrentUser
        let ssh = home.appendingPathComponent(".ssh", isDirectory: true)
        panel.directoryURL = FileManager.default.fileExists(atPath: ssh.path) ? ssh : home
        guard panel.runModal() == .OK, let url = panel.url else { return }
        usePatternFile(url)
    }

    private func acceptPatternFileDrop(_ providers: [NSItemProvider]) -> Bool {
        guard let provider = providers.first else { return false }
        provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { item, _ in
            var dropped: URL?
            if let data = item as? Data {
                dropped = URL(dataRepresentation: data, relativeTo: nil)
            } else if let nsURL = item as? NSURL {
                dropped = nsURL as URL
            }
            guard let dropped else { return }
            DispatchQueue.main.async {
                self.usePatternFile(dropped)
            }
        }
        return true
    }

    private func usePatternFile(_ url: URL) {
        draft.patternFile = abbreviatedPath(url)
        draft.pattern = ""
        validationError = nil
    }

    private func abbreviatedPath(_ url: URL) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let path = url.standardizedFileURL.path
        if path == home { return "~" }
        if path.hasPrefix(home + "/") {
            return "~" + String(path.dropFirst(home.count))
        }
        return path
    }

    private var testKey: String {
        [draft.pattern, draft.patternFile, draft.action == "block" ? "" : draft.replacement, sample]
            .joined(separator: "\u{1f}")
    }

    private func runTest() async {
        guard hasSingleSource, !sample.isEmpty else {
            test = nil
            testError = nil
            testRunning = false
            return
        }
        try? await Task.sleep(nanoseconds: 300_000_000)
        if Task.isCancelled { return }
        testRunning = true
        defer { testRunning = false }
        do {
            test = try await model.testFilter(
                pattern: draft.pattern,
                patternFile: draft.patternFile,
                replacement: draft.action == "block" ? "" : draft.replacement,
                sample: sample
            )
            testError = nil
        } catch {
            test = nil
            testError = error.localizedDescription
        }
    }

    private func save() {
        validationError = draft.validationError()
        guard validationError == nil else { return }
        let kind = draft.kind
        saving = true
        Task {
            defer { saving = false }
            if await model.saveRule(kind: kind, originalName: editingOriginal, originalKind: editingKind, draft: draft) {
                let name = draft.name.trimmingCharacters(in: .whitespacesAndNewlines)
                editingOriginal = name
                editingKind = kind
                selectedID = "\(kind.rawValue):\(name)"
                validationError = nil
            }
        }
    }

    private func primeSelection() {
        guard selectedID == nil, let first = rows.first else { return }
        select(first)
    }

    private func select(_ row: RuleRow) {
        editingOriginal = row.name
        editingKind = row.kind
        selectedID = row.id
        switch row.kind {
        case .filter:
            if let filter = filters.first(where: { $0.name == row.name }) {
                draft = FilterDraft(rule: filter)
            }
        case .redaction:
            if let rule = redactions.first(where: { $0.name == row.name }) {
                draft = FilterDraft(redaction: rule)
            }
        }
        validationError = nil
        test = nil
        testError = nil
    }

    private func newFilter() {
        editingOriginal = nil
        editingKind = nil
        selectedID = nil
        draft = FilterDraft()
        validationError = nil
        test = nil
        testError = nil
    }

    private func matchSource(_ filter: FilterRule) -> String {
        if let file = filter.patternFile, !file.isEmpty {
            return "file: \(file)"
        }
        return filter.pattern
    }

    @ViewBuilder
    private func field<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
            content()
        }
    }
}
