import test from "node:test";
import assert from "node:assert/strict";
import { terminalKeepaliveMessage, terminalReconnectLimit, terminalReconnectPlan } from "./terminal-reconnect.mjs";

test("a stream dropped after being open reconnects at once and resets the count", () => {
  assert.deepEqual(terminalReconnectPlan(4, 60_000), { failures: 1, delay: 500 });
});

test("quick failures back off and stop at the limit", () => {
  let failures = 0;
  const delays = [];
  for (;;) {
    const plan = terminalReconnectPlan(failures, 0);
    failures = plan.failures;
    if (!plan.delay) break;
    delays.push(plan.delay);
  }
  assert.deepEqual(delays, [500, 1000, 2000, 4000, 8000, 15000]);
  assert.equal(delays.length, terminalReconnectLimit);
});

test("the keepalive is a message the terminal stream ignores", () => {
  assert.deepEqual(JSON.parse(terminalKeepaliveMessage), { type: "ping" });
});
