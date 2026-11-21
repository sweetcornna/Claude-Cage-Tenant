import test from "node:test";
import assert from "node:assert/strict";
import { keepSame, reuseItems, sameValue, terminalListDue, terminalListRefreshPolls } from "./poll-state.mjs";

test("values compare by content", () => {
  assert.equal(sameValue({ a: 1, b: [1, { c: "x" }], d: null }, { d: null, b: [1, { c: "x" }], a: 1 }), true);
  assert.equal(sameValue({ a: 1 }, { a: 1, b: undefined }), false);
  assert.equal(sameValue([1, 2], { 0: 1, 1: 2 }), false);
  assert.equal(sameValue({ job: { running: false } }, { job: { running: true } }), false);
  assert.equal(sameValue(null, {}), false);
});

test("an unchanged status keeps its reference", () => {
  const previous = { ready: true, job: { id: "j" } };
  assert.equal(keepSame(previous, { ready: true, job: { id: "j" } }), previous);
  const next = { ready: false, job: { id: "j" } };
  assert.equal(keepSame(previous, next), next);
});

test("unchanged tabs keep their objects and the list", () => {
  const a = { id: "terminal:a", title: "one", closed: false }, b = { id: "web:b", title: "page" };
  const previous = [a, b];
  assert.equal(reuseItems(previous, [{ ...a }, { ...b }]), previous);
  const renamed = reuseItems(previous, [{ ...a, title: "two" }, { ...b }]);
  assert.notEqual(renamed, previous);
  assert.notEqual(renamed[0], a);
  assert.equal(renamed[1], b);
  const reordered = reuseItems(previous, [{ ...b }, { ...a }]);
  assert.deepEqual(reordered, [b, a]);
  assert.equal(reordered[0], b);
  assert.deepEqual(reuseItems(previous, [{ ...a }]), [a]);
});

test("the terminal list is read only when it may have changed", () => {
  const steady = { cached: true, revision: "r1", lastRevision: "r1", polls: 1 };
  assert.equal(terminalListDue(steady), false);
  assert.equal(terminalListDue({ ...steady, force: true }), true);
  assert.equal(terminalListDue({ ...steady, cached: false }), true);
  assert.equal(terminalListDue({ ...steady, revision: "r2" }), true);
  assert.equal(terminalListDue({ ...steady, revision: undefined }), true);
  assert.equal(terminalListDue({ ...steady, polls: terminalListRefreshPolls }), true);
});
