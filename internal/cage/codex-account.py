#!/usr/bin/python3
"""Run the official Codex CLI with a private CODEX_HOME and export only its subscription status."""
import base64
from datetime import date, datetime, time as clock
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import runpy
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import time

try:
    import tomllib
except ImportError:  # Python before 3.11 cannot read config.toml; Codex still runs.
    tomllib = None

_controller_context = {**os.environ, **globals().get("_controller_context", {})}
runtime_source = Path(__file__).resolve()
runtime_path = runtime_source.with_name("client_runtime.py")
if not runtime_path.exists():
    runtime_path = runtime_source.parent.parent / "client_runtime.py"
_runtime = runpy.run_path(runtime_path, init_globals={"_controller_context": _controller_context})
PRIVATE_MODE = _runtime.get("PRIVATE_MODE", 0o700)

SHIM = "/usr/local/lib/cage-codex-bin"
DEFAULT = "default"
# Provider, credential and state-location variables select another login or
# another account's data. A managed account always uses its own CODEX_HOME.
PRIVATE_ENVIRONMENT = ("OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_ORG_ID",
                       "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID", "OPENAI_PROJECT", "CODEX_ACCESS_TOKEN",
                       "CODEX_SQLITE_HOME", "CODEX_HOME")
# Local instructions and helpers are shared. Credentials, conversations,
# history, logs, state databases, plugins and agent roles stay per account.
SHARED_RESOURCES = ("AGENTS.md", "AGENTS.override.md", "prompts", "rules")
# Top-level config.toml keys that only change local behavior. Anything else,
# including model_provider(s), profiles, base URLs, login and credential-store
# settings, stays in the account's private config.toml.
SHARED_CONFIG_KEYS = {"model", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity",
                      "model_supports_reasoning_summaries", "review_model", "approval_policy", "sandbox_mode",
                      "sandbox_workspace_write", "shell_environment_policy", "notify", "hide_agent_reasoning",
                      "show_raw_agent_reasoning", "file_opener", "features", "tools", "web_search",
                      "project_doc_max_bytes", "project_doc_fallback_filenames", "personality", "mcp_servers",
                      "notice", "check_for_update_on_startup", "disable_paste_burst", "projects",
                      "developer_instructions", "compact_prompt", "history", "tui", "skills"}
PRIVATE_CONFIG_WORDS = {"key", "keys", "token", "tokens", "secret", "secrets", "password", "auth", "oauth",
                        "credential", "credentials", "cookie", "cookies", "proxy", "url", "uri", "host", "endpoint",
                        "header", "headers", "provider", "providers", "account", "workspace", "login", "cert",
                        "certs", "ca", "bearer"}
PRIVATE_ENV_WORDS = {"KEY", "TOKEN", "SECRET", "PASSWORD", "AUTH", "OAUTH", "CREDENTIAL", "CREDENTIALS", "COOKIE",
                     "PROXY", "URL", "HOST", "ENDPOINT", "REGION", "PROJECT", "PROFILE", "ACCOUNT", "ORG",
                     "HELPER", "CERT", "CERTS", "CA", "BASE"}
PRIVATE_ENV_PREFIXES = ("OPENAI_", "CODEX_", "AZURE_", "ANTHROPIC_", "AWS_", "GOOGLE_", "GEMINI_")
WINDOWS = ("primary", "secondary")


class StatusError(ValueError):
    pass


class StatusTimeout(StatusError):
    pass


def profile(account_id):
    if account_id != DEFAULT and not re.fullmatch("[0-9a-f]{24}", account_id):
        raise ValueError("invalid account ID")
    return Path.home() / ".local/share/cage/codex-accounts" / account_id


def codex_home(base):
    if base.name == DEFAULT:
        configured = os.environ.get("CODEX_HOME")
        return Path(configured) if configured else Path.home() / ".codex"
    return base / "home"


def cli_environment(base):
    env = os.environ.copy()
    # Executing the real binary must never recurse into the terminal's codex shim.
    path = [p for p in env.get("PATH", os.defpath).split(os.pathsep) if p and p != SHIM]
    env["PATH"] = os.pathsep.join([str(Path.home() / ".local/bin"), *path])
    if base.name != DEFAULT:
        for name in PRIVATE_ENVIRONMENT:
            env.pop(name, None)
        env["CODEX_HOME"] = str(codex_home(base))
    return _runtime["environment"](env)


