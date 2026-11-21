# Super Cage 多租户接口契约

## 目标与部署边界

一个 Web 门户支持多人注册和登录。一个账户可以创建多台具备独立虚拟磁盘、Super Cage 控制器和工作区的 Linux 虚拟机。用户从门户打开第一台虚拟机，随后通过工作台左栏的「虚拟机」查看、创建和切换自己的环境。切换、刷新和退出连接不会停止虚拟机。现有 SSH 连接方式和单实例 CLI 不变。

新增 `cage-tenant` 服务以 Linux Incus 虚拟机为运行时，使用管理员配置的可信、预制 VM 镜像。镜像包含现有 Super Cage 后端、已启用的开机 Super Cage 控制器和 `socat`，并有能运行 `incus exec` 的 Incus guest agent。镜像中已有 Super Cage 工作环境所需的依赖和空白模板，不包含任何账号、历史、令牌或用户数据。若使用 Super Cage 的云工作环境，则在**每台 VM 内部**分别运行 rootless 引擎。部署文档提供构建及预检步骤，允许配置来宾 `control.json` 绝对路径。门户只提供自有实例，既不接受浏览器提交的镜像、Incus 参数、地址或路径，也不把宿主目录、宿主凭据或 Incus 管理 socket 共享给 VM。宿主使用专用 Incus 项目和限制配额，来宾网络和控制器隔离由部署验证。单机试运行默认每账户最多 5 VM、全局 20 VM，管理员可调。VM 隐式创建并首次启动。空闲时网关按冷却策略先冻结、再停机（磁盘保留），下一个请求恢复冻结的实例，门户打开时冷启动已停机的实例；Super Cage 工作台仍管理 VM 内部环境。没有 KVM 的宿主可改用 Incus 无特权系统容器，接口和状态语义不变。运维在 `deploy/tenant/README.md` 说明重启、磁盘保留、容量限制和托管工作台账户。

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
| `POST /api/tenant/vms` | `{ "name": "开发环境", "system_id": "debian-12" }`，`system_id` 可省略 | `201 { "vm": VM }`，持久化所选系统，初始为 `stopped`。省略时使用首个管理员配置的系统 |
| `POST /api/tenant/vms/{id}/open` | `{}` | 未就绪 `202 { "vm": VM }`，发起或加入幂等开机任务。就绪 `200 { "vm": VM, "workspace_url": "https://vm-<id>.cage.example.org/_tenant/enter#<ticket>" }` |

`VM = { "id": "…", "name": "开发环境", "system_id": "debian-12", "system_label": "Debian 12", "state": "stopped" | "starting" | "ready" | "error", "created_at": "2026-11-18T12:00:00Z" }`。`error` 状态包含可选 `message`，内容安全且适合提示。首次打开需数十秒或数分钟。`202` 后用 GET 列表每约两秒刷新目标 VM 状态，成为 `ready` 后再次调用打开取得票据，成为 `error` 时提供手动重试。只轮询当前目标，允许用户在启动时切到另一台。刷新门户时可以恢复浏览器记住的用户所选 VM ID，但必须从当前账号的列表重新验证，随后用同一个打开接口；不能持久化 ticket、URL、用户名密码或 guest 数据。新创建默认选中并尝试打开。门户简化表单可只提交名称，网关使用首个已配置系统；显式 `system_id` 仍按管理员目录校验，保持兼容。切换时卸载原 iframe，取消或忽略旧请求结果，禁止把启动转成停止。iframe 加载失败可提供重试。

用户名统一转小写并去掉首尾空白，3–32 个 ASCII 字符，首字符为字母或数字，其余可含 `._-`。密码至少 12 字符，至多 128 UTF-8 字节，不改写空白。VM 名称去掉首尾空白，1–64 个 Unicode 字符，无控制字符。同一用户可以有重名 VM，身份以 `id` 为准。未登录 `401 unauthenticated`，密码/用户名不匹配 `401 invalid_credentials`，名称冲突 `409 username_taken`，VM 不存在或不归当前账户一律 `404 vm_not_found`，无效字段 `400 invalid_input`，不支持的媒体类型 `415 invalid_content_type`，配额 `409 vm_limit`，来源无效 `403 origin_refused`，频率限制 `429 rate_limited`，运行时不可用 `503 runtime_unavailable`。UI 使用会话接口区分未登录和请求失败。登陆错误不泄露账号是否存在。

