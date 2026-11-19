"""Drive the installed client under Xvfb against a local HTTPS fixture.

Runs as root inside the disposable test container only (tests/in-container.sh):
it adds a test CA to the container's trust store and names to /etc/hosts,
then starts the app as an unprivileged user. Never run it on a real machine.

Usage: python3 -I gui_smoke.py <output folder for screenshots and results>
"""

from __future__ import annotations

import http.server
import json
import os
import pwd
import shutil
import socketserver
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

VM = "0123456789abcdef0123456789abcdef"
USER = "tester"
APP_ID = "io.github.sweetcornna.claude-cage.tenant"
DISPLAY = ":99"
EVENTS = []
LOCK = threading.Lock()
RESULTS = []


def record(**event):
    event["t"] = round(time.time(), 2)
    with LOCK:
        EVENTS.append(event)


def events(**match):
    with LOCK:
        return [e for e in EVENTS if all(e.get(k) == v for k, v in match.items())]


PORTAL = """<!doctype html><meta charset="utf-8"><title>Fixture portal</title>
<style>
 body{margin:0;font:18px sans-serif;background:#eef3ff}
 #popup{display:block;width:100%%;height:220px;font-size:28px;background:#ffd;border:0;border-bottom:3px solid #333}
 iframe{width:96%%;height:260px;border:3px solid #333;margin:8px}
</style>
<button id="popup" onclick="window.open('https://other.test/popup'); fetch('/log?e=popup-clicked')">FIXTURE PORTAL %(host)s: click to try window.open</button>
<p id="s">portal loaded</p>
<iframe sandbox="allow-scripts allow-same-origin allow-forms allow-downloads" referrerpolicy="no-referrer"
 allow="clipboard-read https://vm-%(vm)s.%(host)s; clipboard-write https://vm-%(vm)s.%(host)s"
 src="https://vm-%(vm)s.%(host)s/"></iframe>
<script>
 const log = e => fetch('/log?e=' + encodeURIComponent(e), {cache: 'no-store'});
 log('portal-loaded');
 if (%(leave)s) {
   setTimeout(async () => {
     await log('navigating-away');
     location.href = 'https://other.test/away';
   }, 1000);
   setTimeout(() => { log('still-here'); document.getElementById('s').textContent = 'still on the portal after a blocked navigation'; }, 5000);
 }
</script>"""

VM_PAGE = """<!doctype html><meta charset="utf-8"><title>VM</title>
<body style="background:#dfd;font:18px sans-serif">VM WORKBENCH %(host)s
<script>
 fetch('/vm-session', {method: 'POST', cache: 'no-store'})
   .then(response => { if (!response.ok) throw new Error('session setup failed'); })
   .then(() => fetch('/log?e=vm-loaded', {cache: 'no-store'}));
 setTimeout(() => { location.href = 'https://other.test/inframe'; }, 1500);
</script>"""

OTHER = """<!doctype html><meta charset="utf-8"><title>Other origin</title>
<body style="background:#fbb;font:28px sans-serif">OTHER ORIGIN %(path)s</body>"""


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        host = (self.headers.get("Host") or "").split(":")[0]
        if host not in (f"vm-{VM}.cage.test", f"vm-{VM}.cage2.test") or self.path != "/vm-session":
            self.send_error(404)
            return
        self.send_response(204)
        self.send_header("Set-Cookie", "__Host-fixture_vm=ready; Secure; HttpOnly; SameSite=Strict; Path=/")
        self.end_headers()

    def do_GET(self):
        host = (self.headers.get("Host") or "").split(":")[0]
        url = urllib.parse.urlsplit(self.path)
        cookies = self.headers.get("Cookie") or ""
        cookie = "fixture_sid=" in cookies
        vm_cookie = "__Host-fixture_vm=ready" in cookies
        query = urllib.parse.parse_qs(url.query)
        if url.path == "/log":
            record(host=host, event=query.get("e", [""])[0], cookie=cookie, vm_cookie=vm_cookie)
            self.send_response(204)
            self.end_headers()
            return
        record(host=host, path=url.path, query=url.query, cookie=cookie)
        headers = [("Content-Type", "text/html; charset=utf-8"), ("Cache-Control", "no-store")]
        if host in ("cage.test", "cage2.test"):
            body = PORTAL % {"host": host, "vm": VM, "leave": "true" if LEAVE.is_set() else "false"}
            if not cookie:
                headers.append(("Set-Cookie", "fixture_sid=%s; Secure; HttpOnly; SameSite=Strict; Path=/; Max-Age=3600" % os.urandom(8).hex()))
        elif host.startswith("vm-"):
            body = VM_PAGE % {"host": host}
        else:
            body = OTHER % {"path": url.path}
        data = body.encode("utf-8")
        self.send_response(200)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True