def login_mode(binary, env, cwd, timeout=10):
    """Return chatgpt, api_key, other or None from the CLI's own status report."""
    try:
        result = subprocess.run([binary, "login", "status"], cwd=cwd, env=env, timeout=timeout,
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except subprocess.TimeoutExpired as error:
        raise StatusTimeout("login status timed out") from error
    lines = [line.strip() for line in (result.stderr + result.stdout).decode(errors="replace").splitlines()]
    if result.returncode == 1 and "Not logged in" in lines:
        return None
    if result.returncode == 0:
        if "Logged in using ChatGPT" in lines:
            return "chatgpt"
        if any(line.startswith("API key configured") for line in lines):
            return "api_key"
        if any(line.startswith("Logged in using") for line in lines):
            return "other"
    # Any other failure must not sign a known user out.
    raise StatusError("unexpected login status")


def token_claims(home):
    """Read only identity claims from the ID token; tokens never leave this process."""
    try:
        data = json.loads((home / "auth.json").read_text())
    except FileNotFoundError:
        return None
    tokens = data.get("tokens") if isinstance(data, dict) else None
    token = tokens.get("id_token") if isinstance(tokens, dict) else None
    if not isinstance(token, str) or token.count(".") != 2:
        return None
    segment = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(segment + "=" * (-len(segment) % 4)))
    if not isinstance(claims, dict):
        return None
    auth = claims.get("https://api.openai.com/auth")
    auth = auth if isinstance(auth, dict) else {}
    profile_claims = claims.get("https://api.openai.com/profile")
    profile_claims = profile_claims if isinstance(profile_claims, dict) else {}
    email = claims.get("email") or profile_claims.get("email")
    organization = auth.get("chatgpt_account_id") or tokens.get("account_id") or ""
    plan = auth.get("chatgpt_plan_type")
    return {"email": email if isinstance(email, str) else None,
            "organization": organization if isinstance(organization, str) else "",
            "plan": plan if isinstance(plan, str) else ""}


def identity(binary, env, cwd, timeout=10):
    mode = login_mode(binary, env, cwd, timeout)
    if mode != "chatgpt":
        return {"logged_in": False, "auth_mode": mode or ""}
    claims = token_claims(Path(env.get("CODEX_HOME") or Path.home() / ".codex"))
    if claims is None:
        # A keyring-backed login has no auth.json. Ask the CLI for the account.
        with tempfile.TemporaryDirectory(prefix="codex-status-") as directory:
            with AppServer(binary, env, directory, time.monotonic() + timeout) as server:
                account = server.account()
        claims = {"email": account.get("email"), "organization": "", "plan": account.get("planType") or ""}
    if not claims.get("email"):
        return {"logged_in": False, "auth_mode": mode}
    return {"logged_in": True, "auth_mode": mode, "email": claims["email"], "organization": claims["organization"],
            "plan": claims["plan"]}


def subject(summary):
    if not summary.get("logged_in") or not summary.get("email"):
        return None
    email = summary["email"].strip().lower()
    organization = (summary.get("organization") or "").strip()
    return hashlib.sha256(f"{email}\n{organization}".encode()).hexdigest()


class AppServer:
    """Minimal client of the CLI's stdio app server for account status reads."""

    def __init__(self, binary, env, cwd, deadline):
        self.deadline, self.sequence, self.buffer, self.received = deadline, 0, b"", 0
        self.home = Path(env.get("CODEX_HOME") or Path.home() / ".codex")
        self.process = subprocess.Popen([binary, "app-server"], cwd=cwd, env=env, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)

    def __enter__(self):
        try:
            info = self.request("initialize", {"clientInfo": {"name": "workbench", "title": "Workbench", "version": "1"}})
            # Never attribute another directory's account to this profile.
            if not isinstance(info.get("codexHome"), str) or Path(info["codexHome"]).resolve() != self.home.resolve():
                raise StatusError("unexpected Codex home")
            self.send({"method": "initialized"})
        except BaseException:
            self.__exit__()
            raise
        return self

    def __exit__(self, *_):
        try:
            self.process.stdin.close()
        except OSError:
            pass
        try:
            self.process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.process.stdout.close()

    def send(self, message):
        self.process.stdin.write((json.dumps(message) + "\n").encode())
        self.process.stdin.flush()

    def request(self, method, params=None):
        self.sequence += 1
        message = {"method": method, "id": self.sequence}
        if params is not None:
            message["params"] = params
        self.send(message)
        while True:
            for line in self.lines():
                reply = json.loads(line)
                if isinstance(reply, dict) and reply.get("id") == self.sequence and "method" not in reply:
                    if "error" in reply or not isinstance(reply.get("result"), dict):
                        raise StatusError(method + " failed")
                    return reply["result"]

    def lines(self):
        while b"\n" not in self.buffer:
            left = self.deadline - time.monotonic()
            if left <= 0:
                raise StatusTimeout("app server timed out")
            ready, _, _ = select.select([self.process.stdout], [], [], left)
            if not ready:
                continue
            chunk = os.read(self.process.stdout.fileno(), 65536)
            self.received += len(chunk)
            if not chunk or self.received > (4 << 20):
                raise StatusError("app server closed")
            self.buffer += chunk
        lines, self.buffer = self.buffer.rsplit(b"\n", 1)
        return [line for line in lines.split(b"\n") if line.strip()]

    def account(self):
        account = self.request("account/read", {"refreshToken": False}).get("account")
        if not isinstance(account, dict) or account.get("type") != "chatgpt":
            return {}
        return account


