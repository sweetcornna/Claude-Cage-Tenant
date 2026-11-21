import test from "node:test";
import assert from "node:assert/strict";
import {
  networkState,
  reportForSharing,
  isGuestLocalURL,
  canonicalExitPolicy,
  isSafetyCheckMessage,
  needsEnvironmentRestart,
  countdownText,
  explanationView,
} from "./safety-state.mjs";

test("check failures and incomplete evidence link to the checklist without tagging unrelated errors", () => {
  for (const message of [
    "关键检查尚未通过，外网保持关闭",
    "关键检测证据不完整，外网保持关闭",
    "Super Cage 外网未放行，请在工作台完成关键检查",
    "代理节点尚未应用或配置不一致，请重新应用节点；外网保持关闭",
    "浏览器配置检查未通过：语言不一致",
  ]) assert.equal(isSafetyCheckMessage(message), true, message);
  for (const message of [undefined, "", "关键检查已通过", "关键检查进行中", "项目文件夹连接失败", "Git 设置已写入，但检查尚未通过。", "资源限制未通过核验"])
    assert.equal(isSafetyCheckMessage(message), false, message);
});

test("errors that need an environment restart offer the restart action", () => {
  for (const message of [
    "出口地址或环境设置变化需要重启环境",
    "proxy: 出口地址或环境设置变化需要重启环境",
    "配置已变更或无法读取，请重启环境并重新检测",
    "网络门槛控制记录不可用，请停止并重启环境",
    "浏览器组件未能恢复，请保存工作后重启环境。",
    "浏览器检测未返回完整结果，请更新或重启环境",
  ]) assert.equal(needsEnvironmentRestart(message), true, message);
  for (const message of [undefined, "", "正在重启环境以加载 VM 外网络门槛…", "应用设置并重启环境？", "保存后，重启环境后生效。", "关键检查尚未通过，外网保持关闭"])
    assert.equal(needsEnvironmentRestart(message), false, message);
});

test("an unchanged exit policy does not restart the VM because of empty optional fields", () => {
  assert.deepEqual(
    canonicalExitPolicy({
      mode: "dynamic",
      scope: "country",
      country_code: "US",
    }),
    canonicalExitPolicy({
      mode: "dynamic",
      scope: "country",
      country_code: "US",
      ip: "",
      region: "",
      city: "",
    }),
  );
});

test("only a live gate lease displays networking as allowed", () => {
  const now = Date.now();
  const status = {
    vm_running: true,
    safety: {
      gate: { mode: "open", until: new Date(now + 1000).toISOString() },
      report: { checks: [{ state: "passed" }] },
    },
  };
  assert.equal(networkState(status, now).mode, "open");
  assert.equal(networkState(status, now + 2000).mode, "closed");
  status.safety.gate.mode = "closed";
  assert.equal(networkState(status, now).mode, "closed");
  status.safety.gate = null;
  assert.equal(networkState(status, now).mode, "unknown");
  status.vm_running = false;
  assert.equal(networkState(status, now).mode, "stopped");
});

test("shared reports omit identifiers and secret-derived configuration hashes", () => {
  const report = {
    policy: "v1",
    created_at: "today",
    checks: [],
    run_id: "private-run",
    check_id: "private-check",
    configuration: "secret-derived-hash",
    ip: "private-ip",
  };
  const text = JSON.stringify(reportForSharing(report));
  assert.ok(!text.includes("private") && !text.includes("secret-derived"));
});

test("offline browser previews allow guest loopback only", () => {
  assert.equal(isGuestLocalURL("http://localhost:3000"), true);
  assert.equal(isGuestLocalURL("http://127.0.0.1:18080"), true);
  assert.equal(isGuestLocalURL("http://[::1]:3000"), true);
  assert.equal(isGuestLocalURL("about:blank"), true);
  assert.equal(isGuestLocalURL("https://localhost.attacker.example"), false);
  assert.equal(isGuestLocalURL("http://192.168.5.2:17777"), false);
  assert.equal(isGuestLocalURL("https://example.com"), false);
});

