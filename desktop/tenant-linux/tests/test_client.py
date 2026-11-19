"""Checks for the parts of the Linux client that do not need GTK.

Usage: python3 -I desktop/tenant-linux/tests/test_client.py <TenantTests.swift>

The address vectors come from the macOS client's tests
(desktop/tenant/Tests/TenantTests.swift), so every client accepts and
rejects exactly the same input. Runs on any OS with Python 3.8+.
"""

from __future__ import annotations

import os
import re
import stat
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))

from cage_tenant_desktop import policy  # noqa: E402
from cage_tenant_desktop.launcher import main as launcher_main  # noqa: E402
from cage_tenant_desktop.policy import ConnectAction  # noqa: E402
from cage_tenant_desktop.portal_address import PortalAddressError, try_validate, validate  # noqa: E402
from cage_tenant_desktop.storage import PortalStore, session_key  # noqa: E402

checks = 0
failures = 0


def expect(condition, message):
    global checks, failures
    checks += 1
    if not condition:
        failures += 1
        line = sys._getframe(1).f_lineno
        print(f"FAIL (line {line}): {message}")


# ---- Swift vectors ------------------------------------------------------


def _swift_string(token: str) -> str:
    out, i = [], 0
    while i < len(token):
        c = token[i]
        if c != "\\":
            out.append(c)
            i += 1
            continue
        e = token[i + 1]
        if e == "u":
            close = token.index("}", i)
            out.append(chr(int(token[i + 3 : close], 16)))
            i = close + 1
            continue
        out.append({"\\": "\\", '"': '"', "'": "'", "t": "\t", "n": "\n", "r": "\r", "0": "\0"}[e])
        i += 2
    return "".join(out)


_TERM = re.compile(r'\s*(?:String\(repeating:\s*"((?:[^"\\]|\\.)*)",\s*count:\s*(\d+)\)|"((?:[^"\\]|\\.)*)")\s*')


def _expression(text: str, pos: int):
    value = ""
    while True:
        m = _TERM.match(text, pos)
        if not m:
            raise ValueError(f"bad expression at {pos}: {text}")
        value += _swift_string(m.group(1)) * int(m.group(2)) if m.group(2) else _swift_string(m.group(3))
        pos = m.end()
        if pos < len(text) and text[pos] == "+":
            pos += 1
            continue
        return value, pos


def _cases(swift: str, function: str):
    start = swift.index(function)
    open_ = swift.index("let cases", start)
    end = swift.index("\n        ]\n", open_)
    for raw in swift[open_:end].split("\n")[1:]:
        line = raw.strip()
        if not line:
            continue
        if not (line.startswith("(") and line.endswith("),")):
            raise ValueError("unexpected line: " + line)
        yield line[1:-2]


def swift_vectors(path: str):
    swift = open(path, encoding="utf-8").read()
    accepted, rejected = [], []
    for line in _cases(swift, "func acceptsPortalOrigins()"):
        value, pos = _expression(line, 0)
        assert line[pos] == ","
        origin, pos = _expression(line, pos + 1)
        port = line[pos:].strip(" ,")
        accepted.append((value, origin, None if port == "nil" else int(port)))
    errors = {e.value.lower(): e for e in PortalAddressError}
    for line in _cases(swift, "func rejectsOtherAddresses()"):
        value, pos = _expression(line, 0)
        name = line[pos:].strip(" ,")
        assert name.startswith("."), line
        rejected.append((value, errors[name[1:].lower()]))
    return accepted, rejected


def shared_vectors(path):
    accepted, rejected = swift_vectors(path)
    expect(len(accepted) >= 10, f"parsed {len(accepted)} accepted vectors")
    expect(len(rejected) >= 40, f"parsed {len(rejected)} rejected vectors")
    for value, expected, port in accepted:
        origin, error = try_validate(value)
        if origin is None:
            expect(False, f"{value!r} rejected: {error}")
            continue
        expect(origin.origin == expected, f"{value!r} -> {origin.origin}, expected {expected}")
        expect(origin.port == port, f"{value!r} port {origin.port}")
        expect(origin.url == expected + "/", f"{value!r} url {origin.url}")
        expect(try_validate(origin.origin)[0] == origin, f"{value!r} normalized form revalidates")
        expect(policy.is_portal_document(origin.url, origin), f"{value!r} root is a portal document")
    for value, expected in rejected:
        origin, error = try_validate(value)
        expect(origin is None and error == expected, f"{value[:80]!r}: {origin or error}, expected {expected}")
        expect(bool(expected.message), f"{expected} has a message")
    return len(accepted), len(rejected)


