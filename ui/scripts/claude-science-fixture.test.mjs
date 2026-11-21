import assert from "node:assert/strict";
import test from "node:test";
import { claudeScienceFixtures, claudeScienceRoute } from "./claude-science-fixture.mjs";

function call(state, method, path, body) {
  const route = { fulfill: value => value };
  const request = { method: () => method };
  return claudeScienceRoute(route, request, path, body, state);
}

const fresh = name => ({ claudeScience: name, tabs: [], sequence: 0, status: {} });

test("every state of the contract has a fixture", () => {
  assert.deepEqual(Object.keys(claudeScienceFixtures).sort(), ["error", "installing", "not-installed", "running-signed-in", "running-signed-out", "stopped", "unreachable", "unsupported-architecture", "update-required"]);
  assert.equal(call(fresh("running-signed-in"), "GET", "/claude-science").json.signed_in, true);
  const unreachable = call(fresh("unreachable"), "GET", "/claude-science");
  assert.equal(unreachable.status, 502);
  assert.match(unreachable.body, /Claude Science/);
});

test("actions follow the server's refusals", () => {
  assert.equal(call(fresh("not-installed"), "POST", "/claude-science/uninstall", {}).status, 404);
  assert.equal(call(fresh("not-installed"), "GET", "/claude-science/install").status, 404);
  assert.equal(call(fresh("not-installed"), "POST", "/claude-science/install", { url: "x" }).status, 400);
  assert.equal(call(fresh("not-installed"), "POST", "/claude-science/install", { fresh: true }).status, 400);
  const unsupported = call(fresh("unsupported-architecture"), "POST", "/claude-science/install", {});
  assert.equal(unsupported.status, 409);
  assert.match(unsupported.body, /arm64/);
  assert.equal(call(fresh("stopped"), "POST", "/claude-science/open", {}).status, 409);
});

test("install, start and open move through the states and reuse one tab", () => {
  const state = fresh("not-installed");
  assert.equal(call(state, "POST", "/claude-science/install", {}).json.kind, "claude-science-install");
  assert.equal(call(state, "GET", "/claude-science").json.installed, true);
  call(state, "POST", "/claude-science/start", {});
  assert.equal(call(state, "GET", "/claude-science").json.ready, true);
  const first = call(state, "POST", "/claude-science/open", {}).json;
  assert.deepEqual({ ...first, id: "" }, { id: "", kind: "science", title: "Claude Science", reused: false });
  assert.equal(state.active, first.id);
  const again = call(state, "POST", "/claude-science/open", {}).json;
  assert.equal(again.id, first.id);
  assert.equal(again.reused, true);
  assert.equal(call(state, "POST", "/claude-science/open", { fresh: true }).json.reused, false);
  assert.equal(state.tabs.length, 1);
  call(state, "POST", "/claude-science/stop", {});
  assert.equal(call(state, "GET", "/claude-science").json.running, false);
});
