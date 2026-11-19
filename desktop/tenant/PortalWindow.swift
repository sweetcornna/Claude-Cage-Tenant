import AppKit

/// The settings form: enter the portal origin, save it after validation and
/// open it in the app's portal window, or clear it. This window never loads
/// web content and never sees account names, passwords or cookies.
@MainActor
final class PortalWindowController: NSWindowController {
    /// Opens the portal window for a validated origin.
    typealias Opener = @MainActor (PortalOrigin) -> Void
    /// Called after the saved origin is cleared, to end any open portal session.
    typealias Clearer = @MainActor () -> Void

    let store: PortalStore
    private let opener: Opener
    private let onClear: Clearer
    private let onBack: @MainActor () -> Void
    let backButton = ClaudeButton(title: "返回", kind: .secondary)
    let addressField = NSTextField()
    let statusLabel = NSTextField(wrappingLabelWithString: "")
    let openButton = ClaudeButton(title: "打开门户", kind: .primary)
    let clearButton = ClaudeButton(title: "清除已保存地址", kind: .secondary)

    init(store: PortalStore, opener: @escaping Opener, onClear: @escaping Clearer = {},
         onBack: @escaping @MainActor () -> Void = {}) {
        self.store = store
        self.opener = opener
        self.onClear = onClear
        self.onBack = onBack
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 480, height: 200),
                              styleMask: [.titled, .closable, .miniaturizable, .fullSizeContentView],
                              backing: .buffered, defer: true)
        window.title = "Cage Tenant"
        window.titleVisibility = .hidden
        window.isReleasedWhenClosed = false
        window.isRestorable = false
        ClaudeStyle.page(window)
        super.init(window: window)
        buildContent(in: window)
        showSaved()
        window.center()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    /// Shows the saved origin, if any, and clears any earlier message.
    func showSaved() {
        let saved = store.load()
        addressField.stringValue = saved?.origin ?? ""
        clearButton.isEnabled = saved != nil
        statusLabel.stringValue = ""
        statusLabel.isHidden = true
    }

    private func buildContent(in window: NSWindow) {
        let width: CGFloat = 400
        let title = ClaudeStyle.title("连接远程")
        let fieldLabel = ClaudeStyle.label("门户地址")
        ClaudeStyle.field(addressField)
        addressField.placeholderString = "https://cage.example.org"
        addressField.contentType = .URL
        addressField.isAutomaticTextCompletionEnabled = false
        addressField.lineBreakMode = .byTruncatingTail
        addressField.usesSingleLineMode = true
        addressField.setAccessibilityLabel("门户地址")
        statusLabel.isHidden = true
        statusLabel.isSelectable = true
        statusLabel.font = .systemFont(ofSize: 12)
        statusLabel.preferredMaxLayoutWidth = width

        openButton.target = self
        openButton.action = #selector(openPortal(_:))
        openButton.keyEquivalent = "\r"
        clearButton.target = self
        clearButton.action = #selector(clearSaved(_:))
        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        backButton.target = self
        backButton.action = #selector(goBack(_:))
        let buttons = NSStackView(views: [backButton, clearButton, spacer, openButton])
        buttons.orientation = .horizontal

        let stack = NSStackView(views: [title, fieldLabel, addressField, statusLabel, buttons])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.setCustomSpacing(20, after: title)
        stack.setCustomSpacing(6, after: fieldLabel)
        stack.setCustomSpacing(20, after: statusLabel)
        stack.edgeInsets = NSEdgeInsets(top: 44, left: 40, bottom: 32, right: 40)
        stack.translatesAutoresizingMaskIntoConstraints = false
        for view in [addressField, statusLabel, buttons] {
            view.widthAnchor.constraint(equalToConstant: width).isActive = true
        }
        let content = NSView()
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            content.widthAnchor.constraint(equalToConstant: width + 80),
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            stack.bottomAnchor.constraint(equalTo: content.bottomAnchor),
        ])
        window.contentView = content
        window.initialFirstResponder = addressField
        window.setContentSize(content.fittingSize)
    }

    @objc func goBack(_ sender: Any?) { onBack() }

    @objc func openPortal(_ sender: Any?) {
        let origin: PortalOrigin
        do {
            origin = try PortalAddress.validate(addressField.stringValue)
        } catch {
            show((error as? PortalAddressError)?.message ?? PortalAddressError.notURL.message, isError: true)
            window?.makeFirstResponder(addressField)
            return
        }
        addressField.stringValue = origin.origin
        store.save(origin)
        clearButton.isEnabled = true
        show("已在门户窗口中打开 \(origin.origin)。", isError: false)
        opener(origin)
    }

    @objc func clearSaved(_ sender: Any?) {
        store.clear()
        addressField.stringValue = ""
        clearButton.isEnabled = false
        onClear()
        show("已清除保存的门户地址，并关闭了门户窗口及其登录状态。", isError: false)
        window?.makeFirstResponder(addressField)
    }

    private func show(_ message: String, isError: Bool) {
        statusLabel.stringValue = message
        statusLabel.textColor = isError ? ClaudeStyle.danger : ClaudeStyle.secondaryText
        statusLabel.isHidden = false
        if let window, window.isVisible {
            window.setContentSize(window.contentView?.fittingSize ?? window.frame.size)
        }
        NSAccessibility.post(element: statusLabel, notification: .announcementRequested, userInfo: [
            .announcement: message,
            .priority: NSAccessibilityPriorityLevel.high.rawValue,
        ])
    }
}
