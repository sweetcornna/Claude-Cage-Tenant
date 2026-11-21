// Mocked Codex sessions for UI fixtures, one per owner state. Replies follow the
// workbench contract (internal/workbench/codex_sessions.go, mobile_cli_http.go):
// the desktop list carries Codex rows and "capabilities" only when asked with
// ?providers=claude,codex; /open on a desktop path binds a Codex terminal.
const defaultProject = "/Users/shihao/.claude-cage-workspace";
const at = "2026-11-20T10:00:00Z";
const turn = (id, prompt, text, status = "completed") => ({ id, prompt, text, status });

export function codexSessionFixtures(project = defaultProject) {
  return [
    { id: "c0d3c0d3-0000-4000-8000-000000000001", provider: "codex", workspace: project, title: "整理登录流程", owner: "", running: false, active: false,
      codex_account_id: "", account_label: "", pinned: false, revision: 3, generation: "fixture", updated_at: at,
      turns: [turn("c0d3c0d3-1000-4000-8000-000000000001", "看看登录页", "已检查登录页。")] },
    { id: "c0d3c0d3-0000-4000-8000-000000000002", provider: "codex", workspace: project, title: "电脑上打开的会话", owner: "desktop", running: true, active: true,
      codex_account_id: "aaaaaaaaaaaaaaaaaaaaaaaa", account_label: "工作", pinned: false, revision: 5, generation: "fixture", updated_at: at,
      turns: [turn("c0d3c0d3-1000-4000-8000-000000000002", "重构样式", "", "running")] },
    { id: "c0d3c0d3-0000-4000-8000-000000000003", provider: "codex", workspace: project, title: "手机上运行的一轮", owner: "phone", running: true, active: true,
      codex_account_id: "", account_label: "", pinned: true, revision: 7, generation: "fixture", updated_at: at,
      turns: [turn("c0d3c0d3-1000-4000-8000-000000000003", "跑一遍测试", "", "running")] },
    { id: "c0d3c0d3-0000-4000-8000-000000000004", provider: "codex", workspace: project, title: "其他进程中打开", owner: "external", running: true, active: false,
      codex_account_id: "", account_label: "", pinned: false, revision: 2, generation: "fixture", updated_at: at,
      turns: [turn("c0d3c0d3-1000-4000-8000-000000000004", "外部终端里的问题", "回答。")] },
  ];
}

const notices = {
  desktop: "这段会话正在电脑的 Codex 终端中打开，手机上可以查看和停止本轮。",
  external: "这段会话正在电脑上的 Codex 终端中打开。请先退出该终端中的 Codex，再从手机继续。",
};
const refusals = {
  phone: "手机发起的一轮仍在运行，完成后再在终端中继续。",
  external: "这段会话正在其他 Codex 进程中打开，请先退出该进程后再继续。",
};
const summary = ({ turns, ...row }) => row;
const text = (route, status, body) => route.fulfill({ status, contentType: "text/plain; charset=utf-8", body });

function view(session) {
  return { ...session, can_send: session.owner === "", terminal_available: undefined, connection_notice: notices[session.owner] || "",
    desktop_terminal_id: session.terminal_id || undefined };
}

// Serves /api/cli/sessions* for a workbench fixture. state.codexSessions holds
// the sessions; tests change an owner there to move a session between states.
export function codexSessionsRoute(route, request, url, body, state) {
  const path = url.pathname.replace(/^\/api/, "");
  const sessions = state.codexSessions || (state.codexSessions = codexSessionFixtures(state.status?.projects?.[0]));
  if (path === "/cli/sessions" && request.method() === "GET") {
    const providers = url.searchParams.getAll("providers");
    if (providers.length > 1 || providers.length === 1 && providers[0].split(",").some((name) => !["claude", "codex"].includes(name)))
      return text(route, 400, "会话类型无效");
    const archived = url.searchParams.get("archived") === "true";
    const codex = providers.length === 1 && providers[0].split(",").includes("codex");
    const answer = { sessions: codex ? sessions.filter((item) => Boolean(item.archived) === archived).map(summary) : [], notice: "", revision: "0".repeat(32) };
    if (codex) answer.capabilities = { codex_sessions: true, codex_cli: { installed: true, complete: true, version: "0.171.0", resume: true, no_daemon: true, resume_no_daemon: true, delete: true } };
    return route.fulfill({ json: answer });
  }
  const match = path.match(/^\/cli\/sessions\/([^/]+)(?:\/([a-z]+))?$/);
  const session = match && sessions.find((item) => item.id === match[1]);
  if (!session) return text(route, 404, "404 page not found");
  const action = match[2] || "";
  if (!action && request.method() === "GET") return route.fulfill({ json: view(session) });
  if (request.method() !== "POST") return text(route, 405, "Method Not Allowed");
  if (action === "account") return route.fulfill({ json: { session: { id: session.id }, supported: false, candidates: [] } });
  if (action === "open") {
    if (session.archived) return text(route, 410, "此会话已归档，请先在设置中的已归档会话撤销归档");
    if (refusals[session.owner]) return text(route, 409, refusals[session.owner]);
    let terminal = state.terminals.find((item) => item.cli_session_id === session.id && !item.closed);
    if (!terminal) {
      terminal = { id: "term-" + ++state.sequence, title: session.title, workspace: session.workspace, kind: "codex", closed: false,
        cli_session_id: session.id, cli_session_provider: "codex", codex_account_id: session.codex_account_id, cli_starting: false };
      state.terminals.push(terminal);
    }
    Object.assign(session, { owner: "desktop", running: true, terminal_id: terminal.id });
    return route.fulfill({ json: view(session) });
  }
  if (action === "stop") {
    const last = session.turns.at(-1);
    if (!last || last.status !== "running" || last.id !== body?.request_id) return text(route, 409, "本轮运行状态已变化，请重新读取");
    if (session.owner === "external") return text(route, 409, "这一轮不在 Super Cage 的终端中运行，请在运行它的终端中停止");
    last.status = "stopped";
    Object.assign(session, { active: false, owner: session.owner === "phone" ? "" : session.owner, running: session.owner === "desktop" });
    return route.fulfill({ json: view(session) });
  }
  if (action === "archive" || action === "restore") {
    session.archived = action === "archive";
    if (session.archived) {
      state.terminals = state.terminals.filter((item) => item.cli_session_id !== session.id);
      Object.assign(session, { owner: "", running: false, active: false, terminal_id: undefined });
    }
    return route.fulfill({ json: { ok: true } });
  }
  return text(route, 404, "404 page not found");
}