def linux_specific_input():
    for value, expected in [
        ("\u3000https://cage.example.org\u00a0", "https://cage.example.org"),
        ("\u2028https://cage.example.org/\r\n", "https://cage.example.org"),
        ("https://CAGE.example.org:08443", "https://cage.example.org:8443"),
    ]:
        origin, error = try_validate(value)
        expect(origin is not None and origin.origin == expected, f"{value!r} -> {expected} ({error})")
    for value, expected in [
        ("\ufeffhttps://cage.example.org", PortalAddressError.INVALID_CHARACTERS),
        ("https://cage.example.org/\U0001F600", PortalAddressError.INVALID_CHARACTERS),
        ("https://cage.example.org\\", PortalAddressError.HOST),
        ("https://%63age.example.org", PortalAddressError.HOST),
        (None, PortalAddressError.EMPTY),
    ]:
        origin, error = try_validate(value)
        expect(origin is None and error == expected, f"{value!r}: {origin or error}, expected {expected}")


# ---- storage ------------------------------------------------------------


def _mode(path):
    return stat.S_IMODE(os.lstat(path).st_mode)


def store_keeps_only_the_origin():
    with tempfile.TemporaryDirectory() as root:
        store = PortalStore.for_environment({"XDG_CONFIG_HOME": os.path.join(root, "config")}, home=root)
        expect(store.origin_file == os.path.join(root, "config", "cage-tenant-desktop", "portal-origin"), store.origin_file)
        expect(store.data_dir == os.path.join(root, ".local", "share", "cage-tenant-desktop"), store.data_dir)
        expect(store.cache_dir == os.path.join(root, ".cache", "cage-tenant-desktop"), store.cache_dir)
        relative = PortalStore.for_environment({"XDG_CONFIG_HOME": "relative/config"}, home=root)
        expect(relative.config_dir == os.path.join(root, ".config", "cage-tenant-desktop"), "relative XDG values are ignored")

        expect(store.load() is None, "empty store loads nothing")
        origin = validate("https://Cage.Example.org:8443/")
        store.save(origin)
        expect(store.load() == origin, "saved origin loads back")
        expect(open(store.origin_file).read() == "https://cage.example.org:8443\n", "stored as the normalized origin")
        expect(_mode(store.origin_file) == 0o600, f"origin file mode {oct(_mode(store.origin_file))}")
        expect(_mode(store.config_dir) == 0o700, f"config folder mode {oct(_mode(store.config_dir))}")
        expect(os.listdir(store.config_dir) == ["portal-origin"], f"only the origin file: {os.listdir(store.config_dir)}")
        expect(not os.path.exists(store.data_dir) and not os.path.exists(store.cache_dir), "saving creates no session")

        for content in (b"https://user:pw@cage.example.org", b"h\xff\xfe", b"https://cage.example.org" + b" " * 5000):
            with open(store.origin_file, "wb") as f:
                f.write(content)
            expect(store.load() is None and not os.path.lexists(store.origin_file), f"{content[:40]!r} is removed")

        outside = os.path.join(root, "outside")
        with open(outside, "w") as f:
            f.write("https://cage.example.org\n")
        os.symlink(outside, store.origin_file)
        expect(store.load() is None and not os.path.lexists(store.origin_file), "a link is removed, not followed")
        expect(os.path.exists(outside), "link target is kept")

        with open(store.origin_file, "w") as f:
            f.write("  HTTPS://cage.example.org:443/ \n")
        expect(store.load().origin == "https://cage.example.org", "hand-written valid value is normalized")
        store.save(validate("https://other.example.org"))
        expect(store.load().origin == "https://other.example.org", "save replaces the previous origin")
        expect(os.listdir(store.config_dir) == ["portal-origin"], "no temporary files are left")
        store.clear()
        expect(store.load() is None and not os.path.exists(store.origin_file), "clear removes the origin")
        store.clear()
        expect(True, "clearing twice is harmless")


