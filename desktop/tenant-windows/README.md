# Cage Tenant（Windows 客户端）

Cage Tenant 是多租户门户的 Windows 客户端。它在自己的窗口里用 Microsoft Edge WebView2 显示管理员部署的 HTTPS 门户，注册、登录、选系统、新建和切换虚拟机以及工作台 iframe 都在这个窗口里完成，不打开外部浏览器。设计与 [macOS 客户端](../tenant/README.md) 一致，地址规则相同。

- 程序：`CageTenant.exe`（WinForms + WebView2，.NET 10 自包含单文件），不需要安装 .NET
- 系统：Windows 10 / 11 x64，需要 WebView2 Runtime（常青版，Windows 11 已自带）
- 产物：`Cage-Tenant-<版本>-windows-x64-unsigned.zip`，**未做 Authenticode 签名**
- 与原版 Cage（`desktop/windows`、`Cage.exe`）并存：程序名、数据目录、单实例互斥量都不同，不读写原版的任何文件
- 许可证：MIT，见 [LICENSE](LICENSE)

门户和虚拟机运行在管理员另行部署的 Linux Incus 服务器上，部署见 [deploy/tenant/README.md](../../deploy/tenant/README.md)，接口见 [docs/api-contract.md](../../docs/api-contract.md)。本机不运行 Incus 或虚拟机。

## 边界

| 会做 | 不会做 |
| --- | --- |
| 在应用内输入、校验、保存和更换一个 HTTPS 门户根地址 | 读取或保存账户、密码、Cookie、票据（WebView2 的密码保存和自动填充已关闭） |
| 主框架只显示该精确源（scheme、主机、端口）的页面 | 用主框架规则拦截子框架：VM 工作台 iframe 由门户 CSP、iframe sandbox 和服务端认证约束 |
| 每个门户源一个独立的 WebView2 用户数据目录；换服务器或清除时销毁视图并删除该目录 | 让新服务器沿用旧服务器的会话 |
| 拦截弹窗、新窗口和外部协议（`mailto:` 等） | 从门户页面调用外部浏览器或其他程序（只有启动页的“本地安装”会打开上游 Cage 或其安装程序） |
| 证书错误直接停止加载 | 提供“继续访问”或忽略证书的入口，或附加浏览器参数 |
| | 宿主对象、`postMessage` 桥、脚本注入、DevTools |

启动时程序会从自身环境中删除所有 `WEBVIEW2_*` 变量（例如可附加 `--ignore-certificate-errors` 的 `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`），再创建 WebView2。组策略（`HKLM/HKCU\Software\Policies\Microsoft\Edge\WebView2`）属于管理员控制范围，程序不覆盖。

权限请求：剪贴板读取只对门户源和 `vm-<32 位十六进制>.<门户主机>` 交给 WebView2 自带的询问框，多次自动下载使用 WebView2 默认处理，其余（摄像头、麦克风、位置、通知、文件系统等）一律拒绝。下载使用 WebView2 默认下载界面。

### 可接受的地址

与 macOS 客户端相同：`https://<域名>[:端口][/]`，必须是域名（不支持 IP），不能带用户名密码、路径、查询或片段，只接受可见 ASCII（国际化域名填 `xn--` 形式），末尾不能带点，端口 443 省略。测试直接读取 `desktop/tenant/Tests/TenantTests.swift` 里的全部向量，三个客户端接受和拒绝的输入完全一致。

### 保存的数据

