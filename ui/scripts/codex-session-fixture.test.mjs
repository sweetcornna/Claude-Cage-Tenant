import assert from "node:assert/strict";
import test from "node:test";
import { codexSessionFixtures, codexSessionsRoute, seedCodexSessions } from "./codex-session-fixture.mjs";

async function call(state, method, path, body) {
  const route = { fulfill: (value) => value };
  const request = { method: () => method };
  return codexSessionsRoute(route, request, new URL("http://fixture" + path), body, state);
}
const json = async (...args) => (await call(...args)).json;
const fresh = () => ({ terminals: [], sequence: 0, codexHoldMs: 20 });
const ids = codexSessionFixtures().map((session) => session.id);
const list = "/api/cli/sessions?providers=claude,codex";

test("one fixture per owner state, the desktop one with its terminal open", () => {
  assert.deepEqual(codexSessionFixtures().map((session) => session.owner), ["", "desktop", "phone", "external"]);
  for (const session of codexSessionFixtures()) assert.equal(session.terminal_available, undefined);
  const state = { ...fresh(), status: { projects: ["/p"] } };
  seedCodexSessions(state);
  assert.ok(state.codexSessions.every((session) => session.workspace === "/p"), "sessions sit in the fixture's project");
  assert.deepEqual(state.terminals.map((tab) => [tab.cli_session_id, tab.cli_session_provider, tab.kind]), [[ids[1], "codex", "codex"]]);
  assert.equal(state.codexSessions[1].terminal_id, state.terminals[0].id);
  seedCodexSessions(state);
  assert.equal(state.terminals.length, 1, "seeding twice changes nothing");
});

test("the list honours providers like the server", async () => {
  const state = fresh();
  const plain = await json(state, "GET", "/api/cli/sessions");
  assert.deepEqual(plain.sessions, []);
  assert.equal("capabilities" in plain, false);
  const both = await json(state, "GET", list);
  assert.equal(both.sessions.length, 4);
  assert.equal(both.sessions[0].turns, undefined, "rows carry no turns");
  assert.equal(both.capabilities.codex_sessions, true);
  assert.equal((await call(state, "GET", "/api/cli/sessions?providers=gemini")).status, 400);
  assert.equal((await json(state, "GET", list + "&archived=true")).sessions.length, 0);
});

test("the revision follows the list and a held list waits", async () => {
  const state = fresh();
  const first = await json(state, "GET", list);
  assert.match(first.revision, /^[0-9a-f]{32}$/);
  const started = Date.now();
  const held = await call(state, "GET", list + "&after=" + first.revision + "&wait=25");
  assert.equal(held.status, 204);
  assert.ok(Date.now() - started >= 15, "an unchanged held list waits");
  assert.equal((await call(state, "GET", list + "&wait=25")).status, 400);
  state.codexSessions[2].owner = "";
  const changed = await json(state, "GET", list + "&after=" + first.revision + "&wait=25");
  assert.notEqual(changed.revision, first.revision);
  assert.notEqual((await json(state, "GET", "/api/cli/sessions")).revision, first.revision, "the Claude-only list differs");
});

test("open binds one terminal and refuses another writer", async () => {
  const state = fresh();
  const opened = await json(state, "POST", "/api/cli/sessions/" + ids[0] + "/open", {});
  assert.equal(opened.owner, "desktop");
  const tab = state.terminals.find((item) => item.cli_session_id === ids[0]);
  assert.equal(tab.cli_session_provider, "codex");
  assert.equal(opened.desktop_terminal_id, tab.id);
  assert.equal((await json(state, "POST", "/api/cli/sessions/" + ids[0] + "/open", {})).desktop_terminal_id, tab.id);
  assert.equal((await json(state, "POST", "/api/cli/sessions/" + ids[1] + "/open", {})).desktop_terminal_id, state.codexSessions[1].terminal_id);
  assert.equal(state.terminals.length, 2);
  const phone = await call(state, "POST", "/api/cli/sessions/" + ids[2] + "/open", {});
  assert.equal(phone.status, 409);
  assert.match(phone.body, /手机/);
  assert.equal((await call(state, "POST", "/api/cli/sessions/" + ids[3] + "/open", {})).status, 409);
});

test("stop, archive and the read-only view", async () => {
  const state = fresh();
  const phone = await json(state, "GET", "/api/cli/sessions/" + ids[2]);
  assert.equal(phone.can_send, false);
  const turn = phone.turns.at(-1).id;
  assert.equal((await call(state, "POST", "/api/cli/sessions/" + ids[2] + "/stop", { request_id: "stale" })).status, 409);
  const stopped = await json(state, "POST", "/api/cli/sessions/" + ids[2] + "/stop", { request_id: turn });
  assert.equal(stopped.owner, "");
  assert.equal(stopped.can_send, true);
  await call(state, "POST", "/api/cli/sessions/" + ids[1] + "/archive", {});
  assert.equal(state.terminals.length, 0);
  assert.equal((await call(state, "POST", "/api/cli/sessions/" + ids[1] + "/open", {})).status, 410);
});
