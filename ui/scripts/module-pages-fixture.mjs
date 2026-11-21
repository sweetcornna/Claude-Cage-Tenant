// Populated data for reviewing the module pages (settings sub-pages, accounts,
// connections, browser chrome, initialization) in the isolated workbench
// fixture. Opt-in: call routeModulePages(page, state, options) from a
// fixture's beforeNavigate; nothing here changes workbench-fixture.mjs answers.
//
// options.variant: "full" (default, long names included), "empty" or "error".
// It is read on every request, so a script can switch it and reload.

const HOME = "/Users/shihao";
export const projects = {
  design: HOME + "/Projects/super-cage-design-language-and-desktop-workbench",
  brand: HOME + "/Projects/林间工作室-品牌官网与小程序重构计划",
  workspace: HOME + "/.claude-cage-workspace",
};

const text = (status, body) => ({ status, contentType: "text/plain; charset=utf-8", body });

export function vaultData(now) {
  return {
    entries: [
      { id: "v1", origin: "https://mail.linjian.example", username: "studio@linjian.example", updated_at: now },
      { id: "v2", origin: "https://github.com", username: "linjian-studio" },
      { id: "v3", origin: "https://console.cloud-provider-with-a-very-long-domain-name.example.com", username: "night.shift.automation.long-address@example.test" },
      { id: "v4", origin: "https://figma.com", username: "" },
      { id: "v5", origin: "https://app.netlify.com", username: "design@linjian.example" },
    ],
    blocked: ["https://bank.example.cn", "https://intranet.linjian.example"],
  };
}

export function archivedSessions(now) {
  const ago = (minutes) => new Date(now - minutes * 60000).toISOString();
  return [
    { id: "a1", workspace: projects.design, title: "梳理设置页的分组与保存栏行为", updated_at: ago(60 * 5), archived: true },
    { id: "a2", workspace: projects.brand, title: "Rewrite the onboarding copy for the mini-program landing page and align every illustration with the brand guide", updated_at: ago(60 * 30), archived: true },
    { id: "a3", workspace: projects.workspace, title: "你好", updated_at: ago(60 * 24 * 6), archived: true },
    { id: "a4", workspace: projects.design, title: "排查终端在窄窗口下的换行问题", updated_at: ago(60 * 24 * 12), archived: true },
  ];
}

export const proxyData = (running, now = Date.now()) => ({
  sources: [
    { id: "airport", name: "旅行订阅 · 月付套餐（每 12 小时自动更新）", remote: true, interval_hours: 12,
      host: "sub.travel-proxy.example", updated_at: new Date(now - 3 * 3600000).toISOString(),
      nodes: [
        { id: "la02", name: "🇺🇸 USA Los Angeles 02", type: "anytls" },
        { id: "tokyo", name: "Tokyo Transit", type: "trojan" },
        { id: "sg", name: "Singapore Premium IPLC 专线 · 低延迟 · 适合日常浏览与视频会议", type: "vless" },
        { id: "fra", name: "Frankfurt 01", type: "hysteria2" },
      ] },
    { id: "home", name: "住宅代理", remote: false, interval_hours: 0, updated_at: new Date(now - 4 * 86400000).toISOString(),
      nodes: [{ id: "res", name: "LoopsForge · AI 住宅 IP · 节点001", type: "vless" }] },
  ],
  selected: "la02",
  route: ["la02"],
  active: true,
  running: true,
  core_available: true,
  version: "mihomo 1.19.12",
  port: 17896,
  state: running ? "ready" : "prepared",
  state_label: running ? "已联网" : "待启动环境",
  network_open: Boolean(running),
  route_error: "",
});

export const proxyConnections = () => ({
  uploadTotal: 18_400_000,
  downloadTotal: 412_000_000,
  connections: [
    { id: "c1", host: "api.anthropic.com", port: 443, network: "tcp", upload: 182_000, download: 2_400_000 },
    { id: "c2", host: "statsig.anthropic.com", port: 443, network: "tcp", upload: 12_000, download: 48_000 },
    { id: "c3", host: "objects.githubusercontent.com", port: 443, network: "tcp", upload: 4_000, download: 86_000_000 },
    { id: "c4", host: "registry.npmjs.org", port: 443, network: "tcp", upload: 9_000, download: 3_100_000 },
  ],
});

export const proxyLogs = () => ({ lines: [
  "15:18:02 核心已启动 · mihomo 1.19.12 · 端口 17896",
  "15:18:03 已选择出口 🇺🇸 USA Los Angeles 02 (anytls)",
  "15:18:05 出口检查通过 · United States · Los Angeles",
  "15:19:40 订阅「旅行订阅」已更新 · 4 个节点",
  "15:20:11 DNS 经 https://cloudflare-dns.com/dns-query 解析",
] });

export const environmentVersions = () => ({
  controller: { version: "0.1.15 (b213406)" },
  components: { status: "update_required", update_required: true },
  tools: {
    chromium: { current: "142.0.7444.59", required: "142.0.7444.59" },
    code_server: { current: "4.104.3", required: "4.105.1" },
    claude: { current: "2.0.37", required: "2.0.42" },
    node: { current: "22.21.0", required: "22.21.0" },
    python: { current: "3.13.9", required: "3.13.9" },
    git: { current: "2.51.2", required: "2.51.2" },
    gh: { current: "", required: "2.82.1" },
    proxy: { current: "1.19.12", required: "1.19.12" },
  },
  tools_update_allowed: true,
});

