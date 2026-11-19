# Cage 多租户接口契约

## 目标与部署边界

一个 Web 门户支持多人注册和登录。一个账户可以创建多台具备独立虚拟磁盘、Cage 控制器和工作区的 Linux 虚拟机。用户从顶部切换器打开任一已有虚拟机。切换、刷新、登出都不停止其他虚拟机。现有 Cage 桌面和单实例 CLI 保持原样。

新增 `cage-tenant` 服务以 Linux Incus 虚拟机为运行时，使用管理员配置的可信、预制 VM 镜像。镜像包含现有 Cage 后端、已启用的开机 Cage 控制器和 `socat`，并有能运行 `incus exec` 的 Incus guest agent。镜像中已有 Cage 工作环境所需的依赖和空白模板，不包含任何账号、历史、令牌或用户数据。若使用 Cage 的云工作环境，则在**每台 VM 内部**分别运行 rootless 引擎。部署文档提供构建及预检步骤，允许配置来宾 `control.json` 绝对路径。门户只提供自有实例，既不接受浏览器提交的镜像、Incus 参数、地址或路径，也不把宿主目录、宿主凭据或 Incus 管理 socket 共享给 VM。宿主使用专用 Incus 项目和限制配额，来宾网络和控制器隔离由部署验证。单机试运行默认每账户最多 5 VM、全局 20 VM，管理员可调。VM 隐式创建并首次启动后持续运行，停机由 Cage 工作台管理内部环境，关闭外层 Incus VM 由管理员负责。运维在 `deploy/tenant/README.md` 说明重启、磁盘保留、容量限制和独立的工作台密码。

## Web 来源

门户域名如 `cage.example.org`，虚拟机固定使用 `vm-<id>.cage.example.org`，两者同站点、不同源。`id` 是服务端生成的 32 位小写十六进制，永不重新分配给另一个账户。生产支持固定配置的 HTTPS 门户源和通配符 DNS/TLS，网关按严格 Host 匹配。独立 `http://cage.localhost:<port>` 可用于显式本地开发，且只能监听回环。外部用户既不能拿到工作台的 launcher token，也不能直接连工作台端口。请求的目标固定来自已验证的 VM 记录。门户下不存在绕过 VM 归属校验的通用代理。生产会话 Cookie 使用 `__Host-` 前缀。生产监听默认绑定回环，由可信 TLS 反代保留原始 Host 和 Origin，并开启 WebSocket 转发。

## 门户 API

以下路径仅来自门户源。请求和响应使用 JSON，要求 `Content-Type: application/json`，所有 POST 要求精确匹配门户 `Origin`。Cookie 为 host-only、HttpOnly、SameSite=Strict，生产环境要求 Secure，前端 `fetch` 使用同源会话，永不保存在浏览器存储。所有接口和工作台页面禁止缓存。接口错误一律 `{ "error": { "code": "…", "message": "…" } }`，前端使用文本渲染 `message`。

| 方法和路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `POST /api/tenant/register` | `{ "username": "alice", "password": "…" }` | `201 { "user": { "id": "…", "username": "alice" } }`，同时登录 |
| `POST /api/tenant/login` | 同上 | `200 { "user": { "id": "…", "username": "alice" } }` |
| `GET /api/tenant/session` | 无 | `200 { "user": { "id": "…", "username": "alice" } }` |
| `POST /api/tenant/logout` | `{}` | `204`，撤销服务端会话、VM 连接和未使用票据。响应不改写 Cookie，避免迟到的登出响应覆盖另一标签页的新登录 |
| `GET /api/tenant/systems` | 无 | `200 { "systems": [{ "id": "debian-12", "label": "Debian 12" }] }`，仅返回管理员已配置的可安装系统 |
| `GET /api/tenant/vms` | 无 | `200 { "vms": [VM], "limit": 5 }`，仅本人，稳定按创建时间排序 |
| `POST /api/tenant/vms` | `{ "name": "开发环境", "system_id": "debian-12" }` | `201 { "vm": VM }`，持久化所选系统，初始为 `stopped` |
| `POST /api/tenant/vms/{id}/open` | `{}` | 未就绪 `202 { "vm": VM }`，发起或加入幂等开机任务。就绪 `200 { "vm": VM, "workspace_url": "https://vm-<id>.cage.example.org/_tenant/enter#<ticket>" }` |

`VM = { "id": "…", "name": "开发环境", "system_id": "debian-12", "system_label": "Debian 12", "state": "stopped" | "starting" | "ready" | "error", "created_at": "2026-11-18T12:00:00Z" }`。`error` 状态包含可选 `message`，内容安全且适合提示。首次打开需数十秒或数分钟。`202` 后用 GET 列表每约两秒刷新目标 VM 状态，成为 `ready` 后再次调用打开取得票据，成为 `error` 时提供手动重试。只轮询当前目标，允许用户在启动时切到另一台。刷新门户时可以恢复浏览器记住的用户所选 VM ID，但必须从当前账号的列表重新验证，随后用同一个打开接口；不能持久化 ticket、URL、用户名密码或 guest 数据。新创建默认选中并尝试打开。切换时卸载原 iframe，取消或忽略旧请求结果，禁止把启动转成停止。iframe 加载失败可提供重试。

