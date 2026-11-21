"""Guest-side Codex CLI sessions for the phone: discovery, bounded transcripts and detached exec turns."""
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path.home() / ".local/share/cage/codex-sessions"
ACCOUNTS = Path.home() / ".local/share/cage/codex-accounts"
ACCOUNT_HELPER = Path("/usr/local/bin/cage-codex-account")
PROC = Path("/proc")
PROMPT_LIMIT = 32 << 10
REPLY_LIMIT = 128 << 10
HISTORY_LIMIT = 448 << 10
# Rollouts reach several MiB. Metadata and titles come from a bounded head and
# transcripts from a bounded tail; no operation reads a whole rollout.
HEAD_LIMIT = 256 << 10
TAIL_LIMIT = 4 << 20
LINE_LIMIT = 1 << 20
LISTED = 300
TOOL_LIMIT = 64
ACCOUNT_REFUSED = 78
UUID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
ROLLOUT = re.compile(r"rollout-.+-(" + UUID + r")\.jsonl")
# Newer CLIs hold a per-thread writer lock open while a session is open.
WRITER_LOCK = re.compile(r"(" + UUID + r")\.lock")
# Context some Codex versions record as user input. It is never a prompt.
INJECTED = re.compile(r"\s*(?:<(?:environment_context|user_instructions|turn_aborted|user_shell_command|subagent_notification|skill|INSTRUCTIONS)\b|# AGENTS\.md instructions for )")
TUI_OPEN = "这段会话正在电脑上的 Codex 终端中打开。请先退出该终端中的 Codex，再从手机继续。"
_ORIGINAL_UMASK = None


def identifier(value):
    return str(uuid.UUID(value))


def read_json(path, default=None):
    try:
        return json.loads(path.read_text())
    except (FileNotFoundError, ValueError):
        return default


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        json.dump(value, stream, ensure_ascii=False)
        temporary = stream.name
    os.replace(temporary, path)