def run(cmd, **kw):
    return subprocess.run(cmd, check=True, text=True, capture_output=True, **kw)


def make_certs(tmp):
    """A test CA that signs the fixture names, and an untrusted self-signed cert."""
    ca_key, ca_crt = f"{tmp}/ca.key", f"{tmp}/ca.crt"
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=Cage Tenant Test CA",
         "-keyout", ca_key, "-out", ca_crt])
    names = ["cage.test", "*.cage.test", "cage2.test", "*.cage2.test", "other.test"]
    ext = f"{tmp}/ext.cnf"
    with open(ext, "w") as f:
        f.write("subjectAltName=" + ",".join("DNS:" + n for n in names) + "\nextendedKeyUsage=serverAuth\n")
    run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=cage.test", "-keyout", f"{tmp}/good.key", "-out", f"{tmp}/good.csr"])
    run(["openssl", "x509", "-req", "-in", f"{tmp}/good.csr", "-CA", ca_crt, "-CAkey", ca_key, "-CAcreateserial", "-days", "2",
         "-extfile", ext, "-out", f"{tmp}/good.crt"])
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=badtls.test",
         "-addext", "subjectAltName=DNS:badtls.test", "-keyout", f"{tmp}/bad.key", "-out", f"{tmp}/bad.crt"])
    shutil.copy(ca_crt, "/usr/local/share/ca-certificates/cage-tenant-test-ca.crt")
    run(["update-ca-certificates"])


def start_server(tmp):
    good = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    good.load_cert_chain(f"{tmp}/good.crt", f"{tmp}/good.key")
    bad = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    bad.load_cert_chain(f"{tmp}/bad.crt", f"{tmp}/bad.key")

    def sni(sock, name, ctx):
        if name == "badtls.test":
            record(host="badtls.test", tls="handshake")
            sock.context = bad

    good.sni_callback = sni
    server = Server(("0.0.0.0", 443), Handler)
    server.socket = good.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


class App:
    def __init__(self, out):
        self.out = out
        self.home = pwd.getpwnam(USER).pw_dir
        self.proc = None
        self.shot = 0

    def env(self):
        return {
            "HOME": self.home, "USER": USER, "PATH": "/usr/local/bin:/usr/bin:/bin", "DISPLAY": DISPLAY,
            "LANG": "C.UTF-8", "NO_AT_BRIDGE": "1", "LIBGL_ALWAYS_SOFTWARE": "1",
            "WEBKIT_DISABLE_DMABUF_RENDERER": "1", "WEBKIT_INSPECTOR_SERVER": "127.0.0.1:9222",
        }

    def start(self):
        log = open(f"{self.out}/app.log", "a")
        self.proc = subprocess.Popen(["runuser", "-u", USER, "--", "env", "-i"] + [f"{k}={v}" for k, v in self.env().items()]
                                     + ["cage-tenant-desktop"], stdout=log, stderr=log)
        self.wid = wait(lambda: self.window(), 30, "app window")
        run(["xdotool", "windowsize", self.wid, "1100", "760"], env={"DISPLAY": DISPLAY})
        time.sleep(1)

    def window(self):
        r = subprocess.run(["xdotool", "search", "--onlyvisible", "--name", "^Cage Tenant"], text=True, capture_output=True,
                           env={"DISPLAY": DISPLAY})
        ids = r.stdout.split()
        return ids[0] if ids else None

    def windows(self):
        r = subprocess.run(["xdotool", "search", "--onlyvisible", "--pid", str(self.app_pid())], text=True, capture_output=True,
                           env={"DISPLAY": DISPLAY})
        return r.stdout.split()

    def app_pid(self):
        r = subprocess.run(["pgrep", "-u", USER, "-f", "cage-tenant-desktop"], text=True, capture_output=True)
        return int(r.stdout.split()[0]) if r.stdout.split() else -1

    def x(self, *args):
        run(["xdotool", *args], env={"DISPLAY": DISPLAY})

    def focus(self):
        wid = self.window()
        self.x("windowfocus", "--sync", wid)
        return wid

    def keys(self, *keys):
        self.focus()
        for key in keys:
            self.x("key", "--clearmodifiers", key)
            time.sleep(0.3)

    def type(self, text):
        self.focus()
        self.x("type", "--delay", "30", text)

    def dialog_key(self, key):
        """Send a key to the app's modal dialog: its largest window other than the main one."""
        self.x("mousemove", "1270", "890")
        main = self.window()

        def dialog():
            best = None
            for wid in self.windows():
                if wid == main:
                    continue
                geo = run(["xdotool", "getwindowgeometry", "--shell", wid], env={"DISPLAY": DISPLAY}).stdout
                vals = dict(line.split("=") for line in geo.split())
                area = int(vals["WIDTH"]) * int(vals["HEIGHT"])
                if area > 20000 and (best is None or area > best[0]):
                    best = (area, wid)
            return best and best[1]

        wid = wait(dialog, 10, "confirmation dialog")
        self.x("windowfocus", "--sync", wid)
        time.sleep(0.3)
        self.x("key", "--clearmodifiers", key)
        time.sleep(0.5)

    def click(self, dx, dy):
        wid = self.window()
        geo = run(["xdotool", "getwindowgeometry", "--shell", wid], env={"DISPLAY": DISPLAY}).stdout
        vals = dict(line.split("=") for line in geo.split())
        self.x("mousemove", str(int(vals["X"]) + dx), str(int(vals["Y"]) + dy))
        self.x("click", "1")

    def screenshot(self, name):
        self.shot += 1
        path = f"{self.out}/{self.shot:02d}-{name}.png"
        run(["import", "-window", "root", path], env={"DISPLAY": DISPLAY})
        return path

    def stop(self):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
            subprocess.run(["pkill", "-u", USER])
            time.sleep(1)

    def path(self, *parts):
        return os.path.join(self.home, *parts)


