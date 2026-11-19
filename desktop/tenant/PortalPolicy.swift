import Foundation

/// What the embedded portal may load. The main frame stays on the exact
/// portal origin. Subframes may also load workspace origins
/// `https://vm-<32 hex>.<portal host>[:port]`, which the portal frames under
/// its own CSP. Everything else, new windows and downloads are refused.
struct PortalPolicy: Equatable, Sendable {
    let portal: PortalOrigin

    enum Decision: Equatable, Sendable {
        case allow
        /// A main-frame navigation or redirect to another origin.
        case blockMainFrame(origin: String)
        /// A frame that is neither the portal nor one of its workspaces.
        case blockSubframe(origin: String)
        /// `window.open`, `target="_blank"` and similar.
        case blockNewWindow
        /// Downloads are not implemented in this client.
        case blockDownload
    }

    /// Decides a navigation request before it is sent.
    func decideNavigation(to url: URL?, isMainFrame: Bool, opensNewWindow: Bool, isDownload: Bool) -> Decision {
        if opensNewWindow { return .blockNewWindow }
        if isDownload { return .blockDownload }
        return decideFrame(url, isMainFrame: isMainFrame)
    }

    /// Decides a response before it is displayed, which also covers the final
    /// URL after server redirects.
    func decideResponse(from url: URL?, isMainFrame: Bool, canShowMIMEType: Bool, isAttachment: Bool) -> Decision {
        let frame = decideFrame(url, isMainFrame: isMainFrame)
        guard frame == .allow else { return frame }
        return canShowMIMEType && !isAttachment ? .allow : .blockDownload
    }

    private func decideFrame(_ url: URL?, isMainFrame: Bool) -> Decision {
        if isMainFrame {
            return isPortal(url) ? .allow : .blockMainFrame(origin: Self.displayOrigin(url))
        }
        if isPortal(url) || isWorkspace(url) || Self.isEmptyFrame(url) { return .allow }
        return .blockSubframe(origin: Self.displayOrigin(url))
    }

    /// The exact portal origin.
    func isPortal(_ url: URL?) -> Bool {
        guard let parts = Self.httpsParts(url), parts.port == (portal.port ?? 443) else { return false }
        return parts.host == portal.host
    }

    /// `vm-<id>.<portal host>` on the portal's port, with a 32-digit lowercase
    /// hexadecimal id as issued by the server.
    func isWorkspace(_ url: URL?) -> Bool {
        guard let parts = Self.httpsParts(url), parts.port == (portal.port ?? 443),
              parts.host.hasPrefix("vm-"), parts.host.hasSuffix("." + portal.host) else { return false }
        let id = parts.host.dropFirst(3).dropLast(portal.host.count + 1)
        return id.count == 32 && id.allSatisfy { ("0"..."9").contains($0) || ("a"..."f").contains($0) }
    }

    /// `about:blank` and `about:srcdoc`, which a new iframe holds before its
    /// source loads.
    static func isEmptyFrame(_ url: URL?) -> Bool {
        guard let url, url.scheme?.lowercased() == "about" else { return false }
        let rest = url.absoluteString.dropFirst("about:".count)
        return rest == "blank" || rest == "srcdoc"
    }

    /// Scheme `https`, no user info, lowercase ASCII host and effective port.
    private static func httpsParts(_ url: URL?) -> (host: String, port: Int)? {
        guard let url, let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
              parts.scheme?.lowercased() == "https",
              parts.percentEncodedUser == nil, parts.percentEncodedPassword == nil,
              let host = parts.encodedHost?.lowercased(), !host.isEmpty else { return nil }
        return (host, parts.port ?? 443)
    }

    /// A short, non-sensitive description of where a refused load pointed:
    /// scheme, host and port only, never the path, query or credentials.
    static func displayOrigin(_ url: URL?) -> String {
        guard let url, let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let scheme = parts.scheme?.lowercased() else { return "未知地址" }
        guard let host = parts.encodedHost?.lowercased(), !host.isEmpty else { return scheme + ":" }
        return scheme + "://" + host + (parts.port.map { ":\($0)" } ?? "")
    }
}