def json_file(path):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return {}


def write_json(path, data):
    path.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        json.dump(data, stream, separators=(",", ":"))
    os.replace(stream.name, path)


def valid_window(data, now):
    if not isinstance(data, dict):
        return None
    used, reset, minutes = data.get("used_percentage"), data.get("resets_at"), data.get("window_minutes", 0)
    if type(used) not in (int, float) or not math.isfinite(used) or used < 0:
        return None
    if type(reset) != int or (reset != 0 and reset <= now):
        return None
    if type(minutes) != int or minutes < 0:
        minutes = 0
    # Codex reports usage above 100 once a window is exceeded.
    return {"used_percentage": min(used, 100), "resets_at": reset, "window_minutes": minutes}


def reported_window(used, reset, minutes, seen, now):
    window = valid_window({"used_percentage": used, "resets_at": 0 if reset is None else reset,
                           "window_minutes": 0 if minutes is None else minutes}, now)
    return {**window, "observed_at": seen} if window else None


def read_quota(base):
    try:
        stored = json_file(base / "quota.json")
    except (ValueError, OSError):
        return None
    if not isinstance(stored, dict):
        return None
    digest, seen, now = stored.get("identity", ""), stored.get("observed_at"), int(time.time())
    if not isinstance(digest, str) or not re.fullmatch("[a-f0-9]{64}", digest) or type(seen) != int or not 0 < seen <= now:
        return None
    result = {}
    for key in WINDOWS:
        window = valid_window(stored.get(key), now)
        window_seen = stored[key].get("observed_at", seen) if window else None
        if window and type(window_seen) == int and 0 < window_seen <= now:
            result[key] = {**window, "observed_at": window_seen}
    return {**result, "identity": digest, "observed_at": seen} if result else None


def read_identity(base):
    try:
        record = json_file(base / "identity.json")
    except (ValueError, OSError):
        return None
    if (isinstance(record, dict) and isinstance(record.get("identity"), str) and re.fullmatch("[a-f0-9]{64}", record["identity"])
            and type(record.get("since")) == int and record["since"] >= 0):
        return record
    return None


def note_identity(base, home, digest):
    """Bind later local rate-limit events to the identity that produced them."""
    record = read_identity(base)
    if record and record["identity"] == digest:
        return record
    try:
        # Events after the last credential write cannot belong to an earlier login.
        since = int((home / "auth.json").stat().st_mtime) if not record else int(time.time())
    except OSError:
        since = int(time.time())
    record = {"identity": digest, "since": since}
    write_json(base / "identity.json", record)
    if (read_quota(base) or {}).get("identity") not in (None, digest):
        (base / "quota.json").unlink(missing_ok=True)
    return record


def event_time(stamp):
    try:
        moment = datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        return None
    return int(moment.timestamp()) if moment.tzinfo else None


def rollout_snapshot(line, now):
    try:
        item = json.loads(line)
    except ValueError:
        return None
    payload = item.get("payload") if isinstance(item, dict) and item.get("type") == "event_msg" else None
    if not isinstance(payload, dict) or payload.get("type") != "token_count":
        return None
    limits = payload.get("rate_limits")
    seen = event_time(item.get("timestamp"))
    # A model-specific bucket never substitutes for the account's Codex limit.
    if not isinstance(limits, dict) or limits.get("limit_id") not in (None, "codex") or not seen or seen > now:
        return None
    result = {}
    for key in WINDOWS:
        window = limits.get(key)
        if isinstance(window, dict):
            reading = reported_window(window.get("used_percent"), window.get("resets_at"), window.get("window_minutes"), seen, now)
            if reading:
                result[key] = reading
    return {**result, "observed_at": seen} if result else None


