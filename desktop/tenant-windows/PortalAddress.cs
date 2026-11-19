namespace CageTenant;

/// <summary>A validated multi-tenant portal origin: <c>https://&lt;dns-host&gt;[:port]</c>.</summary>
/// <remarks>
/// Same rules as the macOS client (desktop/tenant/PortalAddress.swift). The
/// portal serves each VM from <c>vm-&lt;id&gt;.&lt;portal host&gt;</c>, so the
/// origin must be a DNS name (not an IP literal) and the root of the site. Only
/// this value is ever stored; accounts, passwords and cookies stay in WebView2.
/// </remarks>
public sealed record PortalOrigin(string Host, int? Port)
{
    public string Origin => "https://" + Host + (Port is int port ? ":" + port : "");
    public Uri Url => new(Origin + "/");
    public int EffectivePort => Port ?? 443;
}

public enum PortalAddressError
{
    Empty,
    InvalidCharacters,
    NotUrl,
    NotHttps,
    UserInfo,
    Query,
    Fragment,
    Path,
    Host,
    IpAddress,
    Port,
}

public sealed class PortalAddressException(PortalAddressError error) : Exception(PortalAddress.Message(error))
{
    public PortalAddressError Error { get; } = error;
}

public static class PortalAddress
{
    public static string Message(PortalAddressError error) => error switch
    {
        PortalAddressError.Empty => "请输入门户地址。",
        PortalAddressError.InvalidCharacters => "地址只能包含英文可见字符，不能有空格或控制字符。国际化域名请填写 xn-- 开头的形式。",
        PortalAddressError.NotUrl => "无法识别该地址，格式应为 https://cage.example.org。",
        PortalAddressError.NotHttps => "门户地址必须以 https:// 开头。",
        PortalAddressError.UserInfo => "门户地址不能包含用户名或密码。请在门户页面中登录。",
        PortalAddressError.Query => "门户地址不能包含查询参数（? 及之后的部分）。",
        PortalAddressError.Fragment => "门户地址不能包含片段（# 及之后的部分）。",
        PortalAddressError.Path => "请填写门户根地址，不要包含域名之后的路径。",
        PortalAddressError.Host => "域名无效。请填写门户的完整域名，例如 cage.example.org。",
        PortalAddressError.IpAddress => "请填写门户域名。虚拟机使用 vm-<id>.<门户域名> 子域名，不支持 IP 地址。",
        PortalAddressError.Port => "端口无效，应为 1 到 65535 之间的数字。",
        _ => "无法识别该地址。",
    };

    public static bool TryValidate(string? input, out PortalOrigin? origin, out PortalAddressError error)
    {
        try
        {
            origin = Validate(input);
            error = default;
            return true;
        }
        catch (PortalAddressException e)
        {
            origin = null;
            error = e.Error;
            return false;
        }
    }

