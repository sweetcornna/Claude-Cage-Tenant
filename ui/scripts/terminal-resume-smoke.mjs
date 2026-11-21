import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

// A terminal stream that drops resumes after the last received byte under the
// same xterm, without flashing a reconnect notice, and a silent stream is
// probed, dropped and resumed. The terminal socket is an isolated stand-in.
const fixture = await createWorkbench({ mockWebSockets: false, beforeNavigate: page => page.clock.install() });
const { browser, page, errors } = fixture;
const sockets = [];
const screen = () => page.evaluate(() => document.querySelector(".xterm-rows")?.textContent || "");
const until = async (condition, message) => {
  for (let i = 0; i < 400; i++) {
    if (await condition()) return;
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw new Error(message);
};
const replay = (entry, offset, reset, text) => {
  const data = Buffer.from(text);
  entry.socket.send(JSON.stringify({ type: "history", offset, reset, size: data.length }));
  entry.socket.send(data);
  return data.length;
};
try {
  await page.routeWebSocket("**/api/**", socket => {
    if (!socket.url().includes("/terminals/")) return socket.close();
    const entry = { socket, after: new URL(socket.url()).searchParams.get("after"), messages: [] };
    socket.onMessage(message => entry.messages.push(JSON.parse(message)));
    sockets.push(entry);
  });
  await page.reload();
  await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).click();
  await page.getByRole("button", { name: "新建终端", exact: true }).first().click();
  await page.getByRole("menuitem", { name: "普通终端", exact: true }).click();
  await until(() => sockets.length === 1, "terminal stream did not connect");
  assert.equal(sockets[0].after, "0", "a new view asks for the kept history");
  let offset = replay(sockets[0], 0, false, "LINE-A\r\n$ ");
  await until(async () => (await screen()).includes("LINE-A"), "history was not shown");
  await page.evaluate(() => {
    window.terminalNotes = [];
    new MutationObserver(() => {
      const note = document.querySelector(".terminal-note");
      if (note) window.terminalNotes.push(note.textContent);
    }).observe(document.body, { childList: true, subtree: true, characterData: true });
  });

  // A quick drop resumes after the received bytes and shows nothing.
  sockets[0].socket.close();
  await until(() => sockets.length === 2, "dropped stream did not reconnect");
  assert.equal(sockets[1].after, String(offset), "reconnect must resume after the received bytes");
  offset += replay(sockets[1], offset, false, "LINE-B\r\n$ ");
  await until(async () => (await screen()).includes("LINE-B"), "missed output was not shown");
  assert.equal((await screen()).split("LINE-A").length - 1, 1, "resumed output must not repeat the history");
  assert.deepEqual(await page.evaluate(() => window.terminalNotes), [], "a quick reconnect must not flash a notice");

  // The view pings a visible stream; the server's pong proves heartbeats.
  sockets[1].socket.send(JSON.stringify({ type: "heartbeat" }));
  await page.clock.fastForward(26_000);
  await until(() => sockets[1].messages.some(message => message.type === "ping"), "visible stream sent no keepalive");
  sockets[1].socket.send(JSON.stringify({ type: "pong" }));

  // A stream that goes silent is probed, then dropped and resumed.
  const pings = sockets[1].messages.filter(message => message.type === "ping").length;
  await page.clock.fastForward(46_000);
  await until(() => sockets[1].messages.filter(message => message.type === "ping").length > pings, "silent stream was not probed");
  await page.clock.fastForward(6_000);
  await until(() => sockets.length === 3, "silent stream was not replaced");
  assert.equal(sockets[2].after, String(offset));

  // An offset the server no longer keeps clears the screen before the tail.
  replay(sockets[2], 9_000_000, true, "FRESH\r\n$ ");
  await until(async () => (await screen()).includes("FRESH"), "reset tail was not shown");
  assert.ok(!(await screen()).includes("LINE-A"), "a reset must clear the previous screen");
  await until(async () => !(await page.locator(".terminal-note").count()), "notice stayed after reconnecting");

  // A session that exits ends the view instead of reconnecting.
  sockets[2].socket.send(JSON.stringify({ type: "exit" }));
  sockets[2].socket.close();
  await page.getByText("终端会话已结束。可点击项目右侧的终端图标新建终端。", { exact: true }).waitFor();
  await new Promise(resolve => setTimeout(resolve, 1500));
  assert.equal(sockets.length, 3, "an ended session must not reconnect");
  assert.equal(await page.getByRole("button", { name: "重新连接", exact: true }).count(), 0);
  assert.deepEqual(errors, []);
  console.log("PASS: terminal resumes after its offset, resets on a lost offset, probes silence and stops on exit");
} finally {
  await browser.close();
}
