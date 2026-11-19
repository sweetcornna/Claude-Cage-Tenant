// 用真实 HTTP 联调：app.mjs 的 API 客户端与控制器 ↔ 仅测试用的 mock 服务。
// 每个“浏览器”有独立 Cookie 罐，按页面来源附加 Origin，与浏览器行为一致。
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import * as app from "./app.mjs";
import { startMockServer } from "./mock-server.mjs";

const PASSWORD = "correct horse battery";

function browser(mock) {
  const jar = new Map(); // host -> Map(name -> value)
  function storeCookies(host, header) {
    for (const line of [].concat(header || [])) {
      const [pair, ...attributes] = line.split(";");
      const index = pair.indexOf("=");
      const name = pair.slice(0, index).trim();
      const value = pair.slice(index + 1).trim();
      const cookies = jar.get(host) || new Map();
      if (attributes.some((attr) => /^\s*max-age=0\s*$/i.test(attr)) || !value) cookies.delete(name);
      else cookies.set(name, value);
      jar.set(host, cookies);
    }
  }
  // pageOrigin 模拟发起请求的页面来源（浏览器只对非 GET 附加 Origin）。
  function request(url, { method = "GET", headers = {}, body, pageOrigin, signal } = {}) {
    const target = new URL(url);
    return new Promise((resolve, reject) => {
      const outgoing = { ...headers, Host: target.host };
      const cookies = jar.get(target.host);
      if (cookies?.size) outgoing.Cookie = [...cookies].map(([k, v]) => `${k}=${v}`).join("; ");
      if (method !== "GET" && pageOrigin) outgoing.Origin = pageOrigin;
      const req = http.request(
        { host: "127.0.0.1", port: mock.port, method, path: target.pathname + target.search, headers: outgoing },
        (res) => {
          const chunks = [];
          res.on("data", (chunk) => chunks.push(chunk));
          res.on("end", () => {
            storeCookies(target.host, res.headers["set-cookie"]);
            resolve({ status: res.statusCode, headers: res.headers, text: Buffer.concat(chunks).toString("utf8") });
          });
        },
      );
      req.on("error", reject);
      signal?.addEventListener("abort", () => {
        req.destroy();
        reject(new DOMException("aborted", "AbortError"));
      });
      if (body !== undefined) req.write(body);
      req.end();
    });
  }
  // 供 createApi 使用的 fetch：门户页面发起的同源请求。
  async function fetch(path, init = {}) {
    const res = await request(mock.origin + path, {
      method: init.method,
      headers: init.headers,
      body: init.body,
      pageOrigin: mock.origin,
      signal: init.signal,
    });
    return new Response(res.status === 204 ? null : res.text, { status: res.status, headers: { "Content-Type": res.headers["content-type"] || "" } });
  }
  return { jar, request, fetch };
}

function memoryStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
    dump: () => Object.fromEntries(map),
  };
}

async function waitFor(check, label, timeout = 5000) {
  const end = Date.now() + timeout;
  for (;;) {
    const value = check();
    if (value) return value;
    if (Date.now() > end) throw new Error(`等待超时：${label}`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

function portalFor(mock, client, storage = memoryStorage()) {
  const portal = app.createPortal({
    api: app.createApi({ fetch: client.fetch }),
    selection: app.createSelectionStore({ local: storage, session: null }),
    location: { protocol: "http:", hostname: mock.portalName, port: String(mock.port), origin: mock.origin },
    pollIntervalMs: 25,
  });
  return { portal, storage, get state() { return portal.getState(); } };
}

// 模拟浏览器在 VM 源上执行 /_tenant/enter 页面脚本。
async function enterWorkspace(mock, client, workspaceUrl) {
  const url = new URL(workspaceUrl);
  const ticket = url.hash.slice(1);
  const page = await client.request(`${url.origin}${url.pathname}`);
  assert.equal(page.status, 200);
  assert.match(page.headers["content-security-policy"], new RegExp(`frame-ancestors ${mock.origin.replace(/[.]/g, "\\.")}`));
  const entered = await client.request(`${url.origin}/_tenant/enter`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ticket }),
    pageOrigin: url.origin,
  });
  return { ticket, status: entered.status, origin: url.origin };
}

