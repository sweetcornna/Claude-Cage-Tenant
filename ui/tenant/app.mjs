// Cage 多租户门户（无构建依赖，契约见 docs/api-contract.md）。
// 浏览器中由 index.html 以模块加载，并在存在 #app 时自动启动；
// 在 Node 中导入时只导出纯逻辑与控制器，供 node --test 使用。

export const VM_ID_PATTERN = /^[0-9a-f]{32}$/;
export const USERNAME_PATTERN = /^[a-z0-9][a-z0-9._-]{2,31}$/;
export const TICKET_PATTERN = /^[A-Za-z0-9._~+/=-]{1,2048}$/;
export const SYSTEM_ID_PATTERN = /^[a-z0-9-]{1,32}$/;
export const VM_STATES = Object.freeze(["stopped", "starting", "ready", "error"]);
export const POLL_INTERVAL_MS = 2000;
export const WARM_POLL_MS = 350;
export const SESSION_CHECK_GAP_MS = 1000;
export const EXPECTED_USER_HEADER = "X-Cage-Expected-User";
export const SESSION_CHANNEL = "cage-tenant-session";
export const SESSION_PING_KEY = "cage-tenant.session-ping.v1";
export const POLL_MAX_FAILURES = 5;
export const POLL_MAX_BACKOFF_MS = 15000;
export const FRAME_SLOW_MS = 30000;
export const IFRAME_SANDBOX = "allow-scripts allow-same-origin allow-forms allow-downloads";
export const IFRAME_REFERRER_POLICY = "no-referrer";

/** 只把剪贴板读写委托给该 VM 的精确源，不扩大 sandbox。 */
export function iframeAllow(origin) {
  return `clipboard-read ${origin}; clipboard-write ${origin}`;
}
export const ENTER_PATH = "/_tenant/enter";
export const SELECTION_KEY = "cage-tenant.selected-vm.v1";
export const THEME_KEY = "cage-tenant.theme.v1";
export const THEMES = Object.freeze(["system", "light", "dark"]);
const SELECTION_MAX_USERS = 8;
const MESSAGE_MAX = 500;

/* ------------------------------------------------------------------ */
/* 校验与规范化                                                        */
/* ------------------------------------------------------------------ */

export function isValidVmId(value) {
  return typeof value === "string" && VM_ID_PATTERN.test(value);
}

export function normalizeUsername(raw) {
  return String(raw ?? "").trim().toLowerCase();
}

export function validateUsername(raw) {
  const value = normalizeUsername(raw);
  if (!value) return { ok: false, message: "请输入用户名。" };
  if (value.length < 3 || value.length > 32) {
    return { ok: false, message: "用户名需为 3–32 个字符。" };
  }
  if (!USERNAME_PATTERN.test(value)) {
    return {
      ok: false,
      message: "用户名只能使用英文字母、数字和 . _ -，并以字母或数字开头。",
    };
  }
  return { ok: true, value };
}

export function utf8Length(value) {
  return new TextEncoder().encode(value).length;
}

// 密码不改写空白。登录时只检查非空与字节上限，避免规则调整后挡住已有账户。
export function validatePassword(raw, { forLogin = false } = {}) {
  const value = typeof raw === "string" ? raw : "";
  if (!value) return { ok: false, message: "请输入密码。" };
  if (utf8Length(value) > 128) {
    return { ok: false, message: "密码不能超过 128 字节（UTF-8）。" };
  }
  if (!forLogin && Array.from(value).length < 12) {
    return { ok: false, message: "密码至少需要 12 个字符。" };
  }
  return { ok: true, value };
}

export function validateVmName(raw) {
  const value = String(raw ?? "").trim();
  if (!value) return { ok: false, message: "请输入虚拟机名称。" };
  if (/\p{Cc}/u.test(value)) return { ok: false, message: "名称不能包含控制字符。" };
  if (Array.from(value).length > 64) return { ok: false, message: "名称最多 64 个字符。" };
  return { ok: true, value };
}

function cleanText(value, max = MESSAGE_MAX) {
  if (typeof value !== "string") return "";
  const text = value.replace(/\p{Cc}+/gu, " ").trim();
  return Array.from(text).slice(0, max).join("");
}

export function normalizeUser(raw) {
  if (!raw || typeof raw !== "object") return null;
  const id = typeof raw.id === "string" ? raw.id : "";
  const username = cleanText(raw.username, 64);
  // 账户 ID 会作为 X-Cage-Expected-User 请求头发送，只接受可见 ASCII。
  if (!/^[\x21-\x7e]{1,200}$/.test(id) || !username) return null;
  return { id, username };
}

export function isValidSystemId(value) {
  return typeof value === "string" && SYSTEM_ID_PATTERN.test(value);
}

export function normalizeSystem(raw) {
  if (!raw || typeof raw !== "object" || !isValidSystemId(raw.id)) return null;
  return { id: raw.id, label: cleanText(raw.label, 64) || raw.id };
}

export function normalizeSystemList(raw) {
  const seen = new Set();
  const systems = [];
  for (const item of Array.isArray(raw) ? raw : []) {
    const system = normalizeSystem(item);
    if (!system || seen.has(system.id)) continue;
    seen.add(system.id);
    systems.push(system);
  }
  return systems;
}

export function normalizeVm(raw) {
  if (!raw || typeof raw !== "object" || !isValidVmId(raw.id)) return null;
  const state = VM_STATES.includes(raw.state) ? raw.state : "unknown";
  const systemId = isValidSystemId(raw.system_id) ? raw.system_id : "";
  const vm = {
    id: raw.id,
    name: cleanText(raw.name, 64),
    system_id: systemId,
    system_label: cleanText(raw.system_label, 64) || systemId,
    state,
    created_at: typeof raw.created_at === "string" ? raw.created_at : "",
  };
  if (state === "error") {
    const message = cleanText(raw.message);
    if (message) vm.message = message;
  }
  return vm;
}

export function normalizeVmList(raw) {
  const seen = new Set();
  const vms = [];
  for (const item of Array.isArray(raw) ? raw : []) {
    const vm = normalizeVm(item);
    if (!vm || seen.has(vm.id)) continue;
    seen.add(vm.id);
    vms.push(vm);
  }
  return vms;
}

function normalizeLimit(raw) {
  return Number.isInteger(raw) && raw >= 0 ? raw : null;
}

function isLoopbackDevHost(hostname) {
  return hostname === "localhost" || hostname.endsWith(".localhost");
}

/**
 * 校验 open 接口返回的 workspace_url 与约定完全一致：
 * 同门户 scheme 和 port、主机严格为 vm-<id>.<门户主机>、路径为 /_tenant/enter、
 * 无查询与凭据、票据只出现在 fragment，且字符串已是规范形式。
 * HTTP 只允许 *.localhost 的显式本地开发。
 */
export function validateWorkspaceUrl(raw, vmId, portal) {
  const refuse = (message) => ({ ok: false, message });
  const generic = "服务器返回的工作台地址未通过校验，已拒绝加载。";
  if (typeof raw !== "string" || !raw || raw.length > 4096) return refuse(generic);
  if (!isValidVmId(vmId)) return refuse(generic);
  const protocol = portal?.protocol;
  const hostname = typeof portal?.hostname === "string" ? portal.hostname : "";
  const port = typeof portal?.port === "string" ? portal.port : "";
  if (!hostname) return refuse(generic);
  if (protocol !== "https:" && !(protocol === "http:" && isLoopbackDevHost(hostname))) {
    return refuse("当前门户地址不支持打开工作台，请使用 HTTPS 门户。");
  }
  let url;
  try {
    url = new URL(raw);
  } catch {
    return refuse(generic);
  }
  if (url.protocol !== protocol) return refuse(generic);
  if (url.username || url.password) return refuse(generic);
  if (url.hostname !== `vm-${vmId}.${hostname}`) return refuse(generic);
  if (url.port !== port) return refuse(generic);
  if (url.pathname !== ENTER_PATH) return refuse(generic);
  if (url.search !== "") return refuse(generic);
  if (!TICKET_PATTERN.test(url.hash.slice(1))) return refuse(generic);
  if (url.href !== raw) return refuse(generic);
  return { ok: true, url: url.href, origin: url.origin };
}

/* ------------------------------------------------------------------ */
/* 门户 API 客户端                                                     */
/* ------------------------------------------------------------------ */

const FALLBACK_MESSAGES = Object.freeze({
  unauthenticated: "登录已失效，请重新登录。",
  session_changed: "登录账户已在其他标签页变化。",
  registration_closed: "此门户未开放自助注册，请联系管理员获取账户。",
  invalid_credentials: "用户名或密码不正确。",
  username_taken: "该用户名已被使用。",
  vm_not_found: "虚拟机不存在或无权访问。",
  invalid_input: "输入内容无效，请检查后重试。",
  invalid_content_type: "请求格式不受支持。",
  vm_limit: "已达到虚拟机数量上限。",
  origin_refused: "请求来源被拒绝，请从门户地址重新打开页面。",
  rate_limited: "操作过于频繁，请稍后再试。",
  runtime_unavailable: "虚拟机运行时暂时不可用，请稍后重试。",
  network_error: "无法连接门户服务，请检查网络后重试。",
  bad_response: "门户服务返回了无法识别的响应。",
  server_error: "门户服务暂时出错，请稍后重试。",
  aborted: "请求已取消。",
});

function codeForStatus(status) {
  if (status === 401) return "unauthenticated";
  if (status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status === 415) return "invalid_content_type";
  if (status === 429) return "rate_limited";
  if (status === 503) return "runtime_unavailable";
  if (status >= 500) return "server_error";
  return "bad_response";
}

export function fallbackMessage(code, status = 0) {
  return (
    FALLBACK_MESSAGES[code] ||
    (status ? `请求失败（HTTP ${status}），请稍后重试。` : "请求失败，请稍后重试。")
  );
}

export class ApiError extends Error {
  constructor({ status = 0, code = "bad_response", message = "", aborted = false } = {}) {
    super(cleanText(message) || fallbackMessage(code, status));
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.aborted = aborted;
  }
}

function transportError(error, signal) {
  if (signal?.aborted || error?.name === "AbortError") {
    return new ApiError({ code: "aborted", aborted: true });
  }
  return new ApiError({ code: "network_error" });
}

/**
 * 只使用同源会话 Cookie。注意保持 fetch 默认的 mode（"cors"）：
 * 若改成 "same-origin"，在 no-referrer 策略下浏览器会把 POST 的 Origin 置为 null。
 */