test("the server's explanation rides along only with the state it describes", () => {
  const now = Date.parse("2026-11-21T08:20:00Z");
  const closed = {
    vm_running: true,
    safety: {
      gate: { mode: "closed", until: "0001-01-01T00:00:00Z" },
      explanation: {
        state: "waiting_memory",
        reason: "环境内存不足（可用约 46 MB，共 2.8 GB），联网许可到期前仍无法读取浏览器时区和语言，外网已暂停",
        next: "外网暂停中。自动完整重检正在等待内存压力缓解，最迟约 60 秒后也会进行，通过后自动恢复联网。",
        recheck_at: "2026-11-21T08:21:00Z",
        memory_short: true,
        advice: ["在隔离浏览器中关闭或刷新占用内存较多的标签页，或结束环境里不需要的程序", 7],
        updated_at: "2026-11-21T08:20:00Z",
      },
    },
  };
  const state = networkState(closed, now);
  assert.equal(state.mode, "closed");
  assert.equal(state.label, "外网已关闭");
  assert.equal(state.explanation.state, "waiting_memory");
  assert.equal(state.explanation.memoryShort, true);
  assert.equal(state.explanation.recheckInSeconds, 60);
  assert.equal(state.explanation.leaseEndsInSeconds, null);
  assert.deepEqual(state.explanation.advice, ["在隔离浏览器中关闭或刷新占用内存较多的标签页，或结束环境里不需要的程序"]);
  assert.match(state.explanation.reason, /环境内存不足/);

  const open = {
    vm_running: true,
    safety: {
      gate: { mode: "open", until: "2026-11-21T08:20:45Z" },
      explanation: { state: "unconfirmed", reason: "联网监测暂时无法读取系统时区和语言", next: "外网暂时保持开放，但联网许可没有延长。", lease_until: "2026-11-21T08:20:45Z", memory_short: false },
    },
  };
  const unconfirmed = networkState(open, now);
  assert.equal(unconfirmed.mode, "open");
  assert.equal(unconfirmed.explanation.state, "unconfirmed");
  assert.equal(unconfirmed.explanation.leaseEndsInSeconds, 45);

  // A stale explanation never contradicts the gate itself.
  assert.equal(networkState({ ...closed, safety: { ...closed.safety, gate: open.safety.gate } }, now).explanation, undefined);
  assert.equal(networkState({ ...open, safety: { ...open.safety, gate: closed.safety.gate } }, now).explanation, undefined);
  assert.equal(networkState({ ...closed, vm_running: false }, now).explanation, undefined);
  // Older servers send no explanation; the result is unchanged.
  assert.deepEqual(networkState({ vm_running: true, safety: { gate: closed.safety.gate } }, now), { mode: "closed", label: "外网已关闭" });
});

test("each explained state reads calmly, with its countdown and the advice as given", () => {
  const now = Date.parse("2026-11-21T08:20:00Z");
  const status = (explanation, gate = { mode: "closed", until: "2026-11-21T08:19:00Z" }) => ({ vm_running: true, ready: true, safety: { gate, explanation } });
  const open = { mode: "open", until: "2026-11-21T09:20:00Z" };
  const view = (explanation, gate) => explanationView(networkState(status(explanation, gate), now).explanation, now);
  const advice = ["关闭占用内存较多的标签页", "需要时调高内存上限，可以调到 6 GB"];

  const unconfirmed = view({ state: "unconfirmed", reason: "联网监测暂时无法读取系统时区和语言", next: "外网暂时保持开放。", lease_until: "2026-11-21T08:20:45Z" }, open);
  assert.equal(unconfirmed.tone, "quiet");
  assert.equal(unconfirmed.label, "已联网 · 监测重试中");
  assert.equal(unconfirmed.recheck, false, "a manual check would close the network that is still up");
  assert.equal(unconfirmed.countdown.seconds, 45);
  assert.match(unconfirmed.countdown.text, /联网许可约 45 秒后到期/);

  const memory = view({ state: "waiting_memory", reason: "环境内存不足（可用约 46 MB，共 2.8 GB）", next: "外网暂停中。", recheck_at: "2026-11-21T08:21:00Z", memory_short: true, advice });
  assert.equal(memory.tone, "warn");
  assert.equal(memory.title, "外网已暂停：环境内存不足");
  assert.equal(memory.countdown.text, "最迟约 1 分钟后自动完整重检。");
  assert.deepEqual(memory.advice, advice, "later advice (\"可以调到 N GB\") is shown as given");
  assert.equal(memory.recheck, true);
  assert.equal(memory.memoryShort, true);

  const scheduled = view({ state: "recheck_scheduled", reason: "联网许可到期前未能确认", next: "外网暂停中，稍后自动重检。", recheck_at: "2026-11-21T08:22:05Z" });
  assert.equal(scheduled.countdown.text, "约 2 分 5 秒后自动重检。");
  assert.equal(scheduled.label, "外网未放行 · 等待自动重检");
  const later = view({ state: "recheck_scheduled", reason: "r", next: "当前操作完成后自动重检。" });
  assert.equal(later.countdown, null, "no time: the next sentence says when");

  const rechecking = view({ state: "rechecking", reason: "r", next: "通过后自动恢复联网。" }, { mode: "checking", until: "2026-11-21T08:25:00Z" });
  assert.equal(rechecking.recheck, false);
  assert.equal(rechecking.label, "外网未放行 · 正在自动重检");

  const required = view({ state: "check_required", reason: "出口地区与设置不符", next: "需要你重新检测。" });
  assert.equal(required.tone, "alarm");
  assert.equal(required.recheck, true);

  const paused = view({ state: "paused", reason: "你已暂停外网", next: "重新检测通过后恢复联网。" });
  assert.equal(paused.label, "外网已手动暂停");
  assert.equal(paused.tone, "warn");

  // A due time that passed reads "即将", never a negative number.
  assert.equal(view({ state: "recheck_scheduled", reason: "r", next: "n", recheck_at: "2026-11-21T08:19:00Z" }).countdown.text, "即将自动重检。");
  // An unknown state is treated as needing the user.
  assert.equal(view({ state: "surprise", reason: "r", next: "n" }).tone, "alarm");
  // No explanation (an older server): nothing to show, today's text stays.
  assert.equal(explanationView(networkState(status(undefined), now).explanation, now), null);
  assert.equal(countdownText(59), "约 59 秒后");
  assert.equal(countdownText(120), "约 2 分钟后");
});