门户页面已知当前用户时，访问系统目录、VM 列表、创建、打开或登出应带 `X-Cage-Expected-User: <user.id>`。网关在读取 Cookie 对应会话时同时比对该账户 ID，再进行任何读取或更改。会话有效但账户不一致时返回 `409 session_changed`，不返回 VM 数据、不写状态、不撤销会话；门户立即卸载旧工作台并重新读取会话。此头不是凭据，不能替代 Cookie；不带该头的非交互客户端仍按 Cookie 的归属校验。页面登录或退出后应通知其他标签页重新读取会话；重新可见时也应校验，不能依赖标签页通知做服务端授权。对之前已运行且返回 `202` 的 VM 可更早做一次就绪查询，之后恢复约两秒的间隔。

公开部署默认关闭自助注册；运维可显式开启，但应将注册入口限制在可信网络或认证后的入口，不能把有限的 VM 配额交给匿名互联网用户。开发模式保留本机自助注册。关闭时 `POST /api/tenant/register` 返回 `403 registration_closed`，现有账户的登录不受影响。预览版没有租户自助删除或自动销毁磁盘。

## Workbench 连接

`/_tenant/enter` 在 VM 源只提供本地固定 HTML 与外部同源 JS。JS 读取 URL fragment，立即移除 fragment，然后同源 JSON `POST /_tenant/enter` 提交 `{ "ticket": "…" }`，成功 `204` 和 VM 专用 host-only HttpOnly Cookie，随后 `location.replace("/")`。票据 60 秒有效、单次消费、绑定发票会话以及精确 VM ID。浏览器看不到内部 Super Cage launcher token 或内部 Cookie。票据不得出现在普通请求路径、日志或 Referer。门户 iframe 仅允许对应 VM 源，通过 `frame-ancestors` 精确允许门户，前端使用 `sandbox="allow-scripts allow-same-origin allow-forms allow-downloads"`，拒绝顶层导航，`referrerpolicy="no-referrer"`。

所有其他 VM 请求，包括 HTML、静态资源、API、WebSocket 和流，先校验 VM Cookie、VM 归属和登录撤销/过期状态。工作台 API 的修改请求和 WebSocket 要求浏览器 `Origin` 精确匹配其 VM 源。网关保留方法和原生路径，重写 Host/Origin 至 Super Cage 回环源。通过 `incus exec` 专用 guest-agent 通道访问该 VM 内的回环 HTTP，例如受控端口 `control.json` 中的端口和逐连接 `socat STDIO TCP4:127.0.0.1:<port>`。VM 只可从该机器的就绪文件取自身 controller 端口及 launcher token，并通过现有 `/api/launch` 后 `/api/session` 换取服务器侧会话；不把浏览器 Cookie、网关认证、转发或内部控制头传给来宾，不转发来宾的 `Set-Cookie` 或 CORS 头，不能依赖来宾响应的 Host。屏蔽来宾 `/api/launch`、`/api/session`、`/api/handoff`、`/api/desktop-local/*` 等内部入口和来宾跨源重定向。网关默认使用托管工作台账户进入空白 VM：首次连接时创建，之后自动解锁，已存在的其它工作台账户和密码保险箱保持不变并继续使用原有界面。托管密钥保存在网关状态目录的私有 `guest-account.json`（也可用 `-guest-account-file` 显式指定），每台 VM 的密码由该密钥派生，不进入门户、浏览器或 `state.json`；密钥不匹配或账户已变更时回退到工作台自己的账户界面。门户负责外层认证，工作台密码不会提交给门户。退出门户后已授权 HTTP 流和 WebSocket 也必须断开。工作台 CSP 同源资源可用，`connect-src` 兼容对应 VM `wss:`（本地开发 `ws:`）。

## 门户内工作台 VM 控制

VM 源为工作台侧栏提供以下接口，均要求该 VM 的 host-only VM Cookie、有效门户登录会话和 VM 归属校验；修改请求要求 `Origin` 精确等于当前 VM 源，并接受可选的 `X-Cage-Expected-User` 校验（不一致 `409 session_changed`）。这些路径由网关直接应答，不转发给 VM 内的 Super Cage，也不暴露 `/api/launch`、`/api/session`、`/api/handoff` 等内部入口。

