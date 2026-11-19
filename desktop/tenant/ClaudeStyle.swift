import AppKit

/// Native pages share the portal's warm Claude palette in light and dark mode.
@MainActor
enum ClaudeStyle {
    static let background = dynamic(light: 0xF5F3EB, dark: 0x22211E)
    static let surface = dynamic(light: 0xFFFDF8, dark: 0x2B2925)
    static let border = dynamic(light: 0xCFC9BB, dark: 0x575248)
    static let text = dynamic(light: 0x292621, dark: 0xF3EFE5)
    static let secondaryText = dynamic(light: 0x6E685D, dark: 0xB7B1A5)
    static let accent = dynamic(light: 0x974129, dark: 0xF1A58B)
    static let accentFill = dynamic(light: 0xD97757, dark: 0xE48C70)
    static let accentHover = dynamic(light: 0xCF6B4B, dark: 0xED9D83)
    static let accentText = dynamic(light: 0x261713, dark: 0x261713)
    static let danger = dynamic(light: 0xA82D2D, dark: 0xF27474)

    static func title(_ string: String) -> NSTextField {
        let label = NSTextField(labelWithString: string)
        label.font = .systemFont(ofSize: 22, weight: .semibold)
        label.textColor = text
        return label
    }

    static func label(_ string: String) -> NSTextField {
        let label = NSTextField(labelWithString: string)
        label.font = .systemFont(ofSize: NSFont.systemFontSize, weight: .medium)
        label.textColor = text
        return label
    }

    static func page(_ window: NSWindow) {
        window.backgroundColor = background
        window.titlebarAppearsTransparent = true
    }

    static func field(_ field: NSTextField) {
        field.font = .systemFont(ofSize: 14)
        field.textColor = text
        field.backgroundColor = surface
        field.drawsBackground = true
        field.focusRingType = .exterior
        field.controlSize = .large
    }

    private static func dynamic(light: UInt32, dark: UInt32) -> NSColor {
        NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            let value = isDark ? dark : light
            return NSColor(srgbRed: CGFloat((value >> 16) & 0xFF) / 255,
                           green: CGFloat((value >> 8) & 0xFF) / 255,
                           blue: CGFloat(value & 0xFF) / 255, alpha: 1)
        }
    }
}

/// Rounded Claude-style button: a filled primary action or an outlined secondary one.
@MainActor
final class ClaudeButton: NSButton {
    enum Kind { case primary, secondary }

    private let kind: Kind
    private var hovering = false
    private var tracking: NSTrackingArea?

    init(title: String, kind: Kind, large: Bool = false) {
        self.kind = kind
        super.init(frame: .zero)
        isBordered = false
        bezelStyle = .regularSquare
        wantsLayer = true
        layer?.cornerRadius = large ? 10 : 8
        layer?.borderWidth = kind == .primary ? 0 : 1
        font = .systemFont(ofSize: large ? 15 : 13, weight: .semibold)
        translatesAutoresizingMaskIntoConstraints = false
        heightAnchor.constraint(equalToConstant: large ? 48 : 32).isActive = true
        setContentHuggingPriority(.defaultHigh, for: .horizontal)
        setText(title)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    override var isEnabled: Bool { didSet { refresh() } }
    override var isHighlighted: Bool { didSet { refresh() } }
    override var wantsUpdateLayer: Bool { true }

    override var intrinsicContentSize: NSSize {
        let size = attributedTitle.size()
        return NSSize(width: ceil(size.width) + 32, height: NSView.noIntrinsicMetric)
    }

    /// Sets the visible label; `title` then reads back the same plain string.
    func setText(_ text: String) {
        let paragraph = NSMutableParagraphStyle()
        paragraph.alignment = .center
        let color = kind == .primary ? ClaudeStyle.accentText : ClaudeStyle.text
        attributedTitle = NSAttributedString(string: text, attributes: [
            .font: font ?? NSFont.systemFont(ofSize: 13, weight: .semibold),
            .foregroundColor: color,
            .paragraphStyle: paragraph,
        ])
        invalidateIntrinsicContentSize()
        refresh()
    }

    override func updateLayer() { refresh() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        setText(title)
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let tracking { removeTrackingArea(tracking) }
        let area = NSTrackingArea(rect: bounds, options: [.mouseEnteredAndExited, .activeInKeyWindow, .inVisibleRect], owner: self)
        addTrackingArea(area)
        tracking = area
    }

    override func mouseEntered(with event: NSEvent) { hovering = true; refresh() }
    override func mouseExited(with event: NSEvent) { hovering = false; refresh() }
    override func resetCursorRects() { addCursorRect(bounds, cursor: .pointingHand) }

    private func refresh() {
        guard let layer else { return }
        let active = hovering || isHighlighted
        let fill: NSColor = switch kind {
        case .primary: active ? ClaudeStyle.accentHover : ClaudeStyle.accentFill
        case .secondary: active ? ClaudeStyle.background : ClaudeStyle.surface
        }
        effectiveAppearance.performAsCurrentDrawingAppearance {
            layer.backgroundColor = fill.cgColor
            layer.borderColor = (active && kind == .secondary ? ClaudeStyle.accentFill : ClaudeStyle.border).cgColor
        }
        layer.opacity = isEnabled ? 1 : 0.5
    }
}
