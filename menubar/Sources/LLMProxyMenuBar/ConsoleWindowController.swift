import AppKit
import SwiftUI

@MainActor
final class ConsoleWindowController: NSWindowController, NSWindowDelegate {
    private let console: ConsoleModel
    private let onClose: () -> Void

    init(stats: StatsModel, console: ConsoleModel, onClose: @escaping () -> Void) {
        self.console = console
        self.onClose = onClose
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1180, height: 760),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "llm-proxy"
        window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 1040, height: 680)
        window.center()
        window.setFrameAutosaveName("LLMProxyConsole")
        if !NSScreen.screens.contains(where: { $0.visibleFrame.intersects(window.frame) }) {
            window.center()
        }
        window.contentViewController = NSHostingController(
            rootView: ConsoleView(stats: stats, model: console)
        )
        window.setContentSize(NSSize(width: 1180, height: 760))
        super.init(window: window)
        window.delegate = self
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not used")
    }

    func windowWillClose(_ notification: Notification) {
        console.stop()
        onClose()
    }
}
