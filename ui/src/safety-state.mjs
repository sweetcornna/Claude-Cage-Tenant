// Errors that only an environment restart clears; progress text ("正在重启环境…")
// and confirmation titles do not match.
export function needsEnvironmentRestart(message) {
  return typeof message === "string" && /需要重启环境|请[^，。；？]*?重启环境|停止并重启环境/.test(message);
}

export function isSafetyCheckMessage(message) {
  return typeof message === "string" && /关键(?:检查|检测)(?:尚未|未|失败|证据不完整)|完成关键检查|外网(?:未放行|保持关闭)|浏览器配置检查未通过/.test(message);
}

// Gate state plus, when it describes that state, the server's explanation
// (status.safety.explanation): why networking is closed or at risk, what
// happens next and what the user can do. Older servers send none.
export function networkState(status, now = Date.now()) {
  const state = gateState(status, now);
  const explanation = networkExplanation(status?.safety?.explanation, state.mode, now);
  return explanation ? { ...state, explanation } : state;
}

function networkExplanation(value, mode, now) {
  if (!value || typeof value !== "object" || typeof value.state !== "string") return null;
  const fits =
    value.state === "unconfirmed"
      ? mode === "open"
      : value.state === "rechecking"
        ? mode === "closed" || mode === "checking"
        : mode === "closed";
  if (!fits) return null;
  const at = (text) => {
    const time = Date.parse(text);
    return Number.isFinite(time) ? time : null;
  };
  const seconds = (time) => (time === null ? null : Math.max(0, Math.ceil((time - now) / 1000)));
  const recheckAt = at(value.recheck_at);
  const leaseUntil = at(value.lease_until);
  return {
    state: value.state,
    reason: typeof value.reason === "string" ? value.reason : "",
    next: typeof value.next === "string" ? value.next : "",
    advice: Array.isArray(value.advice) ? value.advice.filter((item) => typeof item === "string") : [],
    memoryShort: value.memory_short === true,
    recheckAt,
    recheckInSeconds: seconds(recheckAt),
    leaseUntil,
    leaseEndsInSeconds: seconds(leaseUntil),
  };
}

function gateState(status, now) {
  if (!status?.vm_running) return { mode: "stopped", label: "环境已停止" };
  const gate = status.safety?.gate;
  if (!gate) return { mode: "unknown", label: "外网状态待确认" };
  const until = Date.parse(gate.until);
  if (gate.mode === "open" && Number.isFinite(until) && until > now)
    return { mode: "open", label: "外网已放行" };
  if (gate.mode === "checking" && Number.isFinite(until) && until > now)
    return { mode: "checking", label: "正在检查" };
  if (gate.mode === "closed" || (Number.isFinite(until) && until <= now))
    return { mode: "closed", label: "外网已关闭" };
  return { mode: "unknown", label: "外网状态待确认" };
}

export function canonicalExitPolicy(value = {}) {
  return {
    mode: value.mode || "dynamic",
    scope: value.scope || "country",
    ip: (value.ip || "").trim(),
    country_code: (value.country_code || "").trim().toUpperCase(),
    region: (value.region || "").trim().replace(/\s+/g, " "),
    city: (value.city || "").trim().replace(/\s+/g, " "),
  };
}

export function reportForSharing(report) {
  return {
    policy: report.policy,
    created_at: report.created_at,
    checks: report.checks,
    scope: "采样与运行时策略检查；不代表平台账号资格或完整虚拟化审计",
  };
}

export function isGuestLocalURL(value) {
  if (value === "about:blank") return true;
  try {
    const url = new URL(value);
    return (
      ["http:", "https:"].includes(url.protocol) &&
      ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)
    );
  } catch {
    return false;
  }
}
