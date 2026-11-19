# Cage Tenant（macOS 客户端）

Cage Tenant 是多租户门户的轻量 macOS 客户端。它校验管理员部署的 HTTPS 门户地址，并在自己的窗口里用 WKWebView 显示完整门户。注册、登录、创建和切换虚拟机，以及门户内嵌的虚拟机工作台，都在这个窗口中进行。

- 应用：`Cage Tenant.app`，bundle ID `io.github.sweetcornna.claude-cage.tenant`
- 系统：macOS 14 或更新版本。通用二进制，同时支持 Apple Silicon（arm64）和 Intel（x86_64）
- 许可证：MIT，见 [LICENSE](LICENSE)

## 边界

| 会做 | 不会做 |
| --- | --- |
| 校验并保存一个门户根地址 | 保存账户、密码、Cookie、会话或票据 |
| 在本应用窗口中显示门户及其内嵌的虚拟机工作台 | 调用默认浏览器或其他应用打开门户（启动页的“本地安装”只会打开上游 Cage 或其安装包） |
| 每个门户窗口使用独立的内存网站数据（非持久） | 把网站数据写入磁盘，或在应用代码中读取 Cookie、凭据或页面内容 |
| 证书交给系统按默认规则校验 | 处理认证质询、信任自签名证书或绕过证书错误 |
| 在 App Sandbox 中运行，只申请 `network.client` | 申请文件、摄像头、麦克风或监听端口等权限；自动更新；后台常驻 |
| 与原版 Cage 并存（名称和 bundle ID 都不同） | 替换或修改原版 Cage，或在 Mac 上运行 Incus 或虚拟机 |

门户和虚拟机运行在管理员的 Linux Incus 服务器上，部署方法见 [deploy/tenant/README.md](../../deploy/tenant/README.md)，接口见 [docs/api-contract.md](../../docs/api-contract.md)。

### 门户窗口的规则

实现位于 `PortalPolicy.swift` 和 `PortalWebWindow.swift`。

- **主框架**只能停留在精确的门户源上：协议、域名和端口三者都相同。跳到其他源会被阻止，包括服务器重定向、其他端口、`http:`、门户子域、`about:`、`data:`、`blob:` 和 `javascript:`。每次跳转在请求前和收到响应时各检查一次。窗口底部会提示被阻止的源，提示只包含协议、域名和端口。
- **子框架（iframe）**可以加载门户源，以及工作台源 `https://vm-<32 位小写十六进制>.<门户域名>[:同一端口]`，也允许 `about:blank` 和 `about:srcdoc`。其他来源都会被阻止。门户自己的 CSP（`frame-src`）和 iframe sandbox 仍然有效，客户端只会更严格。
- **弹出窗口**：`window.open`、`target="_blank"` 等都会被拒绝（`javaScriptCanOpenWindowsAutomatically = false`，并且 `createWebViewWith` 返回 `nil`）。
- **没有原生桥接**：不注册 script message handler，不注入脚本，不执行 JavaScript，不使用 content world，也不访问 `httpCookieStore`。`test.sh` 会扫描源码，确保没有这些 API。
- **TLS**：没有实现认证质询回调，WebKit 使用系统默认校验。证书错误会作为加载失败显示，并提供“重新加载”按钮。
- **会话存储**：每个门户窗口创建自己的 `WKWebsiteDataStore.nonPersistent()`。以下情况会停止并释放旧的 WebView，删除其内存数据（只删除，不读取），然后丢弃数据存储：
  - 打开另一个门户，此时会新建窗口和新的数据存储；同一门户则沿用当前窗口；
  - 关闭门户窗口；
  - 点击“清除已保存地址”；
  - 退出应用。
- **未实现的功能**（会取消，并在窗口底部提示）：
  - **下载**：包括 `download` 属性、附件响应和无法显示的 MIME 类型。门户的工作台 iframe 带有 `allow-downloads`，但这个客户端不会保存文件；需要下载时，请改用浏览器访问门户。
  - **选择文件上传**（`<input type="file">`）：沙盒没有文件访问权限。
  - **摄像头和麦克风**：一律拒绝。
  - JavaScript 的 `alert`、`confirm`、`prompt` 对话框没有实现，WebKit 会按默认方式忽略。门户不使用这些对话框，工作台 iframe 的 sandbox 也不允许 modal。
