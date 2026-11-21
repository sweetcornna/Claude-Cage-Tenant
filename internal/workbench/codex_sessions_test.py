import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
import warnings
from unittest.mock import patch

sys.dont_write_bytecode = True
source = Path(__file__).with_name("codex_sessions.py").resolve()
spec = importlib.util.spec_from_file_location("codex_sessions", source)
sessions = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sessions)

# A stand-in for the Codex CLI. It records its invocation and appends the
# records a real exec turn writes to its rollout. All content is synthetic.
FAKE_CODEX = """#!{python}
import json, os, pathlib, sys, time, uuid
args = sys.argv[1:]
home = pathlib.Path(os.environ.get("CODEX_HOME") or pathlib.Path.home() / ".codex")
flags = os.environ.get("FAKE_CODEX_FLAGS", "all") == "all"
if args == ["--version"]:
    print("codex-cli 0.171.0")
    sys.exit(0)
if args[-1:] == ["--help"]:
    with open(os.environ["FAKE_CODEX_CALLS"] + ".help", "a") as calls:
        calls.write(" ".join(args) + "\\n")
    if args == ["--help"]:
        print("Codex CLI\\n\\nCommands:\\n  exec    Run Codex non-interactively\\n" + ("  resume  Resume a previous session (picker by default; use --last to continue\\n          the most recent)\\n  delete  Permanently delete a saved session\\n" if flags else "") + "  help    Print this message\\n\\nOptions:\\n" + ("      --no-daemon\\n          Run without the shared background server\\n" if flags else ""))
    elif args[0] == "resume":
        print("Usage: codex resume [OPTIONS] [SESSION_ID]\\n" + ("      --no-daemon\\n" if flags else ""))
    elif args[0] == "delete":
        print("Usage: codex delete [OPTIONS] <SESSION>\\n" + ("      --force\\n" if flags else ""))
    sys.exit(0)
with open(os.environ["FAKE_CODEX_CALLS"], "a") as calls:
    calls.write(json.dumps({{"argv": args, "cwd": os.getcwd(), "subject": os.environ.get("CAGE_CODEX_SUBJECT", ""), "pid": os.getpid(), "home": str(home)}}) + "\\n")
if args[:1] == ["delete"]:
    for path in home.glob("sessions/*/*/*/rollout-*-" + args[-1] + "*.jsonl*"):
        path.unlink()
    sys.exit(0)
if args[:1] != ["exec"]:
    # The interactive TUI: it stays until the test lets it quit.
    while not os.path.exists(os.environ["FAKE_CODEX_CALLS"] + ".quit"):
        time.sleep(0.02)
    sys.exit(0)
mode = os.environ.get("FAKE_CODEX_MODE", "reply")
text = sys.stdin.read()
if mode == "wait":
    time.sleep(30)
if mode == "fail":
    print(json.dumps({{"type": "turn.failed", "error": {{"message": "synthetic usage limit"}}}}), flush=True)
    sys.exit(1)
if "resume" in args:
    thread = args[args.index("resume") + 1]
    path = next(home.glob("sessions/*/*/*/rollout-*-" + thread + ".jsonl"))
else:
    thread = str(uuid.uuid4())
    path = home / "sessions/2026/11/20" / ("rollout-2026-11-20T10-00-00-" + thread + ".jsonl")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({{"type": "session_meta", "payload": {{"id": thread, "cwd": os.getcwd(), "source": "exec", "originator": "codex_exec"}}}}) + "\\n")
print(json.dumps({{"type": "thread.started", "thread_id": thread}}), flush=True)
def row(kind, payload):
    with path.open("a") as stream:
        stream.write(json.dumps({{"type": kind, "payload": payload}}) + "\\n")
row("event_msg", {{"type": "task_started", "turn_id": "turn"}})
row("response_item", {{"type": "message", "role": "user", "content": [{{"type": "input_text", "text": text}}]}})
row("event_msg", {{"type": "item_completed", "item": {{"type": "UserMessage", "content": [{{"type": "text", "text": text}}]}}}})
if mode == "sleep":
    time.sleep(30)
row("response_item", {{"type": "message", "role": "assistant", "content": [{{"type": "output_text", "text": "reply: " + text}}]}})
row("event_msg", {{"type": "task_complete", "last_agent_message": "reply: " + text}})
"""


def record(kind, payload):
    return {"timestamp": "2026-11-20T10:00:00.000Z", "type": kind, "payload": payload}


