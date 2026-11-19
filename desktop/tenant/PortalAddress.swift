import Foundation

/// A validated multi-tenant portal origin: `https://<dns-host>[:port]`.
///
/// The portal serves each VM from `vm-<id>.<portal host>`, so the origin must be
/// a DNS name (not an IP literal) and the root of the site. Only this value is
/// ever stored; accounts, passwords, cookies and tickets stay in the portal
/// window's in-memory web session and are never read by the app.
struct PortalOrigin: Equatable, Sendable {
    let host: String
    /// `nil` for the default HTTPS port.
    let port: Int?

    var origin: String { "https://" + host + (port.map { ":\($0)" } ?? "") }
    var url: URL { URL(string: origin + "/")! }
}

enum PortalAddressError: Error, Equatable, Sendable {
    case empty
    case invalidCharacters
    case notURL
    case notHTTPS
    case userInfo
    case query
    case fragment
    case path
    case host
    case ipAddress
    case port

    var message: String {
        switch self {
        case .empty: "请输入门户地址。"
        case .invalidCharacters: "地址只能包含英文可见字符，不能有空格或控制字符。国际化域名请填写 xn-- 开头的形式。"
        case .notURL: "无法识别该地址，格式应为 https://cage.example.org。"
        case .notHTTPS: "门户地址必须以 https:// 开头。"
        case .userInfo: "门户地址不能包含用户名或密码。请打开门户后在门户页面中登录。"
        case .query: "门户地址不能包含查询参数（? 及之后的部分）。"
        case .fragment: "门户地址不能包含片段（# 及之后的部分）。"
        case .path: "请填写门户根地址，不要包含域名之后的路径。"
        case .host: "域名无效。请填写门户的完整域名，例如 cage.example.org。"
        case .ipAddress: "请填写门户域名。虚拟机使用 vm-<id>.<门户域名> 子域名，不支持 IP 地址。"
        case .port: "端口无效，应为 1 到 65535 之间的数字。"
        }
    }
}

enum PortalAddress {
    /// Parses user input into a portal origin, rejecting anything other than an
    /// HTTPS site root. Specific errors come from a direct reading of the text;
    /// Foundation's URL parser must then agree on every part, so the stored
    /// origin cannot differ from what a URL parser sees.
    static func validate(_ input: String) throws -> PortalOrigin {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { throw PortalAddressError.empty }
        // Printable ASCII only. Newer Foundation percent-encodes invalid
        // characters instead of failing, so reject them before parsing.
        // Foundation also trims U+200B here; anything left inside is rejected.
        guard text.utf8.count <= 2048, text.unicodeScalars.allSatisfy({ (0x21...0x7e).contains($0.value) }) else {
            throw PortalAddressError.invalidCharacters
        }
        let prefix = "https://"
        let lowered = text.lowercased()
        guard lowered.hasPrefix(prefix) else {
            throw lowered.hasPrefix("https:") ? PortalAddressError.notURL : PortalAddressError.notHTTPS
        }

        let rest = text.dropFirst(prefix.count)
        let authorityEnd = rest.firstIndex(where: { "/?#".contains($0) }) ?? rest.endIndex
        let authority = rest[..<authorityEnd]
        let remainder = rest[authorityEnd...]
        if authority.contains("@") { throw PortalAddressError.userInfo }
        if remainder.contains("#") { throw PortalAddressError.fragment }
        if remainder.contains("?") { throw PortalAddressError.query }
        if authority.isEmpty { throw PortalAddressError.host }
        guard remainder.isEmpty || remainder == "/" else { throw PortalAddressError.path }
        if authority.hasPrefix("[") { throw PortalAddressError.ipAddress }

        let hostText: Substring
        var rawPort: Int?
        if let colon = authority.firstIndex(of: ":") {
            hostText = authority[..<colon]
            let digits = authority[authority.index(after: colon)...]
            guard (1...5).contains(digits.count), digits.allSatisfy({ ("0"..."9").contains($0) }),
                  let value = Int(digits), (1...65535).contains(value) else {
                throw PortalAddressError.port
            }
            rawPort = value
        } else {
            hostText = authority
        }
        let host = hostText.lowercased()
        try validateHost(host)

        // `encodedHost` keeps xn-- labels in ASCII; newer Foundation decodes
        // them to percent-encoded Unicode in `percentEncodedHost`.
        guard let parsed = URLComponents(string: text),
              parsed.scheme?.lowercased() == "https",
              parsed.percentEncodedUser == nil, parsed.percentEncodedPassword == nil,
              parsed.encodedHost?.lowercased() == host, parsed.port == rawPort,
              parsed.percentEncodedPath.isEmpty || parsed.percentEncodedPath == "/",
              parsed.percentEncodedQuery == nil, parsed.percentEncodedFragment == nil else {
            throw PortalAddressError.notURL
        }
        return PortalOrigin(host: host, port: rawPort == 443 ? nil : rawPort)
    }

    /// LDH host names only. A trailing dot is rejected because the gateway
    /// matches the Host header exactly. A final label that is numeric or
    /// `0x`-prefixed is parsed by browsers as IPv4.
    private static func validateHost(_ host: String) throws {
        guard !host.isEmpty, host.utf8.count <= 253 else { throw PortalAddressError.host }
        let labels = host.split(separator: ".", omittingEmptySubsequences: false)
        let allowed = Set("abcdefghijklmnopqrstuvwxyz0123456789-")
        for label in labels {
            guard (1...63).contains(label.count), label.allSatisfy(allowed.contains),
                  label.first != "-", label.last != "-" else {
                throw PortalAddressError.host
            }
        }
        let last = labels[labels.count - 1]
        if last.allSatisfy(\.isNumber) || last.hasPrefix("0x") { throw PortalAddressError.ipAddress }
        guard let first = last.first, ("a"..."z").contains(first) else { throw PortalAddressError.host }
    }
}

/// Stores the validated portal origin, and nothing else, in user defaults.
struct PortalStore {
    static let key = "PortalOrigin"
    let defaults: UserDefaults

    /// Returns the stored origin after validating it again. A value that no
    /// longer validates (for example, edited by hand) is removed.
    func load() -> PortalOrigin? {
        guard let stored = defaults.string(forKey: Self.key) else {
            if defaults.object(forKey: Self.key) != nil { clear() }
            return nil
        }
        guard let origin = try? PortalAddress.validate(stored) else {
            clear()
            return nil
        }
        return origin
    }

    func save(_ origin: PortalOrigin) {
        defaults.set(origin.origin, forKey: Self.key)
    }

    func clear() {
        defaults.removeObject(forKey: Self.key)
    }
}
