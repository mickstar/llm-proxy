import Combine
import Foundation
import MenuBarCore

/// ConsoleModel owns the full-app state: configuration, traffic, redactions,
/// and mutation feedback. Stats stay in StatsModel, shared with the menu bar.
@MainActor
final class ConsoleModel: ObservableObject {
    struct Banner: Identifiable, Equatable {
        let id = UUID()
        let text: String
        let isError: Bool
    }

    @Published private(set) var config: ProxyConfig?
    @Published private(set) var traffic: [TrafficEntry] = []
    @Published private(set) var redactions: [RedactionInstance] = []
    @Published private(set) var streaming = false
    @Published private(set) var loading = false
    @Published var banner: Banner?

    let client: ControlClient
    private var streamTask: Task<Void, Never>?
    private var running = false

    init(client: ControlClient = ControlClient()) {
        self.client = client
    }

    func start() {
        guard !running else { return }
        running = true
        Task { await reloadAll() }
        streamTask = Task { await streamLoop() }
    }

    func stop() {
        running = false
        streamTask?.cancel()
        streamTask = nil
    }

    func reloadAll() async {
        loading = true
        defer { loading = false }
        do {
            async let config = client.fetchConfig()
            async let traffic = client.fetchTraffic()
            async let redactions = client.fetchRedactions()
            let (cfg, entries, aliases) = try await (config, traffic, redactions)
            self.config = cfg
            self.traffic = entries
            self.redactions = aliases
        } catch {
            show(error, prefix: "Could not load the proxy state")
        }
    }

    func refreshConfig() async {
        do {
            config = try await client.fetchConfig()
        } catch {
            show(error, prefix: "Could not refresh configuration")
        }
    }

    func refreshTraffic() async {
        do {
            traffic = try await client.fetchTraffic()
        } catch {
            show(error, prefix: "Could not refresh traffic")
        }
    }

    func refreshRedactions() async {
        do {
            redactions = try await client.fetchRedactions()
        } catch {
            show(error, prefix: "Could not refresh redactions")
        }
    }

    // MARK: - Settings

    @discardableResult
    func saveSettings(server: ServerSettings, openrouter: OpenRouterSettings) async -> Bool {
        do {
            config = try await client.saveSettings(server: server, openrouter: openrouter)
            banner = Banner(text: "Settings saved. Listener changes apply after a restart.", isError: false)
            return true
        } catch {
            show(error, prefix: "Could not save settings")
            return false
        }
    }

    // MARK: - Rules

    /// saveRule writes a filter or a redaction. When the draft changes kind,
    /// the rule in the other section is removed after the new one is stored,
    /// so a failed save never loses the existing rule.
    @discardableResult
    func saveRule(kind: RuleKind, originalName: String?, originalKind: RuleKind?, draft: FilterDraft) async -> Bool {
        do {
            switch kind {
            case .filter:
                if let originalName, originalKind == .filter {
                    _ = try await client.updateFilter(name: originalName, draft.rule())
                } else {
                    _ = try await client.createFilter(draft.rule())
                }
            case .redaction:
                if let originalName, originalKind == .redaction {
                    _ = try await client.updateRedaction(name: originalName, draft.redactionRule())
                } else {
                    _ = try await client.createRedaction(draft.redactionRule())
                }
            }
            if let originalName, let originalKind, originalKind != kind {
                do {
                    switch originalKind {
                    case .filter:
                        try await client.deleteFilter(name: originalName)
                    case .redaction:
                        try await client.deleteRedaction(name: originalName)
                    }
                } catch {
                    show(error, prefix: "Saved, but could not remove the old rule")
                }
            }
            await refreshConfig()
            banner = Banner(text: kind == .redaction ? "Redaction saved." : "Filter saved.", isError: false)
            return true
        } catch {
            show(error, prefix: kind == .redaction ? "Could not save redaction" : "Could not save filter")
            return false
        }
    }