export const cacheInventory = (now) => ({
  plan_id: "plan-1",
  notice: "盘点于刚才完成。只列出允许清单中的缓存，预览 10 分钟内有效。",
  items: [
    { id: "npm", title: "npm 下载缓存", location: "~/.npm/_cacache", reason: "可重新下载", bytes: 812 * 1024 * 1024, files: 18342, state: "ready" },
    { id: "pip", title: "pip 下载缓存", location: "~/.cache/pip", reason: "可重新下载", bytes: 164 * 1024 * 1024, files: 2210, state: "ready" },
    { id: "chromium", title: "浏览器缓存（不含登录与 Cookie）", location: "~/.config/chromium/Default/Cache/Cache_Data/with/a/very/long/path/segment", reason: "浏览器正在运行，暂不能清理", bytes: 96 * 1024 * 1024, files: 940, state: "busy" },
  ],
  protected: [
    { title: "Claude 登录资料", present: 2, unknown: 0 },
    { title: "浏览器 Cookie 与登录", present: 1, unknown: 1 },
  ],
  batches: [{ id: "b1", created_at: Math.floor(now / 1000) - 86400 * 2, count: 3 }],
});

export const resources = (now) => ({
  automatic: false, memory_gib: 8, cpus: 4, running: true, restart_required: true,
  recommended_memory_bytes: 8 * 2 ** 30, recommended_cpus: 4, max_memory_gib: 16, max_cpus: 8,
  configured_memory_bytes: 6 * 2 ** 30, configured_cpus: 3, resources_verified: true,
  observation: { observed_at: new Date(now).toISOString(), cpu_percent: 23.4, cpus: 3,
    guest: { total_bytes: 6 * 2 ** 30, available_bytes: 3.4 * 2 ** 30, oom_kills: 0 } },
});

export const aboutBuild = { version: "0.1.15", commit: "b213406c0ffee5e1d9a0c4c2f5b7d81a90e3f2aa", modified: false };
export const aboutCheck = (now) => ({
  build: aboutBuild,
  checkedAt: new Date(now).toISOString(),
  source: { status: "available", commit: "f737fcfb2d9e0c4b8a7d6e5f4c3b2a1908172635", aheadBy: 6 },
  release: { status: "current", version: "v0.1.15", prerelease: false },
});

// Registers the routes. Playwright tries the last registered route first, so
// these answer before the generic fixture handler.
export async function routeModulePages(page, state, options = {}) {
  const now = options.now || Date.now();
  const variant = () => options.variant || "full";
  const json = (route, value) => route.fulfill({ json: value });
  const failed = (route, message) => route.fulfill(text(503, message));
  await page.route("**/api/vault", (route) => {
    if (route.request().method() !== "GET") return json(route, { ok: true });
    if (variant() === "error") return failed(route, "密码保险箱暂时无法读取，请重新解锁 Super Cage 后再试");
    return json(route, variant() === "empty" ? { entries: [], blocked: [] } : vaultData(now));
  });
  await page.route(/\/api\/cli\/sessions\?archived=true$/, (route) => {
    if (variant() === "error") return failed(route, "无法读取已归档会话：会话服务暂时不可用");
    return json(route, { sessions: variant() === "empty" ? [] : archivedSessions(now), notice: "" });
  });
  await page.route("**/api/mobile-pair", (route) => json(route, variant() === "full"
    ? { paired: true, status: "", device: { model: "iPhone 16 Pro Max", platform: "ios" }, keep_awake: { active: true } }
    : { paired: false, status: "" }));
  await page.route("**/api/proxy", (route) => {
    if (route.request().method() !== "GET") return json(route, {});
    const value = proxyData(state.status.vm_running, now);
    if (variant() === "empty") Object.assign(value, { sources: [], selected: "", route: [], active: false, running: false, state_label: "未配置代理" });
    if (variant() === "error") {
      value.route_error = "代理核心拒绝该配置：节点 🇺🇸 USA Los Angeles 02 握手失败；原配置已保留";
      value.sources[0].error = "订阅更新失败：sub.travel-proxy.example 返回 403，已保留上次的 4 个节点";
      value.sources[0].skipped = [{ index: 5, name: "Legacy SS", type: "ss", reason: "不支持的加密方式" }];
    }
    return json(route, value);
  });
  await page.route("**/api/proxy/connections", (route) => json(route, variant() === "empty" ? { uploadTotal: 0, downloadTotal: 0, connections: [] } : proxyConnections()));
  await page.route("**/api/proxy/logs", (route) => json(route, proxyLogs()));
  await page.route("**/api/environment/versions", (route) => variant() === "error"
    ? failed(route, "无法读取环境版本：环境服务没有响应") : json(route, environmentVersions()));
  await page.route("**/api/environment/cache/inventory", (route) => variant() === "error"
    ? failed(route, "测试：请重新盘点，已有资料保留。") : json(route, variant() === "empty" ? { ...cacheInventory(now), items: [], batches: [] } : cacheInventory(now)));
  await page.route("**/api/environment/resources", (route) => route.request().method() === "GET"
    ? json(route, resources(now)) : json(route, { ok: true }));
  await page.route("**/api/diagnostics", (route) => json(route, { bytes: 12.4 * 1024 * 1024, files: 37 }));
  await page.route("**/api/environment/storage", (route) => json(route, { directory: "/Volumes/Studio SSD/Super Cage/环境", has_environment: true, backup: "/Users/shihao/Library/Application Support/Super Cage/backup-2026-11-18" }));
  await page.route("**/api/client/about", (route) => {
    if (route.request().method() === "GET") return json(route, aboutBuild);
    if (variant() === "error") return failed(route, "无法连接 GitHub：主机网络与 Super Cage 代理均未能完成请求");
    return json(route, aboutCheck(now));
  });
  for (const extra of options.extraRoutes || []) await extra(page, state, { now, variant });
}