export function createApi({ fetch: fetchImpl = globalThis.fetch?.bind(globalThis), base = "" } = {}) {
  async function call(method, path, { body, signal, expectedUser } = {}) {
    const headers = { Accept: "application/json" };
    if (typeof expectedUser === "string" && expectedUser) headers[EXPECTED_USER_HEADER] = expectedUser;
    const init = {
      method,
      headers,
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
      signal,
    };
    if (method !== "GET") {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body ?? {});
    }
    let response;
    try {
      response = await fetchImpl(base + path, init);
    } catch (error) {
      throw transportError(error, signal);
    }
    let text = "";
    try {
      text = response.status === 204 ? "" : await response.text();
    } catch (error) {
      throw transportError(error, signal);
    }
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = null;
      }
    }
    if (!response.ok) {
      const detail =
        data && typeof data === "object" && data.error && typeof data.error === "object"
          ? data.error
          : {};
      const code =
        typeof detail.code === "string" && /^[a-z0-9_]{1,64}$/.test(detail.code)
          ? detail.code
          : codeForStatus(response.status);
      throw new ApiError({ status: response.status, code, message: detail.message });
    }
    if (response.status !== 204 && (!data || typeof data !== "object" || Array.isArray(data))) {
      throw new ApiError({ status: response.status, code: "bad_response" });
    }
    return { status: response.status, data };
  }
  return {
    session: (options) => call("GET", "/api/tenant/session", options),
    login: (username, password, options) =>
      call("POST", "/api/tenant/login", { ...options, body: { username, password } }),
    register: (username, password, options) =>
      call("POST", "/api/tenant/register", { ...options, body: { username, password } }),
    logout: (options) => call("POST", "/api/tenant/logout", { ...options, body: {} }),
    listSystems: (options) => call("GET", "/api/tenant/systems", options),
    listVms: (options) => call("GET", "/api/tenant/vms", options),
    createVm: (name, systemId, options) =>
      call("POST", "/api/tenant/vms", { ...options, body: { name, system_id: systemId } }),
    openVm: (id, options) =>
      isValidVmId(id)
        ? call("POST", `/api/tenant/vms/${id}/open`, { ...options, body: {} })
        : Promise.reject(new ApiError({ status: 404, code: "vm_not_found" })),
  };
}

/* ------------------------------------------------------------------ */
/* 本地记忆：只保存“账户 ID → 所选 VM ID”，不保存票据、URL、用户名或密码   */
/* ------------------------------------------------------------------ */

export function safeStorage(name) {
  try {
    const storage = globalThis[name];
    return storage && typeof storage.getItem === "function" ? storage : null;
  } catch {
    return null;
  }
}

export function createSelectionStore({
  local = safeStorage("localStorage"),
  session = safeStorage("sessionStorage"),
} = {}) {
  const stores = [session, local].filter(Boolean);
  const validUser = (userId) =>
    typeof userId === "string" && userId.length > 0 && userId.length <= 200;
  function readPairs(storage) {
    try {
      const parsed = JSON.parse(storage.getItem(SELECTION_KEY) || "[]");
      if (!Array.isArray(parsed)) return [];
      return parsed.filter(
        (pair) =>
          Array.isArray(pair) && pair.length === 2 && validUser(pair[0]) && isValidVmId(pair[1]),
      );
    } catch {
      return [];
    }
  }
  function writePairs(storage, pairs) {
    try {
      if (pairs.length) storage.setItem(SELECTION_KEY, JSON.stringify(pairs));
      else storage.removeItem(SELECTION_KEY);
    } catch {
      /* 存储不可用时只是不记忆所选项 */
    }
  }
  return {
    read(userId) {
      if (!validUser(userId)) return null;
      for (const storage of stores) {
        const pair = readPairs(storage).find(([user]) => user === userId);
        if (pair) return pair[1];
      }
      return null;
    },
    write(userId, vmId) {
      if (!validUser(userId) || !isValidVmId(vmId)) return;
      for (const storage of stores) {
        const pairs = readPairs(storage).filter(([user]) => user !== userId);
        pairs.push([userId, vmId]);
        writePairs(storage, pairs.slice(-SELECTION_MAX_USERS));
      }
    },
    forget(userId, vmId = null) {
      if (!validUser(userId)) return;
      for (const storage of stores) {
        const pairs = readPairs(storage);
        const next = pairs.filter(([user, vm]) => user !== userId || (vmId && vm !== vmId));
        if (next.length !== pairs.length) writePairs(storage, next);
      }
    },
  };
}

/* ------------------------------------------------------------------ */
/* 门户控制器（状态机）                                                  */
/* ------------------------------------------------------------------ */

const DEFAULT_AUTH = Object.freeze({ mode: "login", busy: false, error: null, notice: null });
const NO_SYSTEMS = Object.freeze({ status: "idle", items: Object.freeze([]), error: null });
const CLOSED_CREATE = Object.freeze({ open: false, seq: 0, busy: false, error: null, systems: NO_SYSTEMS, systemId: null });

function errorView(error) {
  return {
    code: error?.code || "bad_response",
    status: error?.status || 0,
    message: error?.message || fallbackMessage(error?.code, error?.status),
  };
}

function signedOutState(auth = DEFAULT_AUTH) {
  return {
    phase: "signed-out",
    bootError: null,
    user: null,
    auth: { ...DEFAULT_AUTH, ...auth },
    vms: [],
    limit: null,
    listStatus: "idle",
    listError: null,
    selectedId: null,
    target: null,
    create: CLOSED_CREATE,
    logout: { busy: false },
    banner: null,
  };
}

function switchedBanner(previous, user) {
  if (!previous) return { kind: "info", message: `已在其他标签页登录为 ${user.username}。`, action: null };
  if (previous.id === user.id) return { kind: "info", message: "登录会话已刷新，可以重新打开虚拟机。", action: null };
  return {
    kind: "info",
    message: `登录账户已在其他标签页切换为 ${user.username}，原账户的工作台已关闭。`,
    action: null,
  };
}

/**
 * 依赖全部可注入：api、存储、计时器、当前时间、门户 location、页面可见性、跨标签通知。
 * target 表示当前要打开的 VM，status 为
 *   opening（open 请求中）→ starting（202 后轮询）→ connecting（iframe 载入中）→ ready
 *   以及 error（可手动重试）。
 * 每次切换都会递增 targetGen、中止旧请求并清除计时器，旧结果一律丢弃。
 * 已知用户时，目录/列表/创建/打开/登出请求都带 X-Cage-Expected-User；
 * 409 session_changed 时立即卸载旧工作台、清空旧账户数据并重新读取会话，不重放原操作。
 */