class CodexSessionTests(unittest.TestCase):
    def setUp(self):
        # send() deliberately leaves its detached supervisor running.
        warnings.filterwarnings("ignore", "subprocess [0-9]+ is still running", ResourceWarning)
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name).resolve()
        self.project = self.home / "project"
        self.project.mkdir()
        bin_directory = self.home / "bin"
        bin_directory.mkdir()
        fake = bin_directory / "codex"
        fake.write_text(FAKE_CODEX.format(python=sys.executable))
        fake.chmod(0o755)
        self.calls = self.home / "calls.jsonl"
        self.env = {**os.environ, "HOME": str(self.home), "PATH": str(bin_directory) + os.pathsep + os.environ.get("PATH", os.defpath),
                    "FAKE_CODEX_CALLS": str(self.calls), "TMPDIR": str(self.home)}
        self.env.pop("CODEX_HOME", None)
        environment = patch.dict(os.environ, self.env, clear=True)
        environment.start()
        self.addCleanup(environment.stop)
        for name, value in (("ROOT", self.home / ".local/share/cage/codex-sessions"), ("ACCOUNTS", self.home / ".local/share/cage/codex-accounts")):
            patcher = patch.object(sessions, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.addCleanup(self.stop_runs)

    def stop_runs(self):
        for path in sessions.ROOT.glob("*/state.json"):
            run = sessions.read_json(path, {}).get("run") or {}
            if sessions.alive(run):
                try:
                    os.killpg(run["pid"], 9)
                except OSError:
                    pass

    def rollout(self, rows, thread=None, home=None, day="2026/11/20", now=False):
        thread = thread or str(uuid.uuid4())
        path = (home or self.home / ".codex") / "sessions" / day / ("rollout-2026-11-20T10-00-00-" + thread + ".jsonl")
        path.parent.mkdir(parents=True, exist_ok=True)
        meta = record("session_meta", {"id": thread, "cwd": str(self.project), "originator": "codex-tui", "source": "cli",
                                       "base_instructions": {"text": "synthetic base instructions"}})
        if now:
            meta["timestamp"] = time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime())
        path.write_text("".join(json.dumps(row) + "\n" for row in [meta, *rows]))
        return thread, path

    def turn_rows(self, prompt, reply, tools=()):
        rows = [record("event_msg", {"type": "task_started", "turn_id": "turn"}),
                record("response_item", {"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "synthetic developer policy"}]}),
                record("response_item", {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "<environment_context>synthetic</environment_context>"}]}),
                record("response_item", {"type": "message", "role": "user", "content": [{"type": "input_text", "text": prompt}]}),
                record("event_msg", {"type": "item_completed", "item": {"type": "UserMessage", "content": [{"type": "text", "text": prompt}]}}),
                record("world_state", {"full": True, "state": {"tools": {"synthetic": "description"}}}),
                record("response_item", {"type": "reasoning", "summary": [], "encrypted_content": "synthetic-encrypted"}),
                record("response_item", {"type": "agent_message", "author": "/root/worker", "recipient": "/root", "content": [{"type": "input_text", "text": "synthetic inter-agent note"}]}),
                record("response_item", {"type": "message", "role": "assistant", "phase": "commentary", "content": [{"type": "output_text", "text": "checking"}]})]
        rows += tools
        rows += [record("event_msg", {"type": "token_count", "info": {}}),
                 record("response_item", {"type": "message", "role": "assistant", "phase": "final_answer", "content": [{"type": "output_text", "text": reply}]}),
                 record("event_msg", {"type": "item_completed", "item": {"type": "AgentMessage", "content": [{"type": "Text", "text": reply}]}}),
                 record("event_msg", {"type": "task_complete", "last_agent_message": reply})]
        return rows

    def wait(self, request, predicate, timeout=15):
        deadline = time.monotonic() + timeout
        while True:
            result = sessions.view(request)
            if predicate(result):
                return result
            if time.monotonic() > deadline:
                self.fail("view did not reach the expected state: " + json.dumps(result, ensure_ascii=False))
            time.sleep(0.1)

    def test_transcript_keeps_conversation_and_skips_internal_records(self):
        tools = [
            record("response_item", {"type": "function_call", "name": "exec_command", "arguments": json.dumps({"cmd": "ls -la\nsecond line"}), "call_id": "call-1"}),
            record("response_item", {"type": "function_call_output", "call_id": "call-1", "output": {"content": "synthetic", "success": False}}),
            record("response_item", {"type": "custom_tool_call", "name": "apply_patch", "input": "*** Begin Patch\n*** Update File: src/app.py\n@@\n", "call_id": "call-2"}),
            record("response_item", {"type": "custom_tool_call_output", "call_id": "call-2", "output": [{"type": "input_text", "text": "done"}]}),
            record("response_item", {"type": "function_call", "name": "spawn_agent", "namespace": "collaboration", "arguments": "{}", "call_id": "call-3"}),
        ]
        thread, path = self.rollout(self.turn_rows("first question", "final answer", tools) + self.turn_rows("second question", "second answer")[:4]
                                    + [record("event_msg", {"type": "turn_aborted", "reason": "interrupted"})])
        turns, cwd, pending, truncated = sessions.transcript(path, thread)
        self.assertEqual(cwd, str(self.project))
        self.assertFalse(pending)
        self.assertFalse(truncated)
        self.assertEqual([turn["prompt"] for turn in turns], ["first question", "second question"])
        self.assertEqual(turns[0]["text"], "checking\n\nfinal answer")
        self.assertEqual(turns[0]["status"], "completed")
        self.assertEqual(turns[1]["status"], "stopped")
        self.assertEqual([(tool["name"], tool.get("detail"), tool["status"]) for tool in turns[0]["tools"]],
                         [("exec_command", "ls -la", "failed"), ("apply_patch", "src/app.py", "completed"), ("collaboration.spawn_agent", None, "called")])
        serialized = json.dumps([{key: value for key, value in turn.items() if not key.startswith("_")} for turn in turns])
        for hidden in ("synthetic developer policy", "environment_context", "synthetic-encrypted", "synthetic inter-agent note", "synthetic base instructions"):
            self.assertNotIn(hidden, serialized)
        self.assertEqual(turns[0]["id"], sessions.turn_id(thread, turns[0]["_offset"]))
        self.assertEqual(uuid.UUID(turns[0]["id"]).version, 4)

    def test_tail_is_bounded_and_history_is_trimmed(self):
        rows = []
        for number in range(40):
            rows += self.turn_rows("question %d" % number, "answer %d " % number + "x" * 200)
        thread, path = self.rollout(rows)
        with patch.object(sessions, "TAIL_LIMIT", 6000), patch.object(sessions, "HISTORY_LIMIT", 2000):
            turns, _, _, truncated = sessions.transcript(path, thread)
            self.assertTrue(truncated)
            self.assertLess(len(turns), 40)
            self.assertEqual(turns[-1]["prompt"], "question 39")
            # Output before the first prompt in the window is never attributed to it.
            self.assertTrue(all(turn["text"].startswith("checking\n\nanswer") for turn in turns))
            result = sessions.view({"thread": thread, "account": "default"})
        self.assertTrue(result["history_truncated"])
        self.assertLessEqual(len(json.dumps(result["turns"], ensure_ascii=False).encode()), 2000)
        self.assertEqual(result["turns"][-1]["prompt"], "question 39")
        self.assertEqual(result["title"], "question 0")

    def test_discovery_lists_project_sessions_without_subagents(self):
        thread, _ = self.rollout(self.turn_rows("list me\nplease", "ok"))
        sub, subpath = self.rollout(self.turn_rows("worker task", "ok"))
        rows = subpath.read_text().splitlines()
        meta = json.loads(rows[0])
        meta["payload"]["thread_source"] = "subagent"
        meta["payload"]["source"] = {"subagent": {"thread_spawn": {"parent_thread_id": thread, "depth": 1}}}
        subpath.write_text("\n".join([json.dumps(meta), *rows[1:]]) + "\n")
        other = self.home / "other"
        other.mkdir()
        outside, outside_path = self.rollout(self.turn_rows("elsewhere", "ok"))
        outside_path.write_text(outside_path.read_text().replace(json.dumps(str(self.project)), json.dumps(str(other)), 1))
        account = "0123456789abcdef01234567"
        managed, _ = self.rollout(self.turn_rows("managed account", "ok"), home=self.home / ".local/share/cage/codex-accounts" / account / "home")
        found = sessions.discover({"accounts": ["default", account], "workspaces": [str(self.project)]})
        self.assertEqual(sorted((item["thread_id"], item["account"], item["title"]) for item in found),
                         sorted([(thread, "default", "list me please"), (managed, account, "managed account")]))
        self.assertTrue(all(not item["running"] and not item["active"] and item["connection"] == "" for item in found))
        # The cached head answers later listings without re-reading rollouts.
        with patch.object(sessions, "head", side_effect=AssertionError("re-read")):
            self.assertEqual(len(sessions.discover({"accounts": ["default", account], "workspaces": [str(self.project)]})), 2)

    def test_send_runs_detached_exec_and_keeps_request_ids(self):
        connection, first, second = (str(uuid.uuid4()) for _ in range(3))
        request = {"connection": connection, "request_id": first, "text": "hello codex", "account": "default", "cwd": str(self.project)}
        self.assertEqual(sessions.send(request), {"ok": True})
        self.assertEqual(sessions.send(request), {"ok": True})
        with self.assertRaisesRegex(ValueError, "同一发送编号"):
            sessions.send({**request, "text": "different"})
        query = {"connection": connection, "account": "default", "thread": ""}
        result = self.wait(query, lambda result: result["turns"] and result["turns"][-1]["status"] == "completed" and not result["running"])
        thread = result["thread_id"]
        self.assertEqual(uuid.UUID(thread).hex, uuid.UUID(thread).hex)
        self.assertEqual([(turn["id"], turn["prompt"], turn["text"]) for turn in result["turns"]], [(first, "hello codex", "reply: hello codex")])
        self.assertTrue(result["can_send"])
        self.assertEqual(result["workspace"], str(self.project))
        self.assertEqual(sessions.send({**request, "request_id": second, "text": "next step", "thread": thread}), {"ok": True})
        result = self.wait({**query, "thread": thread}, lambda result: len(result["turns"]) == 2 and result["turns"][-1]["status"] == "completed" and not result["running"])
        self.assertEqual([turn["id"] for turn in result["turns"]], [first, second])
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0]["argv"], ["exec", "--json", "--skip-git-repo-check", "-C", str(self.project), "-"])
        self.assertEqual(calls[1]["argv"], ["exec", "--json", "--skip-git-repo-check", "-C", str(self.project), "resume", thread, "-"])
        self.assertEqual(calls[1]["cwd"], str(self.project))
        state = sessions.read_json(sessions.ROOT / connection / "state.json")
        self.assertEqual(state["thread_id"], thread)
        self.assertNotIn("text", json.dumps(state["sent"]))

    def test_running_turn_blocks_sending_and_stops(self):
        os.environ["FAKE_CODEX_MODE"] = "sleep"
        connection, request_id = str(uuid.uuid4()), str(uuid.uuid4())
        sessions.send({"connection": connection, "request_id": request_id, "text": "long task", "account": "default", "cwd": str(self.project)})
        query = {"connection": connection, "account": "default"}
        result = self.wait(query, lambda result: result["thread_id"] and result["turns"] and result["turns"][-1]["id"] == request_id
                           and result["turns"][-1]["prompt"] == "long task")
        self.assertEqual(result["turns"][-1]["status"], "running")
        self.assertTrue(result["running"] and result["active"])
        self.assertFalse(result["can_send"])
        listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual([(item["thread_id"], item["connection"], item["running"], item["active"]) for item in listed],
                         [(result["thread_id"], connection, True, True)])
        with self.assertRaisesRegex(ValueError, "正在处理"):
            sessions.send({"connection": connection, "request_id": str(uuid.uuid4()), "text": "again", "account": "default", "cwd": str(self.project)})
        with self.assertRaisesRegex(ValueError, "已结束或不是从手机发起"):
            sessions.stop({"connection": connection, "request_id": str(uuid.uuid4())})
        self.assertEqual(sessions.stop({"connection": connection, "request_id": request_id}), {"ok": True})
        result = sessions.view(query)
        self.assertEqual(result["turns"][-1]["status"], "stopped")
        self.assertFalse(result["running"])
        self.assertTrue(result["can_send"])

    def test_new_session_is_listed_before_its_rollout_exists(self):
        os.environ["FAKE_CODEX_MODE"] = "wait"
        connection, request_id = str(uuid.uuid4()), str(uuid.uuid4())
        sessions.send({"connection": connection, "request_id": request_id, "text": "start", "account": "default", "cwd": str(self.project)})
        listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual([(item["thread_id"], item["connection"], item["running"]) for item in listed], [("", connection, True)])
        result = sessions.view({"connection": connection, "account": "default"})
        self.assertEqual([(turn["id"], turn["prompt"], turn["status"]) for turn in result["turns"]], [(request_id, "start", "running")])

    def test_failed_turn_reports_reason(self):
        os.environ["FAKE_CODEX_MODE"] = "fail"
        connection, request_id = str(uuid.uuid4()), str(uuid.uuid4())
        sessions.send({"connection": connection, "request_id": request_id, "text": "will fail", "account": "default", "cwd": str(self.project)})
        result = self.wait({"connection": connection, "account": "default"}, lambda result: result["turns"] and result["turns"][-1]["status"] == "failed")
        self.assertEqual(result["turns"][-1]["error"], "synthetic usage limit")
        self.assertTrue(result["can_send"])

    def test_account_home_runs_through_account_helper(self):
        account, subject = "0123456789abcdef01234567", "a" * 64
        helper = self.home / "cage-codex-account"
        helper.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" \"$CAGE_CODEX_SUBJECT\" > " + str(self.home / "helper-call") + "\nexit 78\n")
        helper.chmod(0o755)
        connection, request_id = str(uuid.uuid4()), str(uuid.uuid4())
        request = {"connection": connection, "request_id": request_id, "text": "use account", "account": account, "subject": subject, "cwd": str(self.project)}
        with patch.object(sessions, "ACCOUNT_HELPER", helper):
            with self.assertRaisesRegex(ValueError, "账号状态"):
                sessions.send({**request, "subject": ""})
            sessions.send(request)
            result = self.wait({"connection": connection, "account": account}, lambda result: result["turns"] and result["turns"][-1]["status"] == "failed")
        self.assertTrue(result["account_refused"])
        self.assertIn("Codex 账号", result["turns"][-1]["error"])
        self.assertEqual((self.home / "helper-call").read_text().splitlines(),
                         ["run " + account + " exec --json --skip-git-repo-check -C " + str(self.project) + " -", subject])
        with self.assertRaisesRegex(ValueError, "账号已变化"):
            sessions.send({**request, "request_id": str(uuid.uuid4()), "account": "default"})

    def test_interactive_terminal_blocks_sending(self):
        thread, path = self.rollout(self.turn_rows("in the terminal", "ok")[:5])
        proc = self.home / "proc"
        (proc / "4000001/fd").mkdir(parents=True)
        (proc / "4000001/cmdline").write_bytes(b"node\0/usr/lib/codex.js\0")
        os.symlink(path, proc / "4000001/fd/7")
        (proc / "4000002/fd").mkdir(parents=True)
        (proc / "4000002/cmdline").write_bytes(b"/home/cage/.local/bin/codex\0resume\0" + thread.encode() + b"\0")
        (proc / "4000003/fd").mkdir(parents=True)
        (proc / "4000003/cmdline").write_bytes(b"bash\0")
        other = str(uuid.uuid4())
        lock = self.home / ".codex/thread-writer-locks" / (other + ".lock")
        lock.parent.mkdir(parents=True)
        lock.touch()
        (proc / "4000004/fd").mkdir(parents=True)
        (proc / "4000004/cmdline").write_bytes(b"node\0")
        os.symlink(lock, proc / "4000004/fd/9")
        with patch.object(sessions, "PROC", proc):
            files, threads = sessions.rollout_users()
            self.assertEqual(files, {str(path): {4000001}})
            self.assertEqual(threads, {thread: {4000002}, other: {4000004}})
            result = sessions.view({"thread": thread, "account": "default"})
            self.assertFalse(result["can_send"])
            self.assertTrue(result["running"] and result["active"])
            self.assertEqual(result["connection_notice"], sessions.TUI_OPEN)
            self.assertEqual(result["turns"][-1]["status"], "running")
            with self.assertRaisesRegex(ValueError, "Codex 终端"):
                sessions.send({"connection": str(uuid.uuid4()), "request_id": str(uuid.uuid4()), "text": "x", "account": "default",
                               "thread": thread, "cwd": str(self.project)})
            listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
            self.assertEqual([(item["running"], item["active"]) for item in listed], [(True, True)])
            # Processes of our own exec run are not an interactive terminal.
            with patch.object(sessions.os, "getpgid", return_value=4242):
                self.assertEqual(sessions.foreign(path, thread, (files, threads), {4242}), set())
        self.assertEqual(sessions.view({"thread": thread, "account": "default"})["turns"][-1]["status"], "interrupted")

    def test_purge_removes_only_the_authorized_rollout(self):
        thread, path = self.rollout(self.turn_rows("remove me", "ok"))
        keep, kept = self.rollout(self.turn_rows("keep me", "ok"))
        with self.assertRaisesRegex(ValueError, "不一致"):
            sessions.purge({"thread": thread, "account": "default", "workspace": str(self.home / "other")})
        self.assertTrue(path.exists())
        self.assertEqual(sessions.purge({"thread": thread, "account": "default", "workspace": str(self.project)}), {"ok": True})
        self.assertFalse(path.exists())
        self.assertTrue(kept.exists())
        self.assertEqual(sessions.purge({"thread": thread, "account": "default", "workspace": str(self.project)}), {"ok": True})

    def calls_of(self, kind=None):
        if not self.calls.exists():
            return []
        rows = [json.loads(line) for line in self.calls.read_text().splitlines()]
        return [row for row in rows if kind is None or (row["argv"][:1] == ["exec"]) == (kind == "exec")]

    def launch(self, connection, lease=None, thread="", account="default", subject="", wait=True):
        request = {"connection": connection, "lease": lease or str(uuid.uuid4()), "thread": thread, "account": account,
                   "subject": subject, "cwd": str(self.project), "additional_directories": []}
        process = subprocess.Popen([sys.executable, "-I", str(source), "tui-launch", json.dumps(request)], cwd=str(self.project),
                                   env=self.env_now(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.addCleanup(self.finish, process)
        if wait:
            deadline = time.monotonic() + 15
            while not any(row.get("pid") == process.pid for row in self.calls_of("tui")):
                if process.poll() is not None:
                    self.fail("tui-launch ended: %r" % (process.stderr.read().decode(),))
                if time.monotonic() > deadline:
                    self.fail("the TUI did not start")
                time.sleep(0.02)
        return process

    def env_now(self):
        return dict(os.environ)

    def finish(self, process):
        if process.poll() is None:
            process.kill()
        process.communicate()

    def state(self, connection):
        return sessions.read_json(sessions.ROOT / connection / "state.json", {})

    def send(self, connection, thread="", text="from the phone"):
        return sessions.send({"connection": connection, "request_id": str(uuid.uuid4()), "text": text, "account": "default",
                              "thread": thread, "cwd": str(self.project)})

    def test_probe_reads_the_cli_help(self):
        result = sessions.probe({})
        self.assertEqual({key: result[key] for key in ("installed", "version", "resume", "no_daemon", "resume_no_daemon", "delete")},
                         {"installed": True, "version": "0.171.0", "resume": True, "no_daemon": True, "resume_no_daemon": True, "delete": True})
        asked = self.calls.with_name("calls.jsonl.help").read_text().splitlines()
        self.assertEqual(sorted(asked), ["--help", "delete --help", "resume --help"])
        # Cached for the same binary.
        sessions.probe({})
        self.assertEqual(len(self.calls.with_name("calls.jsonl.help").read_text().splitlines()), 3)
        (sessions.ROOT / "probe.json").unlink()
        os.environ["FAKE_CODEX_FLAGS"] = "none"
        result = sessions.probe({})
        self.assertEqual((result["resume"], result["no_daemon"], result["delete"]), (False, False, False))
        os.environ["PATH"] = str(self.home / "empty")
        self.assertFalse(sessions.probe({})["installed"])
        self.assertEqual(sessions.help_commands("Commands:\n  resume  x\n          wrapped words\n  fork-it  y\n\n  later  z"), {"resume", "fork-it"})

    def test_tui_launch_takes_a_lease_that_blocks_the_phone_until_codex_ends(self):
        thread, _ = self.rollout(self.turn_rows("desk", "ok"))
        connection, lease = str(uuid.uuid4()), str(uuid.uuid4())
        process = self.launch(connection, lease, thread)
        state = self.state(connection)
        # Every step execs, so the lease records the PID that is now Codex.
        self.assertEqual(state["lease"]["pid"], process.pid)
        self.assertEqual((state["lease"]["id"], state["lease"]["thread"], state["thread_id"]), (lease, thread, thread))
        tui = [row for row in self.calls_of("tui") if row["pid"] == process.pid][0]
        self.assertEqual(tui["argv"], ["resume", "-C", str(self.project), "--no-daemon", thread])
        with self.assertRaisesRegex(ValueError, "电脑的 Codex 终端"):
            self.send(connection, thread)
        # Another connection of the same thread is refused as well.
        with self.assertRaisesRegex(ValueError, "电脑的 Codex 终端"):
            self.send(str(uuid.uuid4()), thread)
        result = sessions.view({"connection": connection, "thread": thread, "account": "default"})
        self.assertEqual((result["owner"], result["lease"], result["can_send"], result["running"]), ("desktop", lease, False, True))
        self.assertEqual(result["connection_notice"], sessions.DESKTOP_NOTICE)
        listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual([(item["thread_id"], item["owner"], item["lease"]) for item in listed], [(thread, "desktop", lease)])
        self.calls.with_name("calls.jsonl.quit").touch()
        process.wait(10)
        self.calls.with_name("calls.jsonl.quit").unlink()
        self.assertEqual(sessions.view({"connection": connection, "thread": thread, "account": "default"})["owner"], "")
        self.assertNotIn("lease", self.state(connection))
        self.assertEqual(self.send(connection, thread), {"ok": True})

    def test_launch_is_refused_while_another_writer_owns_the_thread(self):
        thread, _ = self.rollout(self.turn_rows("busy", "ok"))
        connection = str(uuid.uuid4())
        with sessions.state_file(connection) as state:
            state.update(thread_id=thread, account="default", run={"request_id": str(uuid.uuid4()), "pid": os.getpid(), "start": sessions.fingerprint(os.getpid())})
        refused = self.launch(connection, thread=thread, wait=False)
        _, error = refused.communicate(timeout=15)
        self.assertEqual(refused.returncode, sessions.LAUNCH_REFUSED)
        self.assertIn(sessions.PHONE_RUNNING, error.decode())
        self.assertEqual(self.calls_of("tui"), [])
        self.assertNotIn("lease", self.state(connection))
        other = str(uuid.uuid4())
        first = self.launch(other, thread=self.rollout(self.turn_rows("second", "ok"))[0])
        for connection_id in (other, str(uuid.uuid4())):
            second = self.launch(connection_id, thread=self.state(other)["thread_id"], wait=False)
            _, error = second.communicate(timeout=15)
            self.assertEqual(second.returncode, sessions.LAUNCH_REFUSED)
            self.assertIn(sessions.OTHER_TERMINAL, error.decode())
        self.assertEqual(len(self.calls_of("tui")), 1)
        self.assertIsNone(first.poll())

    def test_old_corrupt_and_unknown_states(self):
        thread, _ = self.rollout(self.turn_rows("old", "ok"))
        old = str(uuid.uuid4())
        with sessions.state_file(old) as state:
            state.update(thread_id=thread, account="default", cwd=str(self.project))
        self.assertEqual(self.send(old, thread), {"ok": True})
        corrupt = str(uuid.uuid4())
        (sessions.ROOT / corrupt).mkdir(parents=True)
        (sessions.ROOT / corrupt / "state.json").write_text("{not json")
        with self.assertRaisesRegex(ValueError, "状态无法读取"):
            self.send(corrupt)
        self.assertFalse(sessions.view({"connection": corrupt, "account": "default"})["can_send"])
        unknown = str(uuid.uuid4())
        with sessions.state_file(unknown) as state:
            state["lease"] = {"id": str(uuid.uuid4()), "pid": 4242, "start": "x", "thread": ""}
        with patch.object(sessions, "process_start", return_value=None):
            self.assertEqual(sessions.lease_state(self.state(unknown)["lease"]), "unknown")
            with self.assertRaisesRegex(ValueError, "电脑的 Codex 终端"):
                self.send(unknown)
        # A lease of a process that has ended is dropped by the next send.
        ended = str(uuid.uuid4())
        with sessions.state_file(ended) as state:
            state["lease"] = {"id": str(uuid.uuid4()), "pid": 999999, "start": "gone", "thread": ""}
        os.environ["FAKE_CODEX_MODE"] = "wait"
        self.assertEqual(self.send(ended), {"ok": True})
        self.assertNotIn("lease", self.state(ended))

    def test_new_desktop_session_binds_once_and_its_thread_is_withheld_until_then(self):
        connection, lease = str(uuid.uuid4()), str(uuid.uuid4())
        process = self.launch(connection, lease)
        self.assertEqual(self.calls_of("tui")[0]["argv"], ["-C", str(self.project), "--no-daemon"])
        older, _ = self.rollout(self.turn_rows("from yesterday", "ok"))
        thread, path = self.rollout(self.turn_rows("new on the desktop", "ok"), now=True)
        # The rollout appeared after the lease, in its directory: it may be the
        # new session's, so it is neither listed nor writable from the phone.
        listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual(sorted((item["thread_id"], item["connection"], item["owner"]) for item in listed), sorted([(older, "", ""), ("", connection, "desktop")]))
        with self.assertRaisesRegex(ValueError, "电脑的 Codex 终端"):
            self.send(str(uuid.uuid4()), thread)
        # The process holding it binds it.
        proc = self.home / "proc"
        (proc / str(process.pid) / "fd").mkdir(parents=True)
        (proc / str(process.pid) / "cmdline").write_bytes(b"node\0/usr/lib/codex.js\0")
        os.symlink(path, proc / str(process.pid) / "fd" / "7")
        with patch.object(sessions, "PROC", proc):
            listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual(sorted((item["thread_id"], item["connection"], item["owner"]) for item in listed), sorted([(older, "", ""), (thread, connection, "desktop")]))
        self.assertEqual((self.state(connection)["thread_id"], self.state(connection)["lease"]["thread"]), (thread, thread))
        # A thread the TUI opens later never re-binds the session.
        later, later_path = self.rollout(self.turn_rows("switched", "ok"))
        os.unlink(proc / str(process.pid) / "fd" / "7")
        os.symlink(later_path, proc / str(process.pid) / "fd" / "7")
        with patch.object(sessions, "PROC", proc):
            result = sessions.view({"connection": connection, "account": "default"})
        self.assertEqual((result["thread_id"], result["owner"]), (thread, "desktop"))
        self.assertEqual(self.send(str(uuid.uuid4()), later), {"ok": True})

    def test_archive_stop_and_purge(self):
        os.environ["FAKE_CODEX_MODE"] = "sleep"
        connection, request_id = str(uuid.uuid4()), str(uuid.uuid4())
        sessions.send({"connection": connection, "request_id": request_id, "text": "long", "account": "default", "cwd": str(self.project)})
        result = self.wait({"connection": connection, "account": "default"}, lambda result: result["thread_id"] and result["owner"] == "phone")
        self.assertEqual(sessions.stop({"connection": connection, "reason": "archive"}), {"ok": True})
        result = sessions.view({"connection": connection, "account": "default"})
        self.assertEqual((result["owner"], result["turns"][-1]["status"]), ("", "stopped"))
        self.assertEqual(sessions.stop({"connection": connection, "reason": "archive"}), {"ok": True})
        self.assertEqual(sessions.stop({"connection": str(uuid.uuid4()), "reason": "archive"}), {"ok": True})
        thread = result["thread_id"]
        lease = str(uuid.uuid4())
        process = self.launch(connection, lease, thread)
        with self.assertRaisesRegex(ValueError, "请先关闭该终端"):
            sessions.purge({"thread": thread, "account": "default", "connection": connection, "workspace": str(self.project)})
        process.kill()
        process.wait(10)
        self.assertEqual(sessions.purge({"thread": thread, "account": "default", "connection": connection, "workspace": str(self.project)}), {"ok": True})
        deletes = [row for row in self.calls_of("tui") if row["argv"][:1] == ["delete"]]
        self.assertEqual([row["argv"] for row in deletes], [["delete", "--force", thread]])
        self.assertIsNone(sessions.find_rollout(self.home / ".codex", thread))
        # Without delete support the rollout is removed directly.
        (sessions.ROOT / "probe.json").unlink()
        os.environ["FAKE_CODEX_FLAGS"] = "none"
        other, other_path = self.rollout(self.turn_rows("plain", "ok"))
        self.assertEqual(sessions.purge({"thread": other, "account": "default", "workspace": str(self.project)}), {"ok": True})
        self.assertFalse(other_path.exists())
        self.assertEqual(len([row for row in self.calls_of("tui") if row["argv"][:1] == ["delete"]]), 1)

    def test_newer_rollout_names(self):
        thread, path = self.rollout(self.turn_rows("suffixed", "ok"))
        suffixed = path.with_name(path.name[:-len(".jsonl")] + "_" + str(uuid.uuid4()) + ".jsonl")
        path.rename(suffixed)
        listed = sessions.discover({"accounts": ["default"], "workspaces": [str(self.project)]})
        self.assertEqual([item["thread_id"] for item in listed], [thread])
        self.assertEqual(sessions.view({"thread": thread, "account": "default"})["turns"][0]["prompt"], "suffixed")
        squeezed, cold = self.rollout(self.turn_rows("compressed", "ok"))
        cold.rename(cold.with_name(cold.name + ".zst"))
        result = sessions.view({"thread": squeezed, "account": "default"})
        self.assertEqual((result["connection_notice"], result["can_send"], result["history_compressed"]), (sessions.COMPRESSED_NOTICE, False, True))
        # The desktop TUI can still resume what this helper cannot read.
        process = self.launch(str(uuid.uuid4()), thread=squeezed)
        tui = [row for row in self.calls_of("tui") if row["pid"] == process.pid][0]
        self.assertEqual(tui["argv"][-1], squeezed)
        self.assertFalse(sessions.view({"thread": thread, "account": "default"})["history_compressed"])


if __name__ == "__main__":
    unittest.main()