def sessions_are_per_origin():
    with tempfile.TemporaryDirectory() as root:
        store = PortalStore(os.path.join(root, "c"), os.path.join(root, "d"), os.path.join(root, "k"))
        a = validate("https://cage.example.org")
        a443 = validate("https://CAGE.example.org:443/")
        b = validate("https://cage.example.org:8443")
        key = session_key(a)
        expect(re.fullmatch(r"[0-9a-f]{32}", key) is not None, f"session key {key}")
        expect(session_key(a443) == key and session_key(b) != key, "one session per origin")
        data, cache = store.session_dirs(a, create=True)
        expect(data == os.path.join(root, "d", "sessions", key) and cache == os.path.join(root, "k", "sessions", key), (data, cache))
        expect(_mode(data) == 0o700 and _mode(cache) == 0o700, "session folders are private")
        with open(os.path.join(data, "cookies.sqlite"), "w") as f:
            f.write("a")
        bdata, bcache = store.session_dirs(b, create=True)
        with open(os.path.join(root, "d", "sessions", "stray"), "w") as f:
            f.write("x")
        outside = os.path.join(root, "outside")
        os.makedirs(outside)
        with open(os.path.join(outside, "keep"), "w") as f:
            f.write("keep")
        os.symlink(outside, os.path.join(root, "k", "sessions", "link"))

        expect(store.remove_sessions_except(a) == [], "stale sessions removed")
        expect(os.path.isdir(data) and os.path.isdir(cache), "current session kept")
        expect(not os.path.exists(bdata) and not os.path.exists(bcache), "other server's session removed")
        expect(not os.path.lexists(os.path.join(root, "d", "sessions", "stray")), "stray file removed")
        expect(not os.path.lexists(os.path.join(root, "k", "sessions", "link")), "link removed")
        expect(os.path.exists(os.path.join(outside, "keep")), "link target is not followed")
        expect(store.remove_session(a) and not os.path.exists(data) and not os.path.exists(cache), "session removed")
        expect(store.remove_session(a), "removing a missing session succeeds")
        store.session_dirs(b, create=True)
        expect(store.remove_sessions_except(None) == [], "no configured server removes every session")
        expect(os.listdir(os.path.join(root, "d", "sessions")) == [], "data sessions empty")


# ---- policy -------------------------------------------------------------


def main_frame_policy():
    origin = validate("https://cage.example.org")
    for uri in [
        "https://cage.example.org/",
        "https://cage.example.org",
        "https://cage.example.org/login?next=%2F#vm",
        "HTTPS://CAGE.EXAMPLE.ORG:443/app",
    ]:
        expect(policy.is_portal_document(uri, origin), f"{uri} allowed in the main frame")
    vm = "https://vm-0123456789abcdef0123456789abcdef.cage.example.org/_tenant/enter"
    for uri in [
        None, "", "about:blank", "about:srcdoc", "data:text/html,hi", "javascript:alert(1)", "file:///etc/passwd",
        "blob:https://cage.example.org/3f0b", "http://cage.example.org/", "https://cage.example.org:8443/",
        "https://cage.example.org./", "https://user@cage.example.org/", "https://cage.example.org.evil.example/",
        "https://evil.example/cage.example.org", "https://example.org/", "wss://cage.example.org/", vm,
        "https://cage.example.org\\@evil.example/", "https://127.0.0.1/", "https://[::1]/", "mailto:a@cage.example.org",
        "https://cage.example.org:99999/",
    ]:
        expect(not policy.is_portal_document(uri, origin), f"{uri} blocked in the main frame")
    with_port = validate("https://cage.example.org:8443")
    expect(policy.is_portal_document("https://cage.example.org:8443/x", with_port), "custom port allowed")
    expect(not policy.is_portal_document("https://cage.example.org/x", with_port), "default port is another origin")

    for uri in ["https://x/", "http://x/", "about:blank", "about:srcdoc", "data:,x", "blob:https://x/1", "HTTPS://X/"]:
        expect(policy.is_web_navigation(uri), f"{uri} may load in a frame")
    for uri in [None, "", "file:///etc/passwd", "mailto:a@b", "ssh://host", "javascript:alert(1)", "x-github-client://open", "ftp://h/"]:
        expect(not policy.is_web_navigation(uri), f"{uri} is refused in every frame")

    expect(policy.describe("https://user:pw@evil.example:8443/secret?token=1#t") == "https://evil.example:8443", "describe hides credentials and path")
    expect(policy.describe("mailto:a@b") == "mailto:", "describe of mailto")
    expect(policy.describe(None) == "未知地址", "describe of nothing")


