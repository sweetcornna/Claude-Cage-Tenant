import test from "node:test";
import assert from "node:assert/strict";
import { codexDeviceLink, isClaudeLoginLink, isCodexDeviceLink } from "./terminal-login-link.mjs";
import { codexAccountName, codexActiveCounts, codexQuotaView, codexTerminalDetail,
  codexTerminalLabel, codexTerminalSupported, codexWindowLabel } from "./codex-accounts-state.mjs";

test("Codex terminals are offered only in Linux guests", () => {
  assert.equal(codexTerminalSupported("debian-13"), true);
  assert.equal(codexTerminalSupported(undefined), true);
  for (const system of ["macos", "windows-11", "windows-remote"]) assert.equal(codexTerminalSupported(system), false);
});

test("window labels follow the reported window length", () => {
  assert.equal(codexWindowLabel(300), "5h");
  assert.equal(codexWindowLabel(10080, "secondary"), "7d");
  assert.equal(codexWindowLabel(1440), "1d");
  assert.equal(codexWindowLabel(45), "45m");
  assert.equal(codexWindowLabel(0, "primary"), "短周期");
  assert.equal(codexWindowLabel(undefined, "secondary"), "长周期");
});

test("quota view maps primary and secondary windows for the shared quota rows", () => {
  const primary = { used_percentage: 40, resets_at: 10, window_minutes: 300, observed_at: 5 };
  const view = codexQuotaView({ id: "a", quota: { primary, observed_at: 5 } });
  assert.deepEqual(view.quota, { five_hour: primary, seven_day: undefined, observed_at: 5 });
  assert.equal(codexQuotaView({ id: "b" }).quota, undefined);
});

test("terminal labels show the scheduled account or the default login", () => {
  const accounts = [{ id: "a", label: "工作", email: "a@example.test" }, { id: "b", email: "b@example.test" }];
  assert.equal(codexTerminalLabel({ terminalKind: "codex", codex_account_id: "b" }, accounts), "b@example.test");
  assert.equal(codexTerminalLabel({ terminalKind: "codex" }, accounts), "默认登录");
  assert.equal(codexTerminalLabel({ terminalKind: "codex-login", codex_account_id: "new" }, accounts), "新账号登录");
  assert.equal(codexTerminalLabel({ terminalKind: "claude", claude_account_id: "a" }, accounts), "");
  assert.equal(codexTerminalDetail({ terminalKind: "codex", codex_account_id: "a" }, accounts), "Codex · 工作");
  assert.equal(codexTerminalDetail({ terminalKind: "codex" }, accounts), "Codex · 环境默认登录（~/.codex）");
  assert.equal(codexTerminalDetail({ terminalKind: "codex-login", codex_account_id: "a" }, accounts), "Codex 登录 · 工作");
  assert.equal(codexAccountName({ email: "x@example.test" }), "x@example.test");
});

test("active counts ignore closed and unmanaged terminals", () => {
  const counts = codexActiveCounts([{ codex_account_id: "a" }, { codex_account_id: "a", closed: true }, { codex_account_id: "" }, { codex_account_id: "b" }]);
  assert.deepEqual([...counts], [["a", 1], ["b", 1]]);
});

test("only the fixed Codex device page is opened from terminal text", () => {
  const printed = "1. Open this link in your browser\n   https://auth.openai.com/codex/device\n2. Enter ABCD-1234";
  assert.equal(printed.match(codexDeviceLink)?.[0], "https://auth.openai.com/codex/device");
  assert.equal(isCodexDeviceLink("https://auth.openai.com/codex/device"), true);
  for (const uri of ["https://auth.openai.com/codex/device?code=x", "https://auth.openai.com/codex/devices", "http://auth.openai.com/codex/device"]) {
    assert.equal(isCodexDeviceLink(uri), false, uri);
  }
  assert.equal("https://auth.openai.com/codex/device/extra".match(codexDeviceLink), null);
  assert.equal(isClaudeLoginLink("https://auth.openai.com/codex/device"), false);
});