describe("门户 ↔ mock 联调", () => {
  let mock;
  before(async () => {
    mock = await startMockServer({ bootDelayMs: 80 });
  });
  after(async () => {
    await mock?.close();
  });

  test("门户静态文件与安全响应头", async () => {
    const client = browser(mock);
    const index = await client.request(`${mock.origin}/`);
    assert.equal(index.status, 200);
    assert.match(index.headers["content-type"], /text\/html/);
    assert.equal(index.headers["cache-control"], "no-store");
    assert.match(index.headers["content-security-policy"], /script-src 'self'; style-src 'self'/);
    assert.match(index.headers["content-security-policy"], /frame-src http:\/\/\*\.cage\.localhost:\d+/);
    const script = await client.request(`${mock.origin}/app.mjs`);
    assert.match(script.headers["content-type"], /text\/javascript/);
    const other = await client.request(`http://evil.localhost:${mock.port}/`);
    assert.equal(other.status, 403, "严格 Host 匹配");
  });

  test("API 契约：Origin、Content-Type、输入校验、冲突与不泄露账号存在", async () => {
    const client = browser(mock);
    const post = (path, body, { origin = mock.origin, type = "application/json" } = {}) =>
      client.request(`${mock.origin}${path}`, { method: "POST", headers: { "Content-Type": type }, body: JSON.stringify(body), pageOrigin: origin });
    const parse = (res) => JSON.parse(res.text);
    let res = await post("/api/tenant/register", { username: "carol", password: PASSWORD }, { origin: "http://evil.localhost" });
    assert.equal(res.status, 403);
    assert.equal(parse(res).error.code, "origin_refused");
    res = await post("/api/tenant/register", { username: "carol", password: PASSWORD }, { type: "text/plain" });
    assert.equal(res.status, 415);
    assert.equal(parse(res).error.code, "invalid_content_type");
    res = await post("/api/tenant/register", { username: "-x", password: PASSWORD });
    assert.equal(parse(res).error.code, "invalid_input");
    res = await post("/api/tenant/register", { username: " Carol ", password: PASSWORD });
    assert.equal(res.status, 201);
    assert.equal(parse(res).user.username, "carol");
    assert.match(res.headers["set-cookie"][0], /HttpOnly; SameSite=Strict/);
    res = await post("/api/tenant/register", { username: "carol", password: PASSWORD });
    assert.equal(parse(res).error.code, "username_taken");
    const wrongPassword = await post("/api/tenant/login", { username: "carol", password: "wrong password!!" });
    const unknownUser = await post("/api/tenant/login", { username: "nobody", password: "wrong password!!" });
    assert.equal(wrongPassword.status, 401);
    assert.deepEqual(parse(wrongPassword), parse(unknownUser), "错误响应不泄露账号是否存在");
  });

  test("两个账户：多 VM、默认系统、切换、票据、隔离与登出撤销", async () => {
    const aliceBrowser = browser(mock);
    const alice = portalFor(mock, aliceBrowser);
    await alice.portal.boot();
    assert.equal(alice.state.phase, "signed-out");
    alice.portal.setAuthMode("register");
    await alice.portal.register({ username: "alice", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => alice.state.listStatus === "ready", "alice 列表");
    assert.equal(alice.state.vms.length, 0);

    // 新建第一台使用服务端默认系统。
    alice.portal.openCreate();
    await alice.portal.createVm("开发环境");
    const vmA = alice.state.vms[0];
    assert.equal(vmA.system_label, "Debian 12");
    assert.equal(alice.state.selectedId, vmA.id);
    await waitFor(() => alice.state.target?.status === "connecting", "第一台就绪并取得入口");
    const firstUrl = alice.state.target.frame.src;
    assert.equal(app.validateWorkspaceUrl(firstUrl, vmA.id, { protocol: "http:", hostname: mock.portalName, port: String(mock.port) }).ok, true);

    // 浏览器在 VM 源消费票据，取得 VM Cookie 并看到对应工作台。
    const entry = await enterWorkspace(mock, aliceBrowser, firstUrl);
    assert.equal(entry.status, 204);
    const page = await aliceBrowser.request(`${entry.origin}/`);
    assert.equal(page.status, 200);
    assert.match(page.text, new RegExp(`data-vm-id="${vmA.id}"`));
    const replay = await enterWorkspace(mock, browser(mock), firstUrl);
    assert.equal(replay.status, 401, "票据单次消费");
    alice.portal.frameLoaded(alice.state.target.frame.key);
    assert.equal(alice.state.target.status, "ready");

    // 新建第二台（每次打开弹窗都重新开始）。
    alice.portal.openCreate();
    await alice.portal.createVm("测试环境");
    const vmB = alice.state.vms[1];
    assert.equal(alice.state.target.vmId, vmB.id);
    assert.equal(alice.state.target.frame, null, "切换后旧 iframe 已卸载");
    await waitFor(() => alice.state.target?.status === "connecting", "第二台入口");
    const secondUrl = alice.state.target.frame.src;
    assert.ok(secondUrl.startsWith(mock.vmOrigin(vmB.id)));

    // 第二台的票据不能在第一台的源使用。
    const crossUrl = `${mock.vmOrigin(vmA.id)}/_tenant/enter#${new URL(secondUrl).hash.slice(1)}`;
    assert.equal((await enterWorkspace(mock, aliceBrowser, crossUrl)).status, 401, "票据绑定精确 VM");

    // 切回第一台：已就绪，直接取得新票据。
    alice.portal.select(vmA.id);
    await waitFor(() => alice.state.target?.status === "connecting", "切回第一台");
    assert.ok(alice.state.target.frame.src.startsWith(mock.vmOrigin(vmA.id)));
    assert.notEqual(alice.state.target.frame.src, firstUrl);

    // 第二个账户看不到也打不开第一个账户的 VM。
    const bobBrowser = browser(mock);
    const bob = portalFor(mock, bobBrowser);
    await bob.portal.boot();
    bob.portal.setAuthMode("register");
    await bob.portal.register({ username: "bob", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => bob.state.listStatus === "ready", "bob 列表");
    assert.equal(bob.state.vms.length, 0);
    const steal = await bobBrowser.request(`${mock.origin}/api/tenant/vms/${vmA.id}/open`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
      pageOrigin: mock.origin,
    });
    assert.equal(steal.status, 404);
    assert.equal(JSON.parse(steal.text).error.code, "vm_not_found");
    const bobVmPage = await bobBrowser.request(`${mock.vmOrigin(vmA.id)}/`);
    assert.equal(bobVmPage.status, 401, "没有 VM Cookie 无法访问工作台");
    bob.portal.openCreate();
    await bob.portal.createVm("bob 的环境");
    assert.equal(bob.state.vms.length, 1);
    assert.equal(alice.state.vms.length, 2, "账户之间互不影响");

    // 存储中只有账户 ID 与 VM ID。
    const stored = JSON.stringify(alice.storage.dump());
    assert.equal(stored.includes("alice"), false);
    assert.equal(stored.includes("_tenant"), false);

    // 登出：iframe 立即卸载，服务端撤销会话，已授权 VM Cookie 随之失效。
    const loggingOut = alice.portal.logout();
    assert.equal(alice.state.target, null);
    await loggingOut;
    assert.equal(alice.state.phase, "signed-out");
    assert.equal((await aliceBrowser.request(`${entry.origin}/`)).status, 401, "登出后 VM 访问被撤销");
    const session = await aliceBrowser.request(`${mock.origin}/api/tenant/session`);
    assert.equal(session.status, 401);

    // 再次登录后恢复所选 VM（重新验证后用同一 open 接口）。
    const again = portalFor(mock, aliceBrowser, alice.storage);
    await again.portal.boot();
    await again.portal.login({ username: "ALICE", password: PASSWORD });
    await waitFor(() => again.state.target?.status === "connecting", "恢复所选 VM");
    assert.equal(again.state.target.vmId, vmA.id);

    // 票据只出现在 fragment，从不出现在请求路径或 Referer 中。
    for (const record of mock.state.requests) {
      assert.equal(record.path.includes(new URL(firstUrl).hash.slice(1)), false);
      assert.equal(record.path.includes(new URL(secondUrl).hash.slice(1)), false);
    }
    await again.portal.logout();
    await bob.portal.logout();
  });

  test("启动失败可重试，资源不足返回可重试错误", async () => {
    const client = browser(mock);
    const dave = portalFor(mock, client);
    await dave.portal.boot();
    dave.portal.setAuthMode("register");
    await dave.portal.register({ username: "dave", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => dave.state.listStatus === "ready", "dave 列表");
    // 等之前用例里仍在启动的 VM 完成，避免它们消费下面注入的失败。
    await waitFor(() => [...mock.state.vms.values()].every((vm) => vm.state !== "starting"), "其他 VM 启动完成");
    mock.failNextBoot("*", "所选系统镜像缺失，请稍后重试。");
    dave.portal.openCreate();
    await dave.portal.createVm("会失败的环境");
    await waitFor(() => dave.state.target?.status === "error", "启动失败");
    assert.equal(dave.state.target.error.message, "所选系统镜像缺失，请稍后重试。");
    assert.equal(dave.state.vms[0].system_id, "debian-12", "失败后系统不变");

    mock.failNext("POST", `/api/tenant/vms/${dave.state.vms[0].id}/open`, 503, "runtime_unavailable", "主机资源紧张，请稍后重试。");
    dave.portal.retry();
    await waitFor(() => dave.state.target?.status === "error" && dave.state.target.error.code === "runtime_unavailable", "资源不足");
    assert.equal(dave.state.target.error.message, "主机资源紧张，请稍后重试。");

    dave.portal.retry();
    await waitFor(() => dave.state.target?.status === "connecting", "重试成功");
    await dave.portal.logout();
  });

  test("同一浏览器两个标签页：另一页切换账户后，旧页的创建被 409 拒绝并同步到新账户", async () => {
    const shared = browser(mock); // 两个标签页共享同一个 Cookie 罐
    const tab1 = portalFor(mock, shared);
    const tab2 = portalFor(mock, shared);
    await tab1.portal.boot();
    tab1.portal.setAuthMode("register");
    await tab1.portal.register({ username: "frank", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => tab1.state.listStatus === "ready", "frank 列表");
    tab1.portal.openCreate();

    // 另一个标签页退出并以新账户登录。
    await tab2.portal.boot();
    assert.equal(tab2.state.user.username, "frank");
    await tab2.portal.logout();
    tab2.portal.setAuthMode("register");
    await tab2.portal.register({ username: "grace", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => tab2.state.listStatus === "ready", "grace 列表");

    const before = mock.state.vms.size;
    await tab1.portal.createVm("旧页面的新建");
    assert.equal(mock.state.vms.size, before, "旧页面的创建未写入任何账户");
    await waitFor(() => tab1.state.phase === "signed-in" && tab1.state.user?.username === "grace", "旧页面同步到当前账户");
    assert.match(tab1.state.banner.message, /grace/);
    assert.equal(tab1.state.create.open, false);
    await waitFor(() => tab1.state.listStatus === "ready", "grace 列表（旧页面）");
    assert.equal(tab1.state.vms.length, 0);

    // 再次检查会话：同一账户时不打断。
    await tab1.portal.checkSession();
    assert.equal(tab1.state.user.username, "grace");
    await tab2.portal.logout();
    await tab1.portal.checkSession();
    assert.equal(tab1.state.phase, "signed-out", "另一页退出后，重新检查即回到登录页");
  });

  test("注册关闭时显示状态且登录不受影响", async () => {
    mock.setRegistrationOpen(false);
    try {
      const client = browser(mock);
      const page = portalFor(mock, client);
      await page.portal.boot();
      page.portal.setAuthMode("register");
      await page.portal.register({ username: "henry", password: PASSWORD, confirm: PASSWORD });
      assert.equal(page.state.registrationClosed, true);
      assert.equal(page.state.phase, "signed-out");
      const attempts = () => mock.state.requests.filter((r) => r.path === "/api/tenant/register").length;
      const count = attempts();
      await page.portal.register({ username: "henry", password: PASSWORD, confirm: PASSWORD });
      assert.equal(attempts(), count, "不再尝试注册");
      page.portal.setAuthMode("login");
      await page.portal.login({ username: "alice", password: PASSWORD });
      assert.equal(page.state.phase, "signed-in");
      await waitFor(() => page.state.listStatus === "ready", "alice 列表");
      await page.portal.logout();
    } finally {
      mock.setRegistrationOpen(true);
    }
  });

  test("无可用系统时创建弹窗给出明确状态", async () => {
    const client = browser(mock);
    const erin = portalFor(mock, client);
    await erin.portal.boot();
    erin.portal.setAuthMode("register");
    await erin.portal.register({ username: "erin", password: PASSWORD, confirm: PASSWORD });
    await waitFor(() => erin.state.listStatus === "ready", "erin 列表");
    mock.setSystems([]);
    try {
      erin.portal.openCreate();
      await erin.portal.createVm("开发环境");
      assert.match(erin.state.create.error.message, /暂无可安装的系统/);
    } finally {
      mock.setSystems([{ id: "debian-12", label: "Debian 12" }, { id: "ubuntu-24-04", label: "Ubuntu 24.04 LTS" }]);
    }
    await erin.portal.logout();
  });
});