def portal_family_policy():
    origin = validate("https://cage.example.org:8443")
    vm = "0123456789abcdef0123456789abcdef"
    expect(policy.is_portal_family("https://cage.example.org:8443/", origin), "portal origin")
    expect(policy.is_portal_family(f"https://vm-{vm}.cage.example.org:8443/", origin), "VM origin")
    for uri in [
        f"https://vm-{vm}.cage.example.org/", f"http://vm-{vm}.cage.example.org:8443/", f"https://vm-{vm[:31]}.cage.example.org:8443/",
        f"https://x.vm-{vm}.cage.example.org:8443/", f"https://vm-{vm}.other.example.org:8443/", "https://evil.example:8443/",
    ]:
        expect(not policy.is_portal_family(uri, origin), f"{uri} is not the portal family")


def connect_plans():
    plan = policy.plan_connect("http://cage.example.org", None)
    expect(plan.action == ConnectAction.REJECT and plan.error == PortalAddressError.NOT_HTTPS and plan.message, "invalid input rejected")
    plan = policy.plan_connect(" https://cage.example.org/ ", None)
    expect(plan.action == ConnectAction.CONNECT and plan.origin.origin == "https://cage.example.org", "first server connects")
    current = plan.origin
    expect(policy.plan_connect("HTTPS://Cage.Example.org:443", current).action == ConnectAction.KEEP, "same origin keeps the session")
    plan = policy.plan_connect("https://cage.example.org:8443", current)
    expect(plan.action == ConnectAction.SWITCH and plan.origin.port == 8443, "different origin switches")
    plan = policy.plan_connect("https://cage.example.org/login", current)
    expect(plan.action == ConnectAction.REJECT and plan.origin is None, "invalid input never switches")


def environment_and_menu():
    env = {"PATH": "/usr/bin", "WEBKIT_INSPECTOR_SERVER": "0.0.0.0:9", "WEBKIT_INSPECTOR_HTTP_SERVER": "x", "webkit_inspector_pipe": "1", "WEBKIT_DISABLE_COMPOSITING_MODE": "1"}
    expect(policy.webkit_variables_to_clear(env) == ["WEBKIT_INSPECTOR_HTTP_SERVER", "WEBKIT_INSPECTOR_SERVER", "webkit_inspector_pipe"], "inspector variables cleared")
    actions = ["OPEN_LINK", "OPEN_LINK_IN_NEW_WINDOW", "OPEN_IMAGE_IN_NEW_WINDOW", "OPEN_FRAME_IN_NEW_WINDOW", "COPY", "PASTE", "INSPECT_ELEMENT", "DOWNLOAD_LINK_TO_DISK"]
    expect(policy.removable_context_actions(actions) == ["OPEN_LINK_IN_NEW_WINDOW", "OPEN_IMAGE_IN_NEW_WINDOW", "OPEN_FRAME_IN_NEW_WINDOW", "INSPECT_ELEMENT"], "context menu filter")


