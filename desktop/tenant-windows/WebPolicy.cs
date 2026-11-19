namespace CageTenant;

/// <summary>What the Connect button does with the address field.</summary>
public enum ConnectAction
{
    /// <summary>The input is invalid; show the error and keep everything as is.</summary>
    Reject,
    /// <summary>First server: save it and open a fresh session.</summary>
    Connect,
    /// <summary>Same server as before: keep its session and show the portal.</summary>
    Keep,
    /// <summary>Different server: destroy the old view and session, then connect.</summary>
    Switch,
}

public sealed record ConnectPlan(ConnectAction Action, PortalOrigin? Origin, PortalAddressError? Error)
{
    public string? Message => Error is PortalAddressError error ? PortalAddress.Message(error) : null;
}

/// <summary>Pure decisions used by the window, kept apart so they run in tests on any OS.</summary>
public static partial class WebPolicy
{
    public static ConnectPlan PlanConnect(string? input, PortalOrigin? current)
    {
        if (!PortalAddress.TryValidate(input, out var origin, out var error))
            return new ConnectPlan(ConnectAction.Reject, null, error);
        if (current == null) return new ConnectPlan(ConnectAction.Connect, origin, null);
        return new ConnectPlan(origin == current ? ConnectAction.Keep : ConnectAction.Switch, origin, null);
    }

    /// <summary>
    /// Main-frame rule: only documents on the exact portal origin (scheme,
    /// host and port). Subframes are not checked here; the portal's CSP,
    /// iframe sandbox and server authentication govern them.
    /// </summary>
    public static bool IsPortalDocument(string? uri, PortalOrigin origin) =>
        TryParse(uri, out var parsed)
        && string.Equals(parsed.IdnHost, origin.Host, StringComparison.OrdinalIgnoreCase)
        && parsed.Port == origin.EffectivePort;

    /// <summary>
    /// The portal origin or one of its VM origins (<c>vm-&lt;32 hex&gt;.&lt;host&gt;</c>,
    /// same port). Used only to decide whether WebView2 may show its own
    /// clipboard-read prompt; it never widens navigation.
    /// </summary>
    public static bool IsPortalFamily(string? uri, PortalOrigin origin)
    {
        if (!TryParse(uri, out var parsed) || parsed.Port != origin.EffectivePort) return false;
        var host = parsed.IdnHost.ToLowerInvariant();
        if (host == origin.Host) return true;
        var suffix = "." + origin.Host;
        return host.EndsWith(suffix, StringComparison.Ordinal)
            && VmLabel().IsMatch(host[..^suffix.Length]);
    }

    private static bool TryParse(string? uri, out Uri parsed)
    {
        parsed = null!;
        if (string.IsNullOrEmpty(uri) || uri.Length > 65536) return false;
        if (!Uri.TryCreate(uri, UriKind.Absolute, out var value)) return false;
        if (value.Scheme != Uri.UriSchemeHttps || value.UserInfo.Length != 0) return false;
        if (value.HostNameType != UriHostNameType.Dns) return false;
        parsed = value;
        return true;
    }

    [System.Text.RegularExpressions.GeneratedRegex("^vm-[0-9a-f]{32}$", System.Text.RegularExpressions.RegexOptions.CultureInvariant)]
    private static partial System.Text.RegularExpressions.Regex VmLabel();

    /// <summary>
    /// WebView2 reads these variables (browser arguments such as
    /// --ignore-certificate-errors, another browser build, a debugger pipe,
    /// a data folder). The client removes every WEBVIEW2_* variable from its
    /// own environment before creating WebView2.
    /// </summary>
    public static IReadOnlyList<string> WebView2VariablesToClear(System.Collections.IDictionary environment)
    {
        var names = new List<string>();
        foreach (var key in environment.Keys)
        {
            if (key is string name && name.StartsWith("WEBVIEW2_", StringComparison.OrdinalIgnoreCase)) names.Add(name);
        }
        names.Sort(StringComparer.Ordinal);
        return names;
    }
}
