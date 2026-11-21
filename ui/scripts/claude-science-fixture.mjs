// Mocked /api/claude-science for UI fixtures. Each fixture is a reply of the
// workbench contract documented in internal/workbench/claude_science.go;
// a Go test decodes every 200 body into the server's own type.
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
export const claudeScienceFixtures = require("./claude-science-fixtures.json");

// Install leaves the build stopped, start runs it signed out, stop stops it.
const transitions = { install: "stopped", update: "stopped", start: "running-signed-out", stop: "stopped" };

export function claudeScienceRoute(route, request, path, body, state) {
  const fixture = claudeScienceFixtures[state.claudeScience] || claudeScienceFixtures["not-installed"];
  if (path === "/claude-science" && request.method() === "GET") {
    if (fixture.status !== 200) return route.fulfill({ status: fixture.status, contentType: "text/plain; charset=utf-8", body: fixture.text });
    return route.fulfill({ json: fixture.body });
  }
  const action = path.slice("/claude-science/".length);
  if (request.method() !== "POST" || !["install", "update", "start", "stop", "open"].includes(action))
    return route.fulfill({ status: 404, body: "404 page not found" });
  const keys = Object.keys(body || {});
  if (keys.some(key => key !== "fresh") || (keys.length && action !== "open"))
    return route.fulfill({ status: 400, contentType: "text/plain; charset=utf-8", body: "请求格式无效" });
  if (fixture.status === 200 && !fixture.body.supported)
    return route.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: fixture.body.unsupported_reason });
  if (action === "open") {
    if (fixture.status !== 200 || !fixture.body.ready)
      return route.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: "Claude Science 尚未启动，请先启动后再打开。" });
    let tab = state.tabs.find(item => item.kind === "science");
    const reused = Boolean(tab) && body?.fresh !== true;
    if (!tab) {
      tab = { id: "science-" + ++state.sequence, kind: "science", title: "Claude Science", url: "http://localhost:47800/" };
      state.tabs.push(tab);
    }
    state.active = tab.id;
    return route.fulfill({ json: { id: tab.id, kind: "science", title: "Claude Science", reused } });
  }
  state.claudeScience = transitions[action];
  state.status.job = { id: "claude-science-" + ++state.sequence, kind: "claude-science-" + action, running: false };
  return route.fulfill({ json: state.status.job });
}
