import SwiftUI
import MenuBarCore

struct ModelsView: View {
    @ObservedObject var model: ConsoleModel
    @State private var selectedID: String?
    @State private var editingOriginal: String?
    @State private var id = ""
    @State private var name = ""
    @State private var contextLength = ""
    @State private var promptPerM = ""
    @State private var cachePerM = ""
    @State private var completionPerM = ""
    @State private var parameters = ""
    @State private var enabled = true
    @State private var originalPromptRaw = ""
    @State private var originalCacheRaw = ""
    @State private var originalCompletionRaw = ""
    @State private var originalPromptPerM = 0.0
    @State private var originalCachePerM = 0.0
    @State private var originalCompletionPerM = 0.0
    @State private var validationError: String?
    @State private var saving = false
    @State private var confirmDelete = false
    @State private var fetchID = ""
    @State private var fetching = false

    private var models: [CatalogModel] { model.config?.models ?? [] }
    private var modelIDs: [String] { models.map(\.id) }

    var body: some View {
        HSplitView {
            listPane
                .frame(minWidth: 260, idealWidth: 300, maxWidth: 380)
            editorPane
                .frame(minWidth: 470)
        }
        .onChange(of: modelIDs) { ids in
            if let selectedID, ids.contains(selectedID) { return }
            if let first = ids.first {
                select(first)
            } else {
                newModel()
            }
        }
        .onChange(of: selectedID) { value in
            guard let value, value != editingOriginal,
                  models.contains(where: { $0.id == value }) else { return }
            select(value)
        }
        .confirmationDialog(
            "Delete model \"\(id)\"?",
            isPresented: $confirmDelete,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                let target = editingOriginal ?? id
                Task {
                    if await model.deleteModel(id: target) {
                        if let first = model.config?.models.first?.id {
                            select(first)
                        } else {
                            newModel()
                        }
                    }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The catalog entry is removed; proxied traffic is unaffected.")
        }
        .task { primeSelection() }
    }

    private var listPane: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text("\(models.count) entr\(models.count == 1 ? "y" : "ies")")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    newModel()
                } label: {
                    Image(systemName: "plus")
                }
                .buttonStyle(.borderless)
                .help("Add a model manually")
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 9)

            HStack(spacing: 6) {
                TextField("author/slug", text: $fetchID)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(size: 11, design: .monospaced))
                Button {
                    fetch()
                } label: {
                    if fetching {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Fetch")
                    }
                }
                .disabled(fetching || fetchID.trimmingCharacters(in: .whitespaces).isEmpty)
                .help("Fetch metadata from OpenRouter and add or update it")
            }
            .padding(.horizontal, 12)
            .padding(.bottom, 8)
            Divider()

            List(selection: $selectedID) {
                ForEach(models) { item in
                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 6) {
                            Circle()
                                .fill(item.enabled ? Color.green : Color.secondary.opacity(0.35))
                                .frame(width: 6, height: 6)
                            Text(item.id)
                                .font(.system(size: 11, weight: .medium, design: .monospaced))
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                        HStack(spacing: 8) {
                            if !item.name.isEmpty {
                                Text(item.name)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                            }
                            Spacer(minLength: 4)
                            Text("\(perMillionShort(item.pricing.prompt)) / \(perMillionShort(item.pricing.cache)) / \(perMillionShort(item.pricing.completion))")
                                .font(.system(size: 9).monospacedDigit())
                                .foregroundStyle(.tertiary)
                        }
                    }
                    .padding(.vertical, 3)
                    .tag(item.id)
                }
            }
            .listStyle(.inset)
        }
    }

    private var editorPane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(alignment: .firstTextBaseline) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(editingOriginal == nil ? "New model" : "Edit model")
                            .font(.title3.weight(.semibold))
                        Text(editingOriginal ?? "Catalog entry shown to clients and used by allowlist mode")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                    Spacer()
                    if editingOriginal != nil {
                        Button(role: .destructive) {
                            confirmDelete = true
                        } label: {
                            Image(systemName: "trash")
                        }
                        .help("Delete model")
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
                        field("Model id") {
                            TextField("author/slug", text: $id)
                                .textFieldStyle(.roundedBorder)
                                .font(.system(.body, design: .monospaced))
                        }
                        field("Display name") {
                            TextField("optional", text: $name)
                                .textFieldStyle(.roundedBorder)
                        }
                        HStack(alignment: .top, spacing: 16) {
                            field("Context length") {
                                TextField("tokens", text: $contextLength)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 130)
                            }
                            field("Enabled") {
                                Toggle("Enabled", isOn: $enabled)
                                    .toggleStyle(.switch)
                                    .labelsHidden()
                            }
                        }
                        field("Pricing (USD per 1M tokens)") {
                            HStack(spacing: 10) {
                                TextField("prompt", text: $promptPerM)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 120)
                                Text("in")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                TextField("cache", text: $cachePerM)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 120)
                                Text("cached")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                TextField("completion", text: $completionPerM)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 120)
                                Text("out")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        field("Supported parameters") {
                            TextEditor(text: $parameters)
                                .font(.system(size: 11, design: .monospaced))
                                .frame(minHeight: 44, maxHeight: 60)
                                .padding(5)
                                .background(Color.black.opacity(0.08), in: RoundedRectangle(cornerRadius: 7))
                                .overlay(
                                    RoundedRectangle(cornerRadius: 7)
                                        .strokeBorder(Color.primary.opacity(0.10), lineWidth: 0.5)
                                )
                        }
                        Text("Prices are stored as per-token rates in the config file. Enabled entries matter only under the allowlist policy.")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
            }
            .padding(18)
            .frame(maxWidth: 760, alignment: .leading)
        }
    }

    private func save() {
        validationError = validate()
        guard validationError == nil else { return }
        let promptRaw = priceRaw(text: promptPerM, original: originalPromptRaw, originalPerM: originalPromptPerM)
        let cacheRaw = priceRaw(text: cachePerM, original: originalCacheRaw, originalPerM: originalCachePerM)
        let completionRaw = priceRaw(text: completionPerM, original: originalCompletionRaw, originalPerM: originalCompletionPerM)
        let parametersList = parameters
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let modelValue = CatalogModel(
            id: id.trimmingCharacters(in: .whitespacesAndNewlines),
            name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            contextLength: Int(contextLength.trimmingCharacters(in: .whitespaces)) ?? 0,
            pricing: Pricing(prompt: promptRaw, cache: cacheRaw, completion: completionRaw),
            supportedParameters: parametersList,
            enabled: enabled
        )
        saving = true
        Task {
            defer { saving = false }
            if await model.saveModel(originalID: editingOriginal, model: modelValue) {
                editingOriginal = modelValue.id
                selectedID = modelValue.id
                originalPromptRaw = promptRaw
                originalCacheRaw = cacheRaw
                originalCompletionRaw = completionRaw
                originalPromptPerM = Double(promptPerM) ?? 0
                originalCachePerM = Double(cachePerM) ?? 0
                originalCompletionPerM = Double(completionPerM) ?? 0
                validationError = nil
            }
        }
    }

    private func fetch() {
        let target = fetchID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !target.isEmpty else { return }
        fetching = true
        Task {
            defer { fetching = false }
            if let fetched = await model.fetchModel(id: target) {
                fetchID = ""
                select(fetched.id)
            }
        }
    }

    private func validate() -> String? {
        if id.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Model id is required."
        }
        if !contextLength.trimmingCharacters(in: .whitespaces).isEmpty {
            guard let value = Int(contextLength.trimmingCharacters(in: .whitespaces)), value >= 0 else {
                return "Context length must be a non-negative number."
            }
        }
        for (label, value) in [("Prompt", promptPerM), ("Cache", cachePerM), ("Completion", completionPerM)] {
            let text = value.trimmingCharacters(in: .whitespaces)
            if !text.isEmpty {
                guard let parsed = Double(text), parsed >= 0 else {
                    return "\(label) price must be a non-negative number."
                }
            }
        }
        return nil
    }

    private func priceRaw(text: String, original: String, originalPerM: Double) -> String {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        guard let perM = Double(trimmed) else { return original }
        if !original.isEmpty, abs(perM - originalPerM) < 1e-9 {
            return original
        }
        return String(format: "%.12g", perM / 1_000_000)
    }

    private func load(_ item: CatalogModel) {
        editingOriginal = item.id
        id = item.id
        name = item.name
        contextLength = item.contextLength > 0 ? "\(item.contextLength)" : ""
        originalPromptRaw = item.pricing.prompt
        originalCacheRaw = item.pricing.cache
        originalCompletionRaw = item.pricing.completion
        originalPromptPerM = (Double(item.pricing.prompt) ?? 0) * 1_000_000
        originalCachePerM = (Double(item.pricing.cache) ?? 0) * 1_000_000
        originalCompletionPerM = (Double(item.pricing.completion) ?? 0) * 1_000_000
        promptPerM = formatPerMillion(originalPromptPerM)
        cachePerM = formatPerMillion(originalCachePerM)
        completionPerM = formatPerMillion(originalCompletionPerM)
        parameters = item.supportedParameters.joined(separator: ", ")
        enabled = item.enabled
        validationError = nil
    }

    private func select(_ id: String) {
        guard let item = models.first(where: { $0.id == id }) else { return }
        load(item)
        selectedID = id
    }

    private func primeSelection() {
        guard selectedID == nil, let first = models.first else { return }
        select(first.id)
    }

    private func newModel() {
        editingOriginal = nil
        selectedID = nil
        id = ""
        name = ""
        contextLength = ""
        promptPerM = ""
        cachePerM = ""
        completionPerM = ""
        parameters = ""
        enabled = true
        originalPromptRaw = ""
        originalCacheRaw = ""
        originalCompletionRaw = ""
        originalPromptPerM = 0
        originalCachePerM = 0
        originalCompletionPerM = 0
        validationError = nil
    }

    private func formatPerMillion(_ value: Double) -> String {
        guard value != 0 else { return "0" }
        return String(format: "%.6g", value)
    }

    private func perMillionShort(_ raw: String) -> String {
        let perM = (Double(raw) ?? 0) * 1_000_000
        if perM == 0 { return "$0" }
        return String(format: "$%.4g", perM)
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
