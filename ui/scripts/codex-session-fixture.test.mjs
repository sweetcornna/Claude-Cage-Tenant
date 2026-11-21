import assert from "node:assert/strict";
import test from "node:test";
import { codexSessionFixtures, codexSessionsRoute } from "./codex-session-fixture.mjs";

function call(state, method, path, body) {
  const route = { fulfill: (value) => value };
  const request = { method: () => method };
  return codexSessionsRoute(route, request, new URL("http://fixture" + path), body, state);
}
const fresh = () => ({ terminals: [], sequence: 0 });
const ids = codexSessionFixtures().map((session) => session.id);

test("one fixture per owner state", () => {
  assert.deepEqual(codexSessionFixtures().map((session) => session.owner), ["", "desktop", "phone", "external"]);
  const state = { ...fresh(), status: { projects: ["/p"] } };
  call(state, "GET", "/api/cli/sessions?providers=claude,codex");
  assert.ok(state.codexSessions.every((session) => session.workspace === "/p"), "sessions sit in the fixture's project");
  for (const session of codexSessionFixtures()) assert.equal(session.terminal_available, undefined);
});

test("the list honours providers like the server", () => {
  const state = fresh();
  const plain = call(state, "GET", "/api/cli/sessions").json;
  assert.deepEqual(plain.sessions, []);
  assert.equal("capabilities" in plain, false);
  const both = call(state, "GET", "/api/cli/sessions?providers=claude,codex").json;
  assert.equal(both.sessions.length, 4);
  assert.equal(both.sessions[0].turns, undefined, "rows carry no turns");
  assert.equal(both.capabilities.codex_sessions, true);
  assert.equal(call(state, "GET", "/api/cli/sessions?providers=gemini").status, 400);
  assert.equal(call(state, "GET", "/api/cli/sessions?providers=claude,codex&archived=true").json.sessions.length, 0);
});

test("open binds one terminal and refuses another writer", () => {
  const state = fresh();
  const opened = call(state, "POST", "/api/cli/sessions/" + ids[0] + "/open", {}).json;
  assert.equal(opened.owner, "desktop");
  assert.equal(state.terminals.length, 1);
  assert.equal(state.terminals[0].cli_session_provider, "codex");
  assert.equal(opened.desktop_terminal_id, state.terminals[0].id);
  assert.equal(call(state, "POST", "/api/cli/sessions/" + ids[0] + "/open", {}).json.desktop_terminal_id, state.terminals[0].id);
  assert.equal(state.terminals.length, 1);
  const phone = call(state, "POST", "/api/cli/sessions/" + ids[2] + "/open", {});
  assert.equal(phone.status, 409);
  assert.match(phone.body, /手机/);
  assert.equal(call(state, "POST", "/api/cli/sessions/" + ids[3] + "/open", {}).status, 409);
});

test("stop, archive and the read-only view", () => {
  const state = fresh();
  const phone = call(state, "GET", "/api/cli/sessions/" + ids[2]).json;
  assert.equal(phone.can_send, false);
  const turn = phone.turns.at(-1).id;
  assert.equal(call(state, "POST", "/api/cli/sessions/" + ids[2] + "/stop", { request_id: "stale" }).status, 409);
  const stopped = call(state, "POST", "/api/cli/sessions/" + ids[2] + "/stop", { request_id: turn }).json;
  assert.equal(stopped.owner, "");
  assert.equal(stopped.can_send, true);
  call(state, "POST", "/api/cli/sessions/" + ids[1] + "/open", {});
  call(state, "POST", "/api/cli/sessions/" + ids[1] + "/archive", {});
  assert.equal(state.terminals.length, 0);
  assert.equal(call(state, "POST", "/api/cli/sessions/" + ids[1] + "/open", {}).status, 410);
});