- 只有一个窗口：关闭最后一个窗口就退出应用，内存中的会话随之结束。窗口状态不做恢复。

### 可接受的地址

门户地址必须是 HTTPS 站点根：`https://<域名>[:端口][/]`。

- 必须使用域名。虚拟机使用 `vm-<id>.<门户域名>` 子域名，所以不支持 IP 地址（包括浏览器会按 IPv4 解析的数字或 `0x` 结尾的名字）。
- 不能包含用户名或密码、路径、查询参数或片段。
- 只接受可见 ASCII 字符。国际化域名请填写 `xn--` 形式。末尾不能带点。
- 端口 443 会被省略。保存的是规范化后的地址，例如 `https://cage.example.org`。

本地开发用的 `http://cage.localhost:<端口>` 不是 HTTPS，客户端不接受；请直接用浏览器访问。

### 保存的数据

设置只有一个键 `PortalOrigin`，存放在沙盒容器的用户默认设置中：
`~/Library/Containers/io.github.sweetcornna.claude-cage.tenant/Data/Library/Preferences/io.github.sweetcornna.claude-cage.tenant.plist`。
每次读取都会重新校验，手动改坏的值会被删除。窗口位置和窗口状态都不保存。

“本地安装”下载并校验过的上游安装包（约 207 MB）放在同一容器的 `Data/Library/Caches/LocalCage/`，装好后可以删除。

## 使用、安装与卸载

1. 打开 DMG，把 `Cage Tenant.app` 拖到“应用程序”。
2. 打开 Cage Tenant，选择“本地安装”或“连接远程”。
   - **本地安装**会打开已安装的上游 Cage。尚未安装时，Apple Silicon Mac 会下载、校验并打开上游 Cage 0.1.8 安装包，再由用户完成安装。
   - **连接远程**会显示门户地址表单，并自动填入已保存的地址。点击“打开门户”后连接。
3. 用“选择使用方式…”返回启动页，用“门户设置…”（⌘,）更换或清除地址。

卸载时删除 `Cage Tenant.app`。如果也要删除已保存的地址，再删除 `~/Library/Containers/io.github.sweetcornna.claude-cage.tenant`。

预览版 DMG 已使用 Developer ID 签名，尚未完成 Apple 公证。其他 Mac 的 Gatekeeper 可能阻止启动。用于正式分发的构建应完成公证。ad-hoc 构建仅供本机测试。

## 构建与测试

所有脚本在仓库根目录运行，不安装、不提交、不发布。

```sh
desktop/tenant/test.sh         # Swift 6 双架构类型检查、单元测试、元数据、权限、API 边界、脚本检查
desktop/tenant/build.sh        # 构建并签名通用 App
desktop/tenant/package-dmg.sh  # 构建并签名 App，然后制作、签名并校验 DMG，生成 SHA256SUMS
```

`test.sh` 会以 Swift 6 语言模式分别为 arm64 和 x86_64 做类型检查（警告视为错误），并分别编译单元测试。它会运行本机架构的测试；在 Apple Silicon 上，只有装了 Rosetta 才会运行 x86_64 测试，否则只编译。单元测试覆盖：

- 地址校验和存储；
- 导航策略（主框架、工作台 iframe、弹窗、下载、重定向后的响应）；
- WebView 配置（非持久存储、无注入脚本、不能自动开窗）；
- 更换门户时丢弃旧窗口和旧会话；
- 菜单。

测试不加载任何网页，不访问网络，使用内存中的用户默认设置。WebKit 可能会在 `$TMPDIR` 留下以测试程序命名的空目录。

### 输出目录

默认输出到 `_output/tenant-macos/`，内容为 `Cage Tenant.app`、`Cage-Tenant-<版本>-macos-universal.dmg` 和 `SHA256SUMS`。临时目录也建在这里，脚本退出时删除。