| 路径 | 内容 |
| --- | --- |
| `%LOCALAPPDATA%\Cage Tenant\portal-origin.txt` | 规范化后的门户地址，每次读取都重新校验，无效内容会被删除 |
| `%LOCALAPPDATA%\Cage Tenant\Sessions\<sha256 前 32 位>\` | 该门户源的 WebView2 会话（Cookie、站点存储、缓存）。程序从不读取，只会删除 |
| `%LOCALAPPDATA%\Cage Tenant\Downloads\` | “本地安装”下载并校验过的上游安装程序（约 485 MB）。装好后可以删除 |

每次启动都会删除不属于当前地址的会话目录，所以即使上次删除时文件被占用，旧服务器的数据也会在下次启动时清掉。

## 使用

| 操作 | 位置 |
| --- | --- |
| 启动 | 选择“本地安装”或“连接远程” |
| 本地安装 | 打开已安装的上游 Cage；尚未安装时，下载上游 Cage 0.1.8 安装程序，核对 SHA-256 后运行 |
| 连接远程 | 填写门户地址，点击“连接”（或按 Enter） |
| 选择使用方式… | 菜单“门户”，返回启动页 |
| 重新加载 / 回到门户首页 | 菜单“门户” |
| 更换服务器… | 菜单“门户”。地址不同时先确认，然后关闭当前门户、清除并删除旧会话，再用新的独立会话打开 |
| 清除本服务器登录数据… | 菜单“门户”。清除 WebView2 浏览数据、删除会话目录后重新打开门户，需要重新登录 |
| 清除已保存地址… | 菜单“门户”或设置页按钮。删除地址和会话，回到设置页 |

## 安装与卸载

1. 解压 ZIP，把整个 `Cage Tenant` 文件夹放到例如 `%LOCALAPPDATA%\Programs\Cage Tenant`。
2. 运行 `CageTenant.exe`。缺少 WebView2 Runtime 时窗口会提示，请从 Microsoft 官方安装常青版 Runtime。

预览版未签名，SmartScreen 可能拦截首次运行。请用同一次发布的 `SHA256SUMS` 核对 ZIP。用于正式分发的构建应完成 Authenticode 签名。

卸载时删除程序文件夹和 `%LOCALAPPDATA%\Cage Tenant`。

## 构建与测试

在仓库根目录运行（macOS、Linux 或 Windows 的 Git Bash 均可），需要 .NET 10 SDK（`global.json` 允许 10.0.100 及以上的 10.0 SDK；仓库根目录的 `global.json` 固定 10.0.404，本目录的会覆盖它）。首次构建会从 nuget.org 还原 `Microsoft.Web.WebView2` 1.0.4314.35（与 `desktop/windows` 相同，见 `packages.lock.json`）和 Windows Desktop 运行时包。

```sh
desktop/tenant-windows/test.sh    # 逻辑测试 + win-x64 编译（警告视为错误）+ 安全设置和脚本检查
desktop/tenant-windows/build.sh   # 发布自包含单文件，检查 PE 头，打包 ZIP 并生成 SHA256SUMS
```

- `test.sh`：`Tests/` 是跨平台的控制台测试（不需要 Windows），覆盖地址校验（共享向量 + Windows 侧的空白和代理字符）、地址存储（只写一个文件、篡改值删除、原子替换）、按源分离的会话目录和清理（不跟随链接）、主框架源判断、VM 源判断、连接/保持/切换决策、`WEBVIEW2_*` 变量清理。然后以警告视为错误编译 WinForms 程序，扫描源码确认没有宿主对象、脚本注入、Cookie 读取、证书放行或子框架过滤，`Process.Start` 只出现在 `LocalCageLauncher.cs`，安装程序必须按固定的 SHA-256 校验，并检查必需的 WebView2 设置。本地安装的测试覆盖篡改（包括同样大小、内容不同）、超长、断网、下载卡住和中断后残留的半截文件。
- `build.sh`：`dotnet publish -r win-x64` 自包含单文件，`tools/pe_check.py` 检查 x64 PE32+、GUI 子系统、.NET 单文件包标记和清单（asInvoker、PerMonitorV2），并报告是否有签名。ZIP 内容为 `Cage Tenant/CageTenant.exe`、`README.txt`（`zip-readme.txt`）、`LICENSE.txt`。

### 输出目录

默认 `_output/tenant-windows/`：ZIP、`SHA256SUMS`、`.publish/`，以及 MSBuild 的 `bin/obj`（`.build/`，由 `Directory.Build.props` 和脚本的 `-p:ArtifactsPath` 指定，源码目录里不会出现 `bin/`、`obj/`）。设置 `CAGE_TENANT_WINDOWS_OUTPUT_DIR`（绝对路径）可改到别处；仓库内必须是 `_output/` 下的子目录，不能是仓库本身、主目录或含 `.`、`..` 的路径。目录里已有不属于本项目的 ZIP 或 `SHA256SUMS` 时脚本会保留原文件并退出。

## 验证范围

| 项目 | 状态 |
| --- | --- |
| 地址校验、存储、会话目录、导航和权限判断的逻辑测试（macOS 上用 .NET 10 运行） | 已运行，通过 |
| win-x64 编译（警告视为错误）、单文件发布、PE 头和清单检查、ZIP 内容 | 已运行，通过 |
| 在 Windows 上实际打开窗口、WebView2 加载门户、iframe、弹窗拦截、证书错误、换服务器删除会话、下载和剪贴板 | **未运行**。这些行为按 WebView2 SDK 文档实现，仍需在 Windows 10/11 x64 上实测 |

## 源码

| 文件 | 内容 |
| --- | --- |
| `Program.cs` | 入口：清理 `WEBVIEW2_*` 变量、单实例 |
| `MainForm.cs` | 窗口、启动页、设置页、菜单、WebView2 创建/配置/销毁 |
| `ClaudeTheme.cs` | Claude 配色（跟随系统浅色或深色）和按钮样式 |
| `LocalInstaller.cs` | 下载上游安装程序并核对大小和 SHA-256（跨平台，可在任意系统测试） |
| `LocalCageLauncher.cs` | 打开已安装的上游 Cage 或校验过的安装程序（只有这个文件启动其他程序） |
| `PortalAddress.cs` | 地址校验（与 `desktop/tenant/PortalAddress.swift` 同规则） |
| `PortalStore.cs` | 地址文件和按源分离的会话目录 |
| `WebPolicy.cs` | 连接决策、主框架源、VM 源、环境变量清理（纯逻辑，可在任意系统测试） |
| `CageTenant.Windows.csproj`、`app.manifest`、`Directory.Build.props`、`global.json`、`packages.lock.json` | 项目、清单和构建设置。图标沿用 `packaging/windows/Cage.ico`（只读引用） |
| `common.sh`、`build.sh`、`test.sh`、`tools/pe_check.py` | 构建、打包、检查 |
| `Tests/` | 跨平台测试 |
| `zip-readme.txt` | ZIP 内的说明 |