用户名统一转小写并去掉首尾空白，3–32 个 ASCII 字符，首字符为字母或数字，其余可含 `._-`。密码至少 12 字符，至多 128 UTF-8 字节，不改写空白。VM 名称去掉首尾空白，1–64 个 Unicode 字符，无控制字符。同一用户可以有重名 VM，身份以 `id` 为准。未登录 `401 unauthenticated`，密码/用户名不匹配 `401 invalid_credentials`，名称冲突 `409 username_taken`，VM 不存在或不归当前账户一律 `404 vm_not_found`，无效字段 `400 invalid_input`，不支持的媒体类型 `415 invalid_content_type`，配额 `409 vm_limit`，来源无效 `403 origin_refused`，频率限制 `429 rate_limited`，运行时不可用 `503 runtime_unavailable`。UI 使用会话接口区分未登录和请求失败。登陆错误不泄露账号是否存在。

门户页面已知当前用户时，访问系统目录、VM 列表、创建、打开或登出应带 `X-Cage-Expected-User: <user.id>`。网关在读取 Cookie 对应会话时同时比对该账户 ID，再进行任何读取或更改。会话有效但账户不一致时返回 `409 session_changed`，不返回 VM 数据、不写状态、不撤销会话；门户立即卸载旧工作台并重新读取会话。此头不是凭据，不能替代 Cookie；不带该头的非交互客户端仍按 Cookie 的归属校验。页面登录或退出后应通知其他标签页重新读取会话；重新可见时也应校验，不能依赖标签页通知做服务端授权。对之前已运行且返回 `202` 的 VM 可更早做一次就绪查询，之后恢复约两秒的间隔。

公开部署默认关闭自助注册；运维可显式开启，但应将注册入口限制在可信网络或认证后的入口，不能把有限的 VM 配额交给匿名互联网用户。开发模式保留本机自助注册。关闭时 `POST /api/tenant/register` 返回 `403 registration_closed`，现有账户的登录不受影响。预览版没有租户自助删除或自动销毁磁盘。

## Workbench 连接

`/_tenant/enter` 在 VM 源只提供本地固定 HTML 与外部同源 JS。JS 读取 URL fragment，立即移除 fragment，然后同源 JSON `POST /_tenant/enter` 提交 `{ "ticket": "…" }`，成功 `204` 和 VM 专用 host-only HttpOnly Cookie，随后 `location.replace("/")`。票据 60 秒有效、单次消费、绑定发票会话以及精确 VM ID。浏览器看不到内部 Cage launcher token 或内部 Cookie。票据不得出现在普通请求路径、日志或 Referer。门户 iframe 仅允许对应 VM 源，通过 `frame-ancestors` 精确允许门户，前端使用 `sandbox="allow-scripts allow-same-origin allow-forms allow-downloads"`，拒绝顶层导航，`referrerpolicy="no-referrer"`。

所有其他 VM 请求，包括 HTML、静态资源、API、WebSocket 和流，先校验 VM Cookie、VM 归属和登录撤销/过期状态。工作台 API 的修改请求和 WebSocket 要求浏览器 `Origin` 精确匹配其 VM 源。网关保留方法和原生路径，重写 Host/Origin 至 Cage 回环源。通过 `incus exec` 专用 guest-agent 通道访问该 VM 内的回环 HTTP，例如受控端口 `control.json` 中的端口和逐连接 `socat STDIO TCP4:127.0.0.1:<port>`。VM 只可从该机器的就绪文件取自身 controller 端口及 launcher token，并通过现有 `/api/launch` 后 `/api/session` 换取服务器侧会话；不把浏览器 Cookie、网关认证、转发或内部控制头传给来宾，不转发来宾的 `Set-Cookie` 或 CORS 头，不能依赖来宾响应的 Host。屏蔽来宾 `/api/launch`、`/api/session`、`/api/handoff`、`/api/desktop-local/*` 等内部入口和来宾跨源重定向。工作台继续使用它原有的账户界面和密码保险箱，初次打开时用户在该 VM 内创建工作台账户。门户负责外层认证，工作台密码不会提交给门户。退出门户后已授权 HTTP 流和 WebSocket 也必须断开。工作台 CSP 同源资源可用，`connect-src` 兼容对应 VM `wss:`（本地开发 `ws:`）。

## 性能与资源