def rollout_reading(home, since, now, files=8):
    """Read the newest rate-limit event recorded by recent local sessions."""
    def children(folder, pattern):
        try:
            return sorted((p for p in folder.iterdir() if p.is_dir() and not p.is_symlink() and re.fullmatch(pattern, p.name)), reverse=True)
        except OSError:
            return []
    candidates = []
    for year in children(home / "sessions", r"\d{4}"):
        for month in children(year, r"\d{2}"):
            for day in children(month, r"\d{2}"):
                candidates += [p for p in day.glob("rollout-*.jsonl") if p.is_file() and not p.is_symlink()]
                if len(candidates) >= files:
                    break
            if len(candidates) >= files:
                break
        if len(candidates) >= files:
            break
    def modified(path):
        try:
            return path.stat().st_mtime
        except OSError:
            return 0
    best = None
    for path in sorted(candidates, key=modified, reverse=True)[:files]:
        if modified(path) < since:
            continue
        try:
            with path.open("rb") as stream:
                stream.seek(max(0, stream.seek(0, os.SEEK_END) - (1 << 20)))
                tail = stream.read().decode(errors="replace").splitlines()
        except OSError:
            continue
        for line in reversed(tail):
            reading = rollout_snapshot(line, now)
            if reading:
                if reading["observed_at"] >= since and (best is None or reading["observed_at"] > best["observed_at"]):
                    best = reading
                break
    return best


def merged_quota(base, home, expected=None):
    record = read_identity(base)
    digest = expected or (record or {}).get("identity")
    if not digest:
        return None
    now = int(time.time())
    windows = {}
    stored = read_quota(base)
    if stored and stored["identity"] == digest:
        windows = {key: stored[key] for key in WINDOWS if key in stored}
    if record and record["identity"] == digest:
        live = rollout_reading(home, record["since"], now)
        for key in WINDOWS:
            if live and key in live and (key not in windows or live[key]["observed_at"] > windows[key]["observed_at"]):
                windows[key] = live[key]
    if not windows:
        return None
    return {**windows, "identity": digest, "observed_at": max(window["observed_at"] for window in windows.values())}


def app_windows(result, now):
    by_limit = result.get("rateLimitsByLimitId")
    snapshot = by_limit.get("codex") if isinstance(by_limit, dict) else None
    if not isinstance(snapshot, dict):
        snapshot = result.get("rateLimits")
        if not isinstance(snapshot, dict) or snapshot.get("limitId") not in (None, "codex"):
            return {}
    windows = {}
    for key in WINDOWS:
        window = snapshot.get(key)
        if isinstance(window, dict):
            reading = reported_window(window.get("usedPercent"), window.get("resetsAt"), window.get("windowDurationMins"), now, now)
            if reading:
                windows[key] = reading
    return windows


def changed_identity():
    return {"logged_in": False, "usage_status": "identity_changed", "usage_checked_at": int(time.time())}


def query_usage(binary, env, base, home, expected):
    if not expected or not re.fullmatch("[a-f0-9]{64}", expected):
        raise ValueError("missing expected account")
    deadline = time.monotonic() + 15
    summary = identity(binary, env, Path.home(), timeout=5)
    if subject(summary) != expected:
        return changed_identity()
    note_identity(base, home, expected)
    status, windows = "error", None
    with tempfile.TemporaryDirectory(prefix="codex-usage-") as directory:
        try:
            with AppServer(binary, env, directory, deadline) as server:
                account = server.account()
                if not account:
                    status = "not_applicable"
                elif (account.get("email") or "").strip().lower() != summary["email"].strip().lower():
                    return changed_identity()
                else:
                    windows = app_windows(server.request("account/rateLimits/read"), int(time.time()))
                    if account.get("planType"):
                        summary["plan"] = account["planType"]
        except (OSError, ValueError, TypeError, AttributeError, subprocess.SubprocessError):
            # The CLI can print account data on failure. Report a category only.
            windows = None
    after = identity(binary, env, Path.home(), timeout=max(1, min(5, deadline - time.monotonic())))
    if subject(after) != expected:
        return changed_identity()
    if windows:
        stored = read_quota(base) or {}
        record = {key: windows[key] for key in WINDOWS if key in windows}
        for key in WINDOWS:
            if key not in record and stored.get("identity") == expected and key in stored:
                record[key] = stored[key]
        write_json(base / "quota.json", {**record, "identity": expected, "observed_at": int(time.time())})
    if windows is not None and status == "error":
        status = {0: "unavailable", 1: "partial", 2: "reported"}[len(windows)]
    quota = merged_quota(base, home, expected)
    if quota:
        summary["quota"] = quota
        if status == "unavailable":
            status = "cached"
    summary.update(usage_status=status, usage_checked_at=int(time.time()))
    return summary


