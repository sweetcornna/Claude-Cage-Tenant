import AppKit
import WebKit
import CryptoKit

/// Runs without XCTest so that `desktop/tenant/test.sh` needs only swiftc.
/// Compiled with every app source except main.swift. Nothing here loads a
/// page or touches the network.
@main
@MainActor
enum TenantTests {
    static var failures = 0
    static var checks = 0

    static func expect(_ condition: Bool, _ message: @autoclosure () -> String, line: Int = #line) {
        checks += 1
        if !condition {
            failures += 1
            print("FAIL (line \(line)): \(message())")
        }
    }

    static func main() {
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.prohibited)
        acceptsPortalOrigins()
        rejectsOtherAddresses()
        storesOnlyTheOrigin()
        windowValidatesBeforeOpening()
        windowClearsSavedOrigin()
        policyKeepsMainFrameOnPortal()
        policyAllowsOnlyWorkspaceFrames()
        policyRefusesWindowsAndDownloads()
        webWindowHasNoBridgeAndNoPersistence()
        webWindowReportsRefusalsAndErrors()
        delegateStartsWithModeChoice()
        startupRecoversAfterLocalFailure()
        localInstallerChecksDigest()
        delegateSwitchesPortalsCleanly()
        delegateBuildsUsableMenus()
        if failures > 0 {
            print("\(failures) of \(checks) checks failed")
            exit(1)
        }
        print("ok: \(checks) checks passed")
    }

    // MARK: - Address validation

    static func acceptsPortalOrigins() {
        let cases: [(input: String, origin: String, port: Int?)] = [
            ("https://cage.example.org", "https://cage.example.org", nil),
            ("https://cage.example.org/", "https://cage.example.org", nil),
            ("  HTTPS://Cage.Example.ORG/ \n", "https://cage.example.org", nil),
            ("https://cage.example.org:443", "https://cage.example.org", nil),
            ("https://cage.example.org:8443/", "https://cage.example.org:8443", 8443),
            ("https://cage.example.org:080", "https://cage.example.org:80", 80),
            ("https://a-b.portal.example.co.uk", "https://a-b.portal.example.co.uk", nil),
            ("https://xn--fiqs8s.example", "https://xn--fiqs8s.example", nil),
            ("https://cage.XN--fiqs8s/", "https://cage.xn--fiqs8s", nil),
            ("https://xn--bcher-kva.example:8443", "https://xn--bcher-kva.example:8443", 8443),
            ("https://cage.example.org\u{200B}", "https://cage.example.org", nil),
            ("https://1cage.example", "https://1cage.example", nil),
            ("https://localhost", "https://localhost", nil),
        ]
        for item in cases {
            do {
                let origin = try PortalAddress.validate(item.input)
                expect(origin.origin == item.origin, "\(item.input) -> \(origin.origin), expected \(item.origin)")
                expect(origin.port == item.port, "\(item.input) port \(String(describing: origin.port))")
                expect(origin.url.absoluteString == item.origin + "/", "\(item.input) url \(origin.url)")
                expect(origin.url.scheme == "https" && origin.url.user == nil && origin.url.query == nil,
                       "\(item.input) url parts")
                expect((try? PortalAddress.validate(origin.origin)) == origin, "\(item.input) normalized form revalidates")
            } catch {
                expect(false, "\(item.input) rejected: \(error)")
            }
        }
    }

    static func rejectsOtherAddresses() {
        let cases: [(input: String, error: PortalAddressError)] = [
            ("", .empty),
            (" \n\t ", .empty),
            ("cage.example.org", .notHTTPS),
            ("http://cage.example.org", .notHTTPS),
            ("ftp://cage.example.org", .notHTTPS),
            ("javascript:alert(1)", .notHTTPS),
            ("file:///etc/passwd", .notHTTPS),
            ("https:cage.example.org", .notURL),
            ("https:/cage.example.org", .notURL),
            ("https://alice@cage.example.org", .userInfo),
            ("https://alice:secret@cage.example.org", .userInfo),
            ("https://cage.example.org\\@evil.example", .userInfo),
            ("https://evil.example%2f@cage.example.org", .userInfo),
            ("https://cage.example.org?next=x", .query),
            ("https://cage.example.org/?next=x", .query),
            ("https://cage.example.org/#login", .fragment),
            ("https://cage.example.org#", .fragment),
            ("https://cage.example.org/?a#b", .fragment),
            ("https://cage.example.org/login", .path),
            ("https://cage.example.org//", .path),
            ("https://cage.example.org/%2e%2e/", .path),
            ("https://", .host),
            ("https:///", .host),
            ("https://cage.example.org.", .host),
            ("https://cage..example.org", .host),
            ("https://-cage.example.org", .host),
            ("https://cage-.example.org", .host),
            ("https://cage_vm.example.org", .host),
            ("https://cage%2eexample.org", .host),
            ("https://cage.1a", .host),
            ("https://" + String(repeating: "a", count: 64) + ".example", .host),
            ("https://[::1]", .ipAddress),
            ("https://[::1]:8443/", .ipAddress),
            ("https://127.0.0.1", .ipAddress),
            ("https://10.0.0.1:8443", .ipAddress),
            ("https://0x7f.1", .ipAddress),
            ("https://cage.0x1f", .ipAddress),
            ("https://2130706433", .ipAddress),
            ("https://cage.example.org:", .port),
            ("https://cage.example.org:0", .port),
            ("https://cage.example.org:65536", .port),
            ("https://cage.example.org:123456", .port),
            ("https://cage.example.org:+443", .port),
            ("https://cage.example.org:44a", .port),
            ("https://cage.example.org:443:443", .port),
            ("https://例子.测试", .invalidCharacters),
            ("https://cage.example.org /", .invalidCharacters),
            ("https://cage\texample.org", .invalidCharacters),
            ("https://cage.example.org\u{0}", .invalidCharacters),
            ("https://cage\u{200B}.example.org", .invalidCharacters),
            ("https://ｃage.example.org", .invalidCharacters),
            ("https://" + String(repeating: "a", count: 2048) + ".org", .invalidCharacters),
        ]
        for item in cases {
            do {
                let origin = try PortalAddress.validate(item.input)
                expect(false, "\(item.input.debugDescription) accepted as \(origin.origin)")
            } catch let error as PortalAddressError {
                expect(error == item.error, "\(item.input.debugDescription): \(error), expected \(item.error)")
                expect(!error.message.isEmpty, "\(error) has a message")
            } catch {
                expect(false, "\(item.input.debugDescription) threw \(error)")
            }
        }
    }

    // MARK: - Storage

    /// In-memory defaults: tests never write preference files, and every
    /// value the code stores is visible in `values`.
    final class MemoryDefaults: UserDefaults, @unchecked Sendable {
        var values: [String: Any] = [:]
        override func object(forKey defaultName: String) -> Any? { values[defaultName] }
        override func string(forKey defaultName: String) -> String? { values[defaultName] as? String }
        override func set(_ value: Any?, forKey defaultName: String) { values[defaultName] = value }
        override func removeObject(forKey defaultName: String) { values[defaultName] = nil }
    }

    static func withDefaults(_ body: (MemoryDefaults) -> Void) {
        body(MemoryDefaults())
    }

    static func storesOnlyTheOrigin() {
        withDefaults { defaults in
            let store = PortalStore(defaults: defaults)
            expect(store.load() == nil, "empty store loads nothing")

            let origin = try! PortalAddress.validate("https://Cage.Example.org:8443/")
            store.save(origin)
            expect(store.load() == origin, "saved origin loads back")
            expect(defaults.string(forKey: PortalStore.key) == "https://cage.example.org:8443", "stored as normalized origin")
            expect(Set(defaults.values.keys) == [PortalStore.key], "only the portal origin is stored: \(defaults.values.keys)")

            defaults.set("https://user:pw@cage.example.org", forKey: PortalStore.key)
            expect(store.load() == nil, "tampered value is not loaded")
            expect(defaults.object(forKey: PortalStore.key) == nil, "tampered value is removed")

            defaults.set(NSNumber(value: 42), forKey: PortalStore.key)
            expect(store.load() == nil, "non-string value is not loaded")
            expect(defaults.object(forKey: PortalStore.key) == nil, "non-string value is removed")

            store.save(origin)
            store.clear()
            expect(store.load() == nil && defaults.object(forKey: PortalStore.key) == nil, "clear removes the origin")
        }
    }

    // MARK: - Settings form

    @MainActor
    final class OpenerRecorder {
        var origins: [PortalOrigin] = []
        var clears = 0

        func opener() -> PortalWindowController.Opener {
            return { [self] origin in origins.append(origin) }
        }

        func clearer() -> PortalWindowController.Clearer {
            return { [self] in clears += 1 }
        }
    }

    static func windowValidatesBeforeOpening() {
        withDefaults { defaults in
            let store = PortalStore(defaults: defaults)
            let recorder = OpenerRecorder()
            let controller = PortalWindowController(store: store, opener: recorder.opener(), onClear: recorder.clearer())
            expect(controller.window?.title == "Cage Tenant", "window title")
            expect(controller.openButton.title == "打开门户", "open button opens the portal window")
            expect(controller.addressField.stringValue.isEmpty, "no saved address shown")
            expect(!controller.clearButton.isEnabled, "nothing to clear")
            expect(controller.statusLabel.isHidden, "no status before input")

            controller.addressField.stringValue = "http://cage.example.org"
            controller.openPortal(nil)
            expect(recorder.origins.isEmpty, "invalid address is not opened")
            expect(store.load() == nil, "invalid address is not saved")
            expect(!controller.statusLabel.isHidden, "error is shown")
            expect(controller.statusLabel.stringValue == PortalAddressError.notHTTPS.message, "specific error message")
            expect(controller.statusLabel.textColor == ClaudeStyle.danger, "error uses the Claude danger color")

            controller.addressField.stringValue = "  HTTPS://Cage.Example.org:443/ "
            controller.openPortal(nil)
            expect(recorder.origins.map(\.origin) == ["https://cage.example.org"], "opens normalized origin: \(recorder.origins)")
            expect(store.load()?.origin == "https://cage.example.org", "saves validated origin")
            expect(controller.addressField.stringValue == "https://cage.example.org", "field shows normalized origin")
            expect(controller.clearButton.isEnabled, "saved address can be cleared")
            expect(controller.statusLabel.textColor == ClaudeStyle.secondaryText, "success uses secondary text")
            expect(recorder.clears == 0, "opening does not clear")

            let reopened = PortalWindowController(store: store, opener: recorder.opener())
            expect(reopened.addressField.stringValue == "https://cage.example.org", "saved address is prefilled")
            expect(reopened.clearButton.isEnabled, "prefilled address can be cleared")
        }
    }

    static func windowClearsSavedOrigin() {
        withDefaults { defaults in
            let store = PortalStore(defaults: defaults)
            store.save(try! PortalAddress.validate("https://cage.example.org"))
            let recorder = OpenerRecorder()
            let controller = PortalWindowController(store: store, opener: recorder.opener(), onClear: recorder.clearer())
            controller.clearSaved(nil)
            expect(store.load() == nil, "store cleared")
            expect(defaults.object(forKey: PortalStore.key) == nil, "defaults key removed")
            expect(controller.addressField.stringValue.isEmpty, "field cleared")
            expect(!controller.clearButton.isEnabled, "clear disabled after clearing")
            expect(recorder.clears == 1, "clearing ends the portal session")
            expect(recorder.origins.isEmpty, "clearing opens nothing")
        }
    }

    // MARK: - Navigation policy

    static let vmID = "0123456789abcdef0123456789abcdef"

    static func policy(_ origin: String) -> PortalPolicy {
        PortalPolicy(portal: try! PortalAddress.validate(origin))
    }

    static func policyKeepsMainFrameOnPortal() {
        let portal = policy("https://cage.example.org")
        func main(_ url: String) -> PortalPolicy.Decision {
            portal.decideNavigation(to: URL(string: url), isMainFrame: true, opensNewWindow: false, isDownload: false)
        }
        for allowed in ["https://cage.example.org/", "https://cage.example.org", "https://CAGE.example.org/vms?x=1#top",
                        "https://cage.example.org:443/api/tenant/session"] {
            expect(main(allowed) == .allow, "main frame may load \(allowed)")
        }
        let refused: [(String, String)] = [
            ("http://cage.example.org/", "http://cage.example.org"),
            ("https://cage.example.org:8443/", "https://cage.example.org:8443"),
            ("https://evil.example/", "https://evil.example"),
            ("https://cage.example.org.evil.example/", "https://cage.example.org.evil.example"),
            ("https://www.cage.example.org/", "https://www.cage.example.org"),
            ("https://vm-\(vmID).cage.example.org/", "https://vm-\(vmID).cage.example.org"),
            ("https://user:pw@cage.example.org/", "https://cage.example.org"),
            ("https://example.org/", "https://example.org"),
            ("about:blank", "about:"),
            ("data:text/html,hi", "data:"),
            ("javascript:alert(1)", "javascript:"),
            ("file:///etc/passwd", "file:"),
            ("blob:https://cage.example.org/1234", "blob:"),
        ]
        for (url, origin) in refused {
            expect(main(url) == .blockMainFrame(origin: origin), "main frame refuses \(url): \(main(url))")
        }
        expect(portal.decideNavigation(to: nil, isMainFrame: true, opensNewWindow: false, isDownload: false)
            == .blockMainFrame(origin: "未知地址"), "main frame refuses a missing URL")
        expect(PortalPolicy.displayOrigin(URL(string: "https://user:secret@evil.example:8443/p?token=1#t"))
            == "https://evil.example:8443", "refusal messages never include credentials, paths or queries")

        let custom = policy("https://cage.example.org:8443")
        expect(custom.isPortal(URL(string: "https://cage.example.org:8443/")), "custom port portal")
        expect(!custom.isPortal(URL(string: "https://cage.example.org/")), "default port is another origin")
        let idn = policy("https://xn--fiqs8s.example")
        expect(idn.isPortal(URL(string: "https://xn--fiqs8s.example/")), "punycode portal stays ASCII")
    }

    static func policyAllowsOnlyWorkspaceFrames() {
        let portal = policy("https://cage.example.org")
        func frame(_ url: String, _ policy: PortalPolicy = portal) -> PortalPolicy.Decision {
            policy.decideNavigation(to: URL(string: url), isMainFrame: false, opensNewWindow: false, isDownload: false)
        }
        for allowed in ["https://vm-\(vmID).cage.example.org/_tenant/enter#ticket",
                        "https://vm-\(vmID).cage.example.org:443/", "https://cage.example.org/", "about:blank", "about:srcdoc"] {
            expect(frame(allowed) == .allow, "frame may load \(allowed)")
        }
        for refused in ["http://vm-\(vmID).cage.example.org/", "https://vm-\(vmID).cage.example.org:8443/",
                        "https://vm-0123.cage.example.org/", "https://vm-\(vmID)0.cage.example.org/",
                        "https://vm-0123456789abcdef0123456789abcdeg.cage.example.org/",
                        "https://vm-\(vmID).other.example/", "https://x.vm-\(vmID).cage.example.org/",
                        "https://vm-\(vmID).cage.example.org.evil.example/", "https://vm-.cage.example.org/",
                        "https://evil.example/", "data:text/html,hi", "about:blank#x", "about:srcdocx",
                        "javascript:alert(1)", "blob:https://vm-\(vmID).cage.example.org/1"] {
            if case .blockSubframe = frame(refused) {} else { expect(false, "frame refuses \(refused): \(frame(refused))") }
        }
        let custom = policy("https://cage.example.org:8443")
        expect(frame("https://vm-\(vmID).cage.example.org:8443/", custom) == .allow, "workspace on the portal's port")
        if case .blockSubframe = frame("https://vm-\(vmID).cage.example.org/", custom) {} else {
            expect(false, "workspace on another port is refused")
        }
    }

    static func policyRefusesWindowsAndDownloads() {
        let portal = policy("https://cage.example.org")
        let home = URL(string: "https://cage.example.org/")
        let workspace = URL(string: "https://vm-\(vmID).cage.example.org/file")
        expect(portal.decideNavigation(to: home, isMainFrame: true, opensNewWindow: true, isDownload: false)
            == .blockNewWindow, "same-origin popups are refused")
        expect(portal.decideNavigation(to: workspace, isMainFrame: false, opensNewWindow: false, isDownload: true)
            == .blockDownload, "download requests are refused")
        expect(portal.decideResponse(from: workspace, isMainFrame: false, canShowMIMEType: false, isAttachment: false)
            == .blockDownload, "undisplayable responses are refused")
        expect(portal.decideResponse(from: workspace, isMainFrame: false, canShowMIMEType: true, isAttachment: true)
            == .blockDownload, "attachments are refused")
        expect(portal.decideResponse(from: home, isMainFrame: true, canShowMIMEType: true, isAttachment: false)
            == .allow, "portal page is shown")
        expect(portal.decideResponse(from: URL(string: "https://evil.example/"), isMainFrame: true,
                                     canShowMIMEType: true, isAttachment: false)
            == .blockMainFrame(origin: "https://evil.example"), "a redirect off the portal is refused at the response")
        for decision in [PortalPolicy.Decision.blockNewWindow, .blockDownload,
                         .blockMainFrame(origin: "https://evil.example"), .blockSubframe(origin: "https://evil.example")] {
            expect(PortalWebWindowController.message(for: decision)?.isEmpty == false, "message for \(decision)")
        }
        expect(PortalWebWindowController.message(for: .allow) == nil, "no message when allowed")
    }

    // MARK: - Portal window

    static func webWindowHasNoBridgeAndNoPersistence() {
        let origin = try! PortalAddress.validate("https://cage.example.org")
        let first = PortalWebWindowController(origin: origin)
        let second = PortalWebWindowController(origin: origin)
        guard let webView = first.webView else { return expect(false, "web view exists") }
        let configuration = webView.configuration
        expect(!configuration.websiteDataStore.isPersistent, "website data lives in memory only")
        expect(first.dataStore !== second.dataStore, "every portal window has its own store")
        expect(configuration.websiteDataStore === first.dataStore, "the web view uses that store")
        expect(configuration.userContentController.userScripts.isEmpty, "no injected scripts")
        expect(!configuration.preferences.javaScriptCanOpenWindowsAutomatically, "scripts cannot open windows")
        expect(webView.navigationDelegate === first && webView.uiDelegate === first, "policy delegates are installed")
        expect(webView.url == nil, "nothing loads until asked")
        expect(first.window?.isRestorable == false, "window state is not saved")
        expect(first.window?.subtitle == "https://cage.example.org", "window shows the portal origin")
        expect(first.webView(webView, createWebViewWith: WKWebViewConfiguration(), for: WKNavigationAction(),
                             windowFeatures: WKWindowFeatures()) == nil, "new windows are refused")
        expect(first.noticeKind == .info && !first.noticeBar.isHidden, "refusal is shown")

        var discarded: [PortalWebWindowController] = []
        first.onDiscard = { discarded.append($0) }
        first.discard()
        first.discard()
        expect(first.isDiscarded && first.webView == nil, "discard releases the web view")
        expect(webView.superview == nil && webView.navigationDelegate == nil && webView.uiDelegate == nil,
               "the old web view is detached")
        expect(discarded.count == 1 && discarded.first === first, "discard is reported once")
        second.discard()
    }

    static func webWindowReportsRefusalsAndErrors() {
        let controller = PortalWebWindowController(origin: try! PortalAddress.validate("https://cage.example.org"))
        defer { controller.discard() }
        expect(controller.noticeBar.isHidden, "no notice at first")
        controller.show("测试", kind: .error)
        expect(!controller.noticeBar.isHidden && !controller.reloadButton.isHidden, "errors offer reload")
        controller.show("测试", kind: .info)
        expect(controller.reloadButton.isHidden, "notices do not offer reload")
        controller.dismissNotice(nil)
        expect(controller.noticeBar.isHidden && controller.noticeKind == nil, "notice can be dismissed")

        let cancelled = NSError(domain: NSURLErrorDomain, code: NSURLErrorCancelled)
        let interrupted = NSError(domain: "WebKitErrorDomain", code: 102)
        let untrusted = NSError(domain: NSURLErrorDomain, code: NSURLErrorServerCertificateUntrusted)
        let offline = NSError(domain: NSURLErrorDomain, code: NSURLErrorNotConnectedToInternet)
        expect(PortalWebWindowController.message(forLoadError: cancelled) == nil, "cancellations are silent")
        expect(PortalWebWindowController.message(forLoadError: interrupted) == nil, "policy refusals are not load errors")
        expect(PortalWebWindowController.message(forLoadError: untrusted)?.contains("证书未通过系统校验") == true,
               "certificate failures are explained")
        expect(PortalWebWindowController.message(forLoadError: offline)?.hasPrefix("无法载入门户") == true, "other failures")
    }

    // MARK: - Application

    static func delegateStartsWithModeChoice() {
        withDefaults { defaults in
            let saved = try! PortalAddress.validate("https://cage.example.org")
            PortalStore(defaults: defaults).save(saved)
            let delegate = AppDelegate(defaults: defaults, presentsWindows: false)
            delegate.applicationDidFinishLaunching(Notification(name: NSApplication.didFinishLaunchingNotification))
            guard let startup = delegate.startup else { return expect(false, "launch shows the mode selector") }
            expect(delegate.portal == nil, "launch does not open the saved portal")
            expect(startup.localButton.title == "本地安装", "launch offers local installation")
            expect(startup.remoteButton.title == "连接远程", "launch offers a remote portal")
            expect(startup.statusLabel.isHidden, "launch shows only the choices")

            startup.openRemote(nil)
            let form = delegate.settingsController()
            expect(form.addressField.stringValue == saved.origin, "remote choice shows the saved address")
            expect(delegate.portal == nil, "opening remote settings does not connect yet")
            form.goBack(nil)
            expect(delegate.startup === startup, "back returns to the original mode selector")
            expect(PortalStore(defaults: defaults).load() == saved, "back preserves the saved address")
        }
    }

    static func startupRecoversAfterLocalFailure() {
        var attempts = 0
        var remote = 0
        let startup = StartupWindowController(onLocal: {
            attempts += 1
            throw LocalCage.Failure.download
        }, onRemote: { remote += 1 })
        startup.openLocal(nil)
        expect(!startup.localButton.isEnabled && !startup.remoteButton.isEnabled, "both choices wait while local install runs")
        startup.openLocal(nil)
        startup.openRemote(nil)
        expect(remote == 0, "remote cannot start while local install runs")
        let deadline = Date().addingTimeInterval(5)
        while !startup.localButton.isEnabled, Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
        }
        expect(attempts == 1, "a second click while busy does nothing")
        expect(startup.localButton.isEnabled && startup.remoteButton.isEnabled, "both choices come back after a failure")
        expect(!startup.statusLabel.isHidden && startup.statusLabel.stringValue == LocalCage.Failure.download.errorDescription,
               "the failure is shown")
        expect(startup.statusLabel.textColor == ClaudeStyle.danger, "the failure uses the error color")
        startup.openRemote(nil)
        expect(remote == 1 && startup.statusLabel.isHidden, "remote works after a failure and clears the message")
    }

    static func localInstallerChecksDigest() {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("cage-tenant-local-\(UUID().uuidString)")
        try! FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        // "evil" has the same length, so only the SHA-256 comparison can refuse it.
        let good = Data("the real installer".utf8)
        let evil = Data("the evil installer".utf8)
        let digest = SHA256.hash(data: good).map { String(format: "%02x", $0) }.joined()
        let file = folder.appendingPathComponent("installer.dmg")
        try! good.write(to: file)
        expect((try? LocalCage.verify(file, size: good.count, digest: digest)) == true, "the pinned installer is accepted")
        try! evil.write(to: file)
        expect((try? LocalCage.verify(file, size: good.count, digest: digest)) == false, "a same-size installer with another digest is refused")
        expect((try? LocalCage.verify(file, size: good.count + 1, digest: digest)) == false, "a file of another size is refused")
        let link = folder.appendingPathComponent("link.dmg")
        try! FileManager.default.createSymbolicLink(at: link, withDestinationURL: folder)
        expect((try? LocalCage.verify(link, size: good.count, digest: digest)) != true, "a folder behind a link is refused")
    }

    static func delegateSwitchesPortalsCleanly() {
        withDefaults { defaults in
            let delegate = AppDelegate(defaults: defaults, presentsWindows: false)
            let first = try! PortalAddress.validate("https://cage.example.org")
            let second = try! PortalAddress.validate("https://other.example.org")
            let a = delegate.openPortal(first)
            let store = a.dataStore
            expect(delegate.portal === a, "portal window is tracked")
            expect(delegate.openPortal(first) === a && !a.isDiscarded, "same origin keeps the window and session")
            let b = delegate.openPortal(second)
            expect(b !== a && a.isDiscarded && a.webView == nil, "a new origin discards the old web view")
            expect(b.dataStore !== store && !b.dataStore.isPersistent, "a new origin gets a new in-memory store")
            expect(delegate.portal === b, "the new window is current")
            b.window?.close()
            expect(b.isDiscarded && delegate.portal == nil, "closing the window ends the session")

            let c = delegate.openPortal(first)
            let form = delegate.settingsController()
            expect(form === delegate.settingsController(), "one settings form")
            form.addressField.stringValue = "https://cage.example.org"
            form.openPortal(nil)
            expect(delegate.portal === c, "opening the same origin from the form keeps the session")
            expect(PortalStore(defaults: defaults).load() == first, "the form saved the origin")
            form.clearSaved(nil)
            expect(c.isDiscarded && delegate.portal == nil, "clearing the address discards the portal session")
            expect(Set(defaults.values.keys).isEmpty, "nothing else is stored: \(defaults.values.keys)")

            expect(delegate.applicationShouldTerminateAfterLastWindowClosed(NSApp), "quits after the last window closes")
            expect(delegate.applicationSupportsSecureRestorableState(NSApp), "secure restorable state")
            expect(delegate.responds(to: #selector(AppDelegate.showAbout(_:))), "delegate handles About")
        }
    }

    static func delegateBuildsUsableMenus() {
        let menu = AppDelegate.makeMainMenu()
        func find(_ action: String) -> NSMenuItem? {
            for top in menu.items {
                if let item = top.submenu?.items.first(where: { $0.action.map(NSStringFromSelector) == action }) {
                    return item
                }
            }
            return nil
        }
        let expected: [(action: String, key: String, modifiers: NSEvent.ModifierFlags)] = [
            ("showSettings:", ",", .command),
            ("reloadPortal:", "r", .command),
            ("terminate:", "q", .command),
            ("hide:", "h", .command),
            ("hideOtherApplications:", "h", [.command, .option]),
            ("undo:", "z", .command),
            ("redo:", "z", [.command, .shift]),
            ("cut:", "x", .command),
            ("copy:", "c", .command),
            ("paste:", "v", .command),
            ("selectAll:", "a", .command),
            ("performMiniaturize:", "m", .command),
            ("performClose:", "w", .command),
        ]
        for item in expected {
            let found = find(item.action)
            expect(found != nil, "menu has \(item.action)")
            expect(found?.keyEquivalent == item.key, "\(item.action) key")
            expect(found?.keyEquivalentModifierMask == item.modifiers, "\(item.action) modifiers")
        }
        expect(find("showAbout:") != nil, "menu has About")
        expect(NSApp.responds(to: #selector(NSApplication.orderFrontStandardAboutPanel(options:))), "about panel available")
    }
}