如果设置 `CAGE_TENANT_OUTPUT_DIR`（必须是绝对路径），所有产物都写到那里。正式签名构建应使用单独的文件夹，以免覆盖测试构建。以下情况会被拒绝：

- 相对路径，或包含 `.`、`..` 的路径；
- 仓库根目录或它的上级目录；在仓库内时，必须是 `_output/` 下的子文件夹；
- 主目录，或任何“应用程序”文件夹（脚本从不安装）；
- 文件夹中已有不属于本项目的产物：名称或 bundle ID 不符、签名无法验证或正在运行的 App，未用 `io.github.sweetcornna.claude-cage.tenant.dmg` 标识签名的 DMG，或列有其他文件的 `SHA256SUMS`。遇到这些情况，脚本会保留原文件并退出。

### 签名

| 环境变量 | 作用 |
| --- | --- |
| `CAGE_SIGNING_IDENTITY` | 未设置或为 `-` 时使用 ad-hoc 签名。设置为真实证书（发行用 `Developer ID Application`）时，会加安全时间戳 |
| `CAGE_NOTARY_PROFILE` | 可选。notarytool 的钥匙串配置名，用于公证并装订 DMG |
| `CAGE_NOTARY_KEYCHAIN` | 可选。保存该配置的钥匙串 |
| `CAGE_BUILD_NUMBER` | 可选。`CFBundleVersion`，默认 `1`。版本号取自仓库根目录的 `VERSION` |

App 总是启用 hardened runtime，权限只有 `com.apple.security.app-sandbox` 和 `com.apple.security.network.client`；多一项或少一项都会导致校验失败。签名顺序固定：

1. 签名 `.app`，然后核对签名、标识、hardened runtime、两种架构和权限。使用真实证书时还要检查时间戳。
2. 制作 DMG，用标识 `io.github.sweetcornna.claude-cage.tenant.dmg` 签名并验签。然后只读挂载，确认里面 App 的 CDHash 与第 1 步一致。
3. 设置了 `CAGE_NOTARY_PROFILE` 时，公证并装订 DMG，再次验签，并用 `spctl` 评估。
4. 生成 `SHA256SUMS`，并用 `shasum -c` 校验。

正式构建示例：

```sh
CAGE_TENANT_OUTPUT_DIR="$PWD/_output/<新的发行目录>" \
CAGE_SIGNING_IDENTITY='Developer ID Application: <名称> (<TEAMID>)' \
CAGE_NOTARY_PROFILE='<notarytool 配置名>' \
CAGE_BUILD_NUMBER=2 \
desktop/tenant/package-dmg.sh
```

## 源码

| 文件 | 内容 |
| --- | --- |
| `main.swift`、`AppDelegate.swift` | 应用入口、生命周期、门户窗口的创建与丢弃、菜单（含复制粘贴所需的“编辑”菜单） |
| `PortalAddress.swift` | 地址校验与 `PortalStore`（只存门户地址） |
| `PortalPolicy.swift` | 主框架、iframe、弹窗和下载的导航策略（不依赖 WebKit，便于测试） |
| `PortalWebWindow.swift` | WKWebView 门户窗口、非持久会话、提示条 |
| `PortalWindow.swift` | 门户设置表单（打开、清除） |
| `StartupWindow.swift` | 启动页：本地安装或连接远程 |
| `LocalCage.swift` | 打开已安装的上游 Cage，或下载、校验并打开其安装包（只有这个文件使用 `NSWorkspace` 和 `URLSession`） |
| `ClaudeStyle.swift` | Claude 配色（浅色和深色）和按钮样式 |
| `Info.plist`、`CageTenant.entitlements` | 包信息与沙盒权限 |
| `common.sh`、`build.sh`、`package-dmg.sh`、`test.sh` | 构建、打包、校验 |
| `Tests/TenantTests.swift` | 单元测试，不依赖 XCTest |
| `dmg-readme.txt` | DMG 内的安装说明 |

应用图标沿用仓库的 `logo.png`，由 `desktop/Icon.swift` 生成。
