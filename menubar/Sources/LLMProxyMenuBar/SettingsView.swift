import SwiftUI
import MenuBarCore

struct SettingsView: View {
    @ObservedObject var model: ConsoleModel
    @State private var proxyAddr = ""
    @State private var controlAddr = ""
    @State private var modelPolicy = "passthrough"
    @State private var capacity = "500"
    @State private var baseURL = ""
    @State private var apiKey = ""
    @State private var showKey = false
    @State private var initialized = false
    @State private var saving = false
    @State private var validationError: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .firstTextBaseline) {
                    Text("Saved through the control API and applied without restarting, except listener addresses.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("Revert") {
                        if let config = model.config {
                            populate(config)
                        }
                    }
                    .disabled(!hasChanges)
                    Button("Save") {
                        save()
                    }
                    .keyboardShortcut("s", modifiers: .command)
                    .disabled(saving || !hasChanges)
                }

                if let validationError {
                    Text(validationError)
                        .font(.caption)
                        .foregroundStyle(.red)
                }

                Card {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("Server")
                            .font(.headline)
                        HStack(alignment: .top, spacing: 16) {
                            field("Proxy listen address") {
                                TextField("127.0.0.1:8787", text: $proxyAddr)
                                    .textFieldStyle(.roundedBorder)
                                    .font(.system(.body, design: .monospaced))
                            }
                            field("Control listen address") {
                                TextField("127.0.0.1:8788", text: $controlAddr)
                                    .textFieldStyle(.roundedBorder)
                                    .font(.system(.body, design: .monospaced))
                            }
                        }
                        HStack(alignment: .top, spacing: 16) {
                            field("Model policy") {
                                Picker("Model policy", selection: $modelPolicy) {
                                    Text("Passthrough").tag("passthrough")
                                    Text("Allowlist").tag("allowlist")
                                }
                                .labelsHidden()
                                .pickerStyle(.segmented)
                                .frame(width: 240)
                            }
                            field("Traffic log capacity") {
                                TextField("500", text: $capacity)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 120)
                            }
                        }
                        Text("Changing either listen address takes effect when the proxy restarts. The allowlist policy rejects models that are absent or disabled in the catalog.")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }

                Card {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("OpenRouter")
                            .font(.headline)
                        field("Base URL") {
                            TextField("https://openrouter.ai/api/v1", text: $baseURL)
                                .textFieldStyle(.roundedBorder)
                                .font(.system(.body, design: .monospaced))
                        }
                        field("API key") {
                            HStack(spacing: 8) {
                                if showKey {
                                    TextField("sk-or-...", text: $apiKey)
                                        .textFieldStyle(.roundedBorder)
                                        .font(.system(.body, design: .monospaced))
                                } else {
                                    SecureField("sk-or-...", text: $apiKey)
                                        .textFieldStyle(.roundedBorder)
                                }
                                Button {
                                    showKey.toggle()
                                } label: {
                                    Image(systemName: showKey ? "eye.slash" : "eye")
                                }
                                .buttonStyle(.borderless)
                                .help(showKey ? "Hide API key" : "Show API key")
                            }
                        }
                        Text("The key is returned in the clear by GET /api/config because the control listener is loopback-only. It is stored in plaintext in the config file.")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
            }
            .padding(20)
            .frame(maxWidth: 820, alignment: .leading)
        }
        .task {
            if let config = model.config {
                populate(config)
            }
        }
        .onChange(of: model.config) { config in
            guard let config else { return }
            if !initialized || !hasChanges {
                populate(config)
            }
        }
    }

    private var hasChanges: Bool {
        guard let config = model.config else { return false }
        return proxyAddr != config.server.proxyAddr
            || controlAddr != config.server.controlAddr
            || modelPolicy != config.server.modelPolicy
            || capacity != "\(config.server.trafficLogCapacity)"
            || baseURL != config.openrouter.baseUrl
            || apiKey != config.openrouter.apiKey
    }

    private func populate(_ config: ProxyConfig) {
        proxyAddr = config.server.proxyAddr
        controlAddr = config.server.controlAddr
        modelPolicy = config.server.modelPolicy
        capacity = "\(config.server.trafficLogCapacity)"
        baseURL = config.openrouter.baseUrl
        apiKey = config.openrouter.apiKey
        initialized = true
        validationError = nil
    }

    private func save() {
        validationError = nil
        if proxyAddr.trimmingCharacters(in: .whitespaces).isEmpty {
            validationError = "Proxy listen address is required."
            return
        }
        if controlAddr.trimmingCharacters(in: .whitespaces).isEmpty {
            validationError = "Control listen address is required."
            return
        }
        guard let portCapacity = Int(capacity.trimmingCharacters(in: .whitespaces)), portCapacity > 0 else {
            validationError = "Traffic log capacity must be a positive number."
            return
        }
        if baseURL.trimmingCharacters(in: .whitespaces).isEmpty {
            validationError = "OpenRouter base URL is required."
            return
        }
        let server = ServerSettings(
            proxyAddr: proxyAddr.trimmingCharacters(in: .whitespaces),
            controlAddr: controlAddr.trimmingCharacters(in: .whitespaces),
            modelPolicy: modelPolicy,
            trafficLogCapacity: portCapacity
        )
        let openrouter = OpenRouterSettings(
            baseUrl: baseURL.trimmingCharacters(in: .whitespaces),
            apiKey: apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        saving = true
        Task {
            defer { saving = false }
            if await model.saveSettings(server: server, openrouter: openrouter) {
                if let config = model.config {
                    populate(config)
                }
            }
        }
    }

    @ViewBuilder
    private func field<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
