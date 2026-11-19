import AppKit
import WebKit

/// The portal, shown in this app's own window. Each window has its own
/// in-memory website data store, so the portal session (cookies, storage,
/// cache) lives only as long as this window and is never written to disk.
///
/// The app never reads that session: there is no script message handler, no
/// injected script, no JavaScript evaluation and no cookie store access.
/// TLS server trust is left to the system's default validation.
@MainActor
final class PortalWebWindowController: NSWindowController, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate {
    enum NoticeKind: Equatable { case info, error }

    let policy: PortalPolicy
    let dataStore: WKWebsiteDataStore
    private(set) var webView: WKWebView?
    private(set) var isDiscarded = false
    /// Called once when the window is closed or discarded.
    var onDiscard: (@MainActor (PortalWebWindowController) -> Void)?

    let noticeLabel = NSTextField(wrappingLabelWithString: "")
    let noticeBar = NSStackView()
    let reloadButton = ClaudeButton(title: "重新加载", kind: .primary)
    private let dismissButton = ClaudeButton(title: "关闭提示", kind: .secondary)
    private(set) var noticeKind: NoticeKind?

    init(origin: PortalOrigin) {
        policy = PortalPolicy(portal: origin)
        let configuration = Self.makeConfiguration()
        dataStore = configuration.websiteDataStore
        let webView = WKWebView(frame: .zero, configuration: configuration)
        self.webView = webView
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1280, height: 820),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: true)
        window.title = "Cage Tenant"
        window.subtitle = origin.origin
        window.isReleasedWhenClosed = false
        window.isRestorable = false
        window.tabbingMode = .disallowed
        window.contentMinSize = NSSize(width: 720, height: 480)
        window.backgroundColor = ClaudeStyle.background
        super.init(window: window)
        window.delegate = self
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.allowsBackForwardNavigationGestures = false
        webView.setAccessibilityLabel("门户")
        webView.underPageBackgroundColor = ClaudeStyle.background
        buildContent(in: window, webView: webView)
        window.center()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    /// A fresh, non-persistent configuration with no native bridge.
    static func makeConfiguration() -> WKWebViewConfiguration {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        configuration.userContentController = WKUserContentController()
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        configuration.preferences.isFraudulentWebsiteWarningEnabled = true
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        configuration.defaultWebpagePreferences.preferredContentMode = .desktop
        return configuration
    }

    private func buildContent(in window: NSWindow, webView: WKWebView) {
        noticeLabel.isSelectable = true
        noticeLabel.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        reloadButton.target = self
        reloadButton.action = #selector(reloadPortal(_:))
        dismissButton.target = self
        dismissButton.action = #selector(dismissNotice(_:))
        // The message takes the free width; the buttons keep their size.
        noticeLabel.setContentHuggingPriority(.defaultLow - 1, for: .horizontal)
        for button in [reloadButton, dismissButton] {
            button.setContentHuggingPriority(.required, for: .horizontal)
            button.setContentCompressionResistancePriority(.required, for: .horizontal)
        }
        let row = NSStackView(views: [noticeLabel, reloadButton, dismissButton])
        row.orientation = .horizontal
        row.alignment = .centerY
        row.spacing = 8
        row.edgeInsets = NSEdgeInsets(top: 8, left: 12, bottom: 8, right: 12)
        let separator = NSBox()
        separator.boxType = .separator
        noticeBar.addArrangedSubview(separator)
        noticeBar.addArrangedSubview(row)
        noticeBar.orientation = .vertical
        noticeBar.alignment = .width
        noticeBar.spacing = 0
        noticeBar.isHidden = true

        let layout = NSStackView(views: [webView, noticeBar])
        layout.orientation = .vertical
        layout.alignment = .width
        layout.spacing = 0
        layout.translatesAutoresizingMaskIntoConstraints = false
        webView.setContentHuggingPriority(.defaultLow, for: .vertical)
        let content = NSView()
        content.addSubview(layout)
        NSLayoutConstraint.activate([
            layout.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            layout.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            layout.topAnchor.constraint(equalTo: content.topAnchor),
            layout.bottomAnchor.constraint(equalTo: content.bottomAnchor),
        ])
        window.contentView = content
        window.initialFirstResponder = webView
    }

    /// Loads the portal root.
    func load() {
        guard let webView else { return }
        webView.load(URLRequest(url: policy.portal.url))
    }

    @objc func reloadPortal(_ sender: Any?) {
        guard let webView else { return }
        hideNotice()
        if let url = webView.url, policy.isPortal(url) { webView.reload() } else { load() }
    }

    @objc func dismissNotice(_ sender: Any?) { hideNotice() }

    /// Stops loading, releases the web view and empties its in-memory store.
    /// The data is deleted without being read.
    func discard() { discard(closingWindow: true) }

    private func discard(closingWindow: Bool) {
        guard !isDiscarded else { return }
        isDiscarded = true
        if let webView {
            webView.stopLoading()
            webView.navigationDelegate = nil
            webView.uiDelegate = nil
            webView.removeFromSuperview()
        }
        webView = nil
        dataStore.removeData(ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), modifiedSince: .distantPast) {}
        if let window {
            window.delegate = nil
            if closingWindow { window.close() }
        }
        let callback = onDiscard
        onDiscard = nil
        callback?(self)
    }

    /// Closing the window ends the portal session.
    func windowWillClose(_ notification: Notification) { discard(closingWindow: false) }

    // MARK: - Notices

    func show(_ message: String, kind: NoticeKind) {
        noticeKind = kind
        noticeLabel.stringValue = message
        noticeLabel.textColor = kind == .error ? ClaudeStyle.danger : ClaudeStyle.text
        reloadButton.isHidden = kind != .error
        noticeBar.isHidden = false
        NSAccessibility.post(element: noticeLabel, notification: .announcementRequested, userInfo: [
            .announcement: message,
            .priority: NSAccessibilityPriorityLevel.high.rawValue,
        ])
    }

    private func hideNotice() {
        noticeKind = nil
        noticeBar.isHidden = true
    }

    /// The user-facing message for a refused load, or `nil` to stay silent.
    static func message(for decision: PortalPolicy.Decision) -> String? {
        switch decision {
        case .allow: nil
        case .blockMainFrame(let origin): "已阻止离开门户：本窗口只显示门户自身的页面，不会打开 \(origin)。"
        case .blockSubframe(let origin): "已阻止页面内嵌 \(origin) 的内容。"
        case .blockNewWindow: "已阻止打开新窗口。本应用不支持弹出窗口。"
        case .blockDownload: "此客户端暂不支持下载文件，下载已取消。"
        }
    }

    private func apply(_ decision: PortalPolicy.Decision) -> Bool {
        if let message = Self.message(for: decision) {
            show(message, kind: .info)
            return false
        }
        return true
    }

    // MARK: - WKNavigationDelegate

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping @MainActor @Sendable (WKNavigationActionPolicy) -> Void) {
        let decision = policy.decideNavigation(
            to: navigationAction.request.url,
            isMainFrame: navigationAction.targetFrame?.isMainFrame ?? true,
            opensNewWindow: navigationAction.targetFrame == nil,
            isDownload: navigationAction.shouldPerformDownload)
        decisionHandler(apply(decision) ? .allow : .cancel)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse,
                 decisionHandler: @escaping @MainActor @Sendable (WKNavigationResponsePolicy) -> Void) {
        let disposition = (navigationResponse.response as? HTTPURLResponse)?
            .value(forHTTPHeaderField: "Content-Disposition") ?? ""
        let decision = policy.decideResponse(
            from: navigationResponse.response.url,
            isMainFrame: navigationResponse.isForMainFrame,
            canShowMIMEType: navigationResponse.canShowMIMEType,
            isAttachment: disposition.lowercased().hasPrefix("attachment"))
        decisionHandler(apply(decision) ? .allow : .cancel)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        if noticeKind == .error { hideNotice() }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        report(error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        report(error)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        show("门户页面意外停止，请重新加载。", kind: .error)
    }

    private func report(_ error: Error) {
        if let message = Self.message(forLoadError: error) { show(message, kind: .error) }
    }

    /// `nil` for cancellations, including loads this window refused.
    static func message(forLoadError error: Error) -> String? {
        let error = error as NSError
        if error.domain == NSURLErrorDomain, error.code == NSURLErrorCancelled { return nil }
        // WebKitErrorFrameLoadInterruptedByPolicyChange: a load this policy cancelled.
        if error.domain == "WebKitErrorDomain", error.code == 102 { return nil }
        if error.domain == NSURLErrorDomain,
           [NSURLErrorServerCertificateUntrusted, NSURLErrorServerCertificateHasBadDate,
            NSURLErrorServerCertificateHasUnknownRoot, NSURLErrorServerCertificateNotYetValid,
            NSURLErrorClientCertificateRejected, NSURLErrorClientCertificateRequired,
            NSURLErrorSecureConnectionFailed].contains(error.code) {
            return "无法建立安全连接：门户证书未通过系统校验。请联系门户管理员，不要绕过证书错误。（\(error.localizedDescription)）"
        }
        return "无法载入门户：\(error.localizedDescription)"
    }

    // MARK: - WKUIDelegate

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        _ = apply(.blockNewWindow)
        return nil
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping @MainActor @Sendable ([URL]?) -> Void) {
        show("此客户端暂不支持选择文件上传。", kind: .info)
        completionHandler(nil)
    }

    func webView(_ webView: WKWebView, requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                 initiatedByFrame frame: WKFrameInfo, type: WKMediaCaptureType,
                 decisionHandler: @escaping @MainActor @Sendable (WKPermissionDecision) -> Void) {
        decisionHandler(.deny)
    }
}