- 管理员配置系统目录，每项映射到本机可信 Incus VM 镜像。系统 ID 使用小写 ASCII 字母、数字和连字符（最多 32 字符）。关联及显示名随 VM 记录持久化。默认单镜像可作为 `debian-12`，使用多系统配置时，目录应能同时配置 Debian 和 Ubuntu 等各自的管理员预制镜像，实例创建使用所选镜像。现有 VM 可在系统下架后继续连接。若尚未创建的记录对应镜像缺失，显示可重试错误，禁止改用其他系统。每台 VM 初次连接保留上游 Cage 本地账户创建和环境初始化，从空白模板开启。
- 同一个系统模板可复用一个不可变的 VM 基础镜像，生产部署推荐 Incus `zfs` 池的写时复制克隆。让 VM 根盘大小等于存储池内对应镜像的 ZFS zvol 大小（测试环境为 10 GiB，可统一设置池的 `volume.size` 和 profile 的根盘 `size`），并通过 `zfs list -t volume -o name,used,refer,origin` 核实共同的 `@readonly` 来源。本次 Incus 6.0 环境的 Btrfs 根盘和容量不同的 ZFS 根盘实测为独占块，其他驱动或版本须实测后再宣称复用。不同或不兼容的系统使用各自镜像。实例根盘及写入层独立，禁止跨账户共享可写卷。部署指南提供可实际运行的池、配置和占用量检查命令，解释虚拟盘标称容量与分配块的区别。
- 配置每台 VM 的 `limits.cpu` 和 `limits.memory`（试运行建议 2 vCPU、1 GiB，仅适用空闲控制器。完整图形工作环境按软件需求增加），给根盘设置容量，启动任务合并并限制全局并发，等待中的任务状态仍为 `starting`。资源紧张时保留任务、拒绝过量启动并返回可重试错误。运维可显式停止不使用的外层 VM 来释放内存，数据盘保留。挂机、切换或离开门户不会自动停止任务。不要跨租户作内存页去重。
- 复用按 VM 确认的工作台客户端、认证会话和有界连接池，连接关闭与门户会话撤销及运行时生命周期相容。减少 `incus exec` 子进程建立次数，长连接保留回压，严禁空闲连接跨 VM 使用。记录启动及切换耗时和主机资源统计的可复验命令。原接口和状态语义保持不变。

## 客户端与部署

- `cage-tenant` 在 Linux Incus 宿主上运行，并直接提供 `ui/tenant` Web 门户资源。部署者在可信 TLS 反代上配置门户域名及 VM 子域。用户可以直接用浏览器访问，无需安装桌面软件。
- Linux、Windows 和 macOS 各提供独立的 Cage Tenant 桌面客户端。客户端可在应用内配置或清除服务器的 HTTPS 门户地址，并在自己的原生应用窗口中用系统 WebView 显示完整门户；不会打开外部浏览器。注册、登录、选系统、新建、切换和工作台都使用同一服务端接口。
- WebView 主框架仅允许当前精确 HTTPS 门户源。跨源子框架由门户 CSP、iframe sandbox 和服务器认证约束，需能正常加载本账号 VM 子域。客户端禁止弹窗及原生 JS bridge、保留系统 TLS 校验；切换服务器必须销毁旧视图和会话数据存储。应用不读取密码或 Cookie，只保存门户地址；不在本机启动 Incus，不覆盖原版 Cage。若某个平台的下载或剪贴板能力受系统 WebView 限制，明确记录实测行为。
- 分开分发服务端及门户静态文件、Windows 客户端、Linux 客户端和 macOS 客户端。分发说明应区分签名与公证，并标明支持架构和实测范围。

## 交付与验收

- 后端范围 `cmd/cage-tenant/`、`internal/tenant/`、`deploy/tenant/`，另允许根 `assets.go`（需要嵌入的话）及直接依赖的 `go.mod` / `go.sum`。使用 `-ui-dir` 从 `ui/tenant/` 精确挂载预定门户文件也可，默认能从仓库根启动。给出启动参数、生产 DNS/TLS 和 Incus 镜像制作说明。Go 自动测试以假运行时验证完整请求路径、状态持久化和不同账户/VM 归属、票据/过期/登出、原生请求和流的隔离，不接入真实外部模型或生产系统。
- 前端范围 `ui/tenant/`，用无构建依赖的 `index.html`、`app.mjs`、`styles.css`，可加 `*.mjs` 测试或仅测试使用的独立 mock。门户可独立使用，无需修改现有 `ui/src/`。注册、登录、登出、多虚拟机创建、快捷切换、等待与错误恢复、手机尺寸和键盘操作均可验收。CSP 仅允许同源加载门户模块及样式，`frame-src` 由服务端生成。iframe 地址先验证与该 VM ID 约定完全匹配（scheme、port、主机、path），用 `textContent` 渲染服务端及用户文本。
- 联调使用两个独立账号，各至少创建一台 VM，其中至少一个账户创建两台；在同一个浏览器门户切换并确认工作台目标变更，第二账号无法取得第一账号的 VM。用可用的 Incus 主机验证实际引导和持久磁盘，若本机没有可用服务，记录环境限制并按实际测试范围汇报。测试用 mock 不允许绕过正式认证或成为生产路径。