@contextlib.contextmanager
def state_file(connection):
    folder = ROOT / identifier(connection)
    folder.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (folder / "lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        path = folder / "state.json"
        state = read_json(path, {})
        before = json.dumps(state, sort_keys=True)
        yield state
        if json.dumps(state, sort_keys=True) != before:
            save(path, state)


def codex_home(account):
    if account == "default":
        configured = os.environ.get("CODEX_HOME")
        return Path(configured) if configured else Path.home() / ".codex"
    if not isinstance(account, str) or not re.fullmatch(r"[0-9a-f]{24}", account):
        raise ValueError("Codex 账号 ID 无效")
    return ACCOUNTS / account / "home"


def clip(text, limit):
    return text.encode("utf-8")[:limit].decode("utf-8", errors="ignore")


def within(path, root):
    return isinstance(path, str) and (path == root or path.startswith(root.rstrip("/") + "/"))


def fingerprint(pid):
    """Start time of a live process, so a reused PID never matches a recorded run."""
    if not isinstance(pid, int) or pid <= 0:
        return ""
    stat = PROC / str(pid) / "stat"
    if (PROC / "self").exists() or stat.exists():
        try:
            fields = stat.read_text().rsplit(")", 1)[1].split()
            return "" if fields[0] == "Z" else fields[19]
        except (OSError, IndexError):
            return ""
    try:
        listing = subprocess.run(["ps", "-o", "stat=,lstart=", "-p", str(pid)], capture_output=True, text=True, timeout=2).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return ""
    return "" if not listing or listing.startswith("Z") else listing.split(None, 1)[-1]


def alive(run):
    return isinstance(run, dict) and bool(run.get("start")) and fingerprint(run.get("pid")) == run["start"]


def rollout_users():
    """Processes holding each rollout open, and processes holding or naming a thread."""
    files, threads = {}, {}
    try:
        entries = [entry for entry in PROC.iterdir() if entry.name.isdigit()]
    except OSError:
        return files, threads
    for entry in entries:
        pid = int(entry.name)
        try:
            words = [os.fsdecode(word) for word in (entry / "cmdline").read_bytes().split(b"\0") if word]
        except OSError:
            continue
        if any("codex" in Path(word).name for word in words[:3]):
            for word in words:
                if re.fullmatch(UUID, word):
                    threads.setdefault(word, set()).add(pid)
        try:
            descriptors = list((entry / "fd").iterdir())
        except OSError:
            continue
        for descriptor in descriptors:
            try:
                target = os.readlink(descriptor)
            except OSError:
                continue
            name = Path(target).name
            lock = WRITER_LOCK.fullmatch(name) if Path(target).parent.name == "thread-writer-locks" else None
            if lock:
                threads.setdefault(lock.group(1), set()).add(pid)
            elif target.endswith(".jsonl") and ROLLOUT.fullmatch(name):
                files.setdefault(target, set()).add(pid)
    return files, threads


def foreign(path, thread, users, groups):
    """Users of a rollout other than this helper and the exec runs it started."""
    files, threads = users
    found = set(files.get(os.path.realpath(path), ())) | set(threads.get(thread, ()))
    result = set()
    for pid in found:
        try:
            group = os.getpgid(pid)
        except OSError:
            group = None
        if pid != os.getpid() and group not in groups:
            result.add(pid)
    return result


def rollouts(home):
    """Rollout files of one Codex home, newest first. Symlinks are never followed."""
    def children(folder, pattern):
        try:
            with os.scandir(folder) as entries:
                return [Path(entry.path) for entry in entries if entry.is_dir(follow_symlinks=False) and re.fullmatch(pattern, entry.name)]
        except OSError:
            return []
    found = []
    for year in children(home / "sessions", r"\d{4}"):
        for month in children(year, r"\d{2}"):
            for day in children(month, r"\d{2}"):
                try:
                    with os.scandir(day) as entries:
                        for entry in entries:
                            match = ROLLOUT.fullmatch(entry.name)
                            if match and entry.is_file(follow_symlinks=False):
                                found.append((entry.stat(follow_symlinks=False).st_mtime, str(entry.path), match.group(1)))
                except OSError:
                    continue
    found.sort(reverse=True)
    return [(mtime, Path(path), thread) for mtime, path, thread in found]


def find_rollout(home, thread):
    paths = [path for path in (home / "sessions").glob("[0-9]*/[0-9]*/[0-9]*/rollout-*-" + thread + ".jsonl") if path.is_file() and not path.is_symlink()]
    return max(paths, key=lambda path: path.stat().st_mtime, default=None)


def texts(content, kinds):
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    return "\n".join(item["text"] for item in content if isinstance(item, dict) and item.get("type") in kinds and isinstance(item.get("text"), str))


def user_prompt(row):
    """(source, text) of a real user input record, or None."""
    kind, payload = row.get("type"), row.get("payload")
    if not isinstance(payload, dict):
        return None
    text, images = None, False
    if kind == "event_msg" and payload.get("type") == "user_message" and payload.get("kind") in (None, "plain"):
        text = payload.get("message") if isinstance(payload.get("message"), str) else ""
        images = bool(payload.get("images") or payload.get("local_images"))
        source = "event"
    elif kind == "event_msg" and payload.get("type") == "item_completed" and isinstance(payload.get("item"), dict) and payload["item"].get("type") == "UserMessage":
        content = payload["item"].get("content")
        text = texts(content, ("text", "input_text", "Text"))
        images = isinstance(content, list) and any(isinstance(item, dict) and "image" in str(item.get("type", "")).lower() for item in content)
        source = "item"
    elif kind == "response_item" and payload.get("type") == "message" and payload.get("role") == "user":
        text = texts(payload.get("content"), ("input_text", "text"))
        source = "response"
    else:
        return None
    if text and INJECTED.match(text):
        return None
    if not (text or "").strip():
        return (source, "[图片]") if images else None
    return source, text


def preview(value):
    if isinstance(value, list):
        value = " ".join(str(item) for item in value if isinstance(item, (str, int, float)))
    if not isinstance(value, str):
        return ""
    return next((line.strip() for line in value.splitlines() if line.strip()), "")[:160]


def argument_preview(arguments):
    try:
        value = json.loads(arguments) if isinstance(arguments, str) else arguments
    except ValueError:
        return preview(arguments)
    if isinstance(value, dict):
        for field in ("cmd", "command", "path", "file_path", "query", "pattern", "url", "message", "description", "prompt"):
            text = preview(value.get(field))
            if text:
                return text
        return ""
    return preview(value)


def input_preview(text):
    if not isinstance(text, str):
        return ""
    changed = re.search(r"^\*\*\* (?:Add|Update|Delete) File: (.+)$", text, re.M)
    return changed.group(1).strip()[:160] if changed else preview(text)


def tool_call(payload, position):
    """Display summary of a model tool call: name, short input preview and status."""
    kind, status = payload.get("type"), "called"
    if kind == "function_call":
        name, namespace = payload.get("name"), payload.get("namespace")
        if isinstance(name, str) and isinstance(namespace, str) and namespace:
            name = namespace + "." + name
        detail = argument_preview(payload.get("arguments"))
    elif kind == "custom_tool_call":
        name, detail = payload.get("name"), input_preview(payload.get("input"))
    elif kind == "local_shell_call":
        action = payload.get("action") if isinstance(payload.get("action"), dict) else {}
        name, detail = "shell", preview(action.get("command"))
    elif kind == "web_search_call":
        action = payload.get("action") if isinstance(payload.get("action"), dict) else {}
        name, detail, status = "web_search", preview(action.get("query") or action.get("url")), "completed"
    else:
        return None
    key = payload.get("call_id") or payload.get("id") or "call-" + str(position)
    if not isinstance(key, str) or not 0 < len(key.encode()) <= 128:
        return None
    if not isinstance(name, str) or not re.fullmatch(r"[a-zA-Z0-9_.:-]{1,128}", name):
        return None
    tool = {"id": key, "name": name, "status": status}
    if detail:
        tool["detail"] = detail
    return tool


def tool_output(payload):
    if payload.get("type") not in ("function_call_output", "custom_tool_call_output", "local_shell_call_output"):
        return None
    output = payload.get("output")
    failed = isinstance(output, dict) and output.get("success") is False
    return payload.get("call_id"), "failed" if failed else "completed"


def turn_id(thread, position):
    # Stable across reads of the append-only rollout, and shaped like the
    # phone's request IDs so stop requests reach the guest for a clear answer.
    return str(uuid.UUID(bytes=hashlib.sha256((thread + ":" + str(position)).encode()).digest()[:16], version=4))


def prompt_key(text):
    return hashlib.sha256(text.strip().encode()).hexdigest()


def session_meta(line):
    try:
        row = json.loads(line)
    except ValueError:
        return None
    payload = row.get("payload") if isinstance(row, dict) and row.get("type") == "session_meta" else None
    if not isinstance(payload, dict) or not isinstance(payload.get("cwd"), str):
        return None
    try:
        thread = identifier(payload.get("id"))
    except (TypeError, ValueError, AttributeError):
        return None
    source = payload.get("source")
    # Sub-agent threads belong to their parent session and are never listed.
    subagent = payload.get("thread_source") == "subagent" or isinstance(source, dict) and "subagent" in source
    return {"id": thread, "cwd": payload["cwd"], "subagent": bool(subagent), "title": ""}


def head(path):
    """Metadata and the first prompt from a bounded head of a rollout."""
    with path.open("rb") as stream:
        first = stream.readline(LINE_LIMIT)
        meta = session_meta(first)
        if meta is None:
            return None
        budget = HEAD_LIMIT
        while budget > 0:
            line = stream.readline(min(LINE_LIMIT, budget))
            if not line:
                break
            budget -= len(line)
            try:
                row = json.loads(line)
            except ValueError:
                continue
            prompt = user_prompt(row) if isinstance(row, dict) else None
            if prompt:
                meta["title"] = " ".join(prompt[1].split())[:40]
                break
    return meta


def transcript(path, thread):
    """Turns from a bounded tail of a rollout, with the byte offset of each prompt."""
    turns, cwd, pending, truncated = [], "", False, False
    if path is None:
        return turns, cwd, pending, truncated
    with path.open("rb") as stream:
        size = stream.seek(0, os.SEEK_END)
        start = max(0, size - TAIL_LIMIT)
        stream.seek(start)
        data = stream.read(size - start)
    if start:
        # The window starts inside a line; earlier turns are not shown.
        cut = data.find(b"\n") + 1
        data, start, truncated = (data[cut:] if cut else b""), start + cut, True
    offset = start
    for raw in data.split(b"\n"):
        position = offset
        offset += len(raw) + 1
        if not raw.strip() or len(raw) > LINE_LIMIT:
            continue
        try:
            row = json.loads(raw)
        except ValueError:
            continue
        payload = row.get("payload") if isinstance(row, dict) else None
        if not isinstance(payload, dict):
            continue
        kind = row.get("type")
        if kind in ("turn_context", "session_meta") and isinstance(payload.get("cwd"), str):
            cwd = payload["cwd"]
        if kind == "event_msg":
            event = payload.get("type")
            if event == "task_started":
                pending = True
            elif event in ("task_complete", "turn_aborted"):
                pending = False
                if turns:
                    turn = turns[-1]
                    turn["_done"] = True
                    if event == "turn_aborted":
                        turn["status"] = "stopped"
                    else:
                        turn["status"] = "completed"
                        turn.pop("error", None)
                        message = payload.get("last_agent_message")
                        if not turn["text"] and isinstance(message, str):
                            turn["text"], turn["truncated"] = clip(message, REPLY_LIMIT), len(message.encode()) > REPLY_LIMIT
            elif event == "error" and turns and isinstance(payload.get("message"), str):
                turns[-1]["status"], turns[-1]["error"] = "failed", clip(payload["message"], 1000)
        prompt = user_prompt(row)
        if prompt:
            source, text = prompt
            last = turns[-1] if turns else None
            # Codex records one input as a response item and as an event.
            if last and source not in last["_sources"] and last["_key"] == prompt_key(text) and not last["text"] and not last.get("tools"):
                last["_sources"].add(source)
                continue
            turns.append({"id": turn_id(thread, position), "prompt": clip(text, PROMPT_LIMIT), "text": "", "status": "completed",
                          "_offset": position, "_sources": {source}, "_key": prompt_key(text)})
            if len(text.encode()) > PROMPT_LIMIT:
                turns[-1]["prompt_truncated"] = True
            del turns[:-256]
            continue
        if not turns or kind != "response_item":
            continue
        turn = turns[-1]
        if payload.get("type") == "message" and payload.get("role") == "assistant":
            text = texts(payload.get("content"), ("output_text", "text"))
            if text:
                value = turn["text"] + ("\n\n" if turn["text"] else "") + text
                turn["text"], turn["truncated"] = clip(value, REPLY_LIMIT), len(value.encode()) > REPLY_LIMIT
            continue
        tools = turn.setdefault("tools", [])
        output = tool_output(payload)
        if output:
            for tool in tools:
                if tool["id"] == output[0]:
                    tool["status"] = output[1]
        else:
            tool = tool_call(payload, position)
            if tool and len(tools) < TOOL_LIMIT and all(item["id"] != tool["id"] for item in tools):
                tools.append(tool)
        if not tools:
            turn.pop("tools")
    return turns, cwd, pending, truncated


def tail_pending(path):
    """Whether the newest task in a rollout is still unfinished."""
    with path.open("rb") as stream:
        size = stream.seek(0, os.SEEK_END)
        stream.seek(max(0, size - (256 << 10)))
        lines = stream.read().splitlines()
    for line in reversed(lines):
        if b'"event_msg"' not in line:
            continue
        try:
            payload = json.loads(line).get("payload")
        except (ValueError, AttributeError):
            continue
        if isinstance(payload, dict) and payload.get("type") in ("task_started", "task_complete", "turn_aborted"):
            return payload["type"] == "task_started"
    return False


def started_thread(folder):
    """The thread a new exec run announced in its JSON output."""
    try:
        with (folder / "exec.log").open("rb") as stream:
            lines = stream.read(64 << 10).splitlines()
    except OSError:
        return ""
    for line in lines:
        try:
            event = json.loads(line)
            if event.get("type") == "thread.started":
                return identifier(event["thread_id"])
            message = event.get("msg") or {}
            if message.get("type") == "session_configured":
                return identifier(message["session_id"])
        except (ValueError, TypeError, KeyError, AttributeError):
            continue
    return ""


def run_failure(folder, run):
    """A short reason for an exec run that ended without completing its turn."""
    code = run.get("exit_code")
    if code == ACCOUNT_REFUSED:
        return "所选 Codex 账号未登录或已变化。请在 Codex 账号管理中检查登录状态后重试。"
    message = ""
    try:
        with (folder / "exec.log").open("rb") as stream:
            size = stream.seek(0, os.SEEK_END)
            stream.seek(max(0, size - (64 << 10)))
            lines = stream.read().splitlines()
    except OSError:
        lines = []
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        error = event.get("error") if event.get("type") == "turn.failed" else event if event.get("type") == "error" else None
        if isinstance(error, dict) and isinstance(error.get("message"), str):
            message = error["message"]
    if message:
        return clip(message.strip(), 500)
    return "Codex 未能完成本轮（退出码 " + str(code) + "）。请检查电脑上的 Codex 登录和网络后重试。"


def state_view(connection):
    if not connection or not (ROOT / connection).is_dir():
        return contextlib.nullcontext({})
    return state_file(connection)


def view(request):
    account = request.get("account") or "default"
    home = codex_home(account)
    thread = identifier(request["thread"]) if request.get("thread") else ""
    connection = identifier(request["connection"]) if request.get("connection") else ""
    with state_view(connection) as state:
        if state and not state.get("thread_id"):
            state["thread_id"] = started_thread(ROOT / connection)
        if state.get("thread_id"):
            if thread and thread != state["thread_id"]:
                raise ValueError("会话身份已变化，请重新读取会话列表")
            thread = state["thread_id"]
        if state and state.get("account", account) != account:
            raise ValueError("会话账号已变化，请重新读取会话列表")
        path = find_rollout(home, thread) if thread else None
        meta = head(path) if path else None
        turns, cwd, pending, truncated = transcript(path, thread)
        run = state.get("run") or {}
        ours = alive(run)
        groups = {run["pid"]} if ours else set()
        tui = foreign(path, thread, rollout_users(), groups) if path else set()
        claimed = set()
        for sent in state.get("sent", []):
            match = next((turn for turn in turns if id(turn) not in claimed and turn["_offset"] >= sent["offset"] and turn["_key"] == sent["key"]), None)
            if match:
                claimed.add(id(match))
                match["id"] = sent["id"]
                if sent.pop("text", None) is not None:
                    sent["delivered"] = True
            elif not sent.get("delivered"):
                status = "running" if ours and run.get("request_id") == sent["id"] else "interrupted"
                turns.append({"id": sent["id"], "prompt": sent.get("text", ""), "text": "", "status": status, "_offset": sent["offset"]})
        if turns:
            last = turns[-1]
            mine = last["id"] == run.get("request_id")
            if ours and (pending or mine and not last.get("_done")) or tui and pending:
                last["status"] = "running"
            elif pending and last["status"] == "completed" and not last.get("_done"):
                last["status"] = "interrupted"
            if not ours and mine and not last.get("_done"):
                if state.get("stopped") == last["id"]:
                    last["status"] = "stopped"
                elif run.get("exit_code") not in (None, 0):
                    last["status"], last["error"] = "failed", run_failure(ROOT / connection, run)
        notice = ""
        if tui:
            notice = TUI_OPEN
        elif thread and path is None:
            notice = "未找到这段 Codex 会话的历史，可能已在电脑上删除。"
        for turn in turns:
            for key in ("_offset", "_sources", "_key", "_done"):
                turn.pop(key, None)
        shortened = truncated or any(turn.pop("prompt_truncated", False) for turn in turns)
        while len(turns) > 1 and len(json.dumps(turns, ensure_ascii=False).encode()) > HISTORY_LIMIT:
            turns.pop(0)
            shortened = True
        updated = max(path.stat().st_mtime if path else 0, float(run.get("finished_at") or run.get("started_at") or 0))
        return {"thread_id": thread, "account": account, "connection": connection,
                "workspace": cwd or (meta or {}).get("cwd") or state.get("cwd", ""),
                "title": (meta or {}).get("title") or (turns[0]["prompt"].strip()[:40] if turns else ""),
                "updated_at": updated, "turns": turns, "running": bool(ours or tui), "active": bool(ours or tui and pending),
                "can_send": not ours and not tui and not (thread and path is None), "history_truncated": shortened,
                "connection_notice": notice, "account_refused": not ours and run.get("exit_code") == ACCOUNT_REFUSED}


def cached_head(path, index, fresh):
    key, size = str(path), path.stat().st_size
    entry = index.get(key)
    # A rollout only grows. Re-read the head only while it lacks a title.
    if not isinstance(entry, dict) or (entry.get("skip") or not entry.get("title")) and entry.get("size") != size:
        meta = head(path)
        entry = {"size": size, **(meta or {"skip": True})}
    fresh[key] = entry
    return None if entry.get("skip") else entry


def discover(request):
    roots = [root for root in request.get("workspaces", []) if isinstance(root, str) and root.startswith("/")]
    accounts = [account for account in request.get("accounts", []) if isinstance(account, str)]
    states = {}
    for path in ROOT.glob("*/state.json"):
        try:
            states[identifier(path.parent.name)] = read_json(path, {}) or {}
        except ValueError:
            continue
    groups = {state["run"]["pid"] for state in states.values() if alive(state.get("run"))}
    users = rollout_users()
    index = read_json(ROOT / "index.json", {}) or {}
    fresh, sessions, listed = {}, [], set()
    for account in accounts:
        try:
            home = codex_home(account)
        except ValueError:
            continue
        for mtime, path, _ in rollouts(home)[:LISTED]:
            try:
                meta = cached_head(path, index, fresh)
            except OSError:
                continue
            if not meta or meta["subagent"] or not any(within(meta["cwd"], root) for root in roots):
                continue
            thread = meta["id"]
            connection, state = next(((key, value) for key, value in states.items()
                                      if value.get("thread_id") == thread and value.get("account", "default") == account), ("", {}))
            run = state.get("run") or {}
            ours = connection and alive(run)
            tui = foreign(path, thread, users, groups)
            sessions.append({"thread_id": thread, "account": account, "connection": connection, "workspace": meta["cwd"],
                             "title": meta.get("title") or "", "updated_at": max(mtime, float(run.get("started_at") or 0)),
                             "running": bool(ours or tui), "active": bool(ours or tui and tail_pending(path))})
            listed.add(connection)
    # A new session's first turn can run before its rollout is listed.
    for connection, state in states.items():
        run = state.get("run") or {}
        if connection in listed or state.get("account", "default") not in accounts or not alive(run):
            continue
        if not any(within(state.get("cwd", ""), root) for root in roots):
            continue
        sessions.append({"thread_id": state.get("thread_id") or started_thread(ROOT / connection), "account": state.get("account", "default"),
                         "connection": connection, "workspace": state.get("cwd", ""), "title": "",
                         "updated_at": float(run.get("started_at") or 0), "running": True, "active": True})
    if fresh != index:
        save(ROOT / "index.json", fresh)
    return sessions


def launch(folder, connection, text, cwd, env):
    with tempfile.TemporaryFile(dir=folder) as prompt, (folder / "exec.log").open("wb") as log:
        prompt.write(text.encode())
        prompt.flush()
        prompt.seek(0)
        # The supervisor leads its own session and process group, outlives this
        # SSH request and records Codex's exit status.
        process = subprocess.Popen([sys.executable, "-I", str(Path(__file__).resolve()), "supervise", connection],
                                   stdin=prompt, stdout=log, stderr=subprocess.STDOUT, cwd=cwd, env=env,
                                   start_new_session=True, umask=_ORIGINAL_UMASK if _ORIGINAL_UMASK is not None else -1)
    return process.pid


def send(request):
    connection = identifier(request["connection"])
    request_id = identifier(request["request_id"])
    text = request["text"]
    if not isinstance(text, str) or not text.strip() or len(text.encode()) > PROMPT_LIMIT:
        raise ValueError("消息为空或过长")
    account = request.get("account") or "default"
    home = codex_home(account)
    thread = identifier(request["thread"]) if request.get("thread") else ""
    cwd = Path(request["cwd"])
    folder = ROOT / connection
    with state_file(connection) as state:
        known = state.get("thread_id") or started_thread(folder)
        if known and thread and known != thread:
            raise ValueError("会话身份已变化，请重新读取会话列表")
        thread = thread or known
        if state.get("account", account) != account:
            raise ValueError("会话账号已变化，请重新读取会话列表")
        digest = hashlib.sha256(text.encode()).hexdigest()
        requests = state.setdefault("requests", {})
        if request_id in requests:
            if requests[request_id] != digest:
                raise ValueError("同一发送编号已用于另一条消息")
            return {"ok": True}
        previous = state.get("run") or {}
        if alive(previous):
            raise ValueError("这段会话已有消息正在处理，请等待完成后再发送")
        path = None
        if thread:
            path = find_rollout(home, thread)
            if path is None:
                raise ValueError("未找到这段 Codex 会话的历史，请重新读取会话列表")
            if foreign(path, thread, rollout_users(), set()):
                raise ValueError(TUI_OPEN)
        if not cwd.is_absolute() or not cwd.is_dir():
            raise ValueError("会话目录不可用，请确认项目文件夹已连接")
        args = ["exec", "--json", "--skip-git-repo-check", "-C", str(cwd)]
        for directory in request.get("additional_directories") or []:
            if not isinstance(directory, str) or not Path(directory).is_absolute() or not Path(directory).is_dir():
                raise ValueError("项目文件夹未连接，请重新打开项目")
            args += ["--add-dir", directory]
        args += ["resume", thread, "-"] if thread else ["-"]
        env = {key: value for key, value in os.environ.items() if not key.startswith("CAGE_")}
        env["PATH"] = os.pathsep.join([str(Path.home() / ".local/bin"), env.get("PATH", os.defpath)])
        if account == "default":
            binary = shutil.which("codex", path=env["PATH"])
            if not binary:
                raise ValueError("未找到 Codex CLI。请先在环境中安装 Codex 后重试。")
            argv = [binary, *args]
        else:
            # The account helper verifies the signed-in identity and runs the
            # official CLI with the account's own CODEX_HOME.
            subject = request.get("subject") or ""
            if not re.fullmatch(r"[0-9a-f]{64}", subject):
                raise ValueError("Codex 账号状态已变化，请重试")
            if not ACCOUNT_HELPER.is_file():
                raise ValueError("Super Cage 环境组件需要更新，请在电脑更新环境组件后重试")
            argv = [str(ACCOUNT_HELPER), "run", account, *args]
            env["CAGE_CODEX_SUBJECT"] = subject
        backup = json.loads(json.dumps(state))
        state.update(thread_id=thread, account=account, cwd=str(cwd))
        requests[request_id] = digest
        for stale in list(requests)[:-256]:
            requests.pop(stale)
        sent = state.setdefault("sent", [])
        sent.append({"id": request_id, "text": text, "key": prompt_key(text), "offset": path.stat().st_size if path else 0})
        del sent[:-64]
        state["run"] = {"request_id": request_id, "argv": argv, "started_at": time.time()}
        state.pop("stopped", None)
        # Admission is durable before Codex can read the prompt, so a lost
        # acknowledgement never starts the same request twice.
        save(folder / "state.json", state)
        try:
            pid = launch(folder, connection, text, cwd, env)
        except OSError:
            state.clear()
            state.update(backup)
            save(folder / "state.json", state)
            raise ValueError("无法启动 Codex，请检查环境后重试")
        state["run"].update(pid=pid, start=fingerprint(pid))
    return {"ok": True}


def supervise(connection):
    for name in ("SIGINT", "SIGTERM", "SIGHUP"):
        # Stop signals the whole group. Outlive Codex to record its exit.
        signal.signal(getattr(signal, name), lambda *_: None)
    with state_file(connection) as state:
        run = dict(state.get("run") or {})
    if not run.get("argv"):
        return 1
    try:
        code = subprocess.call(run["argv"])
    except OSError:
        code = 127
    thread = started_thread(ROOT / identifier(connection))
    with state_file(connection) as state:
        current = state.get("run") or {}
        if current.get("request_id") == run.get("request_id"):
            current.update(exit_code=code, finished_at=time.time())
        if thread and not state.get("thread_id"):
            state["thread_id"] = thread
    return 0


def stop(request):
    connection = identifier(request["connection"])
    request_id = identifier(request["request_id"])
    if not (ROOT / connection).is_dir():
        raise ValueError("这次运行不是从手机发起的，请在电脑终端停止")
    with state_file(connection) as state:
        run = state.get("run") or {}
        if run.get("request_id") != request_id or not alive(run):
            raise ValueError("这次运行已结束或不是从手机发起的，请重新读取会话")
        state["stopped"] = request_id
    # Signal outside the lock: the supervisor needs it to record the exit.
    for number, wait in ((signal.SIGINT, 5), (signal.SIGTERM, 2), (signal.SIGKILL, 1)):
        try:
            os.killpg(run["pid"], number)
        except ProcessLookupError:
            break
        deadline = time.monotonic() + wait
        while time.monotonic() < deadline and alive(run):
            time.sleep(0.05)
        if not alive(run):
            break
    return {"ok": True}


def purge(request):
    account = request.get("account") or "default"
    home = codex_home(account)
    thread = identifier(request["thread"]) if request.get("thread") else ""
    connection = identifier(request["connection"]) if request.get("connection") else ""
    state = read_json(ROOT / connection / "state.json", {}) if connection else {}
    if alive((state or {}).get("run")):
        raise ValueError("Codex 仍在处理这段会话，请先停止本轮再删除")
    thread = thread or (state or {}).get("thread_id", "")
    path = find_rollout(home, thread) if thread else None
    if path is not None:
        meta = head(path)
        if not meta or meta["id"] != thread or not within(meta["cwd"], request["workspace"]):
            raise ValueError("会话历史与已授权项目不一致，请检查电脑上的 Codex 配置")
        if foreign(path, thread, rollout_users(), set()):
            raise ValueError("这段会话正在电脑上的 Codex 终端中打开，请先退出后再删除")
        path.unlink()
    if connection:
        shutil.rmtree(ROOT / connection, ignore_errors=True)
    return {"ok": True}


def main():
    global _ORIGINAL_UMASK
    _ORIGINAL_UMASK = os.umask(0o077)
    mode, argument = sys.argv[1:3]
    if mode == "supervise":
        os.umask(_ORIGINAL_UMASK)
        return supervise(argument)
    request = json.load(sys.stdin) if argument == "-" else json.loads(argument)
    operations = {"discover": discover, "view": view, "send": send, "stop": stop, "purge": purge}
    if mode not in operations:
        raise ValueError("unsupported operation")
    try:
        result = operations[mode](request)
    except ValueError as error:
        result = {"error": str(error)}
    except (OSError, KeyError, TypeError, subprocess.SubprocessError):
        result = {"error": "电脑未能处理 Codex 会话请求，请稍后重试。"}
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
