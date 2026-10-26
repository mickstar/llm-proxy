import AppKit

MainActor.assumeIsolated {
    let app = NSApplication.shared
    let delegate = AppDelegate()
    app.delegate = delegate
    app.setActivationPolicy(.accessory)
    // NSApplication holds its delegate weakly; keeping the local alive for the
    // duration of run() prevents ARC from releasing it before launch.
    withExtendedLifetime(delegate) {
        app.run()
    }
}