def private_config_name(name):
    return bool(PRIVATE_CONFIG_WORDS.intersection(re.split(r"[^a-z0-9]+", str(name).lower())))


def shared_env_variable(name):
    """A shared variable may change tool behavior but never routing or login."""
    if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
        return False
    upper = name.upper()
    return not upper.startswith(PRIVATE_ENV_PREFIXES) and not PRIVATE_ENV_WORDS.intersection(upper.split("_"))


def shared_value(value, name=""):
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            if name in ("env", "set"):
                if shared_env_variable(key) and not isinstance(item, (dict, list)):
                    result[key] = item
            elif name == "projects":
                # Project paths are local folders; only their settings are filtered.
                result[key] = shared_value(item)
            elif name == "mcp_servers":
                # A server with a credential, endpoint or header stays private as a whole.
                filtered = shared_value(item)
                if filtered == item:
                    result[key] = item
            elif not private_config_name(key):
                result[key] = shared_value(item, key)
        return result
    if isinstance(value, list):
        return [shared_value(item) for item in value]
    return value


def subtract(shared, layer, atomic=False):
    """Remove settings another config layer already defines."""
    result = {}
    for key, value in shared.items():
        if key not in layer:
            result[key] = value
        elif not atomic and isinstance(value, dict) and isinstance(layer[key], dict):
            rest = subtract(value, layer[key], key == "mcp_servers")
            if rest:
                result[key] = rest
    return result


def toml_value(value):
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        if math.isnan(value):
            return "nan"
        return ("inf" if value > 0 else "-inf") if math.isinf(value) else repr(value)
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False).replace("\x7f", "\\u007f")
    if isinstance(value, (datetime, date, clock)):
        return value.isoformat()
    if isinstance(value, list):
        return "[" + ", ".join(toml_value(item) for item in value) + "]"
    if isinstance(value, dict):
        return "{" + ", ".join(toml_value(str(key)) + " = " + toml_value(item) for key, item in value.items()) + "}"
    raise TypeError("unsupported TOML value")


def shared_overrides(home, cwd):
    """Apply shared local settings as CLI flags without changing layer precedence."""
    common = Path.home() / ".codex/config.toml"
    if tomllib is None or home.resolve() == common.parent.resolve():
        return []
    try:
        loaded = tomllib.loads(common.read_text())
    except FileNotFoundError:
        return []
    except (OSError, ValueError):
        print("~/.codex/config.toml 无法读取，本次未共用其中的设置。", file=sys.stderr)
        return []
    shared = {key: shared_value(value, key) for key, value in loaded.items() if key in SHARED_CONFIG_KEYS}
    layers = [home / "config.toml", *[folder / ".codex/config.toml" for folder in (cwd, *cwd.parents)]]
    for path in layers:
        if path.resolve() == common.resolve():
            continue
        try:
            shared = subtract(shared, tomllib.loads(path.read_text()))
        except FileNotFoundError:
            continue
        except (OSError, ValueError):
            # The CLI reports an unreadable layer itself; never override it blindly.
            return []
    return [argument for key, value in shared.items() for argument in ("-c", key + "=" + toml_value(value))]


def same_file(local, shared):
    try:
        return local.is_file() and shared.is_file() and local.read_bytes() == shared.read_bytes()
    except OSError:
        return False


