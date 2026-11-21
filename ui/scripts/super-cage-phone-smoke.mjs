// Reads the one-time URL from stdin. Run by TestSuperCagePhoneInBrowser.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
const parts = [];
for await (const chunk of process.stdin) parts.push(chunk);
const entry = Buffer.concat(parts).toString().trim();
const engine = process.env.CAGE_TEST_MOBILE_BROWSER;
const playwright = await import(process.env.CAGE_PLAYWRIGHT_MODULE || "playwright");
const browser = await playwright[engine].launch();
const capture = process.env.CAGE_TEST_MOBILE_CAPTURE;
const shot = async (page, name) => { if (capture) { await mkdir(capture, { recursive: true }); await page.screenshot({ path: path.join(capture, engine + "-" + name + ".png") }); } };
let page;
try {
  page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, colorScheme: "light" });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(entry);
  // Home: greeting, project, recent session.
  await page.locator(".greeting").waitFor();
  await page.getByText("今天想在").waitFor();
  await page.getByRole("button", { name: "整理发布说明", exact: true }).waitFor();
  await shot(page, "home");
  // Drawer tools.
  await page.getByRole("button", { name: "展开侧栏", exact: true }).click();
  const drawer = page.getByRole("dialog", { name: "工作区侧栏" });
  for (const name of ["新对话", "终端", "文件", "环境"]) await drawer.getByRole("button", { name, exact: true }).waitFor();
  await drawer.getByRole("button", { name: "文件", exact: true }).click();
  // Files: list, preview, upload.
  await page.getByRole("button", { name: /^README\.md/ }).waitFor();
  await page.getByRole("button", { name: /^src/ }).click();
  await page.getByRole("button", { name: /^app\.py/ }).waitFor();
  await page.getByRole("button", { name: /^上一级/ }).click();
  await page.getByRole("button", { name: /^README\.md/ }).click();
  await page.getByText("Super Cage 测试项目").waitFor();
  await shot(page, "preview");
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await page.locator('.files-actions input[type="file"]').setInputFiles({ name: "photo.txt", mimeType: "text/plain", buffer: Buffer.from("uploaded from phone\n") });
  await page.getByRole("button", { name: /^photo\.txt/ }).waitFor({ timeout: 20000 });
  await page.getByRole("button", { name: "新建文件夹", exact: true }).click();
  await page.getByLabel("名称").fill("notes");
  await page.getByRole("button", { name: "创建", exact: true }).click();
  await page.getByRole("button", { name: /^notes/ }).waitFor();
  await shot(page, "files");
  // Environment: the test host has no running guest.
  await page.getByRole("button", { name: "展开侧栏", exact: true }).click();
  await drawer.getByRole("button", { name: "环境", exact: true }).click();
  await page.locator(".env-hero h1").waitFor();
  const title = await page.locator(".env-hero h1").innerText();
  assert.match(title, /环境/);
  await page.getByRole("button", { name: "切换节点", exact: true }).waitFor();
  await shot(page, "environment");
  // Terminals list renders.
  await page.getByRole("button", { name: "展开侧栏", exact: true }).click();
  await drawer.getByRole("button", { name: "终端", exact: true }).click();
  await page.getByText("在手机上直接操作环境里的终端").waitFor();
  await shot(page, "terminals");
  // Chat with attachment.
  await page.locator(".topbar").getByRole("button", { name: "新对话", exact: true }).click();
  await page.getByRole("button", { name: "整理发布说明", exact: true }).click();
  await page.getByText("发布说明已整理好").waitFor();
  await page.locator('.compose input[type="file"]').setInputFiles({ name: "screen.png", mimeType: "image/png", buffer: Buffer.from("89504e470d0a1a0a", "hex") });
  await page.locator(".attachment.done").waitFor({ timeout: 20000 });
  await shot(page, "chat");
  assert.deepEqual(errors, []);
  console.log(engine + " home, drawer, files (list, preview, upload, mkdir), environment, terminals and chat attachment passed");
} catch (error) {
  if (page) { await shot(page, "failure").catch(() => {}); console.error("page text:", (await page.locator("body").innerText().catch(() => "")).slice(0, 1200)); }
  throw error;
} finally {
  await browser.close();
}
