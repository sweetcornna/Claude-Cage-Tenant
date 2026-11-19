import AppKit

@MainActor
final class StartupWindowController: NSWindowController {
    let localButton = ClaudeButton(title: "本地安装", kind: .secondary, large: true)
    let remoteButton = ClaudeButton(title: "连接远程", kind: .primary, large: true)
    let statusLabel = NSTextField(wrappingLabelWithString: "")
    private let onLocal: @MainActor () async throws -> Void
    private let onRemote: @MainActor () -> Void

    init(onLocal: @escaping @MainActor () async throws -> Void, onRemote: @escaping @MainActor () -> Void) {
        self.onLocal = onLocal
        self.onRemote = onRemote
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 440, height: 240),
                              styleMask: [.titled, .closable, .miniaturizable, .fullSizeContentView],
                              backing: .buffered, defer: true)
        window.title = "Cage Tenant"
        window.titleVisibility = .hidden
        window.isReleasedWhenClosed = false
        window.isRestorable = false
        ClaudeStyle.page(window)
        super.init(window: window)

        let icon = NSImageView(image: NSApp.applicationIconImage ?? NSImage())
        icon.imageScaling = .scaleProportionallyUpOrDown
        let title = ClaudeStyle.title("选择使用方式")
        localButton.target = self
        localButton.action = #selector(openLocal(_:))
        remoteButton.target = self
        remoteButton.action = #selector(openRemote(_:))
        remoteButton.keyEquivalent = "\r"
        let choices = NSStackView(views: [localButton, remoteButton])
        choices.distribution = .fillEqually
        choices.spacing = 12

        statusLabel.preferredMaxLayoutWidth = 344
        statusLabel.alignment = .center
        statusLabel.font = .systemFont(ofSize: 12)
        statusLabel.isHidden = true
        let stack = NSStackView(views: [icon, title, choices, statusLabel])
        stack.orientation = .vertical
        stack.alignment = .centerX
        stack.spacing = 16
        stack.setCustomSpacing(10, after: icon)
        stack.setCustomSpacing(24, after: title)
        stack.edgeInsets = NSEdgeInsets(top: 40, left: 48, bottom: 36, right: 48)
        stack.translatesAutoresizingMaskIntoConstraints = false
        let content = NSView()
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            content.widthAnchor.constraint(equalToConstant: 440),
            icon.widthAnchor.constraint(equalToConstant: 64),
            icon.heightAnchor.constraint(equalToConstant: 64),
            choices.widthAnchor.constraint(equalToConstant: 344),
            statusLabel.widthAnchor.constraint(equalTo: choices.widthAnchor),
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            stack.bottomAnchor.constraint(equalTo: content.bottomAnchor),
        ])
        window.contentView = content
        window.initialFirstResponder = remoteButton
        window.setContentSize(content.fittingSize)
        window.center()
    }
    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    @objc func openLocal(_ sender: Any?) {
        guard localButton.isEnabled else { return }
        localButton.isEnabled = false
        remoteButton.isEnabled = false
        setStatus("正在准备本地 Cage…")
        Task {
            defer {
                localButton.isEnabled = true
                remoteButton.isEnabled = true
            }
            do {
                try await onLocal()
                setStatus(nil)
            } catch {
                setStatus(error.localizedDescription, isError: true)
            }
        }
    }

    @objc func openRemote(_ sender: Any?) {
        guard remoteButton.isEnabled else { return }
        setStatus(nil)
        onRemote()
    }

    private func setStatus(_ message: String?, isError: Bool = false) {
        statusLabel.stringValue = message ?? ""
        statusLabel.textColor = isError ? ClaudeStyle.danger : ClaudeStyle.secondaryText
        statusLabel.isHidden = message == nil
        if let content = window?.contentView {
            window?.setContentSize(content.fittingSize)
        }
        if let message {
            NSAccessibility.post(element: statusLabel, notification: .announcementRequested,
                                 userInfo: [.announcement: message])
        }
    }
}