export function createPortal({
  api,
  selection = createSelectionStore({ local: null, session: null }),
  timers = { setTimeout: (fn, ms) => setTimeout(fn, ms), clearTimeout: (id) => clearTimeout(id) },
  now = () => Date.now(),
  location: portalLocation = globalThis.location,
  isHidden = () => false,
  broadcast = null,
  pollIntervalMs = POLL_INTERVAL_MS,
  warmPollMs = WARM_POLL_MS,
  frameSlowMs = FRAME_SLOW_MS,
  checkGapMs = SESSION_CHECK_GAP_MS,
} = {}) {
  if (!api) throw new Error("createPortal 需要 api");
  let registrationClosed = false;
  const signedOut = (auth) => ({ ...signedOutState(auth), registrationClosed });
  let state = { ...signedOut(), phase: "booting" };
  const listeners = new Set();
  let sessionGen = 0;
  let targetGen = 0;
  let listSeq = 0;
  let appliedListSeq = 0;
  let frameSeq = 0;
  let checkSeq = 0;
  let lastCheckAt = -Infinity;
  let membershipEpoch = 0;
  const localAdds = new Map(); // 本页新建的 VM ID → 新建时的 membershipEpoch
  let targetAbort = null;
  let listAbort = null;
  let authAbort = null;
  let systemsAbort = null;
  let checkAbort = null;
  let createSeq = 0;
  let pollTimer = null;
  let frameTimer = null;
  let pollDeferred = false;
  let openInFlight = false;
  let pendingRestore = false;

  function set(patch) {
    state = { ...state, ...patch };
    for (const listener of [...listeners]) listener(state);
  }

  function setTarget(patch) {
    if (state.target) set({ target: { ...state.target, ...patch } });
  }

  const findVm = (id) => state.vms.find((vm) => vm.id === id) || null;

  // 请求发出时绑定当前显示的账户；服务端发现 Cookie 已属于其他账户时返回 409 session_changed。
  const expected = (options = {}) => ({ ...options, expectedUser: state.user?.id });

  function notifyOtherTabs() {
    try {
      broadcast?.post();
    } catch {
      /* 通知只是加速同步，失败时依赖可见性检查 */
    }
  }

  function clearPollTimer() {
    if (pollTimer !== null) timers.clearTimeout(pollTimer);
    pollTimer = null;
    pollDeferred = false;
  }

  function clearFrameTimer() {
    if (frameTimer !== null) timers.clearTimeout(frameTimer);
    frameTimer = null;
  }

  function cancelTarget() {
    targetGen += 1;
    targetAbort?.abort();
    targetAbort = null;
    openInFlight = false;
    clearPollTimer();
    clearFrameTimer();
  }

  function cancelSession() {
    sessionGen += 1;
    cancelTarget();
    listAbort?.abort();
    listAbort = null;
    authAbort?.abort();
    authAbort = null;
    systemsAbort?.abort();
    systemsAbort = null;
    checkAbort?.abort();
    checkAbort = null;
    pendingRestore = false;
  }

  function expire(notice = "登录已失效，请重新登录。") {
    cancelSession();
    set(signedOut({ notice }));
  }

  /** 统一处理会话类错误；返回 true 表示已处理。 */
  function handleSessionError(error) {
    if (error?.code === "unauthenticated") {
      expire();
      return true;
    }
    if (error?.code === "session_changed") {
      sessionChanged();
      return true;
    }
    return false;
  }

  /* ---- 会话 ---- */

  async function loadSession(previous, announce) {
    const gen = sessionGen;
    const controller = new AbortController();
    authAbort = controller;
    try {
      const { data } = await api.session({ signal: controller.signal });
      if (gen !== sessionGen) return;
      const user = normalizeUser(data?.user);
      if (!user) throw new ApiError({ status: 200, code: "bad_response" });
      signedIn(user, { banner: announce ? switchedBanner(previous, user) : null });
    } catch (error) {
      if (gen !== sessionGen || error?.aborted) return;
      if (error?.code === "unauthenticated") {
        set(signedOut({ notice: announce && previous ? "登录状态已在其他标签页变化，请重新登录。" : null }));
      } else {
        set({ ...signedOut(), phase: "session-error", bootError: errorView(error) });
      }
    }
  }

  async function boot() {
    cancelSession();
    set({ ...signedOut(), phase: "booting" });
    await loadSession(null, false);
  }

  function sessionChanged() {
    const previous = state.user;
    cancelSession();
    // 立即卸载旧工作台并清空旧账户数据，再重新读取会话；不把原操作重放到新账户。
    set({ ...signedOut(), phase: "booting" });
    void loadSession(previous, true);
  }

  /**
   * 重新读取会话（跨标签通知、页面重新可见或获得焦点时）。
   * 新的检查会取代进行中的检查；期间若状态已变化，结果视为过期并忽略。
   * force=false 时在短间隔内合并重复触发。
   */
  async function checkSession({ force = true } = {}) {
    if (state.phase === "booting") return;
    if (state.phase === "session-error") return void boot();
    if (state.phase === "signed-out" && state.auth.busy) return;
    if (state.phase === "signed-in" && state.logout.busy) return;
    if (!force && (checkAbort || now() - lastCheckAt < checkGapMs)) return;
    checkAbort?.abort();
    const controller = new AbortController();
    checkAbort = controller;
    lastCheckAt = now();
    const gen = sessionGen;
    const seq = ++checkSeq;
    const shown = state.user;
    try {
      const { data } = await api.session({ signal: controller.signal });
      if (checkAbort === controller) checkAbort = null;
      if (gen !== sessionGen || seq !== checkSeq) return;
      const user = normalizeUser(data?.user);
      if (!user || (shown && user.id === shown.id)) return;
      signedIn(user, { banner: switchedBanner(shown, user) });
    } catch (error) {
      if (checkAbort === controller) checkAbort = null;
      if (gen !== sessionGen || seq !== checkSeq || error?.aborted) return;
      if (error?.code === "unauthenticated" && shown) {
        expire("已在其他标签页退出登录，或登录已过期。请重新登录。");
      }
    }
  }

  function signedIn(user, { banner = null } = {}) {
    cancelSession();
    localAdds.clear();
    set({ ...signedOut(), phase: "signed-in", user, listStatus: "loading", banner });
    pendingRestore = true;
    void refreshList();
  }

  function setAuth(patch) {
    set({ auth: { ...state.auth, ...patch } });
  }

  function setAuthMode(mode) {
    if (state.phase !== "signed-out" || state.auth.busy) return;
    if (mode !== "login" && mode !== "register") return;
    set({ auth: { ...DEFAULT_AUTH, mode } });
  }

  async function authenticate(kind, { username, password, confirm } = {}) {
    if (state.phase !== "signed-out" || state.auth.busy) return;
    const register = kind === "register";
    if (register && registrationClosed) return; // 已知注册关闭，不再尝试
    let name;
    if (register) {
      const checked = validateUsername(username);
      if (!checked.ok) return setAuth({ error: { field: "username", message: checked.message } });
      name = checked.value;
    } else {
      name = normalizeUsername(username);
      if (!name) return setAuth({ error: { field: "username", message: "请输入用户名。" } });
    }
    const checkedPassword = validatePassword(password, { forLogin: !register });
    if (!checkedPassword.ok) {
      return setAuth({ error: { field: "password", message: checkedPassword.message } });
    }
    if (register && confirm !== password) {
      return setAuth({ error: { field: "confirm", message: "两次输入的密码不一致。" } });
    }
    const gen = sessionGen;
    authAbort?.abort();
    const controller = new AbortController();
    authAbort = controller;
    setAuth({ busy: true, error: null, notice: null });
    try {
      const request = register ? api.register : api.login;
      const { data } = await request(name, checkedPassword.value, { signal: controller.signal });
      if (gen !== sessionGen) return;
      const user = normalizeUser(data?.user);
      if (!user) throw new ApiError({ status: 200, code: "bad_response" });
      signedIn(user);
      notifyOtherTabs();
    } catch (error) {
      if (gen !== sessionGen || error?.aborted) return;
      if (register && error?.code === "registration_closed") {
        registrationClosed = true;
        set({ registrationClosed: true, auth: { ...state.auth, busy: false, error: null } });
        return;
      }
      const field = error?.code === "username_taken" ? "username" : null;
      setAuth({ busy: false, error: { field, message: errorView(error).message } });
    }
  }

  async function logout() {
    if (state.phase !== "signed-in" || state.logout.busy) return;
    const resumeId = state.selectedId;
    // 先卸载 iframe、停止轮询，再请求服务端撤销会话。
    cancelTarget();
    listAbort?.abort();
    listAbort = null;
    pendingRestore = false;
    const options = expected();
    set({ target: null, selectedId: null, logout: { busy: true }, banner: null });
    const gen = sessionGen;
    try {
      await api.logout(options);
    } catch (error) {
      if (gen !== sessionGen) return;
      if (error?.code === "session_changed") return sessionChanged();
      if (error?.code !== "unauthenticated") {
        set({
          logout: { busy: false },
          banner: { kind: "error", message: `退出未完成：${errorView(error).message}`, action: "logout" },
        });
        // 仍处于登录状态：继续加载列表，并恢复原先打开的虚拟机。
        if (state.listStatus !== "ready") {
          pendingRestore = true;
          void refreshList();
        } else if (resumeId && findVm(resumeId)) {
          select(resumeId);
        }
        return;
      }
    }
    if (gen !== sessionGen) return;
    cancelSession();
    set(signedOut({ notice: "已安全退出。" }));
    notifyOtherTabs();
  }

  /* ---- 列表 ---- */

  function applyList(data, seq, epoch) {
    if (seq < appliedListSeq) return false;
    appliedListSeq = seq;
    const fresh = normalizeVmList(data?.vms);
    const ids = new Set(fresh.map((vm) => vm.id));
    // 列表请求发出后本页才新建的 VM 不会出现在这份较旧的列表里，保留它们，避免旧列表覆盖新建结果。
    const kept = state.vms.filter((vm) => !ids.has(vm.id) && (localAdds.get(vm.id) ?? -1) > epoch);
    set({ vms: [...fresh, ...kept], limit: normalizeLimit(data?.limit) ?? state.limit });
    if (state.selectedId && !findVm(state.selectedId)) vmGone(state.selectedId);
    return true;
  }

  function vmGone(id, message = "该虚拟机不存在或已无权访问。") {
    const known = findVm(id);
    if (state.target?.vmId === id || state.selectedId === id) {
      cancelTarget();
      set({ target: null, selectedId: null });
    }
    if (known) set({ vms: state.vms.filter((vm) => vm.id !== id) });
    localAdds.delete(id);
    if (state.user) selection.forget(state.user.id, id);
    set({ banner: { kind: "error", message, action: null } });
  }

  async function refreshList() {
    if (state.phase !== "signed-in" || state.logout.busy) return;
    const gen = sessionGen;
    const seq = ++listSeq;
    const epoch = membershipEpoch;
    listAbort?.abort();
    const controller = new AbortController();
    listAbort = controller;
    if (state.listStatus !== "ready") set({ listStatus: "loading", listError: null });
    try {
      const { data } = await api.listVms(expected({ signal: controller.signal }));
      if (gen !== sessionGen) return;
      if (listAbort === controller) listAbort = null;
      applyList(data, seq, epoch);
      set({ listStatus: "ready", listError: null });
      if (pendingRestore) {
        pendingRestore = false;
        restoreSelection();
      }
    } catch (error) {
      if (gen !== sessionGen || error?.aborted) return;
      if (listAbort === controller) listAbort = null;
      if (handleSessionError(error)) return;
      set({ listStatus: "error", listError: errorView(error) });
    }
  }

  function restoreSelection() {
    if (!state.user) return;
    const remembered = selection.read(state.user.id);
    if (!remembered) return;
    if (findVm(remembered)) select(remembered);
    else selection.forget(state.user.id, remembered);
  }

  function upsertVm(vm) {
    const index = state.vms.findIndex((item) => item.id === vm.id);
    const vms =
      index < 0 ? [...state.vms, vm] : state.vms.map((item, i) => (i === index ? vm : item));
    set({ vms });
  }

  /* ---- 打开与切换 ---- */

  function select(id, { force = false } = {}) {
    if (state.phase !== "signed-in" || state.logout.busy || !findVm(id)) return;
    const current = state.target;
    if (current && current.vmId === id && !force && current.status !== "error") return;
    // 同一目标正在提交 open 时不重复提交。
    if (current && current.vmId === id && openInFlight) return;
    const warm = findVm(id).state === "ready"; // 此前已在运行：202 时更早确认一次
    cancelTarget();
    const gen = targetGen;
    selection.write(state.user.id, id);
    set({
      selectedId: id,
      target: {
        vmId: id,
        status: "opening",
        since: now(),
        warm,
        quickChecked: false,
        error: null,
        pollFailures: 0,
        frame: null,
      },
    });
    void open(gen);
  }

  async function open(gen) {
    if (gen !== targetGen || !state.target || openInFlight) return;
    const id = state.target.vmId;
    const controller = new AbortController();
    targetAbort = controller;
    openInFlight = true;
    let result;
    try {
      result = await api.openVm(id, expected({ signal: controller.signal }));
    } catch (error) {
      if (gen !== targetGen || error?.aborted) return;
      openInFlight = false;
      targetAbort = null;
      return targetFailed(gen, error);
    }
    if (gen !== targetGen) return;
    openInFlight = false;
    targetAbort = null;
    const { status, data } = result;
    const vm = normalizeVm(data?.vm);
    if (!vm || vm.id !== id) {
      return fail(gen, { code: "bad_response", message: "服务器返回的虚拟机信息与请求不一致。" });
    }
    upsertVm(vm);
    if (status === 200) {
      const checked = validateWorkspaceUrl(data.workspace_url, id, portalLocation);
      if (!checked.ok) return fail(gen, { code: "invalid_workspace_url", message: checked.message });
      return mountFrame(gen, checked.url);
    }
    if (status === 202) {
      const quick = state.target.warm && !state.target.quickChecked;
      setTarget({ status: "starting", quickChecked: true });
      return schedulePoll(gen, quick ? warmPollMs : pollIntervalMs);
    }
    return fail(gen, { code: "bad_response", message: `服务器返回了意外的状态（HTTP ${status}）。` });
  }

  function schedulePoll(gen, delay) {
    if (gen !== targetGen) return;
    clearPollTimer();
    pollTimer = timers.setTimeout(() => {
      pollTimer = null;
      if (gen !== targetGen) return;
      if (isHidden()) {
        pollDeferred = true; // 页面隐藏时暂停，恢复可见后补一次
        return;
      }
      void poll(gen);
    }, delay);
  }

  async function poll(gen) {
    if (gen !== targetGen || !state.target || state.target.status !== "starting") return;
    const id = state.target.vmId;
    const seq = ++listSeq;
    const epoch = membershipEpoch;
    const controller = new AbortController();
    targetAbort = controller;
    let data;
    try {
      ({ data } = await api.listVms(expected({ signal: controller.signal })));
    } catch (error) {
      if (gen !== targetGen || error?.aborted) return;
      targetAbort = null;
      if (handleSessionError(error)) return;
      const failures = (state.target.pollFailures || 0) + 1;
      if (failures >= POLL_MAX_FAILURES) return fail(gen, errorView(error));
      setTarget({ pollFailures: failures });
      return schedulePoll(gen, Math.min(pollIntervalMs * 2 ** failures, POLL_MAX_BACKOFF_MS));
    }
    if (gen !== targetGen) return;
    targetAbort = null;
    applyList(data, seq, epoch);
    if (gen !== targetGen) return; // 目标已从列表消失
    const vm = findVm(id);
    if (!vm) return;
    if (state.target.pollFailures) setTarget({ pollFailures: 0 });
    if (vm.state === "ready") return open(gen);
    if (vm.state === "error") {
      return fail(gen, { code: "vm_error", message: vm.message || "虚拟机启动失败。" });
    }
    if (vm.state === "stopped") {
      return fail(gen, { code: "vm_stopped", message: "虚拟机未在启动，可能已被管理员停止。可以重试启动。" });
    }
    if (state.target.warm) setTarget({ warm: false }); // 确认后仍在启动：按开机处理
    return schedulePoll(gen, pollIntervalMs);
  }

  function visibilityChanged() {
    if (isHidden() || !pollDeferred) return;
    pollDeferred = false;
    void poll(targetGen);
  }

  function targetFailed(gen, error) {
    if (handleSessionError(error)) return;
    if (error?.code === "vm_not_found") {
      return vmGone(state.target?.vmId, errorView(error).message);
    }
    return fail(gen, errorView(error));
  }

  function fail(gen, error) {
    if (gen !== targetGen || !state.target) return;
    clearPollTimer();
    clearFrameTimer();
    setTarget({ status: "error", error, frame: null });
  }

  function mountFrame(gen, href) {
    if (gen !== targetGen) return;
    clearFrameTimer();
    const key = ++frameSeq;
    setTarget({
      status: "connecting",
      error: null,
      frame: { key, src: href, loaded: false, slow: false },
    });
    startFrameTimer(gen, key);
  }

  function startFrameTimer(gen, key) {
    clearFrameTimer();
    frameTimer = timers.setTimeout(() => {
      frameTimer = null;
      const frame = state.target?.frame;
      if (gen !== targetGen || !frame || frame.key !== key || frame.loaded) return;
      setTarget({ frame: { ...frame, slow: true } });
    }, frameSlowMs);
  }

  function frameLoaded(key) {
    const frame = state.target?.frame;
    if (!frame || frame.key !== key || frame.loaded) return;
    clearFrameTimer();
    // 载入后不再在内存中保留带票据的地址。
    setTarget({ status: "ready", frame: { key, src: null, loaded: true, slow: false } });
  }

  function frameFailed(key, message = "工作台页面载入失败。") {
    const frame = state.target?.frame;
    if (!frame || frame.key !== key) return;
    fail(targetGen, { code: "frame_error", message });
  }

  function keepWaiting() {
    const frame = state.target?.frame;
    if (!frame || frame.loaded || !frame.slow) return;
    setTarget({ frame: { ...frame, slow: false } });
    startFrameTimer(targetGen, frame.key);
  }

  function retry() {
    const target = state.target;
    if (!target) return;
    if (target.status === "error" || target.frame?.slow) select(target.vmId, { force: true });
  }

  function reconnect() {
    const target = state.target;
    if (!target || openInFlight) return;
    select(target.vmId, { force: true });
  }

  function goHome() {
    if (state.phase !== "signed-in") return;
    cancelTarget();
    selection.forget(state.user.id);
    set({ target: null, selectedId: null });
  }

  /* ---- 新建：每次打开都重新读取系统目录、清空选择 ---- */

  function setCreate(patch) {
    set({ create: { ...state.create, ...patch } });
  }

  function openCreate() {
    if (state.phase !== "signed-in" || state.logout.busy || state.create.open) return;
    const seq = ++createSeq;
    set({ create: { ...CLOSED_CREATE, open: true, seq } });
    void loadSystems(seq);
  }

  function closeCreate() {
    if (!state.create.open || state.create.busy) return;
    systemsAbort?.abort();
    systemsAbort = null;
    set({ create: CLOSED_CREATE });
  }

  async function loadSystems(seq = state.create.seq) {
    if (!state.create.open || state.create.seq !== seq) return;
    const gen = sessionGen;
    systemsAbort?.abort();
    const controller = new AbortController();
    systemsAbort = controller;
    setCreate({ systems: { status: "loading", items: state.create.systems.items, error: null } });
    try {
      const { data } = await api.listSystems(expected({ signal: controller.signal }));
      if (gen !== sessionGen || state.create.seq !== seq || !state.create.open) return;
      systemsAbort = null;
      const items = normalizeSystemList(data?.systems);
      // 用户已做的选择若已下架则清空，不自动换成其他系统；仅在尚未选择时预选唯一系统。
      const previous = state.create.systemId;
      const kept = items.some((system) => system.id === previous) ? previous : null;
      const systemId = previous ? kept : items.length === 1 ? items[0].id : null;
      const error = state.create.error?.field === "system" ? null : state.create.error;
      setCreate({ systems: { status: "ready", items, error: null }, systemId, error });
    } catch (error) {
      if (gen !== sessionGen || error?.aborted || state.create.seq !== seq) return;
      systemsAbort = null;
      if (handleSessionError(error)) return;
      setCreate({ systems: { status: "error", items: [], error: errorView(error) }, systemId: null });
    }
  }

  function chooseSystem(id) {
    if (!state.create.open || state.create.busy) return;
    if (!state.create.systems.items.some((system) => system.id === id)) return;
    const error = state.create.error?.field === "system" ? null : state.create.error;
    setCreate({ systemId: id, error });
  }

  const atLimit = () => state.limit !== null && state.vms.length >= state.limit;

  async function createVm(name) {
    if (state.phase !== "signed-in" || !state.create.open || state.create.busy) return;
    const seq = state.create.seq;
    const checked = validateVmName(name);
    if (!checked.ok) {
      return setCreate({ error: { field: "name", code: "invalid_input", message: checked.message } });
    }
    const system = state.create.systems.items.find((item) => item.id === state.create.systemId);
    if (!system) {
      const message =
        state.create.systems.status === "ready" && !state.create.systems.items.length
          ? "暂无可安装的系统，无法新建虚拟机。"
          : "请选择要安装的系统。";
      return setCreate({ error: { field: "system", code: "invalid_input", message } });
    }
    if (atLimit()) {
      return setCreate({
        error: { field: null, code: "vm_limit", message: `已达到每个账户 ${state.limit} 台虚拟机的上限。` },
      });
    }
    const gen = sessionGen;
    setCreate({ busy: true, error: null });
    try {
      const { data } = await api.createVm(checked.value, system.id, expected());
      if (gen !== sessionGen) return;
      const vm = normalizeVm(data?.vm);
      if (!vm) throw new ApiError({ status: 201, code: "bad_response" });
      localAdds.set(vm.id, ++membershipEpoch);
      upsertVm(vm);
      if (state.create.seq === seq) set({ create: CLOSED_CREATE });
      select(vm.id); // 新建默认选中并尝试打开，进入该 VM 原有的 Cage 工作台配置
    } catch (error) {
      if (gen !== sessionGen || error?.aborted) return;
      if (handleSessionError(error)) return;
      if (state.create.seq !== seq) return;
      setCreate({ busy: false, error: { field: null, ...errorView(error) } });
      if (error?.code === "vm_limit") void refreshList();
      if (error?.code === "invalid_input") void loadSystems(seq); // 系统可能刚被下架
    }
  }

  function dismissBanner() {
    if (state.banner) set({ banner: null });
  }

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    boot,
    checkSession,
    setAuthMode,
    login: (fields) => authenticate("login", fields),
    register: (fields) => authenticate("register", fields),
    logout,
    refreshList,
    select,
    retry,
    reconnect,
    keepWaiting,
    goHome,
    frameLoaded,
    frameFailed,
    visibilityChanged,
    openCreate,
    closeCreate,
    loadSystems: () => loadSystems(),
    chooseSystem,
    createVm,
    dismissBanner,
  };
}