| 方法和路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /api/client/virtual-machines` | 无 | `200 { "vms": [VM], "current_id": "<当前 VM id>" }`，只含 Cookie 登录账户拥有的 VM |
| `POST /api/client/virtual-machines` | `{ "name": "开发环境" }` | `201 { "vm": VM }`，只写入持久记录，不启动 VM；使用首个已配置系统 |
| `POST /api/client/virtual-machines/{id}/open` | `{}` | `200 { "vm_id": "<id>", "vm": VM }`，校验目标属于当前登录账户；未就绪时加入幂等开机任务。不签发票据；门户随后调用 `POST /api/tenant/vms/{id}/open` 取得入口并切换 iframe |

`/api/client/context` 在租户工作台中额外返回 `"virtual_machines": true` 和 `"portal_origin": "<门户源>"`。工作台侧栏可通过 `window.parent.postMessage({ "type": "cage:open-virtual-machine", "vm_id": "<id>" }, portal_origin)` 通知门户；门户必须校验消息来源是当前精确 VM 帧源、目标属于当前账户，再刷新列表并执行已有打开/切换流程。VM 控制接口不签发票据，避免重复票据。

## 每台虚拟机的独立设置

每台虚拟机运行自己的 Super Cage 工作台，使用自己的配置目录。单实例 Super Cage 里的环境设置在多虚拟机中全部按虚拟机保存：网络与代理（订阅来源、节点、代理链路、出口 IP 变化规则、代理核心凭据和代理端口）、加密 DNS、时区与语言、客机身份、外观、工作台账户与密码保险箱、共享项目、环境存储位置和诊断记录。切换虚拟机后，工作台显示目标虚拟机自己的设置，修改不影响其他虚拟机。新建虚拟机从空白配置开始，不复制默认环境或其他虚拟机的代理、凭据和路径。设置页标出当前虚拟机名称。

- **桌面本机**：各虚拟机的代理核心共用宿主回环地址，所以一个代理端口只属于一台虚拟机，已停止的也算。新建时自动分配未占用端口。应用节点或修改端口时，如果端口已分配给同一桌面的另一台虚拟机，返回错误并保留原配置。未配置代理的默认环境保留默认端口 17896。一台虚拟机共享的项目不能包含其他虚拟机或默认环境的配置、环境存储和迁移备份。各虚拟机共用桌面网关的网页源，所以工作台把标签、侧栏等浏览器端布局按 `virtual_machine_id` 分开保存；默认环境沿用原有的键。桌面网关的 `/api/client/context` 返回 `virtual_machine_id` 和虚拟机名称 `name`。
- **服务器（Incus）**：每台 VM 的代理核心运行在该 VM 内，出口走该 VM 自己的网卡，端口和凭据不与其他 VM 共享。租户 VM 需要管理员按部署指南配置受管 NIC 后才能联网；示例配置没有网卡，此时任何代理都无法连通。VM 之间以及 VM 与宿主网段的隔离仍由宿主侧规则负责。门户内工作台的 `/api/client/context` 返回 VM 名称 `name`。
- **不随虚拟机切换的内容**：桌面的 SSH 主机与服务器连接列表、应用更新，以及声音和 Claude Desktop 显示比例等客户端偏好。公共系统镜像缓存经校验后只读共享。

## 性能与资源

- 管理员配置系统目录，每项映射到本机可信 Incus VM 镜像。系统 ID 使用小写 ASCII 字母、数字和连字符（最多 32 字符）。关联及显示名随 VM 记录持久化。默认单镜像可作为 `debian-12`，使用多系统配置时，目录应能同时配置 Debian 和 Ubuntu 等各自的管理员预制镜像，实例创建使用所选镜像。现有 VM 可在系统下架后继续连接。若尚未创建的记录对应镜像缺失，显示可重试错误，禁止改用其他系统。每台 VM 从空白模板开始，网关准备独立账户，环境初始化仍由工作台处理。
- 同一个系统模板可复用一个不可变的 VM 基础镜像，生产部署推荐 Incus `zfs` 池的写时复制克隆。让 VM 根盘大小等于存储池内对应镜像的 ZFS zvol 大小（测试环境为 10 GiB，可统一设置池的 `volume.size` 和 profile 的根盘 `size`），并通过 `zfs list -t volume -o name,used,refer,origin` 核实共同的 `@readonly` 来源。本次 Incus 6.0 环境的 Btrfs 根盘和容量不同的 ZFS 根盘实测为独占块，其他驱动或版本须实测后再宣称复用。不同或不兼容的系统使用各自镜像。实例根盘及写入层独立，禁止跨账户共享可写卷。部署指南提供可实际运行的池、配置和占用量检查命令，解释虚拟盘标称容量与分配块的区别。
- 配置每台 VM 的 `limits.cpu` 和 `limits.memory`（试运行建议 2 vCPU、1 GiB，仅适用空闲控制器。完整图形工作环境按软件需求增加），给根盘设置容量，启动任务合并并限制全局并发，等待中的任务状态仍为 `starting`。资源紧张时保留任务、拒绝过量启动并返回可重试错误。运维可显式停止不使用的外层 VM 来释放内存，数据盘保留。挂机、切换或离开门户不会自动停止任务。不要跨租户作内存页去重。
- 复用按 VM 确认的工作台客户端、认证会话和有界连接池，连接关闭与门户会话撤销及运行时生命周期相容。减少 `incus exec` 子进程建立次数，长连接保留回压，严禁空闲连接跨 VM 使用。记录启动及切换耗时和主机资源统计的可复验命令。原接口和状态语义保持不变。

## 客户端与部署

- `cage-tenant` 在 Linux Incus 宿主上运行，并直接提供 `ui/tenant` Web 门户资源。部署者在可信 TLS 反代上配置门户域名及 VM 子域。用户用普通浏览器即可使用，无需安装桌面软件。门户只负责登录和 VM 入口，通过 iframe 嵌入所选 VM 的原版 Super Cage 工作台，不另做一套工作台。
- 原生桌面版在工作台左栏使用同一个 **虚拟机** 组件。桌面网关的同路径接口要求已解锁的本机账户和当前 `X-Cage-Connection`。清单标记 `current_id`，创建接收 `{ "name": "…" }`，打开返回 `{ "current_id": "…" }` 并更新连接代际，工作台随后重新加载。桌面本机与网页服务器各自维护账户与 VM 清单。
- 集成桌面安装包尚未发布，服务端归档不包含桌面应用。Preview 2（`v0.1.8-preview.2`）的独立桌面包已退役，仅作历史留档。集成版应用更新源使用 `sweetcornna/Super-Cage`，原生更新器的签名信任规则不变。

## 交付与验收

- 后端范围 `cmd/cage-tenant/`、`internal/tenant/`、`deploy/tenant/`，另允许根 `assets.go`（需要嵌入的话）及直接依赖的 `go.mod` / `go.sum`。使用 `-ui-dir` 从 `ui/tenant/` 精确挂载预定门户文件也可，默认能从仓库根启动。给出启动参数、生产 DNS/TLS 和 Incus 镜像制作说明。Go 自动测试以假运行时验证完整请求路径、状态持久化和不同账户/VM 归属、票据/过期/登出、原生请求和流的隔离，不接入真实外部模型或生产系统。
- 门户使用无构建依赖的 `ui/tenant/index.html`、`app.mjs` 和 `styles.css`。网页和桌面工作台共用 `ui/src/virtual-machines.jsx`。注册、登录、登出、多虚拟机创建、快捷切换、等待与错误恢复、手机尺寸和键盘操作均可验收。CSP 仅允许同源加载门户模块及样式，`frame-src` 由服务端生成。iframe 地址先验证与该 VM ID 约定完全匹配（scheme、port、主机、path），用 `textContent` 渲染服务端及用户文本。
- 联调使用两个独立账号，各至少创建一台 VM，其中至少一个账户创建两台；在同一个浏览器门户切换并确认工作台目标变更，第二账号无法取得第一账号的 VM。用可用的 Incus 主机验证实际引导和持久磁盘，若本机没有可用服务，记录环境限制并按实际测试范围汇报。测试用 mock 不允许绕过正式认证或成为生产路径。
- 浏览器与桌面版使用相同的工作台界面和主要操作。仅原生端提供的能力应有浏览器替代方式，或明确标为平台专属。完整图形工作流的一致性仍待验证。
