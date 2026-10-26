import Combine
import Foundation
import MenuBarCore

/// StatsModel owns the polling state shared by the status item and the
/// control-center panel. It keeps the last good snapshot so the panel can show
/// history while a transient poll fails, but reports failures separately.
@MainActor
final class StatsModel: ObservableObject {
    @Published private(set) var snapshot: StatsSnapshot?
    @Published private(set) var raw: Data?
    @Published private(set) var offlineReason: String?
    @Published private(set) var lastUpdated: Date?

    let client = ControlClient()

    var isOffline: Bool { offlineReason != nil }

    func refresh() async {
        do {
            let (snapshot, raw) = try await client.fetchSnapshot()
            self.snapshot = snapshot
            self.raw = raw
            self.offlineReason = nil
            self.lastUpdated = Date()
        } catch {
            self.offlineReason = Self.message(for: error)
        }
    }

    func resume() async {
        do {
            try await client.resume()
        } catch {
            self.offlineReason = Self.message(for: error)
        }
        await refresh()
    }

    private static func message(for error: Error) -> String {
        if let control = error as? ControlError, control.isNotFound {
            return "This proxy has no /api/stats endpoint. Restart it from a current build (task reload)."
        }
        return error.localizedDescription
    }
}
