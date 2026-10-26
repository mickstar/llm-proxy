import AppKit
import Combine
import MenuBarCore
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var refreshTask: Task<Void, Never>?
    private let model = StatsModel()
    private var panel: StatusPanel!
    private var hosting: NSHostingView<ControlCenterView>!
    private var cancellables = Set<AnyCancellable>()
    private var localKeyMonitor: Any?
    private var resignObserver: NSObjectProtocol?
    private var consoleModel: ConsoleModel?
    private var consoleWindow: ConsoleWindowController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        installMainMenu()
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            button.image = NSImage(systemSymbolName: "bolt.horizontal.fill", accessibilityDescription: "llm-proxy")
            button.image?.isTemplate = true
            button.imagePosition = .imageLeading
            button.font = .monospacedDigitSystemFont(ofSize: 12, weight: .medium)
            button.title = "—"
            button.target = self
            button.action = #selector(togglePanel)
            button.sendAction(on: [.leftMouseUp])
            button.toolTip = "llm-proxy"
        }

        makePanel()
        observeModel()

        if CommandLine.arguments.contains("--console") {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) { self.showConsole() }
        }
        refreshTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                await model.refresh()
                updateStatusItem()
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        refreshTask?.cancel()
        removeDismissHandlers()
        consoleModel?.stop()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    // MARK: - Panel

    private func makePanel() {
        let panel = StatusPanel(
            contentRect: NSRect(x: 0, y: 0, width: 360, height: 600),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.level = .statusBar
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient]
        panel.hidesOnDeactivate = false
        panel.isMovable = false
        panel.becomesKeyOnlyIfNeeded = false
        panel.animationBehavior = .utilityWindow

        let view = ControlCenterView(
            model: model,
            controlURL: model.client.baseURL.absoluteString,
            onCopy: { [weak self] in self?.copyStats() },
            onQuit: { NSApp.terminate(nil) },
            onExpand: { [weak self] in self?.showConsole() }
        )
        let hosting = NSHostingView(rootView: view)
        hosting.autoresizingMask = [.width, .height]
        hosting.wantsLayer = true
        hosting.layer?.backgroundColor = NSColor.clear.cgColor
        panel.contentView = hosting

        self.panel = panel
        self.hosting = hosting
        resizePanel()
    }

    /// resizePanel sizes the window to the SwiftUI content so the glass panel
    /// hugs its contents instead of leaving empty space.
    private func resizePanel() {
        guard let panel, let hosting else { return }
        let width: CGFloat = 360
        hosting.frame = NSRect(x: 0, y: 0, width: width, height: 200)
        hosting.layoutSubtreeIfNeeded()
        let height = max(hosting.fittingSize.height, 180)
        let size = NSSize(width: width, height: height)
        let changed = panel.frame.size != size
        if changed {
            let wasVisible = panel.isVisible
            let top = panel.frame.maxY
            panel.setContentSize(size)
            if wasVisible {
                panel.setFrameOrigin(NSPoint(x: panel.frame.minX, y: top - size.height))
            }
        }
        hosting.frame = NSRect(origin: .zero, size: size)
    }

    private func showPanel() {
        resizePanel()
        positionPanel()
        panel.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        installDismissHandlers()
    }

    private func hidePanel() {
        panel.orderOut(nil)
        removeDismissHandlers()
    }

    @objc private func togglePanel() {
        if panel.isVisible {
            hidePanel()
        } else {
            showPanel()
        }
    }

    /// showConsole opens the full native console. The process temporarily
    /// becomes a regular app so the window gets a Dock tile and Cmd-Tab entry,
    /// then returns to agent-only when the window closes.
    private func showConsole() {
        hidePanel()
        if consoleWindow == nil {
            let console = ConsoleModel()
            consoleModel = console
            consoleWindow = ConsoleWindowController(stats: model, console: console) { [weak self] in
                self?.consoleModel?.stop()
                NSApp.setActivationPolicy(.accessory)
            }
        }
        consoleModel?.start()
        NSApp.setActivationPolicy(.regular)
        consoleWindow?.showWindow(nil)
        consoleWindow?.window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func installMainMenu() {
        let mainMenu = NSMenu()

        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(
            withTitle: "About LLM Proxy",
            action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)),
            keyEquivalent: ""
        )
        appMenu.addItem(.separator())
        appMenu.addItem(
            withTitle: "Hide LLM Proxy",
            action: #selector(NSApplication.hide(_:)),
            keyEquivalent: "h"
        )
        appMenu.addItem(
            withTitle: "Quit LLM Proxy",
            action: #selector(NSApplication.terminate(_:)),
            keyEquivalent: "q"
        )
        appItem.submenu = appMenu

        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = editMenu

        let windowItem = NSMenuItem()
        mainMenu.addItem(windowItem)
        let windowMenu = NSMenu(title: "Window")
        windowMenu.addItem(
            withTitle: "Minimize",
            action: #selector(NSWindow.performMiniaturize(_:)),
            keyEquivalent: "m"
        )
        windowMenu.addItem(
            withTitle: "Close",
            action: #selector(NSWindow.performClose(_:)),
            keyEquivalent: "w"
        )
        windowItem.submenu = windowMenu
        NSApp.windowsMenu = windowMenu

        NSApp.mainMenu = mainMenu
    }

    private func positionPanel() {
        guard let button = statusItem.button, let window = button.window else { return }
        let buttonRect = window.convertToScreen(button.convert(button.bounds, to: nil))
        let size = panel.frame.size
        let screen = window.screen ?? NSScreen.main
        let visible = screen?.visibleFrame ?? .zero

        var x = buttonRect.midX - size.width / 2
        var y = buttonRect.minY - size.height - 7
        if !visible.isEmpty {
            x = min(max(x, visible.minX + 8), visible.maxX - size.width - 8)
            if y < visible.minY + 8 {
                y = visible.minY + 8
            }
        }
        panel.setFrameOrigin(NSPoint(x: x, y: y))
    }

    private func installDismissHandlers() {
        removeDismissHandlers()
        localKeyMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            if event.keyCode == 53 { // esc
                self?.hidePanel()
                return nil
            }
            return event
        }
        resignObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didResignActiveNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.hidePanel() }
        }
    }

    private func removeDismissHandlers() {
        if let monitor = localKeyMonitor {
            NSEvent.removeMonitor(monitor)
            localKeyMonitor = nil
        }
        if let observer = resignObserver {
            NotificationCenter.default.removeObserver(observer)
            resignObserver = nil
        }
    }

    private func observeModel() {
        Publishers.CombineLatest(model.$snapshot, model.$offlineReason)
            .map { snapshot, offline in
                "\(min(snapshot?.models.count ?? 0, 4))|\(offline != nil)|\(snapshot?.halt.halted ?? false)"
            }
            .removeDuplicates()
            .sink { [weak self] _ in
                DispatchQueue.main.async { self?.resizePanel() }
            }
            .store(in: &cancellables)
    }

    // MARK: - Status item

    private func updateStatusItem() {
        guard let button = statusItem.button else { return }
        if model.offlineReason != nil {
            setStatusTitle("offline", color: .secondaryLabelColor)
            button.toolTip = "llm-proxy unreachable at \(model.client.baseURL.absoluteString)"
            return
        }
        guard let snapshot = model.snapshot else {
            setStatusTitle("…", color: .secondaryLabelColor)
            return
        }
        setStatusTitle(
            Presentation.statusTitle(snapshot),
            color: snapshot.halt.halted ? .systemRed : .labelColor
        )
        button.toolTip = [
            String(format: "%.1f completion tok/s · %.1f req/min", snapshot.throughput.completionTokensPerSecond, snapshot.throughput.requestsPerMinute),
            "\(Presentation.grouped(snapshot.totals.totalTokens)) tokens · \(Presentation.money(snapshot.totals.costUsd))",
        ].joined(separator: "\n")
    }

    private func setStatusTitle(_ text: String, color: NSColor) {
        guard let button = statusItem.button else { return }
        button.attributedTitle = NSAttributedString(
            string: text,
            attributes: [
                .font: NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .medium),
                .foregroundColor: color,
            ]
        )
    }

    private func copyStats() {
        guard let data = model.raw, let text = String(data: data, encoding: .utf8) else { return }
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(text, forType: .string)
    }
}

/// StatusPanel is a borderless panel that can take key focus for Escape while
/// never becoming the app's main window.
final class StatusPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
}