/* ------------------------------------------------------------------ */
/* 展示辅助                                                            */
/* ------------------------------------------------------------------ */

export const VM_STATE_LABELS = Object.freeze({
  stopped: "已停止",
  starting: "启动中",
  ready: "运行中",
  error: "启动失败",
  unknown: "状态未知",
});

export const TARGET_STATUS_LABELS = Object.freeze({
  opening: "正在连接",
  starting: "开机中",
  connecting: "正在载入",
  ready: "已连接",
  error: "无法打开",
});

/** 此前已在运行的 VM 返回 202 时显示“确认中”，与真正的开机区分。 */
export function targetStatusLabel(target) {
  if (target?.status === "starting" && target.warm) return "确认中";
  return TARGET_STATUS_LABELS[target?.status] || "";
}

export function formatElapsed(ms) {
  const total = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = String(total % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
}

export function displayName(vm) {
  return vm?.name || "未命名虚拟机";
}

function formatCreated(value) {
  const date = new Date(value);
  if (!value || Number.isNaN(date.getTime())) return "";
  try {
    return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(date);
  } catch {
    return date.toISOString();
  }
}

export function createThemeController(doc, storage) {
  let mode = "system";
  try {
    const saved = storage?.getItem(THEME_KEY);
    if (THEMES.includes(saved)) mode = saved;
  } catch {
    /* 忽略 */
  }
  function apply() {
    const root = doc.documentElement;
    if (mode === "system") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", mode);
  }
  return {
    get mode() {
      return mode;
    },
    apply,
    cycle() {
      mode = THEMES[(THEMES.indexOf(mode) + 1) % THEMES.length];
      try {
        storage?.setItem(THEME_KEY, mode);
      } catch {
        /* 忽略 */
      }
      apply();
      return mode;
    },
  };
}

/* ------------------------------------------------------------------ */
/* DOM 视图（只用 createElement / textContent，不拼接 HTML）             */
/* ------------------------------------------------------------------ */

const SVG_NS = "http://www.w3.org/2000/svg";
const ICONS = {
  logo: ["M7 3.5h10a3 3 0 0 1 3 3v11a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-11a3 3 0 0 1 3-3z", "M9.3 3.5v17", "M14.7 3.5v17"],
  plus: ["M12 5v14", "M5 12h14"],
  grid: ["M4 4h6v6H4z", "M14 4h6v6h-6z", "M4 14h6v6H4z", "M14 14h6v6h-6z"],
  refresh: ["M20 12a8 8 0 1 1-2.34-5.66", "M20 4v5h-5"],
  logout: ["M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3", "M10 16l-4-4 4-4", "M6 12h10"],
  sun: ["M12 8a4 4 0 1 0 0 8 4 4 0 1 0 0-8z", "M12 2v2", "M12 20v2", "M4.93 4.93l1.41 1.41", "M17.66 17.66l1.41 1.41", "M2 12h2", "M20 12h2", "M4.93 19.07l1.41-1.41", "M17.66 6.34l1.41-1.41"],
  moon: ["M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z"],
  monitor: ["M3 5h18v11H3z", "M8 20h8", "M12 16v4"],
  alert: ["M12 3.5l9.5 16.5h-19z", "M12 10v4.5", "M12 17.5v.01"],
  info: ["M12 3a9 9 0 1 0 0 18 9 9 0 1 0 0-18z", "M12 11v5", "M12 7.5v.01"],
  close: ["M6 6l12 12", "M18 6L6 18"],
  server: ["M4 4h16v6H4z", "M4 14h16v6H4z", "M8 7v.01", "M8 17v.01"],
};

const THEME_LABELS = { system: "跟随系统", light: "浅色", dark: "深色" };
const THEME_ICONS = { system: "monitor", light: "sun", dark: "moon" };

export function mountPortal(root, portal, { doc = root.ownerDocument, win = doc.defaultView, theme = null } = {}) {
  let screen = null;
  let uidSeq = 0;
  const uid = (prefix) => `${prefix}-${++uidSeq}`;

  function h(tag, props = {}, children = []) {
    const el = doc.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (value === null || value === undefined || value === false) continue;
      if (key === "class") el.className = value;
      else if (key === "text") el.textContent = value;
      else if (key.startsWith("on") && typeof value === "function") el.addEventListener(key.slice(2), value);
      else el.setAttribute(key, value === true ? "" : String(value));
    }
    for (const child of [].concat(children)) {
      if (child === null || child === undefined || child === false) continue;
      el.append(typeof child === "string" ? doc.createTextNode(child) : child);
    }
    return el;
  }

  function icon(name, className = "icon") {
    const svg = doc.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    svg.setAttribute("focusable", "false");
    svg.setAttribute("class", className);
    for (const d of ICONS[name] || []) {
      const path = doc.createElementNS(SVG_NS, "path");
      path.setAttribute("d", d);
      svg.append(path);
    }
    return svg;
  }

  function brand(compact = false) {
    return h("div", { class: compact ? "brand brand-compact" : "brand" }, [
      h("span", { class: "brand-mark" }, icon("logo", "icon brand-icon")),
      h("span", { class: "brand-name" }, "Cage"),
    ]);
  }

  function themeButton() {
    const button = h("button", { type: "button", class: "icon-btn", "data-focus-key": "theme" });
    const sync = () => {
      const mode = theme?.mode || "system";
      const label = `主题：${THEME_LABELS[mode]}（点击切换）`;
      button.replaceChildren(icon(THEME_ICONS[mode]));
      button.setAttribute("aria-label", label);
      button.setAttribute("title", label);
    };
    button.addEventListener("click", () => {
      theme?.cycle();
      sync();
    });
    sync();
    if (!theme) button.hidden = true;
    return button;
  }

  // 忙碌时用 aria-disabled 而不是 disabled，避免键盘焦点掉到 <body>；重复点击由控制器忽略。
  function setBusy(button, busy) {
    button.setAttribute("aria-disabled", busy ? "true" : "false");
  }

  function setFieldError(input, el, message) {
    el.textContent = message || "";
    el.hidden = !message;
    input.setAttribute("aria-invalid", message ? "true" : "false");
  }

  function focusByKey(container, key) {
    if (!key) return false;
    const match = [...container.querySelectorAll("[data-focus-key]")].find((el) => el.dataset.focusKey === key);
    if (match) {
      match.focus({ preventScroll: true });
      return true;
    }
    return false;
  }

  /* ---- 启动中 ---- */

  function buildBooting() {
    const el = h("main", { class: "center-screen" }, [
      h("div", { class: "splash", role: "status" }, [
        h("span", { class: "spinner", "aria-hidden": "true" }),
        h("p", { text: "正在检查登录状态…" }),
      ]),
    ]);
    return { phase: "booting", el, update() {} };
  }

  /* ---- 会话检查失败（与未登录区分） ---- */

  function buildSessionError() {
    const message = h("p", { class: "muted" });
    const retry = h("button", { type: "button", class: "btn btn-primary", onclick: () => portal.boot() }, "重新连接");
    const el = h("main", { class: "center-screen" }, [
      h("section", { class: "card status-card", role: "alert", "aria-labelledby": "session-error-title" }, [
        h("span", { class: "status-icon is-error" }, icon("alert")),
        h("h1", { id: "session-error-title", text: "暂时无法连接门户服务" }),
        message,
        h("div", { class: "actions" }, [retry]),
      ]),
    ]);
    return {
      phase: "session-error",
      el,
      update(state) {
        message.textContent = `${state.bootError?.message || "请稍后重试。"} 这不代表你已退出登录。`;
      },
      focus: () => retry.focus(),
    };
  }

  /* ---- 登录 / 注册 ---- */

  function buildAuth() {
    const ids = {
      title: uid("auth-title"),
      user: uid("auth-user"),
      userError: uid("auth-user-error"),
      pass: uid("auth-pass"),
      passError: uid("auth-pass-error"),
      confirm: uid("auth-confirm"),
      confirmError: uid("auth-confirm-error"),
    };
    const title = h("h1", { id: ids.title, class: "auth-title" });
    const loginTab = h("button", { type: "button", class: "seg", onclick: () => portal.setAuthMode("login") }, "登录");
    const registerTab = h("button", { type: "button", class: "seg", onclick: () => portal.setAuthMode("register") }, "注册");
    const userInput = h("input", {
      id: ids.user,
      name: "username",
      type: "text",
      autocomplete: "username",
      autocapitalize: "none",
      autocorrect: "off",
      spellcheck: "false",
      maxlength: "64",
      required: true,
      "aria-describedby": ids.userError,
    });
    const userError = h("p", { id: ids.userError, class: "field-error", hidden: true });
    const passInput = h("input", {
      id: ids.pass,
      name: "password",
      type: "password",
      autocomplete: "current-password",
      required: true,
      "aria-describedby": ids.passError,
    });
    const passError = h("p", { id: ids.passError, class: "field-error", hidden: true });
    const confirmInput = h("input", {
      id: ids.confirm,
      name: "confirm",
      type: "password",
      autocomplete: "new-password",
      "aria-describedby": ids.confirmError,
    });
    const confirmError = h("p", { id: ids.confirmError, class: "field-error", hidden: true });
    const confirmField = h("div", { class: "field" }, [
      h("label", { for: ids.confirm, text: "确认密码" }),
      confirmInput,
      confirmError,
    ]);
    const notice = h("p", { class: "auth-notice", role: "status", hidden: true });
    const formError = h("p", { class: "form-error", role: "alert", hidden: true });
    const submit = h("button", { type: "submit", class: "btn btn-primary btn-block" });
    const form = h("form", { class: "auth-form", novalidate: true, "aria-labelledby": ids.title }, [
      h("div", { class: "field" }, [h("label", { for: ids.user, text: "用户名" }), userInput, userError]),
      h("div", { class: "field" }, [h("label", { for: ids.pass, text: "密码" }), passInput, passError]),
      confirmField,
      formError,
      submit,
    ]);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const fields = { username: userInput.value, password: passInput.value, confirm: confirmInput.value };
      if (portal.getState().auth.mode === "register") portal.register(fields);
      else portal.login(fields);
    });
    const backToLogin = h("button", { type: "button", class: "btn btn-primary btn-block", "data-focus-key": "back-to-login", onclick: () => portal.setAuthMode("login") }, "返回登录");
    const closedPanel = h("section", { class: "closed-panel", role: "alert", hidden: true }, [
      h("h2", { class: "closed-title", text: "暂未开放自助注册" }),
      h("p", { text: "请联系管理员。" }),
      backToLogin,
    ]);
    const el = h("main", { class: "center-screen auth-screen" }, [
      h("div", { class: "auth-corner" }, [themeButton()]),
      h("section", { class: "card auth-card", "aria-labelledby": ids.title }, [
        brand(),
        title,
        h("div", { class: "segmented", role: "group", "aria-label": "选择登录或注册" }, [loginTab, registerTab]),
        notice,
        closedPanel,
        form,
      ]),
    ]);
    let lastMode = null;
    let lastError = null;
    let lastClosed = false;
    return {
      phase: "signed-out",
      el,
      update(state) {
        const { mode, busy, error, notice: note } = state.auth;
        const register = mode === "register";
        const closed = register && state.registrationClosed;
        const focusWasInPanel = closedPanel.contains(doc.activeElement);
        closedPanel.hidden = !closed;
        form.hidden = closed;
        if (closed && !lastClosed && el.contains(doc.activeElement)) backToLogin.focus();
        // 提示面板隐藏时焦点会丢失，交给用户名输入框。
        if (!closed && lastClosed && (focusWasInPanel || doc.activeElement === doc.body)) userInput.focus();
        lastClosed = closed;
        title.textContent = register ? "创建门户账户" : "登录 Cage 门户";
        loginTab.setAttribute("aria-pressed", String(!register));
        registerTab.setAttribute("aria-pressed", String(register));
        setBusy(loginTab, busy);
        setBusy(registerTab, busy);
        passInput.setAttribute("autocomplete", register ? "new-password" : "current-password");
        confirmField.hidden = !register;
        confirmInput.disabled = !register;
        setBusy(submit, busy);
        submit.textContent = busy ? (register ? "正在注册…" : "正在登录…") : register ? "注册并登录" : "登录";
        form.setAttribute("aria-busy", String(busy));
        setFieldError(userInput, userError, error?.field === "username" ? error.message : "");
        setFieldError(passInput, passError, error?.field === "password" ? error.message : "");
        setFieldError(confirmInput, confirmError, error?.field === "confirm" ? error.message : "");
        const generalError = error && !error.field ? error.message : "";
        formError.textContent = generalError;
        formError.hidden = !generalError;
        notice.textContent = note || "";
        notice.hidden = !note;
        if (mode !== lastMode) {
          if (lastMode !== null) confirmInput.value = "";
          lastMode = mode;
        }
        if (error && error !== lastError && error.field) {
          ({ username: userInput, password: passInput, confirm: confirmInput })[error.field]?.focus();
        }
        lastError = error;
      },
      focus: () => userInput.focus(),
    };
  }

  /* ---- 已登录工作台 ---- */

  function buildShell() {
    const stageId = uid("stage");
    const tabEls = new Map();
    const home = h("button", { type: "button", class: "icon-btn home-btn", "aria-label": "虚拟机总览", title: "虚拟机总览", onclick: () => portal.goHome() }, [
      icon("grid"),
      h("span", { class: "btn-text", text: "总览" }),
    ]);
    const tablist = h("div", { class: "tabs", role: "tablist", "aria-label": "我的虚拟机" });
    const addButton = h("button", { type: "button", class: "icon-btn add-btn", "aria-label": "新建虚拟机", title: "新建虚拟机", onclick: () => portal.openCreate() }, icon("plus"));
    const userLabel = h("span", { class: "user-label", title: "当前门户账户" });
    const logoutButton = h("button", { type: "button", class: "icon-btn logout-btn", onclick: () => portal.logout() }, [
      icon("logout"),
      h("span", { class: "btn-text", text: "退出" }),
    ]);
    const skipToSwitcher = h("button", { type: "button", class: "skip-link" }, "跳到虚拟机切换");
    const skipToStage = h("button", { type: "button", class: "skip-link" }, "跳到工作台");

    const ctxDot = h("span", { class: "status-dot", "aria-hidden": "true" });
    const ctxName = h("strong", { class: "ctx-name" });
    const ctxStatus = h("span", { class: "ctx-status" });
    const ctxId = h("span", { class: "ctx-id mono" });
    const ctxSystem = h("span", { class: "ctx-system" });
    const ctxQuota = h("span", { class: "ctx-quota" });
    const reconnectButton = h("button", { type: "button", class: "btn btn-ghost btn-sm", "data-focus-key": "reconnect", onclick: () => portal.reconnect() }, [
      icon("refresh"),
      h("span", { class: "btn-text", text: "重新连接" }),
    ]);
    reconnectButton.setAttribute("aria-label", "重新连接当前虚拟机");
    const contextBar = h("div", { class: "contextbar", role: "region", "aria-label": "当前虚拟机" }, [
      h("div", { class: "ctx-main" }, [ctxDot, h("span", { class: "ctx-label", text: "当前" }), ctxName, ctxStatus, ctxSystem, ctxId]),
      h("div", { class: "ctx-side" }, [ctxQuota, reconnectButton]),
    ]);

    const bannerText = h("span", { class: "banner-text" });
    const bannerAction = h("button", { type: "button", class: "btn btn-sm btn-ghost" });
    const bannerClose = h("button", { type: "button", class: "icon-btn", "aria-label": "关闭提示", onclick: () => portal.dismissBanner() }, icon("close"));
    const bannerIcon = h("span", { class: "banner-icon" }, icon("alert"));
    const banner = h("div", { class: "banner", role: "alert", hidden: true }, [bannerIcon, bannerText, bannerAction, bannerClose]);

    const frameHost = h("div", { class: "frame-host" });
    const overlay = h("div", { class: "overlay" });
    const stage = h("main", { id: stageId, class: "stage", tabindex: "-1", "aria-label": "工作台" }, [frameHost, overlay]);
    const live = h("p", { class: "sr-only", "aria-live": "polite" });

    const dialog = buildCreateDialog();

    const el = h("div", { class: "shell" }, [
      skipToSwitcher,
      skipToStage,
      h("header", { class: "topbar" }, [
        brand(true),
        home,
        h("nav", { class: "switcher", "aria-label": "虚拟机切换" }, [tablist]),
        addButton,
        h("div", { class: "account" }, [userLabel, themeButton(), logoutButton]),
      ]),
      contextBar,
      banner,
      stage,
      live,
      dialog.el,
    ]);

    let mountedFrame = null; // { key, el, listeners: AbortController }
    let parkedFocus = null;
    let overlaySig = null;
    let liveKey = "";
    let lastSelected = null;
    let ticker = null;
    let current = portal.getState();

    function selectedTab() {
      return tabEls.get(current.selectedId)?.tab || tablist.querySelector("[role=tab]");
    }

    skipToSwitcher.addEventListener("click", () => (selectedTab() || home).focus());
    skipToStage.addEventListener("click", () => (mountedFrame?.el || stage).focus());

    tablist.addEventListener("keydown", (event) => {
      const tabs = [...tablist.querySelectorAll("[role=tab]")];
      const index = tabs.indexOf(doc.activeElement);
      if (index < 0 || !tabs.length) return;
      const keys = { ArrowRight: index + 1, ArrowDown: index + 1, ArrowLeft: index - 1, ArrowUp: index - 1, Home: 0, End: tabs.length - 1 };
      if (!(event.key in keys)) return;
      event.preventDefault();
      const next = (keys[event.key] + tabs.length) % tabs.length;
      tabs.forEach((tab, i) => (tab.tabIndex = i === next ? 0 : -1));
      tabs[next].focus();
      tabs[next].scrollIntoView?.({ block: "nearest", inline: "nearest" });
    });

    function createTab(id) {
      const dot = h("span", { class: "status-dot", "aria-hidden": "true" });
      const name = h("span", { class: "tab-name" });
      const status = h("span", { class: "sr-only" });
      const tab = h("button", { type: "button", role: "tab", class: "tab", "aria-controls": stageId, "data-focus-key": `tab-${id}` }, [dot, name, status]);
      tab.addEventListener("click", () => portal.select(id));
      return { tab, name, status };
    }

    function updateTabs(state) {
      const ids = new Set(state.vms.map((vm) => vm.id));
      for (const [id, entry] of tabEls) {
        if (!ids.has(id)) {
          entry.tab.remove();
          tabEls.delete(id);
        }
      }
      let previous = null;
      const focusedTab = tablist.contains(doc.activeElement) ? doc.activeElement : null;
      state.vms.forEach((vm, index) => {
        let entry = tabEls.get(vm.id);
        if (!entry) {
          entry = createTab(vm.id);
          tabEls.set(vm.id, entry);
        }
        const selected = vm.id === state.selectedId;
        const target = selected ? state.target : null;
        const label = target ? targetStatusLabel(target) : VM_STATE_LABELS[vm.state];
        entry.tab.setAttribute("aria-selected", String(selected));
        const rovingHere = focusedTab ? focusedTab === entry.tab : selected || (!state.selectedId && index === 0);
        entry.tab.tabIndex = rovingHere ? 0 : -1;
        entry.tab.dataset.state = vm.state;
        entry.tab.dataset.status = target?.status || "";
        entry.name.textContent = displayName(vm);
        entry.status.textContent = `，${label}`;
        const system = vm.system_label ? ` · ${vm.system_label}` : "";
        entry.tab.setAttribute("title", `${displayName(vm)}${system} · ${label} · ${vm.id.slice(0, 8)}`);
        const expected = previous ? previous.nextSibling : tablist.firstChild;
        if (expected !== entry.tab) tablist.insertBefore(entry.tab, expected);
        previous = entry.tab;
      });
      if (state.selectedId !== lastSelected) {
        lastSelected = state.selectedId;
        tabEls.get(state.selectedId)?.tab.scrollIntoView?.({ block: "nearest", inline: "nearest" });
      }
    }

    function updateContext(state) {
      const target = state.target;
      const vm = target ? state.vms.find((item) => item.id === target.vmId) : null;
      const count = state.vms.length;
      ctxQuota.textContent = state.limit !== null ? `已创建 ${count} / ${state.limit} 台` : `已创建 ${count} 台`;
      if (!target) {
        contextBar.dataset.status = "none";
        ctxDot.dataset.state = "none";
        ctxName.textContent = "未打开虚拟机";
        ctxStatus.hidden = true;
        ctxSystem.hidden = true;
        ctxId.hidden = true;
        reconnectButton.hidden = true;
        return;
      }
      contextBar.dataset.status = target.status;
      ctxDot.dataset.state = vm?.state || "unknown";
      ctxName.textContent = displayName(vm);
      ctxStatus.hidden = false;
      ctxStatus.dataset.status = target.status;
      ctxStatus.textContent = targetStatusLabel(target);
      ctxSystem.hidden = !vm?.system_label;
      ctxSystem.textContent = vm?.system_label || "";
      ctxSystem.setAttribute("title", vm?.system_id ? `系统：${vm.system_label}（${vm.system_id}）` : "系统");
      ctxId.hidden = false;
      ctxId.textContent = `vm-${target.vmId.slice(0, 8)}`;
      ctxId.setAttribute("title", `虚拟机 ID：${target.vmId}`);
      reconnectButton.hidden = false;
      setBusy(reconnectButton, target.status === "opening");
    }

    function updateBanner(state) {
      const info = state.banner;
      banner.hidden = !info;
      if (!info) return;
      const kind = info.kind === "info" ? "info" : "error";
      if (banner.dataset.kind !== kind) bannerIcon.replaceChildren(icon(kind === "info" ? "info" : "alert"));
      banner.dataset.kind = kind;
      banner.setAttribute("role", info.kind === "info" ? "status" : "alert");
      bannerText.textContent = info.message;
      bannerAction.hidden = info.action !== "logout";
      if (info.action === "logout") {
        bannerAction.textContent = "重试退出";
        bannerAction.onclick = () => portal.logout();
      }
    }

    function unmountFrame() {
      if (!mountedFrame) return;
      mountedFrame.listeners.abort(); // 释放 load/error 回调
      mountedFrame.el.remove(); // 移除即销毁浏览上下文，断开其中的流和 WebSocket
      mountedFrame = null;
    }

    function updateFrame(state) {
      const target = state.target;
      const frame = target?.frame;
      if (!frame) return unmountFrame();
      if (mountedFrame && mountedFrame.key === frame.key) {
        mountedFrame.el.classList.toggle("is-loading", !frame.loaded);
        return;
      }
      unmountFrame();
      // 挂载前再次校验地址（纵深防御）。
      const checked = validateWorkspaceUrl(frame.src, target.vmId, win.location);
      if (!checked.ok) {
        const key = frame.key;
        queueMicrotask(() => portal.frameFailed(key, checked.message));
        return;
      }
      const vm = state.vms.find((item) => item.id === target.vmId);
      const iframe = doc.createElement("iframe");
      iframe.setAttribute("sandbox", IFRAME_SANDBOX);
      iframe.setAttribute("referrerpolicy", IFRAME_REFERRER_POLICY);
      iframe.setAttribute("allow", iframeAllow(checked.origin));
      iframe.setAttribute("title", `工作台：${displayName(vm)}`);
      iframe.className = "workspace-frame is-loading";
      const listeners = new AbortController();
      const key = frame.key;
      iframe.addEventListener("load", () => portal.frameLoaded(key), { signal: listeners.signal });
      iframe.addEventListener("error", () => portal.frameFailed(key), { signal: listeners.signal });
      iframe.setAttribute("src", checked.url);
      mountedFrame = { key, el: iframe, listeners };
      frameHost.append(iframe);
    }

    function overlaySignature(state) {
      const target = state.target;
      if (!target) {
        const list = state.vms.map((vm) => `${vm.id}:${vm.state}:${vm.name}:${vm.system_label}:${vm.message || ""}`).join(",");
        return `home|${state.listStatus}|${state.limit}|${state.listError?.message || ""}|${list}`;
      }
      if (target.status === "ready") return `ready|${target.vmId}`;
      const vm = state.vms.find((item) => item.id === target.vmId);
      return `t|${target.vmId}|${target.status}|${target.warm ? 1 : 0}|${target.frame?.slow ? 1 : 0}|${target.pollFailures}|${target.error?.code || ""}|${target.error?.message || ""}|${vm?.name || ""}`;
    }

    function panel(children, { role = null, tone = "" } = {}) {
      return h("section", { class: `panel ${tone}`.trim(), role }, children);
    }

    function spinnerPanel(heading, lines = [], extra = []) {
      return panel(
        [
          h("span", { class: "spinner spinner-lg", "aria-hidden": "true" }),
          h("h2", { text: heading }),
          ...lines.map((line) => (typeof line === "string" ? h("p", { class: "muted", text: line }) : line)),
          ...extra,
        ],
        { role: "status" },
      );
    }

    function buildOverlay(state) {
      const target = state.target;
      if (!target) return buildHome(state);
      const vm = state.vms.find((item) => item.id === target.vmId);
      const name = displayName(vm);
      if (target.status === "ready") return [];
      if (target.status === "opening") return [spinnerPanel(`正在连接「${name}」`, ["正在向门户确认虚拟机状态…"])];
      if (target.status === "starting") {
        const elapsed = h("p", { class: "elapsed mono", "data-since": String(target.since) });
        const lines = target.warm
          ? [elapsed, "这台虚拟机此前在运行，正在确认工作台已就绪，通常几秒内完成。"]
          : [elapsed, `${vm?.system_label ? `系统：${vm.system_label}。` : ""}首次开机通常需要数十秒到几分钟。开机在服务器上持续进行，切换到其他虚拟机或离开页面都不会中断。`];
        if (target.pollFailures) lines.push(h("p", { class: "warn-text", text: `网络不稳定，正在重试（${target.pollFailures}/${POLL_MAX_FAILURES}）…` }));
        return [spinnerPanel(target.warm ? `正在恢复「${name}」的工作台` : `「${name}」正在开机`, lines)];
      }
      if (target.status === "connecting") {
        if (!target.frame?.slow) return [h("div", { class: "veil" }, spinnerPanel(`正在载入「${name}」的工作台`, []))];
        return [
          h("div", { class: "veil" }, panel([
            h("span", { class: "status-icon is-warn" }, icon("alert")),
            h("h2", { text: "工作台载入时间较长" }),
            h("p", { class: "muted", text: "可继续等待或重新连接。" }),
            h("div", { class: "actions" }, [
              h("button", { type: "button", class: "btn btn-primary", "data-focus-key": "retry", "data-autofocus": "", onclick: () => portal.retry() }, "重新连接"),
              h("button", { type: "button", class: "btn", "data-focus-key": "wait", onclick: () => portal.keepWaiting() }, "继续等待"),
            ]),
          ], { role: "alert" })),
        ];
      }
      const error = target.error || {};
      const heading = error.code === "vm_error" || error.code === "vm_stopped" ? `「${name}」启动失败` : `无法打开「${name}」`;
      return [
        panel([
          h("span", { class: "status-icon is-error" }, icon("alert")),
          h("h2", { text: heading }),
          h("p", { class: "error-text", text: error.message || "发生未知错误。" }),
          h("div", { class: "actions" }, [
            h("button", { type: "button", class: "btn btn-primary", "data-focus-key": "retry", "data-autofocus": "", onclick: () => portal.retry() }, "重试"),
            h("button", { type: "button", class: "btn", "data-focus-key": "home", onclick: () => portal.goHome() }, "返回总览"),
          ]),
        ], { role: "alert", tone: "panel-error" }),
      ];
    }

    function buildHome(state) {
      if (state.listStatus === "loading" || state.listStatus === "idle") {
        return [spinnerPanel("正在加载虚拟机列表", [])];
      }
      if (state.listStatus === "error") {
        return [
          panel([
            h("span", { class: "status-icon is-error" }, icon("alert")),
            h("h2", { text: "无法加载虚拟机列表" }),
            h("p", { class: "error-text", text: state.listError?.message || "请稍后重试。" }),
            h("div", { class: "actions" }, [
              h("button", { type: "button", class: "btn btn-primary", "data-focus-key": "list-retry", "data-autofocus": "", onclick: () => portal.refreshList() }, "重试"),
            ]),
          ], { role: "alert", tone: "panel-error" }),
        ];
      }
      if (!state.vms.length) {
        return [
          panel([
            h("span", { class: "status-icon" }, icon("server")),
            h("h2", { text: "创建你的第一台虚拟机" }),
            h("div", { class: "actions" }, [
              h("button", { type: "button", class: "btn btn-primary", "data-focus-key": "empty-create", "data-autofocus": "", onclick: () => portal.openCreate() }, [icon("plus"), "新建虚拟机"]),
            ]),
          ]),
        ];
      }
      const full = state.limit !== null && state.vms.length >= state.limit;
      const cards = state.vms.map((vm) => {
        const created = formatCreated(vm.created_at);
        return h("li", {}, h("button", { type: "button", class: "vm-card", "data-state": vm.state, "data-focus-key": `card-${vm.id}`, onclick: () => portal.select(vm.id) }, [
          h("span", { class: "vm-card-top" }, [
            h("span", { class: "status-dot", "data-state": vm.state, "aria-hidden": "true" }),
            h("span", { class: "vm-card-name", text: displayName(vm) }),
          ]),
          h("span", { class: "vm-card-meta" }, [
            h("span", { class: "badge", "data-state": vm.state, text: VM_STATE_LABELS[vm.state] }),
            vm.system_label ? h("span", { class: "vm-card-system", text: vm.system_label }) : null,
            h("span", { class: "mono", text: `vm-${vm.id.slice(0, 8)}` }),
          ]),
          vm.state === "error" && vm.message ? h("span", { class: "vm-card-error", text: vm.message }) : null,
          created ? h("span", { class: "vm-card-date", text: `创建于 ${created}` }) : null,
          h("span", { class: "vm-card-open", text: vm.state === "ready" ? "打开工作台" : "启动并打开" }),
        ]));
      });
      return [
        h("section", { class: "home", "aria-labelledby": "home-title" }, [
          h("div", { class: "home-head" }, [
            h("h2", { id: "home-title", text: "我的虚拟机" }),
            h("button", { type: "button", class: "btn btn-primary", "data-focus-key": "home-create", onclick: () => portal.openCreate() }, [icon("plus"), full ? "已达上限" : "新建虚拟机"]),
          ]),
          h("ul", { class: "vm-grid", role: "list" }, cards),
        ]),
      ];
    }

    function updateOverlay(state) {
      const sig = overlaySignature(state);
      if (sig === overlaySig) return;
      overlaySig = sig;
      const active = doc.activeElement;
      const hadFocus = active && overlay.contains(active);
      const key = hadFocus ? active.dataset.focusKey : null;
      const content = buildOverlay(state);
      overlay.replaceChildren(...content);
      overlay.hidden = content.length === 0;
      stage.dataset.mode = state.target ? state.target.status : "home";
      if (hadFocus && !focusByKey(overlay, key)) {
        const fallback = overlay.querySelector("[data-autofocus]");
        (fallback || stage).focus({ preventScroll: true });
      }
      updateElapsed();
    }

    function updateElapsed() {
      const el = overlay.querySelector(".elapsed");
      if (!el) return;
      el.textContent = `已等待 ${formatElapsed(Date.now() - Number(el.dataset.since))}`;
    }

    function updateTicker(state) {
      const needs = state.target?.status === "starting";
      if (needs && ticker === null) ticker = win.setInterval(updateElapsed, 1000);
      if (!needs && ticker !== null) {
        win.clearInterval(ticker);
        ticker = null;
      }
    }

    function announce(state) {
      const target = state.target;
      const key = target ? `${target.vmId}:${target.status}:${target.warm ? 1 : 0}:${target.frame?.slow ? 1 : 0}` : "";
      if (key === liveKey) return;
      liveKey = key;
      if (!target) {
        live.textContent = "";
        return;
      }
      const name = displayName(state.vms.find((vm) => vm.id === target.vmId));
      const text = {
        opening: `正在连接${name}`,
        starting: target.warm ? `正在确认${name}的工作台` : `${name}正在开机`,
        connecting: target.frame?.slow ? `${name}的工作台载入时间较长` : `正在载入${name}的工作台`,
        ready: `${name}的工作台已打开`,
        error: `${name}：${target.error?.message || "无法打开"}`,
      }[target.status];
      live.textContent = text || "";
    }

    function update(state) {
      current = state;
      userLabel.textContent = state.user?.username || "";
      setBusy(logoutButton, state.logout.busy);
      const logoutLabel = state.logout.busy ? "正在退出…" : `退出登录（${state.user?.username || ""}）`;
      logoutButton.setAttribute("aria-label", logoutLabel);
      logoutButton.setAttribute("title", logoutLabel);
      const full = state.limit !== null && state.vms.length >= state.limit;
      addButton.setAttribute("title", full ? `已达到 ${state.limit} 台上限` : "新建虚拟机");
      updateTabs(state);
      updateContext(state);
      updateBanner(state);
      updateFrame(state);
      updateOverlay(state);
      updateTicker(state);
      dialog.update(state);
      announce(state);
      settleParkedFocus(state);
    }

    function settleParkedFocus(state) {
      if (!parkedFocus) return;
      if (doc.activeElement !== parkedFocus) {
        parkedFocus = null; // 用户已自行移动焦点
        return;
      }
      if (state.listStatus !== "ready" && state.listStatus !== "error") return;
      const next =
        tabEls.get(state.selectedId)?.tab ||
        overlay.querySelector("[data-autofocus]") ||
        tablist.querySelector('[role=tab][tabindex="0"]');
      parkedFocus = null;
      next?.focus({ preventScroll: true });
    }

    return {
      phase: "signed-in",
      el,
      update,
      focus() {
        const preferred = tabEls.get(current.selectedId)?.tab || overlay.querySelector("[data-autofocus]");
        const target = preferred || tablist.querySelector("[role=tab]") || home;
        target.focus({ preventScroll: true });
        // 列表仍在加载时先把焦点暂放在这里，加载完成后再交给更合适的元素。
        parkedFocus = preferred ? null : target;
      },
      destroy() {
        unmountFrame();
        if (ticker !== null) win.clearInterval(ticker);
        ticker = null;
        dialog.destroy();
      },
    };
  }

  function buildCreateDialog() {
    const ids = {
      title: uid("create-title"),
      name: uid("create-name"),
      nameError: uid("create-name-error"),
      systems: uid("create-systems"),
      systemError: uid("create-system-error"),
      radio: uid("create-system"),
    };
    const heading = h("h2", { id: ids.title, tabindex: "-1", text: "新建虚拟机" });
    const systemStatus = h("div", { class: "system-status" });
    const systemList = h("div", { class: "system-list", role: "radiogroup", "aria-labelledby": ids.systems, "aria-describedby": ids.systemError });
    const systemError = h("p", { id: ids.systemError, class: "field-error", hidden: true });
    const input = h("input", {
      id: ids.name,
      name: "vm-name",
      type: "text",
      autocomplete: "off",
      maxlength: "256",
      placeholder: "例如：开发环境",
      "aria-describedby": ids.nameError,
    });
    const nameError = h("p", { id: ids.nameError, class: "field-error", hidden: true });
    const formError = h("p", { class: "form-error", role: "alert", hidden: true });
    const quota = h("p", { class: "muted quota" });
    const cancel = h("button", { type: "button", class: "btn", onclick: () => portal.closeCreate() }, "取消");
    const submit = h("button", { type: "submit", class: "btn btn-primary" }, "创建并打开");
    const form = h("form", { class: "dialog-form", novalidate: true }, [
      heading,
      h("fieldset", { class: "field system-field" }, [
        h("legend", { id: ids.systems, text: "系统" }),
        systemStatus,
        systemList,
        systemError,
      ]),
      h("div", { class: "field" }, [
        h("label", { for: ids.name, text: "名称" }),
        input,
        nameError,
      ]),
      formError,
      quota,
      h("div", { class: "actions actions-end" }, [cancel, submit]),
    ]);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      portal.createVm(input.value);
    });
    systemList.addEventListener("change", (event) => {
      if (event.target?.name === ids.radio) portal.chooseSystem(event.target.value);
    });
    const el = h("dialog", { class: "dialog", "aria-labelledby": ids.title }, [form]);
    el.addEventListener("cancel", (event) => {
      event.preventDefault(); // 由状态驱动关闭，忙碌时保持打开
      portal.closeCreate();
    });
    el.addEventListener("close", () => {
      // close 事件是异步派发的：若期间已重新打开（新会话），这是上一次关闭的过时事件，忽略。
      if (el.open) return;
      const create = portal.getState().create;
      if (create.open && create.seq === shownSeq) portal.closeCreate();
    });

    let shownSeq = 0;
    let listSig = null;
    let statusSig = null;
    let lastError = null;

    function show(seq) {
      const fresh = shownSeq !== seq;
      if (fresh) {
        shownSeq = seq;
        input.value = ""; // 每次打开都重新开始
        listSig = null;
        statusSig = null;
        lastError = null;
      }
      if (!el.open) {
        if (typeof el.showModal === "function") el.showModal();
        else el.setAttribute("open", "");
      }
      if (fresh) heading.focus();
    }

    function hide() {
      if (!el.open) return;
      if (typeof el.close === "function") el.close();
      else el.removeAttribute("open");
    }

    function renderStatus(systems) {
      const sig = `${systems.status}|${systems.items.length}|${systems.error?.message || ""}`;
      if (sig === statusSig) return;
      statusSig = sig;
      const hadFocus = systemStatus.contains(doc.activeElement);
      let content = [];
      if (systems.status === "loading" || systems.status === "idle") {
        content = [h("p", { class: "system-note", role: "status" }, [h("span", { class: "spinner", "aria-hidden": "true" }), "正在读取可安装的系统…"])];
      } else if (systems.status === "error") {
        content = [
          h("div", { class: "system-note is-error", role: "alert" }, [
            h("span", { text: `无法读取系统列表：${systems.error?.message || "请稍后重试。"}` }),
            h("button", { type: "button", class: "btn btn-sm", "data-focus-key": "systems-reload", onclick: () => portal.loadSystems() }, "重新加载"),
          ]),
        ];
      } else if (!systems.items.length) {
        content = [
          h("div", { class: "system-note is-empty", role: "alert" }, [
            h("strong", { text: "暂无可安装的系统" }),
            h("span", { text: "请联系管理员。" }),
            h("button", { type: "button", class: "btn btn-sm", "data-focus-key": "systems-reload", onclick: () => portal.loadSystems() }, "重新加载"),
          ]),
        ];
      }
      systemStatus.replaceChildren(...content);
      systemStatus.hidden = content.length === 0;
      if (hadFocus && !focusByKey(systemStatus, "systems-reload")) heading.focus();
    }

    function renderList(create) {
      const items = create.systems.status === "ready" ? create.systems.items : [];
      const sig = items.map((system) => `${system.id}:${system.label}`).join("|");
      if (sig !== listSig) {
        listSig = sig;
        const options = items.map((system) => {
          const radio = h("input", { type: "radio", name: ids.radio, value: system.id, class: "system-radio" });
          return h("label", { class: "system-option" }, [
            radio,
            h("span", { class: "system-option-body" }, [
              h("span", { class: "system-option-label", text: system.label }),
              h("span", { class: "system-option-id mono", text: system.id }),
            ]),
          ]);
        });
        systemList.replaceChildren(...options);
        systemList.hidden = options.length === 0;
      }
      for (const radio of systemList.querySelectorAll("input[type=radio]")) {
        radio.checked = radio.value === create.systemId;
        radio.disabled = create.busy;
      }
    }

    return {
      el,
      update(state) {
        const create = state.create;
        if (!create.open) {
          hide();
          return;
        }
        show(create.seq);
        renderStatus(create.systems);
        renderList(create);
        const full = state.limit !== null && state.vms.length >= state.limit;
        const noSystems = create.systems.status !== "ready" || !create.systems.items.length;
        quota.textContent = state.limit !== null ? `已创建 ${state.vms.length} / ${state.limit} 台${full ? "，已达上限" : ""}` : `已创建 ${state.vms.length} 台`;
        setBusy(submit, create.busy || full || noSystems);
        setBusy(cancel, create.busy);
        input.readOnly = create.busy;
        submit.textContent = create.busy ? "正在创建…" : "创建并打开";
        const error = create.error;
        setFieldError(input, nameError, error?.field === "name" ? error.message : "");
        systemError.textContent = error?.field === "system" ? error.message : "";
        systemError.hidden = error?.field !== "system";
        systemList.setAttribute("aria-invalid", error?.field === "system" ? "true" : "false");
        const general = error && !error.field ? error.message : "";
        formError.textContent = general;
        formError.hidden = !general;
        // 系统读取完成后，若焦点仍在标题上，把焦点交给系统选择。
        if (doc.activeElement === heading && create.systems.status === "ready" && create.systems.items.length) {
          const radios = [...systemList.querySelectorAll("input[type=radio]")];
          (radios.find((radio) => radio.checked) || radios[0])?.focus();
        }
        if (error && error !== lastError) {
          if (error.field === "name") input.focus();
          else if (error.field === "system") {
            const radios = [...systemList.querySelectorAll("input[type=radio]")];
            (radios.find((radio) => radio.checked) || radios[0] || heading).focus();
          }
        }
        lastError = error;
      },
      destroy: hide,
    };
  }

  const SCREENS = {
    booting: buildBooting,
    "session-error": buildSessionError,
    "signed-out": buildAuth,
    "signed-in": buildShell,
  };

  function titleFor(state) {
    if (state.phase !== "signed-in") return "Cage 门户";
    const target = state.target;
    if (!target) return "虚拟机总览 · Cage";
    const vm = state.vms.find((item) => item.id === target.vmId);
    return `${displayName(vm)} · Cage`;
  }

  function render(state) {
    if (!screen || screen.phase !== state.phase) {
      const previous = screen;
      screen = SCREENS[state.phase]();
      root.replaceChildren(screen.el);
      root.removeAttribute("aria-busy");
      previous?.destroy?.();
      screen.update(state);
      screen.focus?.();
    } else {
      screen.update(state);
    }
    doc.title = titleFor(state);
  }

  const unsubscribe = portal.subscribe(render);
  render(portal.getState());
  return {
    destroy() {
      unsubscribe();
      screen?.destroy?.();
      screen = null;
    },
  };
}