def wait(fn, timeout, what):
    end = time.time() + timeout
    while time.time() < end:
        value = fn()
        if value:
            return value
        time.sleep(0.25)
    raise AssertionError("timed out waiting for " + what)


def check(name, ok, detail=""):
    RESULTS.append({"check": name, "ok": bool(ok), "detail": detail})
    print(("ok   " if ok else "FAIL ") + name + (f" ({detail})" if detail else ""), flush=True)


def session_dirs(app):
    root = app.path(".local", "share", "cage-tenant-desktop", "sessions")
    return sorted(os.listdir(root)) if os.path.isdir(root) else []


def main(out):
    os.makedirs(out, exist_ok=True)
    tmp = tempfile.mkdtemp()
    with open("/etc/hosts", "a") as f:
        f.write(f"127.0.0.1 cage.test cage2.test other.test badtls.test vm-{VM}.cage.test vm-{VM}.cage2.test\n")
    make_certs(tmp)
    start_server(tmp)
    # Any attempt to hand a URL to another program would land here.
    os.makedirs("/usr/local/bin", exist_ok=True)
    for tool in ("xdg-open", "gio", "x-www-browser", "sensible-browser", "firefox", "chromium"):
        with open(f"/usr/local/bin/{tool}", "w") as f:
            f.write("#!/bin/sh\necho \"$0 $*\" >> /tmp/external-open.log\n")
        os.chmod(f"/usr/local/bin/{tool}", 0o755)
    xvfb = subprocess.Popen(["Xvfb", DISPLAY, "-screen", "0", "1280x900x24", "-nolisten", "tcp"])
    time.sleep(2)
    app = App(out)
    try:
        # 1. First run: the mode choice, then the setup page and its Connect action.
        app.start()
        time.sleep(1)
        app.screenshot("start-page")
        check("launch shows the mode choice before any request", not events(host="cage.test"))
        app.keys("alt+r")
        time.sleep(0.5)
        app.screenshot("first-run-setup")
        app.type("http://cage.test")
        app.keys("Return")
        time.sleep(1)
        app.screenshot("invalid-address-error")
        check("an http:// address is refused without any request", not events(host="cage.test"))
        app.keys("ctrl+a")
        app.type("https://cage.test")
        app.keys("Return")
        wait(lambda: events(host="cage.test", event="portal-loaded"), 30, "portal")
        wait(lambda: events(host=f"vm-{VM}.cage.test", event="vm-loaded"), 30, "VM iframe")
        check("portal loads in the window", True)
        check("cross-origin VM iframe sets and sends its session cookie",
              events(host=f"vm-{VM}.cage.test", event="vm-loaded")[0]["vm_cookie"])
        import socket
        try:
            socket.create_connection(("127.0.0.1", 9222), timeout=1).close()
            inspector = True
        except OSError:
            inspector = False
        check("WEBKIT_INSPECTOR_SERVER from the environment is ignored", not inspector)
        wait(lambda: events(host="other.test", path="/inframe"), 20, "subframe navigation")
        check("a subframe may navigate to another origin (left to the portal's CSP)", True)
        app.screenshot("portal-with-vm-iframe")
        origin_file = app.path(".config", "cage-tenant-desktop", "portal-origin")
        check("only the origin is saved", os.listdir(os.path.dirname(origin_file)) == ["portal-origin"]
              and open(origin_file).read() == "https://cage.test\n", open(origin_file).read().strip())
        check("first load had no cookie", events(host="cage.test", path="/")[0]["cookie"] is False)

        # 2. Popups never open a window.
        before = len(app.windows())
        app.click(400, 150)
        app.x("mousemove", "1270", "890")
        wait(lambda: events(host="cage.test", event="popup-clicked"), 10, "popup click")
        time.sleep(2)
        check("window.open from a click opens no window", len(app.windows()) == before and not events(path="/popup"),
              f"windows {before}->{len(app.windows())}")
        app.screenshot("popup-blocked")

        # 3. The session persists across reloads; the main frame stays on the portal.
        app.keys("F5")
        wait(lambda: len(events(host="cage.test", path="/")) >= 2, 15, "reload")
        check("the portal cookie is kept by the session", events(host="cage.test", path="/")[-1]["cookie"] is True)

        # 4. Change server, then switch back: the old session is deleted.
        sessions_before = session_dirs(app)
        app.keys("ctrl+l")
        time.sleep(0.5)
        app.keys("ctrl+a")
        app.type("https://cage2.test")
        app.keys("Return")
        app.dialog_key("alt+s")
        wait(lambda: events(host="cage2.test", event="portal-loaded"), 30, "second portal")
        sessions_after = session_dirs(app)
        check("switching servers deletes the old session", len(sessions_before) == 1 and len(sessions_after) == 1
              and sessions_before != sessions_after, f"{sessions_before} -> {sessions_after}")
        app.screenshot("switched-server")
        app.keys("ctrl+l")
        time.sleep(0.5)
        app.keys("ctrl+a")
        app.type("https://cage.test")
        app.keys("Return")
        app.dialog_key("alt+s")
        wait(lambda: len(events(host="cage.test", event="portal-loaded")) >= 3, 30, "back to first portal")
        check("switching back starts without the old cookie", events(host="cage.test", path="/")[-1]["cookie"] is False)

        # 5. Clear this server's login data.
        app.keys("F5")
        wait(lambda: events(host="cage.test", path="/")[-1]["cookie"] is True, 15, "cookie after reload")
        app.keys("ctrl+shift+Delete")
        app.dialog_key("alt+c")
        wait(lambda: len(events(host="cage.test", event="portal-loaded")) >= 5, 30, "reload after clearing")
        check("clearing login data drops the cookie", events(host="cage.test", path="/")[-1]["cookie"] is False)

        # 6. Restart with the saved address: the session survives. Then the
        # page tries to send the main frame to another origin.
        loads_before = len(events(host="cage.test", path="/"))
        app.stop()
        LEAVE.set()
        app.start()
        time.sleep(2)
        check("restart waits for the mode choice", len(events(host="cage.test", path="/")) == loads_before)
        app.keys("alt+r")
        time.sleep(0.5)
        app.keys("Return")
        wait(lambda: events(host="cage.test", event="navigating-away"), 30, "navigation attempt")
        check("the saved session survives a restart", events(host="cage.test", path="/")[loads_before]["cookie"] is True)
        wait(lambda: events(host="cage.test", event="still-here"), 15, "portal still alive")
        check("main-frame navigation to another origin is not shown", True,
              "request reached other.test, response refused" if events(path="/away") else "no request reached other.test")
        app.screenshot("main-frame-navigation-blocked")
        LEAVE.clear()

        # 7. Clear the saved address from the setup page, then a bad certificate.
        app.keys("ctrl+l")
        time.sleep(0.5)
        app.keys("Tab", "Tab", "Tab", "Return")
        app.dialog_key("alt+c")
        time.sleep(1.5)
        check("clearing the address removes it and every session",
              not os.path.exists(origin_file) and session_dirs(app) == [], str(session_dirs(app)))
        app.screenshot("address-cleared")
        app.type("https://badtls.test")
        app.keys("Return")
        wait(lambda: events(host="badtls.test", tls="handshake"), 20, "TLS handshake")
        time.sleep(2)
        check("an untrusted certificate stops the load before any request", not events(host="badtls.test", path="/"))
        app.screenshot("tls-error")

        external = open("/tmp/external-open.log").read() if os.path.exists("/tmp/external-open.log") else ""
        check("no external browser or opener was started", external == "", external.strip())
    finally:
        app.stop()
        xvfb.terminate()
        with open(f"{out}/events.json", "w") as f:
            json.dump(EVENTS, f, indent=1)
        with open(f"{out}/results.json", "w") as f:
            json.dump(RESULTS, f, indent=1)
    failed = [r for r in RESULTS if not r["ok"]]
    print(f"GUI smoke: {len(RESULTS) - len(failed)} of {len(RESULTS)} checks passed")
    return 1 if failed else 0


LEAVE = threading.Event()

if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.stderr.write(__doc__)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
