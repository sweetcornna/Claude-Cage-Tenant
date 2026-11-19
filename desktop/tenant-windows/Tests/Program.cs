using System.Collections;
using System.Text;
using CageTenant;

namespace CageTenant.Tests;

/// <summary>
/// Runs without a test framework so that test.sh needs only the .NET SDK.
/// Usage: CageTenant.Windows.Tests &lt;path to desktop/tenant/Tests/TenantTests.swift&gt;
/// The address vectors come from the macOS client's tests, so every client
/// accepts and rejects exactly the same input.
/// </summary>
internal static class Program
{
    private static int checks;
    private static int failures;

    private static void Expect(bool condition, string message, [System.Runtime.CompilerServices.CallerLineNumber] int line = 0)
    {
        checks++;
        if (condition) return;
        failures++;
        Console.WriteLine($"FAIL (line {line}): {message}");
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1 || !File.Exists(args[0]))
        {
            Console.Error.WriteLine("Usage: CageTenant.Windows.Tests <desktop/tenant/Tests/TenantTests.swift>");
            return 2;
        }
        var vectors = SwiftVectors.Parse(File.ReadAllText(args[0]));
        Expect(vectors.Accepted.Count >= 10, $"parsed {vectors.Accepted.Count} accepted vectors");
        Expect(vectors.Rejected.Count >= 40, $"parsed {vectors.Rejected.Count} rejected vectors");
        AcceptsSharedVectors(vectors.Accepted);
        RejectsSharedVectors(vectors.Rejected);
        WindowsSpecificInput();
        StoreKeepsOnlyTheOrigin();
        SessionsArePerOrigin();
        MainFramePolicy();
        PortalFamilyPolicy();
        ConnectPlans();
        WebView2EnvironmentIsScrubbed();
        LocalInstallerIsPinned().GetAwaiter().GetResult();
        if (failures > 0)
        {
            Console.WriteLine($"{failures} of {checks} checks failed");
            return 1;
        }
        Console.WriteLine($"ok: {checks} checks passed ({vectors.Accepted.Count} accepted and {vectors.Rejected.Count} rejected shared vectors)");
        return 0;
    }

    private sealed class FakeHandler(Func<HttpRequestMessage, HttpResponseMessage> respond) : HttpMessageHandler
    {
        public List<Uri?> Requests { get; } = [];

        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            Requests.Add(request.RequestUri);
            return Task.FromResult(respond(request));
        }
    }

    /// <summary>A response body that never sends another byte.</summary>
    private sealed class StalledStream : Stream
    {
        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
        public override void Flush() { }
        public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
        public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
            ReadAsync(buffer.AsMemory(offset, count), cancellationToken).AsTask();
        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
        {
            await Task.Delay(Timeout.Infinite, cancellationToken);
            return 0;
        }
    }

    private static async Task LocalInstallerIsPinned()
    {
        Expect(LocalInstaller.Supported(System.Runtime.InteropServices.Architecture.X64), "x64 Windows can install locally");
        Expect(!LocalInstaller.Supported(System.Runtime.InteropServices.Architecture.Arm64), "ARM64 Windows is refused before downloading");
        Expect(LocalInstaller.Url.AbsoluteUri == "https://github.com/Lee-xeo/Claude-Cage/releases/download/v0.1.8/Cage-0.1.8-windows-amd64-setup.exe",
            "pinned upstream installer URL");
        var folder = Path.Combine(Path.GetTempPath(), "cage-tenant-local-" + Guid.NewGuid().ToString("N"));
        try
        {
            // A small stand-in for the pinned file. "evil" has the same length, so
            // only the SHA-256 comparison can refuse it.
            var good = "the real installer"u8.ToArray();
            var evil = "the evil installer"u8.ToArray();
            var goodDigest = Convert.ToHexStringLower(System.Security.Cryptography.SHA256.HashData(good));
            var target = Path.Combine(folder, LocalInstaller.FileName);
            async Task<string?> Attempt(Func<HttpRequestMessage, HttpResponseMessage> respond, List<Uri?>? requests = null, bool pinned = true, TimeSpan? idle = null)
            {
                var handler = new FakeHandler(respond);
                try
                {
                    if (pinned) await LocalInstaller.PrepareAsync(folder, handler);
                    else await LocalInstaller.PrepareAsync(folder, good.Length, goodDigest, idle ?? TimeSpan.FromSeconds(10), handler);
                    return null;
                }
                catch (LocalCageException e)
                {
                    return e.Message;
                }
                finally
                {
                    requests?.AddRange(handler.Requests);
                    handler.Dispose();
                }
            }
            var requests = new List<Uri?>();
            var tampered = await Attempt(_ => new HttpResponseMessage(System.Net.HttpStatusCode.OK) { Content = new ByteArrayContent("not the installer"u8.ToArray()) }, requests);
            Expect(tampered?.Contains("校验失败") == true, $"a tampered installer is refused: {tampered}");
            Expect(requests.Count == 1 && requests[0] == LocalInstaller.Url, "one request to the pinned URL");
            Expect(Directory.GetFileSystemEntries(folder).Length == 0, "a refused download leaves no file");
            var missing = await Attempt(_ => new HttpResponseMessage(System.Net.HttpStatusCode.NotFound));
            Expect(missing?.Contains("下载") == true, "an HTTP error is reported as a download failure");
            var offline = await Attempt(_ => throw new HttpRequestException("offline"));
            Expect(offline?.Contains("下载") == true, "a network failure is reported");
            Expect(Directory.GetFileSystemEntries(folder).Length == 0, "failed downloads leave no file");
            File.WriteAllText(target, "stale");
            Expect(!LocalInstaller.Verify(target), "a cached file with the wrong size is not trusted");
            File.Delete(target);

            HttpResponseMessage Body(byte[] body) => new(System.Net.HttpStatusCode.OK) { Content = new ByteArrayContent(body) };
            var swapped = await Attempt(_ => Body(evil), pinned: false);
            Expect(swapped?.Contains("校验失败") == true, $"a same-size installer with another digest is refused: {swapped}");
            Expect(Directory.GetFileSystemEntries(folder).Length == 0, "the refused same-size download leaves no file");
            var oversized = await Attempt(_ => Body([.. good, .. good]), pinned: false);
            Expect(oversized?.Contains("校验失败") == true, "a response longer than the installer is refused");
            Expect(await Attempt(_ => Body(good), pinned: false) == null, "the matching installer is accepted");
            Expect(LocalInstaller.Verify(target, good.Length, goodDigest) && Directory.GetFileSystemEntries(folder).Length == 1,
                "only the verified installer is kept");
            File.WriteAllBytes(target, evil);
            Expect(!LocalInstaller.Verify(target, good.Length, goodDigest), "a cached same-size file with another digest is not trusted");
            var cachedRequests = new List<Uri?>();
            File.WriteAllBytes(target, good);
            Expect(await Attempt(_ => throw new HttpRequestException("offline"), cachedRequests, pinned: false) == null && cachedRequests.Count == 0,
                "a verified cached installer is reused without downloading");
            File.Delete(target);

            File.WriteAllText(target + ".partial", "left by an interrupted run");
            Expect(await Attempt(_ => Body(good), pinned: false) == null && !File.Exists(target + ".partial"),
                "a partial file from an interrupted run is replaced");
            File.Delete(target);

            var clock = System.Diagnostics.Stopwatch.StartNew();
            var stalled = await Attempt(_ => new HttpResponseMessage(System.Net.HttpStatusCode.OK) { Content = new StreamContent(new StalledStream()) },
                pinned: false, idle: TimeSpan.FromMilliseconds(300));
            Expect(stalled?.Contains("下载") == true && clock.Elapsed < TimeSpan.FromSeconds(10), $"a stalled download times out: {stalled} after {clock.Elapsed}");
            Expect(Directory.GetFileSystemEntries(folder).Length == 0, "a stalled download leaves no file");
        }
        finally
        {
            if (Directory.Exists(folder)) Directory.Delete(folder, recursive: true);
        }
    }

    private static string Show(string value)
    {
        var builder = new StringBuilder("\"");
        foreach (var c in value.Length > 80 ? value[..80] + "…" : value)
            builder.Append(c is >= ' ' and <= '~' ? c.ToString() : $"\\u{{{(int)c:X}}}");
        return builder.Append('"').ToString();
    }

    private static void AcceptsSharedVectors(IReadOnlyList<(string Input, string Origin, int? Port)> cases)
    {
        foreach (var (input, expected, port) in cases)
        {
            if (!PortalAddress.TryValidate(input, out var origin, out var error))
            {
                Expect(false, $"{Show(input)} rejected: {error}");
                continue;
            }
            Expect(origin!.Origin == expected, $"{Show(input)} -> {origin.Origin}, expected {expected}");
            Expect(origin.Port == port, $"{Show(input)} port {origin.Port}");
            Expect(origin.Url.AbsoluteUri == expected + "/", $"{Show(input)} url {origin.Url.AbsoluteUri}");
            Expect(PortalAddress.TryValidate(origin.Origin, out var again, out _) && again == origin, $"{Show(input)} normalized form revalidates");
            Expect(WebPolicy.IsPortalDocument(origin.Url.AbsoluteUri, origin), $"{Show(input)} root is a portal document");
        }
    }

    private static void RejectsSharedVectors(IReadOnlyList<(string Input, PortalAddressError Error)> cases)
    {
        foreach (var (input, expected) in cases)
        {
            if (PortalAddress.TryValidate(input, out var origin, out var error))
            {
                Expect(false, $"{Show(input)} accepted as {origin!.Origin}");
                continue;
            }
            Expect(error == expected, $"{Show(input)}: {error}, expected {expected}");
            Expect(PortalAddress.Message(error).Length > 0, $"{error} has a message");
        }
    }

    private static void WindowsSpecificInput()
    {
        var accepted = new (string, string)[]
        {
            ("\u3000https://cage.example.org\u00a0", "https://cage.example.org"),
            ("\u2028https://cage.example.org/\r\n", "https://cage.example.org"),
            ("https://CAGE.example.org:08443", "https://cage.example.org:8443"),
        };
        foreach (var (input, expected) in accepted)
            Expect(PortalAddress.TryValidate(input, out var origin, out _) && origin!.Origin == expected, $"{Show(input)} -> {expected}");
        var rejected = new (string, PortalAddressError)[]
        {
            ("\ufeffhttps://cage.example.org", PortalAddressError.InvalidCharacters),
            ("https://cage.example.org/\ud83d\ude00", PortalAddressError.InvalidCharacters),
            ("https://cage.example.org\ud800", PortalAddressError.InvalidCharacters),
            ("https://cage.example.org\\", PortalAddressError.Host),
            ("https://cage.example.org\\login", PortalAddressError.Host),
            ("https://%63age.example.org", PortalAddressError.Host),
        };
        foreach (var (input, expected) in rejected)
        {
            var ok = PortalAddress.TryValidate(input, out _, out var error);
            Expect(!ok && error == expected, $"{Show(input)}: {(ok ? "accepted" : error.ToString())}, expected {expected}");
        }
        Expect(!PortalAddress.TryValidate(null, out _, out var nullError) && nullError == PortalAddressError.Empty, "null is empty");
    }

    private static string TempRoot()
    {
        var root = Path.Combine(Path.GetTempPath(), "cage-tenant-windows-tests-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        return root;
    }

    private static void StoreKeepsOnlyTheOrigin()
    {
        var root = TempRoot();
        try
        {
            var store = new PortalStore(Path.Combine(root, "Cage Tenant"));
            Expect(store.Load() == null, "empty store loads nothing");
            var origin = PortalAddress.Validate("https://Cage.Example.org:8443/");
            store.Save(origin);
            Expect(store.Load() == origin, "saved origin loads back");
            Expect(File.ReadAllText(store.OriginFile) == "https://cage.example.org:8443\n", "stored as the normalized origin");
            Expect(Directory.GetFileSystemEntries(store.Root).Select(Path.GetFileName).SequenceEqual([PortalStore.OriginFileName]),
                "only the origin file is written: " + string.Join(", ", Directory.GetFileSystemEntries(store.Root)));

            File.WriteAllText(store.OriginFile, "https://user:pw@cage.example.org");
            Expect(store.Load() == null && !File.Exists(store.OriginFile), "tampered value is removed");
            File.WriteAllBytes(store.OriginFile, [0x68, 0xff, 0xfe]);
            Expect(store.Load() == null && !File.Exists(store.OriginFile), "invalid UTF-8 is removed");
            File.WriteAllText(store.OriginFile, "https://cage.example.org" + new string(' ', 5000));
            Expect(store.Load() == null && !File.Exists(store.OriginFile), "oversized file is removed");
            File.WriteAllText(store.OriginFile, "  HTTPS://cage.example.org:443/ \n");
            Expect(store.Load()?.Origin == "https://cage.example.org", "hand-written valid value is normalized on load");

            store.Save(origin);
            store.Save(PortalAddress.Validate("https://other.example.org"));
            Expect(store.Load()?.Origin == "https://other.example.org", "save replaces the previous origin");
            Expect(Directory.GetFiles(store.Root).Length == 1, "no temporary files are left behind");
            store.Clear();
            Expect(store.Load() == null && !File.Exists(store.OriginFile), "clear removes the origin");
            store.Clear();
            Expect(!File.Exists(store.OriginFile), "clearing twice is harmless");
        }
        finally { Directory.Delete(root, true); }
    }

    private static void SessionsArePerOrigin()
    {
        var root = TempRoot();
        try
        {
            var store = new PortalStore(Path.Combine(root, "Cage Tenant"));
            var a = PortalAddress.Validate("https://cage.example.org");
            var a443 = PortalAddress.Validate("https://CAGE.example.org:443/");
            var b = PortalAddress.Validate("https://cage.example.org:8443");
            var key = PortalStore.SessionKey(a);
            Expect(key.Length == 32 && key.All(c => c is (>= '0' and <= '9') or (>= 'a' and <= 'f')), $"session key is 32 hex: {key}");
            Expect(PortalStore.SessionKey(a443) == key, "same origin, same session");
            Expect(PortalStore.SessionKey(b) != key, "a different port is a different session");
            Expect(Path.GetDirectoryName(store.SessionFolder(a)) == store.SessionsRoot, "session folder is under Sessions");

            Expect(store.RemoveSessionsExcept(a).Count == 0, "nothing to remove before any session exists");
            Directory.CreateDirectory(Path.Combine(store.SessionFolder(a), "EBWebView", "Default"));
            File.WriteAllText(Path.Combine(store.SessionFolder(a), "EBWebView", "Default", "Cookies"), "a");
            Directory.CreateDirectory(Path.Combine(store.SessionFolder(b), "EBWebView"));
            File.WriteAllText(Path.Combine(store.SessionsRoot, "stray.txt"), "x");
            var outside = Path.Combine(root, "outside");
            Directory.CreateDirectory(outside);
            File.WriteAllText(Path.Combine(outside, "keep.txt"), "keep");
            var link = Path.Combine(store.SessionsRoot, "link");
            var linked = TryCreateLink(link, outside);

            Expect(store.RemoveSessionsExcept(a).Count == 0, "stale sessions removed");
            Expect(Directory.Exists(store.SessionFolder(a)), "current session kept");
            Expect(!Directory.Exists(store.SessionFolder(b)), "other server's session removed");
            Expect(!File.Exists(Path.Combine(store.SessionsRoot, "stray.txt")), "stray file removed");
            if (linked)
            {
                Expect(!Directory.Exists(link) && !File.Exists(link), "link removed");
                Expect(File.Exists(Path.Combine(outside, "keep.txt")), "link target is not followed");
            }

            Expect(store.RemoveSession(a) && !Directory.Exists(store.SessionFolder(a)), "session removed");
            Expect(store.RemoveSession(a), "removing a missing session succeeds");
            Directory.CreateDirectory(store.SessionFolder(b));
            Expect(store.RemoveSessionsExcept(null).Count == 0 && !Directory.EnumerateFileSystemEntries(store.SessionsRoot).Any(),
                "no configured server removes every session");
        }
        finally { Directory.Delete(root, true); }
    }

    private static bool TryCreateLink(string link, string target)
    {
        try { Directory.CreateSymbolicLink(link, target); return true; }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException) { return false; }
    }

    private static void MainFramePolicy()
    {
        var origin = PortalAddress.Validate("https://cage.example.org");
        foreach (var uri in new[]
        {
            "https://cage.example.org/", "https://cage.example.org", "https://cage.example.org/login?next=%2F#vm",
            "HTTPS://CAGE.EXAMPLE.ORG:443/app", "https://cage.example.org/_tenant/../",
        })
            Expect(WebPolicy.IsPortalDocument(uri, origin), $"{uri} allowed in the main frame");
        var vm = "https://vm-0123456789abcdef0123456789abcdef.cage.example.org/_tenant/enter";
        foreach (var uri in new string?[]
        {
            null, "", "about:blank", "about:srcdoc", "data:text/html,hi", "javascript:alert(1)", "file:///C:/Windows/win.ini",
            "blob:https://cage.example.org/3f0b", "http://cage.example.org/", "https://cage.example.org:8443/",
            "https://cage.example.org./", "https://user@cage.example.org/", "https://cage.example.org.evil.example/",
            "https://evil.example/cage.example.org", "https://example.org/", "wss://cage.example.org/", vm,
            "https://127.0.0.1/", "https://[::1]/", "mailto:a@cage.example.org", "ms-settings:privacy",
        })
            Expect(!WebPolicy.IsPortalDocument(uri, origin), $"{uri ?? "null"} blocked in the main frame");
        var withPort = PortalAddress.Validate("https://cage.example.org:8443");
        Expect(WebPolicy.IsPortalDocument("https://cage.example.org:8443/x", withPort), "custom port allowed");
        Expect(!WebPolicy.IsPortalDocument("https://cage.example.org/x", withPort), "default port is another origin");
    }

    private static void PortalFamilyPolicy()
    {
        var origin = PortalAddress.Validate("https://cage.example.org:8443");
        const string id = "0123456789abcdef0123456789abcdef";
        Expect(WebPolicy.IsPortalFamily("https://cage.example.org:8443/", origin), "portal origin");
        Expect(WebPolicy.IsPortalFamily($"https://vm-{id}.cage.example.org:8443/", origin), "VM origin");
        foreach (var uri in new[]
        {
            $"https://vm-{id}.cage.example.org/", $"http://vm-{id}.cage.example.org:8443/", $"https://vm-{id[..31]}.cage.example.org:8443/",
            $"https://x.vm-{id}.cage.example.org:8443/", $"https://vm-{id}.other.example.org:8443/", $"https://vm-{id}g.cage.example.org:8443/",
            "https://evil.example:8443/", $"https://vm-{id}.cage.example.org.evil.example:8443/",
        })
            Expect(!WebPolicy.IsPortalFamily(uri, origin), $"{uri} is not the portal family");
    }

    private static void ConnectPlans()
    {
        var plan = WebPolicy.PlanConnect("http://cage.example.org", null);
        Expect(plan.Action == ConnectAction.Reject && plan.Error == PortalAddressError.NotHttps && plan.Message == PortalAddress.Message(PortalAddressError.NotHttps), "invalid input is rejected with its message");
        plan = WebPolicy.PlanConnect(" https://cage.example.org/ ", null);
        Expect(plan.Action == ConnectAction.Connect && plan.Origin?.Origin == "https://cage.example.org", "first server connects");
        var current = plan.Origin;
        plan = WebPolicy.PlanConnect("HTTPS://Cage.Example.org:443", current);
        Expect(plan.Action == ConnectAction.Keep, "same origin keeps the session");
        plan = WebPolicy.PlanConnect("https://cage.example.org:8443", current);
        Expect(plan.Action == ConnectAction.Switch && plan.Origin?.Port == 8443, "different origin switches");
        plan = WebPolicy.PlanConnect("https://cage.example.org/login", current);
        Expect(plan.Action == ConnectAction.Reject && plan.Origin == null, "invalid input never switches");
    }

    private static void WebView2EnvironmentIsScrubbed()
    {
        IDictionary env = new Hashtable
        {
            ["PATH"] = "C:\\Windows",
            ["WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"] = "--ignore-certificate-errors",
            ["webview2_user_data_folder"] = "C:\\x",
            ["WEBVIEW2_BROWSER_EXECUTABLE_FOLDER"] = "C:\\evil",
            ["MY_WEBVIEW2_SETTING"] = "1",
        };
        var names = WebPolicy.WebView2VariablesToClear(env);
        Expect(names.SequenceEqual(["WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "WEBVIEW2_BROWSER_EXECUTABLE_FOLDER", "webview2_user_data_folder"]),
            "WEBVIEW2_* variables are cleared: " + string.Join(", ", names));
    }
}

/// <summary>Reads the address vectors from desktop/tenant/Tests/TenantTests.swift.</summary>
internal static class SwiftVectors
{
    public sealed record Result(List<(string Input, string Origin, int? Port)> Accepted, List<(string Input, PortalAddressError Error)> Rejected);

    public static Result Parse(string swift)
    {
        var accepted = new List<(string, string, int?)>();
        foreach (var line in Block(swift, "func acceptsPortalOrigins()"))
        {
            var p = new Parser(line);
            p.Expect('(');
            var input = p.Expression();
            p.Expect(',');
            var origin = p.Expression();
            p.Expect(',');
            int? port = p.TryWord("nil") ? null : p.Integer();
            p.Expect(')');
            accepted.Add((input, origin, port));
        }
        var rejected = new List<(string, PortalAddressError)>();
        foreach (var line in Block(swift, "func rejectsOtherAddresses()"))
        {
            var p = new Parser(line);
            p.Expect('(');
            var input = p.Expression();
            p.Expect(',');
            p.Expect('.');
            var name = p.Identifier();
            p.Expect(')');
            rejected.Add((input, Enum.Parse<PortalAddressError>(name, ignoreCase: true)));
        }
        return new Result(accepted, rejected);
    }

    /// <summary>The tuple lines of the `let cases = [...]` array in one test function.</summary>
    private static IEnumerable<string> Block(string swift, string function)
    {
        var start = swift.IndexOf(function, StringComparison.Ordinal);
        if (start < 0) throw new FormatException($"{function} not found");
        var open = swift.IndexOf("let cases", start, StringComparison.Ordinal);
        var end = swift.IndexOf("\n        ]\n", open, StringComparison.Ordinal);
        if (open < 0 || end < 0) throw new FormatException($"cases array not found in {function}");
        foreach (var raw in swift[open..end].Split('\n').Skip(1))
        {
            var line = raw.Trim();
            if (line.Length == 0) continue;
            if (!line.StartsWith('(') || !line.EndsWith("),", StringComparison.Ordinal)) throw new FormatException($"unexpected line: {line}");
            yield return line[..^1];
        }
    }

    private sealed class Parser(string text)
    {
        private int i;

        private void Space() { while (i < text.Length && text[i] == ' ') i++; }

        public void Expect(char c)
        {
            Space();
            if (i >= text.Length || text[i] != c) throw new FormatException($"expected '{c}' at {i} in {text}");
            i++;
        }

        public bool TryWord(string word)
        {
            Space();
            if (string.CompareOrdinal(text, i, word, 0, word.Length) != 0) return false;
            i += word.Length;
            return true;
        }

        public int Integer()
        {
            Space();
            var start = i;
            while (i < text.Length && char.IsAsciiDigit(text[i])) i++;
            if (start == i) throw new FormatException($"expected a number at {start} in {text}");
            return int.Parse(text[start..i], System.Globalization.CultureInfo.InvariantCulture);
        }

        public string Identifier()
        {
            var start = i;
            while (i < text.Length && char.IsAsciiLetterOrDigit(text[i])) i++;
            return text[start..i];
        }

        /// <summary>String literals and String(repeating:count:) joined by +.</summary>
        public string Expression()
        {
            var value = new StringBuilder(Term());
            while (true)
            {
                Space();
                if (i >= text.Length || text[i] != '+') return value.ToString();
                i++;
                value.Append(Term());
            }
        }

        private string Term()
        {
            Space();
            if (TryWord("String(repeating:"))
            {
                var unit = Literal();
                Expect(',');
                if (!TryWord("count:")) throw new FormatException($"expected count: in {text}");
                var count = Integer();
                Expect(')');
                return string.Concat(Enumerable.Repeat(unit, count));
            }
            return Literal();
        }

        private string Literal()
        {
            Expect('"');
            var value = new StringBuilder();
            while (true)
            {
                if (i >= text.Length) throw new FormatException($"unterminated string in {text}");
                var c = text[i++];
                if (c == '"') return value.ToString();
                if (c != '\\') { value.Append(c); continue; }
                var e = text[i++];
                switch (e)
                {
                    case '\\': value.Append('\\'); break;
                    case '"': value.Append('"'); break;
                    case '\'': value.Append('\''); break;
                    case 't': value.Append('\t'); break;
                    case 'n': value.Append('\n'); break;
                    case 'r': value.Append('\r'); break;
                    case '0': value.Append('\0'); break;
                    case 'u':
                        Expect('{');
                        var close = text.IndexOf('}', i);
                        value.Append(char.ConvertFromUtf32(Convert.ToInt32(text[i..close], 16)));
                        i = close + 1;
                        break;
                    default: throw new FormatException($"unsupported escape \\{e} in {text}");
                }
            }
        }
    }
}