def local_installer_checks():
    import hashlib
    import http.client
    import io

    from cage_tenant_desktop import local_cage

    with tempfile.TemporaryDirectory() as root:
        dpkg = os.path.join(root, "dpkg")
        open(dpkg, "w").close()
        missing = os.path.join(root, "no-dpkg")
        expect(local_cage.supported("x86_64", dpkg) and local_cage.supported("AMD64", dpkg), "x86_64 Debian hosts can install locally")
        expect(not local_cage.supported("aarch64", dpkg), "ARM64 hosts are refused before any download")
        expect(not local_cage.supported("x86_64", missing), "hosts without dpkg are refused before any download")
    expect(local_cage.URL.startswith("https://github.com/Lee-xeo/Claude-Cage/releases/download/v0.1.8/"), "pinned upstream URL")

    class Response(io.BytesIO):
        status = 200

    def attempt(folder, opener):
        try:
            return local_cage.prepare_installer(folder, opener=opener), None
        except local_cage.LocalCageError as e:
            return None, str(e)

    with tempfile.TemporaryDirectory() as root:
        folder = os.path.join(root, "local")
        calls = []

        def tampered(url, timeout):
            calls.append((url, timeout))
            return Response(b"not the pinned package")

        _, error = attempt(folder, tampered)
        expect(error is not None and "校验失败" in error, "a package of the wrong size is refused")
        expect(calls == [(local_cage.URL, 60)], "one download from the pinned URL with a read timeout")
        expect(os.listdir(folder) == [], "a refused download leaves no file")
        expect(_mode(folder) == 0o700, "a folder the app creates is private")

        _, error = attempt(folder, lambda url, timeout: Response(b"x" * (local_cage.SIZE + 1)))
        expect(error is not None and os.listdir(folder) == [], "an oversized download is refused and removed")

        def offline(url, timeout):
            raise OSError("offline")

        _, error = attempt(folder, offline)
        expect(error == local_cage.DOWNLOAD_FAILED, "a network failure is reported as a download failure")

        class Broken(Response):
            def __init__(self, failure):
                super().__init__(b"partial")
                self.failure = failure
                self.reads = 0

            def read(self, size=-1):
                self.reads += 1
                if self.reads > 1:
                    raise self.failure
                return super().read(size)

        for failure in (http.client.IncompleteRead(b"x"), http.client.BadStatusLine("garbage"), TimeoutError("stalled")):
            _, error = attempt(folder, lambda url, timeout, failure=failure: Broken(failure))
            expect(error == local_cage.DOWNLOAD_FAILED and os.listdir(folder) == [],
                   f"{type(failure).__name__} while reading is a download failure and leaves no file")

        # A small stand-in for the pinned package. "evil" has the same length,
        # so only the SHA-256 comparison can refuse it.
        good, evil = b"the real package", b"the evil package"
        saved = local_cage.SIZE, local_cage.SHA256
        local_cage.SIZE, local_cage.SHA256 = len(good), hashlib.sha256(good).hexdigest()
        try:
            _, error = attempt(folder, lambda url, timeout: Response(evil))
            expect(error == local_cage.CHECKSUM_FAILED and os.listdir(folder) == [],
                   "a same-size package with another digest is refused and removed")
            partial = os.path.join(folder, "." + local_cage.NAME + ".part")
            with open(partial, "wb") as f:
                f.write(b"left by an interrupted run")
            path, error = attempt(folder, lambda url, timeout: Response(good))
            expect(error is None and path == os.path.join(folder, local_cage.NAME), "the matching package is accepted")
            expect(os.listdir(folder) == [local_cage.NAME], "a partial file from an interrupted run is replaced")
            path, error = attempt(folder, offline)
            expect(error is None and path is not None, "a verified cached package is reused without downloading")
            with open(path, "wb") as f:
                f.write(evil)
            expect(not local_cage.verified(path), "a cached same-size package with another digest is not trusted")
        finally:
            local_cage.SIZE, local_cage.SHA256 = saved

        real_fdopen = os.fdopen

        class Writer:
            def __init__(self, fd, mode):
                self.file = real_fdopen(fd, mode)

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                self.file.close()

            def write(self, chunk):
                raise OSError(28, "No space left on device")

        local_cage.os.fdopen = Writer
        try:
            _, error = attempt(folder + "-full", lambda url, timeout: Response(b"data"))
        finally:
            local_cage.os.fdopen = real_fdopen
        expect(error == local_cage.DISK_FAILED, "a full disk is reported as a disk failure, not a network one")


def launcher_arguments():
    import contextlib
    import io

    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        expect(launcher_main(["--help"]) == 0, "--help succeeds")
    expect("Usage: cage-tenant-desktop" in out.getvalue(), "--help prints usage")
    err = io.StringIO()
    with contextlib.redirect_stderr(err):
        expect(launcher_main(["https://cage.example.org"]) == 2, "an address argument is refused")
    expect("Usage" in err.getvalue(), "refusal prints usage")


def main(argv):
    if len(argv) != 2 or not os.path.isfile(argv[1]):
        sys.stderr.write("Usage: test_client.py <desktop/tenant/Tests/TenantTests.swift>\n")
        return 2
    accepted, rejected = shared_vectors(argv[1])
    linux_specific_input()
    store_keeps_only_the_origin()
    sessions_are_per_origin()
    main_frame_policy()
    portal_family_policy()
    connect_plans()
    environment_and_menu()
    local_installer_checks()
    launcher_arguments()
    if failures:
        print(f"{failures} of {checks} checks failed")
        return 1
    print(f"ok: {checks} checks passed ({accepted} accepted and {rejected} rejected shared vectors)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