    @discardableResult
    func deleteRule(kind: RuleKind, name: String) async -> Bool {
        do {
            switch kind {
            case .filter:
                try await client.deleteFilter(name: name)
            case .redaction:
                try await client.deleteRedaction(name: name)
            }
            await refreshConfig()
            banner = Banner(text: kind == .redaction ? "Redaction deleted." : "Filter deleted.", isError: false)
            return true
        } catch {
            show(error, prefix: kind == .redaction ? "Could not delete redaction" : "Could not delete filter")
            return false
        }
    }

    @discardableResult
    func setFilterEnabled(_ filter: FilterRule, enabled: Bool) async -> Bool {
        var updated = filter
        updated.enabled = enabled
        do {
            _ = try await client.updateFilter(name: filter.name, updated)
            await refreshConfig()
            return true
        } catch {
            show(error, prefix: "Could not update filter")
            return false
        }
    }

    @discardableResult
    func setRedactionEnabled(_ rule: RedactionRule, enabled: Bool) async -> Bool {
        var updated = rule
        updated.enabled = enabled
        do {
            _ = try await client.updateRedaction(name: rule.name, updated)
            await refreshConfig()
            return true
        } catch {
            show(error, prefix: "Could not update redaction")
            return false
        }
    }

    func testFilter(pattern: String, patternFile: String, replacement: String, sample: String) async throws -> FilterTestResult {
        try await client.testFilter(pattern: pattern, patternFile: patternFile, replacement: replacement, sample: sample)
    }

    // MARK: - Models

    @discardableResult
    func saveModel(originalID: String?, model: CatalogModel) async -> Bool {
        do {
            if let originalID, !originalID.isEmpty {
                _ = try await client.updateModel(id: originalID, model)
            } else {
                _ = try await client.createModel(model)
            }
            await refreshConfig()
            banner = Banner(text: "Model saved.", isError: false)
            return true
        } catch {
            show(error, prefix: "Could not save model")
            return false
        }
    }

    @discardableResult
    func deleteModel(id: String) async -> Bool {
        do {
            try await client.deleteModel(id: id)
            await refreshConfig()
            banner = Banner(text: "Model deleted.", isError: false)
            return true
        } catch {
            show(error, prefix: "Could not delete model")
            return false
        }
    }

    @discardableResult
    func fetchModel(id: String) async -> CatalogModel? {
        do {
            let model = try await client.fetchModel(id: id)
            await refreshConfig()
            banner = Banner(text: "Fetched \(model.id).", isError: false)
            return model
        } catch {
            show(error, prefix: "Could not fetch model")
            return nil
        }
    }

    // MARK: - Traffic

    func trafficDetail(id: UInt64) async throws -> TrafficEntry {
        try await client.fetchTrafficEntry(id: id)
    }

    func resume() async {
        do {
            _ = try await client.resume()
            banner = Banner(text: "Proxy resumed.", isError: false)
        } catch {
            show(error, prefix: "Could not resume the proxy")
        }
    }

    // MARK: - Live stream

    private func streamLoop() async {
        while !Task.isCancelled {
            do {
                streaming = true
                for try await entry in client.trafficStream() {
                    if Task.isCancelled { return }
                    insert(entry)
                }
                streaming = false
            } catch {
                streaming = false
                if Task.isCancelled { return }
                banner = Banner(text: "Traffic stream disconnected: \(error.localizedDescription)", isError: true)
            }
            try? await Task.sleep(nanoseconds: 2_000_000_000)
        }
    }

    private func insert(_ entry: TrafficEntry) {
        traffic.removeAll { $0.id == entry.id }
        traffic.insert(entry, at: 0)
        if traffic.count > 500 {
            traffic.removeLast(traffic.count - 500)
        }
    }

    private func show(_ error: Error, prefix: String) {
        banner = Banner(text: "\(prefix): \(error.localizedDescription)", isError: true)
    }
}
