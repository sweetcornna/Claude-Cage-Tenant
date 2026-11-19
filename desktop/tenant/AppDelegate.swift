import AppKit

/// Application lifecycle for Cage Tenant: a settings form and at most one
/// portal window. Opening a different portal discards the previous window,
/// its web view and its in-memory session before the new one is used.
/// There is no update check and no native bridge into the web content.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let store: PortalStore
    /// False in tests: windows are created but never shown or loaded.
    private let presentsWindows: Bool
    private var form: PortalWindowController?
    private(set) var startup: StartupWindowController?
    private(set) var portal: PortalWebWindowController?
    private let openLocal: @MainActor () async throws -> Void

    init(defaults: UserDefaults = .standard, presentsWindows: Bool = true,
         openLocal: @escaping @MainActor () async throws -> Void = LocalCage.open) {
        store = PortalStore(defaults: defaults)
        self.presentsWindows = presentsWindows
        self.openLocal = openLocal
        super.init()
    }

    func applicationWillFinishLaunching(_ notification: Notification) {
        NSWindow.allowsAutomaticWindowTabbing = false
        NSApp.mainMenu = Self.makeMainMenu()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        showStartup(nil)
        if presentsWindows { NSApp.activate() }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            if let portal { present(portal) } else { showStartup(nil) }
        }
        return true
    }

    @objc func showStartup(_ sender: Any?) {
        if startup == nil {
            startup = StartupWindowController(onLocal: openLocal, onRemote: { [weak self] in self?.showSettings(nil) })
        }
        if let startup, presentsWindows { present(startup) }
        form?.window?.orderOut(nil)
    }

    /// Closing the last window quits; the in-memory session ends with it.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool { true }

    /// Returns the one settings form, creating it on first use.
    func settingsController() -> PortalWindowController {
        if let form { return form }
        let controller = PortalWindowController(
            store: store,
            opener: { [weak self] origin in self?.openPortal(origin) },
            onClear: { [weak self] in self?.discardPortal() },
            onBack: { [weak self] in self?.showStartup(nil) })
        form = controller
        return controller
    }

    @objc func showSettings(_ sender: Any?) {
        let controller = settingsController()
        controller.showSaved()
        if presentsWindows { present(controller) }
        startup?.window?.orderOut(nil)
    }

    /// Shows the portal window for `origin`. The same origin keeps the current
    /// window and session; a different origin gets a new window with a new
    /// in-memory store, and the old one is discarded.
    @discardableResult
    func openPortal(_ origin: PortalOrigin) -> PortalWebWindowController {
        if let portal, !portal.isDiscarded, portal.policy.portal == origin {
            if presentsWindows { present(portal) }
            form?.window?.orderOut(nil)
            startup?.window?.orderOut(nil)
            return portal
        }
        let previous = portal
        let next = PortalWebWindowController(origin: origin)
        next.onDiscard = { [weak self] closed in
            if self?.portal === closed { self?.portal = nil }
        }
        portal = next
        if presentsWindows {
            present(next)
            next.load()
        }
        // Show the new window first so closing the old one never closes the last window.
        previous?.discard()
        form?.window?.orderOut(nil)
        startup?.window?.orderOut(nil)
        return next
    }

    func discardPortal() {
        let current = portal
        portal = nil
        current?.discard()
    }

    private func present(_ controller: NSWindowController) {
        controller.showWindow(nil)
        controller.window?.makeKeyAndOrderFront(nil)
    }

    @objc func showAbout(_ sender: Any?) {
        NSApp.orderFrontStandardAboutPanel()
        NSApp.activate()
    }

    /// Builds the menu bar. The Edit menu is required for copy and paste in
    /// the address field and the portal; AppKit routes these actions through
    /// the responder chain. Reload is handled by the portal window.
    static func makeMainMenu() -> NSMenu {
        let appName = "Cage Tenant"
        let main = NSMenu()

        let app = NSMenu(title: appName)
        app.addItem(item("关于 \(appName)", #selector(showAbout(_:))))
        app.addItem(.separator())
        app.addItem(item("选择使用方式…", #selector(showStartup(_:))))
        app.addItem(item("门户设置…", #selector(showSettings(_:)), ","))
        app.addItem(.separator())
        app.addItem(item("隐藏 \(appName)", #selector(NSApplication.hide(_:)), "h"))
        app.addItem(item("隐藏其他", #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]))
        app.addItem(item("全部显示", #selector(NSApplication.unhideAllApplications(_:))))
        app.addItem(.separator())
        app.addItem(item("退出 \(appName)", #selector(NSApplication.terminate(_:)), "q"))
        add(app, to: main)

        let edit = NSMenu(title: "编辑")
        edit.addItem(item("撤销", Selector(("undo:")), "z"))
        edit.addItem(item("重做", Selector(("redo:")), "z", [.command, .shift]))
        edit.addItem(.separator())
        edit.addItem(item("剪切", #selector(NSText.cut(_:)), "x"))
        edit.addItem(item("拷贝", #selector(NSText.copy(_:)), "c"))
        edit.addItem(item("粘贴", #selector(NSText.paste(_:)), "v"))
        edit.addItem(item("全选", #selector(NSText.selectAll(_:)), "a"))
        add(edit, to: main)

        let view = NSMenu(title: "显示")
        view.addItem(item("重新加载门户", #selector(PortalWebWindowController.reloadPortal(_:)), "r"))
        add(view, to: main)

        let window = NSMenu(title: "窗口")
        window.addItem(item("最小化", #selector(NSWindow.performMiniaturize(_:)), "m"))
        window.addItem(item("关闭", #selector(NSWindow.performClose(_:)), "w"))
        add(window, to: main)
        NSApp?.windowsMenu = window

        return main
    }

    private static func item(_ title: String, _ action: Selector, _ key: String = "",
                             _ modifiers: NSEvent.ModifierFlags = .command) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        if !key.isEmpty { item.keyEquivalentModifierMask = modifiers }
        return item
    }

    private static func add(_ submenu: NSMenu, to main: NSMenu) {
        let holder = NSMenuItem(title: submenu.title, action: nil, keyEquivalent: "")
        holder.submenu = submenu
        main.addItem(holder)
    }
}
