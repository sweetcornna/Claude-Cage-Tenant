// The terminal on the WebGL renderer: chosen automatically in Chromium, same
// grid metrics as the DOM renderer, IME commits, selection and copy, links,
// and a lost WebGL context falling back to the DOM renderer with its text.
import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const E = "\u001b[";
const screenText = "\r\n" + E + "1mCage" + E + "0m  ·  " + E + "32mgreen" + E + "0m " + E + "31mred" + E + "0m " + E + "34mblue" + E + "0m\r\nCOPY-THIS-LINE 复制这一行\r\nDocs at \u001b]8;;https://example.test/docs\u0007the docs\u001b]8;;\u0007 here\r\n$ ";

async function open(renderer) {
  process.env.CAGE_TERMINAL_RENDERER = renderer;
  const inputs = [], resizes = [], copies = [];
  const fixture = await createWorkbench({
    mockWebSockets: false,
    nativeRequest({ action, text }) { if (action === "clipboardWrite") copies.push(text); },
    async beforeNavigate(page) {
      await page.routeWebSocket("**/api/**", socket => {
        if (!socket.url().includes("/terminals/")) return socket.close();
        socket.onMessage(message => {
          const value = JSON.parse(message);
          if (value.type === "input") { inputs.push(value.data); if (value.data === "\r") socket.send(Buffer.from("\r\nINPUT_DONE\r\n$ ")); }
          if (value.type === "resize") resizes.push([value.cols, value.rows]);
        });
        socket.send(Buffer.from(screenText));
      });
    },
  });
  const { page } = fixture;
  await page.getByRole("button", { name: "启动 Super Cage 环境", exact: true }).click();
  await page.getByRole("button", { name: "新建终端", exact: true }).first().click();
  await page.getByRole("menuitem", { name: "普通终端", exact: true }).click();
  await page.locator(".terminal-view:not(.concealed) .xterm").waitFor();
  await page.waitForFunction(() => window.__cagePerf?.terminals?.length > 0);
  return { ...fixture, inputs, resizes, copies };
}

const renderer = (page) => page.evaluate(() => window.__cagePerf.terminals.at(-1).renderer);
let dom;
try {
  dom = await open("dom");
  await dom.page.locator(".xterm-rows").getByText("COPY-THIS-LINE").waitFor();
  await dom.page.waitForTimeout(300);
  assert.equal(await renderer(dom.page), "dom");
  const domSize = dom.resizes.at(-1);
  await dom.browser.close();
  dom = null;

  const { page, browser, errors, inputs, resizes, copies } = await open("auto");
  try {
    await page.waitForFunction(() => window.__cagePerf.terminals.at(-1).renderer === "webgl");
    assert.equal(await page.locator(".xterm-rows").count(), 0, "WebGL draws the grid instead of DOM rows");
    assert.ok(await page.locator(".terminal-view:not(.concealed) .xterm-screen canvas").count() >= 1);
    await page.waitForTimeout(300);
    assert.deepEqual(resizes.at(-1), domSize, "the same font metrics give the same grid as the DOM renderer");

    // Selection and copy: drag over the second line and copy it.
    const screen = await page.locator(".terminal-view:not(.concealed) .xterm-screen").boundingBox();
    const [cols, rows] = resizes.at(-1);
    const cell = { width: screen.width / cols, height: screen.height / rows };
    await page.mouse.move(screen.x + 1, screen.y + cell.height * 2.5);
    await page.mouse.down();
    await page.mouse.move(screen.x + cell.width * 14 - 1, screen.y + cell.height * 2.5, { steps: 5 });
    await page.mouse.up();
    await page.keyboard.press("Meta+c");
    await page.waitForFunction(() => true);
    for (let i = 0; i < 40 && !copies.length; i++) await page.waitForTimeout(25);
    assert.deepEqual(copies, ["COPY-THIS-LINE"], "a selection on the WebGL renderer copies its text");

    // A hyperlink (OSC 8) opens behind the usual confirmation.
    const dialogs = [];
    page.on("dialog", dialog => { dialogs.push(dialog.message()); void dialog.dismiss(); });
    await page.mouse.move(screen.x + cell.width * 10.5, screen.y + cell.height * 3.5);
    await page.waitForTimeout(150);
    await page.mouse.down();
    await page.mouse.up();
    for (let i = 0; i < 40 && !dialogs.length; i++) await page.waitForTimeout(25);
    assert.ok(dialogs.some(text => text.includes("https://example.test/docs")), "a link is detected and activated");

    // IME direct commits reach the shell once each, as on the DOM renderer.
    const textarea = page.locator(".terminal-view:not(.concealed) .xterm-helper-textarea");
    await textarea.focus();
    await textarea.evaluate(async el => {
      const key = (type, key, keyCode) => el.dispatchEvent(new KeyboardEvent(type, { key, keyCode, shiftKey: true, bubbles: true, cancelable: true, composed: true }));
      for (const mark of ["（", "）"]) {
        key("keydown", "Shift", 16);
        key("keydown", "Process", 229);
        const init = { inputType: "insertText", data: mark, bubbles: true, composed: true };
        el.dispatchEvent(new InputEvent("beforeinput", init));
        el.value += mark;
        el.dispatchEvent(new InputEvent("input", init));
        await new Promise(resolve => setTimeout(resolve, 0));
        key("keyup", "Process", 229);
        key("keyup", "Shift", 16);
      }
    });
    await page.keyboard.press("a");
    await page.keyboard.press("Enter");
    for (let i = 0; i < 40 && !inputs.includes("\r"); i++) await page.waitForTimeout(25);
    assert.deepEqual(inputs, ["（", "）", "a", "\r"]);

    // A lost context returns the terminal to the DOM renderer with its text.
    await page.evaluate(() => {
      for (const canvas of document.querySelectorAll(".terminal-view:not(.concealed) .xterm-screen canvas")) {
        const context = canvas.getContext("webgl2");
        context?.getExtension("WEBGL_lose_context")?.loseContext();
      }
    });
    await page.waitForFunction(() => window.__cagePerf.terminals.at(-1).renderer === "dom", null, { timeout: 10000 });
    await page.locator(".xterm-rows").getByText("INPUT_DONE").waitFor();
    assert.ok((await page.locator(".xterm-rows").textContent()).includes("COPY-THIS-LINE"));
    assert.deepEqual(errors, []);
    console.log("PASS: WebGL chosen in Chromium, same grid as DOM, selection copy, links, IME commits, and DOM fallback after a lost context");
  } finally { await browser.close(); }
} finally { await dom?.browser.close(); }