/* ------------------------------------------------------------------ */
/* 浏览器启动                                                          */
/* ------------------------------------------------------------------ */

export function start(doc = globalThis.document, win = globalThis) {
  const root = doc?.getElementById("app");
  if (!root || root.dataset.mounted === "1") return null;
  root.dataset.mounted = "1";
  const local = safeStorage("localStorage");
  const theme = createThemeController(doc, local);
  theme.apply();
  // 登录或退出后通知同源的其他标签页重新读取会话（只是加速同步，授权仍以服务端为准）。
  let channel = null;
  try {
    channel = typeof win.BroadcastChannel === "function" ? new win.BroadcastChannel(SESSION_CHANNEL) : null;
  } catch {
    channel = null;
  }
  const broadcast = {
    post() {
      if (channel) channel.postMessage({ type: "session" });
      else local?.setItem(SESSION_PING_KEY, `${Date.now()}-${Math.random()}`);
    },
  };
  const portal = createPortal({
    api: createApi(),
    selection: createSelectionStore(),
    location: win.location,
    isHidden: () => doc.visibilityState === "hidden",
    broadcast,
  });
  mountPortal(root, portal, { doc, win, theme });
  channel?.addEventListener("message", (event) => {
    if (event.data?.type === "session") void portal.checkSession();
  });
  win.addEventListener("storage", (event) => {
    if (event.key === SESSION_PING_KEY) void portal.checkSession();
  });
  doc.addEventListener("visibilitychange", () => {
    if (doc.visibilityState !== "visible") return;
    portal.visibilityChanged();
    void portal.checkSession({ force: false });
  });
  win.addEventListener("focus", () => void portal.checkSession({ force: false }));
  win.addEventListener("online", () => {
    const state = portal.getState();
    if (state.target?.status === "error" && state.target.error?.code === "network_error") portal.retry();
    else if (state.phase === "signed-in" && state.listStatus === "error") void portal.refreshList();
    else if (state.phase === "session-error") void portal.boot();
  });
  win.addEventListener("pageshow", (event) => {
    if (event.persisted) void portal.boot();
  });
  void portal.boot();
  return portal;
}

if (typeof document !== "undefined") start();