    /// <summary>
    /// Parses user input into a portal origin, rejecting anything other than an
    /// HTTPS site root. Specific errors come from a direct reading of the text;
    /// System.Uri must then agree on every part, so the stored origin cannot
    /// differ from what a URL parser sees.
    /// </summary>
    public static PortalOrigin Validate(string? input)
    {
        var text = Trim(input ?? "");
        if (text.Length == 0) throw new PortalAddressException(PortalAddressError.Empty);
        // Printable ASCII only (this also rejects surrogates and IDN Unicode).
        if (text.Length > 2048 || !text.All(c => c >= 0x21 && c <= 0x7e))
            throw new PortalAddressException(PortalAddressError.InvalidCharacters);

        const string prefix = "https://";
        var lowered = text.ToLowerInvariant();
        if (!lowered.StartsWith(prefix, StringComparison.Ordinal))
            throw new PortalAddressException(lowered.StartsWith("https:", StringComparison.Ordinal)
                ? PortalAddressError.NotUrl : PortalAddressError.NotHttps);

        var rest = text[prefix.Length..];
        var authorityEnd = rest.IndexOfAny(['/', '?', '#']);
        if (authorityEnd < 0) authorityEnd = rest.Length;
        var authority = rest[..authorityEnd];
        var remainder = rest[authorityEnd..];
        if (authority.Contains('@')) throw new PortalAddressException(PortalAddressError.UserInfo);
        if (remainder.Contains('#')) throw new PortalAddressException(PortalAddressError.Fragment);
        if (remainder.Contains('?')) throw new PortalAddressException(PortalAddressError.Query);
        if (authority.Length == 0) throw new PortalAddressException(PortalAddressError.Host);
        if (remainder.Length != 0 && remainder != "/") throw new PortalAddressException(PortalAddressError.Path);
        if (authority.StartsWith('[')) throw new PortalAddressException(PortalAddressError.IpAddress);

        string hostText;
        int? rawPort = null;
        var colon = authority.IndexOf(':');
        if (colon >= 0)
        {
            hostText = authority[..colon];
            var digits = authority[(colon + 1)..];
            if (digits.Length is < 1 or > 5 || !digits.All(c => c is >= '0' and <= '9'))
                throw new PortalAddressException(PortalAddressError.Port);
            var value = int.Parse(digits, System.Globalization.CultureInfo.InvariantCulture);
            if (value is < 1 or > 65535) throw new PortalAddressException(PortalAddressError.Port);
            rawPort = value;
        }
        else
        {
            hostText = authority;
        }
        var host = hostText.ToLowerInvariant();
        ValidateHost(host);

        if (!Uri.TryCreate(text, UriKind.Absolute, out var parsed)
            || parsed.Scheme != Uri.UriSchemeHttps
            || parsed.UserInfo.Length != 0
            || !string.Equals(parsed.IdnHost, host, StringComparison.OrdinalIgnoreCase)
            || parsed.HostNameType != UriHostNameType.Dns
            || parsed.Port != (rawPort ?? 443)
            || parsed.AbsolutePath != "/"
            || parsed.Query.Length != 0
            || parsed.Fragment.Length != 0)
        {
            throw new PortalAddressException(PortalAddressError.NotUrl);
        }
        return new PortalOrigin(host, rawPort == 443 ? null : rawPort);
    }

    /// <summary>
    /// LDH host names only. A trailing dot is rejected because the gateway
    /// matches the Host header exactly. A final label that is numeric or
    /// <c>0x</c>-prefixed is parsed by browsers as IPv4.
    /// </summary>
    private static void ValidateHost(string host)
    {
        if (host.Length is 0 or > 253) throw new PortalAddressException(PortalAddressError.Host);
        var labels = host.Split('.');
        foreach (var label in labels)
        {
            if (label.Length is < 1 or > 63
                || !label.All(c => c is (>= 'a' and <= 'z') or (>= '0' and <= '9') or '-')
                || label[0] == '-' || label[^1] == '-')
            {
                throw new PortalAddressException(PortalAddressError.Host);
            }
        }
        var last = labels[^1];
        if (last.All(char.IsAsciiDigit) || last.StartsWith("0x", StringComparison.Ordinal))
            throw new PortalAddressException(PortalAddressError.IpAddress);
        if (last[0] is < 'a' or > 'z') throw new PortalAddressException(PortalAddressError.Host);
    }

    /// <summary>
    /// The characters Foundation's <c>whitespacesAndNewlines</c> trims, so that
    /// all clients accept the same pasted input: Unicode Zs, tab, line breaks
    /// (U+000A–U+000D, U+0085, U+2028, U+2029) and U+200B.
    /// </summary>
    public static bool IsTrimmed(char c) => c is
        (>= '\u0009' and <= '\u000d') or ' ' or '\u0085' or '\u00a0' or '\u1680'
        or (>= '\u2000' and <= '\u200b') or '\u2028' or '\u2029' or '\u202f' or '\u205f' or '\u3000';

    private static string Trim(string value)
    {
        int start = 0, end = value.Length;
        while (start < end && IsTrimmed(value[start])) start++;
        while (end > start && IsTrimmed(value[end - 1])) end--;
        return value[start..end];
    }
}
