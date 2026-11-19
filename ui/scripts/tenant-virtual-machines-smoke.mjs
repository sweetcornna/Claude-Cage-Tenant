// Run after npm --prefix ui run build. The portal and workbench bundle are
// real; the VM runtime and workbench services are local test fixtures.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { startMockServer } from "../tenant/mock-server.mjs";

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.CAGE_PLAYWRIGHT_MODULE || "playwright");
const user = { username: "testowner", password: "fixture password only" };
const mock = await startMockServer({
  workbenchDir: fileURLToPath(new URL("../../internal/workbench/web/", import.meta.url)),
  bootDelayMs: 150,
});
try {
  const browser = await chromium.launch({ executablePath: process.env.CAGE_TEST_BROWSER_PATH });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 850 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(`${message.location().url}: ${message.text()}`);
    });
    const post = (path, data) => page.request.post(mock.origin + path, {
      data, headers: { Origin: mock.origin },
    });
    assert.equal((await post("/api/tenant/register", user)).status(), 201);
    const first = await (await post("/api/tenant/vms", { name: "开发环境" })).json();
    await page.goto(mock.origin);
    await page.locator(".vm-card").filter({ hasText: "开发环境" }).click();

    const workbench = page.frameLocator("iframe");
    const openInventory = async () => {
      try {
        await workbench.getByRole("button", { name: "虚拟机", exact: true }).click();
      } catch (error) {
        const pages = await Promise.all(page.frames().map((frame) => frame.locator("body").innerText({ timeout: 1000 }).catch(() => "")));
        throw new Error(`Sidebar unavailable\n${pages.join("\n")}\n${errors.join("\n")}`, { cause: error });
      }
      return workbench.getByRole("dialog", { name: "虚拟机" });
    };
    const dialog = await openInventory();
    assert.equal(await dialog.getByRole("button", { name: /开发环境.*当前虚拟机/ }).isDisabled(), true);
    assert.equal(await dialog.locator("input").count(), 1);
    const name = dialog.getByRole("textbox", { name: "虚拟机名称" });
    await name.fill("😀".repeat(65));
    await name.press("Enter");
    assert.match(await dialog.getByRole("alert").textContent(), /64 个字符/);
    await name.fill("  预览环境  ");
    await name.press("Enter");
    const second = dialog.getByRole("button", { name: "预览环境", exact: true });
    await second.waitFor();
    const vm = [...mock.state.vms.values()].find((item) => item.name === "预览环境");
    assert.equal(vm.state, "stopped", "creation does not boot the VM");
    assert.equal(vm.owner, mock.state.vms.get(first.vm.id).owner);
    assert.equal(vm.system_id, first.vm.system_id);
    if (process.env.TENANT_VM_DIALOG_SCREENSHOT) {
      await page.screenshot({ path: process.env.TENANT_VM_DIALOG_SCREENSHOT, animations: "disabled" });
    }
    const oldFrame = page.frames().find((frame) => frame.url() === `${mock.vmOrigin(first.vm.id)}/`);
    assert.ok(oldFrame);

    await page.evaluate((id) => window.postMessage({ type: "cage:open-virtual-machine", vm_id: id }, location.origin), vm.id);
    await second.click();
    await page.waitForURL(mock.origin + "/");
    await page.waitForFunction((origin) => document.querySelector("iframe")?.src.startsWith(`${origin}/_tenant/enter#`), mock.vmOrigin(vm.id));
    assert.equal(oldFrame.isDetached(), true);
    assert.equal(await page.locator("iframe").count(), 1);
    assert.ok(mock.state.requests.some((request) => request.host === mock.vmHost(first.vm.id) && request.path === `/api/client/virtual-machines/${vm.id}/open`));
    assert.ok(mock.state.requests.some((request) => request.host === mock.portalHost && request.path === `/api/tenant/vms/${vm.id}/open`));
    const afterSwitch = await openInventory();
    assert.equal(await afterSwitch.getByRole("button", { name: /预览环境.*当前虚拟机/ }).isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log("PASS: name-only VM creation and cross-origin switch from the real workbench sidebar");
  } finally {
    await browser.close();
  }
} finally {
  await mock.close();
}