def share_resources(base, home):
    """Link local customizations from ~/.codex; never move or overwrite account files."""
    common = Path.home() / ".codex"
    if home.resolve() == common.resolve():
        return []
    pairs = [(home / name, common / name) for name in SHARED_RESOURCES]
    skills = common / "skills"
    if skills.is_dir() and not (home / "skills").is_symlink():
        (home / "skills").mkdir(mode=PRIVATE_MODE, exist_ok=True)
        # Codex installs its bundled .system skills itself in every home.
        pairs += [(home / "skills" / entry.name, entry) for entry in sorted(skills.iterdir()) if entry.name != ".system"]
    kept = []
    with (base / "resources.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        for local, shared in pairs:
            if not shared.exists():
                continue
            if local.is_symlink():
                if local.resolve() != shared.resolve():
                    kept.append(local.name)
                continue
            if local.exists():
                if not same_file(local, shared):
                    kept.append(local.name)
                    continue
                local.unlink()
            local.symlink_to(shared, target_is_directory=shared.is_dir())
    return kept


def print_json(value):
    print(json.dumps(value, separators=(",", ":")))


def main(args):
    mode, account_id, *rest = args
    if mode == "quotas":
        result = {}
        for candidate in [account_id, *rest]:
            base = profile(candidate)
            quota = merged_quota(base, codex_home(base))
            if quota:
                result[candidate] = quota
        print_json(result)
        return 0
    base = profile(account_id)
    home = codex_home(base)
    if mode in ("snapshot", "usage") and not home.is_dir():
        print_json(changed_identity() if mode == "usage" else {"logged_in": False})
        return 0
    original_umask = os.umask(0o077)
    base.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    if account_id != DEFAULT:
        home.mkdir(mode=PRIVATE_MODE, exist_ok=True)
    env = cli_environment(base)
    binary = shutil.which("codex", path=env["PATH"])
    if not binary:
        raise FileNotFoundError("Codex is not installed")
    if mode == "snapshot":
        summary = identity(binary, env, Path.home())
        digest = subject(summary)
        if digest:
            note_identity(base, home, digest)
            quota = merged_quota(base, home, digest)
            if quota:
                summary["quota"] = quota
        print_json(summary)
        return 0
    if mode == "usage":
        if len(rest) != 1:
            raise ValueError("usage requires an expected account")
        print_json(query_usage(binary, env, base, home, rest[0]))
        return 0
    if mode == "login":
        if account_id == DEFAULT:
            raise ValueError("new logins require a private profile")
        def interrupted(_signum, _frame):
            raise KeyboardInterrupt()
        signal.signal(signal.SIGHUP, interrupted)
        signal.signal(signal.SIGTERM, interrupted)
        for name in ("quota.json", "identity.json"):
            (base / name).unlink(missing_ok=True)
        print("请在任意浏览器打开下方链接，登录 ChatGPT 后输入一次性验证码。完成后此终端会自动结束。", flush=True)
        return subprocess.call([binary, "login", "--device-auth"], cwd=Path.home(), env=env)
    if mode == "run":
        kept = share_resources(base, home) if account_id != DEFAULT else []
        if kept:
            print("Codex 账号目录中的 " + "、".join(sorted(set(kept))) + " 与 ~/.codex 不同，已保留账号自己的版本。", file=sys.stderr)
        expected = _controller_context.get("CAGE_CODEX_SUBJECT")
        reading = identity(binary, env, Path.cwd())
        digest = subject(reading)
        if not expected or digest != expected:
            print("此终端未登录所选 Codex 账号。请在 Codex 账号管理中检查登录状态或重新登录。", file=sys.stderr)
            return 1
        note_identity(base, home, digest)
        overrides = shared_overrides(home, Path.cwd()) if account_id != DEFAULT else []
        # Codex and its tools create files in the shared project with the user's umask.
        os.umask(original_umask)
        os.execve(binary, [binary, *overrides, *rest], env)
    raise ValueError("unknown operation")


def entrypoint(args):
    try:
        return main(args)
    except KeyboardInterrupt:
        return 130
    except StatusTimeout:
        print("读取 Codex 登录状态超时，请稍后重试；现有登录信息已保留。", file=sys.stderr)
    except StatusError:
        print("无法读取 Codex 登录状态，请确认 Codex CLI 可正常运行后重试；现有登录信息已保留。", file=sys.stderr)
    except _runtime["RuntimePathConflict"]:
        print("终端运行路径发生冲突，原数据已保留，请同步环境组件后重试。", file=sys.stderr)
    except FileNotFoundError:
        print("未找到 Codex CLI。请先在环境中安装 Codex（例如 ~/.local/bin/codex）后重试。", file=sys.stderr)
    except (OSError, ValueError, subprocess.SubprocessError, TypeError):
        # No raw CLI output, paths, credentials or device codes go to logs.
        print("准备 Codex 账号或启动 Codex CLI 失败，请检查目录权限并同步环境组件后重试；现有登录信息已保留。", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(entrypoint(sys.argv[1:]))
