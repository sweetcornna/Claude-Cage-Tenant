# Cage 多租户宿主部署

`cage-tenant` 在 Linux Incus 宿主机上运行。门户每个 VM 记录对应专用项目中的 `cage-<id>` 虚拟机和独立可写根盘。网关只通过 Incus guest agent 接入每台 VM 自己的 Cage 回环端口。网关为每台 VM 默认启用托管的工作台账户：门户登录后自动进入工作台，不需要再次输入密码。首次连接空白 VM 时创建该账户；已有工作台账户保持不变，继续使用原有的解锁界面和密码保险箱。

## 1. 准备 Linux 宿主机和专用项目

准备带 KVM 的 Linux 主机和已初始化的本机 Incus。生产可使用专门的 ZFS zpool 为 VM 根盘提供写时复制。示例使用已由管理员创建的专用 zpool `cage-zfs`，以及名为 `tenant-pool` 的 Incus 存储池。

```sh
incus --force-local storage create tenant-pool zfs source=cage-zfs volume.size=10GiB volume.zfs.reserve_space=false
incus --force-local project create cage-tenant \
  -c features.images=true -c features.profiles=true \
  -c restricted=true -c restricted.devices.disk=managed \
  -c restricted.devices.nic=block \
  -c limits.virtual-machines=20 -c limits.containers=0 \
  -c limits.cpu=80 -c limits.memory=160GiB -c limits.disk=1TiB
incus --force-local --project cage-tenant profile device add default root disk \
  path=/ pool=tenant-pool size=10GiB
incus --force-local --project cage-tenant profile set default limits.cpu=4
incus --force-local --project cage-tenant profile set default limits.memory=8GiB
incus --force-local --project cage-tenant profile show default
```

项目和每台 VM 的 CPU、内存与磁盘配额应按宿主储备设置。示例分配每台 4 vCPU、8 GiB 内存和 10 GiB 根盘。完整图形环境可能需要更大的根盘与更多内存。仅验证空闲控制器可从 2 vCPU、1 GiB 起步。

网关明确选择专用项目内的 `-incus-profile`（默认 `default`），不使用镜像关联的 profile。项目必须启用 `features.profiles=true`。网关在配备、接管及就绪复查时核对展开配置：只接受该 profile，要求显式正整数 vCPU、绝对内存大小，且有效值与 profile 一致；有效设备也必须一致。根盘只允许 `type=disk`、`path=/`、`pool` 和显式 `size`，不接受额外盘、宿主路径、socket、代理或其他共享设备，也不接受 `raw.*` 等自定义实例配置。已有实例若不匹配，查看网关宿主日志；保留原盘，先停机并由管理员核对配置再调整，不要临时放宽限制。不要在网关运行时改动 profile 或已运行 VM 的宿主侧配置：网关不是连续的配置审计器，也不能撤销已经生效的宿主配置。

示例配置的租户 VM 没有网卡。需要联网时，仅在管理员已配置并验证受信网络后，允许在 profile 中增加一个仅含 `type=nic`、`network=<网络名>` 和可选 `name` 的受管 NIC，设置 `-incus-network` 为同一个名字，并相应调整专用项目的 NIC 限制（不要放开其他网络）。使用宿主侧规则隔离 VM 间流量，并阻止 VM 访问宿主的服务与管理网段、其他私网和云元数据地址。IPv4、IPv6 与二层转发均须在部署时验证，网关不验证这些网络规则。控制端口继续只监听 VM 回环，宿主防火墙仅向浏览器开放 TLS 入口。Incus 管理 socket 的权限相当于宿主管理员权限，运行网关的专用系统账户和部署配置属于宿主信任边界。

工作台页面和响应由该 VM 控制。网关按持久化记录选择目标，只连接对应 VM 内的回环端口。工作台内部会话由网关持有，浏览器只接收门户和对应 VM 源的 Cookie。部署时将工作台响应视作租户内容。

## 2. 制作空白 VM 镜像

1. 在独立构建项目中从已验证的 Incus Linux **VM** 基础镜像启动一次性构建机。选择已安装并启用 Incus guest agent 的基础镜像。在 `cage-image-build` 项目导入验证过的 `debian-12-vm` 后执行 `incus --project cage-image-build launch local:debian-12-vm cage-template --vm`。为 Ubuntu 24.04 单独制作模板并使用独立镜像别名。
2. 在构建机中安装 Cage Linux 服务端、所选工作环境的依赖、`socat` 和 `coreutils`。确认 `/usr/bin/socat` 与 `/usr/bin/head` 存在。以普通 `cage` 系统账户运行 `cage serve`，使用独立配置目录。`CLAUDE_CAGE_CONFIG=/home/cage/.claude-cage/config.json` 对应默认的 `-guest-control-path`。可用专用 systemd 用户服务和 `loginctl enable-linger cage` 确保无需登录即可开机启动。
3. 如采用 Cage 云工作环境，按云端文档准备当前版本的完整工作环境模板、rootless 引擎与用户服务。将经过校验的模板预载进**构建 VM 内**的 rootless 引擎，并用 `cage init` 和 `cage container-configure --json` 写入空白运行配置。使用 `deploy/cloud/cage-container-engine.service` 与 `deploy/cloud/cage-server.service`，依赖、镜像与 socket 都留在每台租户 VM 内。仅复制空白模板层，让新 VM 自行生成工作台、浏览器和工作环境身份。[Cage 云端构建说明](../cloud/README.md)
4. 在一次性构建机停止 Cage 服务和引擎。按发行版镜像制作步骤清理构建机身份、SSH 密钥和构建日志。检查 Cage 配置和引擎镜像层，确保镜像内没有工作台账户或保险箱、项目、历史、`control.json`、会话、模型或代理凭据，以及任何用户数据。保持开机服务与 guest agent 启用后停止构建 VM。

将空白 VM 镜像收录到专用项目。生产启动命令可选择项目内通用别名或固定指纹。

```sh
incus --force-local --project cage-image-build stop cage-template
incus --force-local --project cage-image-build publish cage-template --alias cage-blank-build
image_dir=$(mktemp -d)
incus --force-local --project cage-image-build image export \
  local:cage-blank-build "$image_dir/cage-blank"
incus --force-local --project cage-tenant image import \
  "$image_dir/cage-blank.tar.gz" --alias cage-blank
incus --force-local --project cage-tenant image info local:cage-blank
```

本机导出和导入不要求 Incus 管理 API 在网络上监听。上述文件名适用于 `incus publish` 的默认压缩格式；如果改了格式，请使用实际导出的文件名。为每个不同系统重复此步骤。

多系统部署使用 `-systems-file /etc/cage-tenant/systems.json`。文件由宿主管理员维护，每条条目绑定该项目内的完整镜像指纹。将下面两个示例值分别换为验证过的 Debian 和 Ubuntu 镜像指纹。

```json
[
  {"id":"debian-12","label":"Debian 12","image":"1111111111111111111111111111111111111111111111111111111111111111"},
  {"id":"ubuntu-2404","label":"Ubuntu 24.04","image":"2222222222222222222222222222222222222222222222222222222222222222"}
]
```

门户和工作台按名称创建时，使用目录第一项作为默认系统。每台 VM 保存创建时的系统 ID、显示名和完整镜像指纹。可以在目录下架系统并保留已配备 VM。尚未配备的记录使用保存的指纹，镜像失效时可在恢复原指纹后重试。单系统部署使用 `-incus-image`，对应 `debian-12`。生产建议传完整指纹。本机别名在网关启动时解析为固定指纹。配置 `-systems-file` 或 `-incus-image` 后才能创建新虚拟机。已有实例可用原记录继续打开。

从专用项目启动一次性预检 VM，确认 agent、Cage 监听与镜像隔离。预检只打印是否通过，不打印控制文件、请求体或任何令牌。在示例项目的配额里为预检 VM 留一个位置，预检结束后按运维流程回收它。

```sh
incus --force-local --project cage-tenant launch local:cage-blank cage-preflight --vm --profile default
incus --force-local --project cage-tenant exec cage-preflight --mode=non-interactive -- \
  /usr/bin/test -s /home/cage/.claude-cage/control.json
incus --force-local --project cage-tenant exec cage-preflight --mode=non-interactive -- \
  /usr/bin/ss -ltn
incus --force-local --project cage-tenant config show cage-preflight --expanded
```

首次引导时，待控制服务就绪再重试预检。`ss` 的 Cage 端口应位于 `127.0.0.1`。用两个独立预检租户确认实际项目、各自的 VM 根盘、网段隔离和独立账户。关停再启动其中一台后，验证测试项目文件仍存在。验证过程使用本地测试文件，保持模型调用关闭。

## 3. 构建并启动网关

服务端归档 `Cage-Tenant-Server-*-linux-<架构>.tar.gz` 包含 `cage-tenant`、Web 门户、本文和 API 契约，不包含 Incus、反代或 VM 镜像。下载与宿主架构匹配的归档，校验发布页的 SHA-256，再在独立部署目录解压。在干净的 Git 工作区运行 `bash deploy/tenant/package-server.sh` 可在 `_output/` 构建 amd64 和 arm64 归档，打包时会校验二进制记录的提交号；仅需本机编译时，在仓库根执行 `go build -o ./cage-tenant ./cmd/cage-tenant`。

以下从包含 `cage-tenant` 和 `ui/tenant` 的目录启动。`ui/tenant` 只需 `index.html`、`app.mjs` 和 `styles.css`。使用可信部署目录里的文件，并确保运行账户可写的状态目录权限为 `0700`。

```sh
./cage-tenant \
  -listen 127.0.0.1:8080 -origin https://cage.example.org \
  -state-dir /var/lib/cage-tenant -ui-dir ./ui/tenant \
  -incus-bin /usr/bin/incus -incus-project cage-tenant -incus-profile default -incus-image cage-blank \
  -guest-control-path /home/cage/.claude-cage/control.json \
  -account-vm-limit 5 -total-vm-limit 20 -parallel-starts 2 \
  -trust-loopback-client-ip
```

托管工作台账户默认启用。首次启动时，网关在状态目录创建私有文件 `guest-account.json`（`0600`），内容为 `{"username":"cage","secret":"<64 位小写十六进制>"}`；每个 VM 的密码由该密钥和 VM ID 派生，不写入 `state.json` 或浏览器。需要显式指定或与其它主机共享同一密钥时，可加 `-guest-account-file /etc/cage-tenant/guest-account.json`，文件必须为 `0600` 的同一 JSON 格式。更改或丢失密钥后，已有托管账户不会被改写或删除，但会回退到工作台自己的密码界面；需要保留自动进入时，应随状态目录一起备份该文件。

将同一条启动命令配置为宿主 systemd 服务，使用专用账户、固定 `WorkingDirectory`、`StateDirectory=cage-tenant`、`StateDirectoryMode=0700` 和 `Restart=on-failure`，并在本机 Incus 服务就绪后启动。为 Incus CLI 配置可信的本机 Unix socket 访问。预制镜像和有效项目 profile 都由宿主管理员维护。

**首批门户账号：** 生产默认关闭注册。先在私有 VPN 或有身份认证的 TLS 入口限制门户的 `POST /api/tenant/register`，确认未获准用户无法抵达该路径，再临时加 `-enable-preview-registration`。让首批获准用户在该入口自己注册并保管密码；随后移除该参数并重启，已有账户仍可登录，重启前会话需重新登录。预览版没有邀请系统或账户管理 CLI；不要为了注册修改状态文件，也不要在匿名公网入口开启此开关。所有 VM 记录（包括未配备、失败和停机）都占配额；开放之前先按获准人数与宿主容量核对每账户和全局限额。当前没有自动回收或归档流程。

本地开发运行 `./_output/cage-tenant -dev -listen 127.0.0.1:8080 -state-dir /path/to/private/dev-state -incus-image cage-blank` 后访问 `http://cage.localhost:8080`。浏览器需将 `cage.localhost` 和 `vm-<id>.cage.localhost` 解析到回环。HTTP 开发使用单独的 Cookie 名称。实际打开 VM 仍需本机 Linux Incus。

## 4. 配置 DNS、TLS 和反代

将 `cage.example.org` 与 `*.cage.example.org` 的 DNS 指向同一 TLS 代理。证书应覆盖这两个名字。对外域名、`-origin`、证书及客户端使用的端口须一致。反代保留原始 Host 和 Origin，用直接客户端地址覆盖 `X-Real-IP`，支持 WebSocket 并关闭响应缓冲。开启 `-trust-loopback-client-ip` 时，只有可信 TLS 代理可以直连网关。

向用户提供 HTTPS 门户地址。用户直接在浏览器登录，创建或打开第一台 VM。门户随后加载该 VM 的 Cage 工作台，用户可从左栏 **虚拟机** 创建和切换。原生桌面版使用同一个工作台组件管理本机虚拟机。服务器清单归门户账户，本机清单归设备上的 Cage 账户。

Nginx 示例。`map` 位于 `http` 块，其他虚拟主机使用各自配置。生产环境请避免记录请求体、Cookie、来源票据、查询字符串及工作台内容。

```nginx
map $http_upgrade $cage_connection {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name cage.example.org *.cage.example.org;
    ssl_certificate     /etc/nginx/tls/cage-fullchain.pem;
    ssl_certificate_key /etc/nginx/tls/cage-key.pem;
    access_log off;
    error_log /dev/null;
    client_max_body_size 2050m;
    client_body_timeout 30s;

    location ^~ /api/tenant/ {
        client_max_body_size 4k;
        client_body_timeout 5s;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
    }

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $cage_connection;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
    }
}
```

工作台反代上限为 2050 MiB，给单文件 2 GiB 上限留出请求封装余量；更大的整个请求仍可能得到 `413`。`client_body_timeout` 限制两次读取之间的空闲时间，不限制正常持续上传的总时长。网关另外限制小 JSON 的整个读取时间；会话撤销会尝试中断网关与下游之间的读写，已提交到来宾的修改或反代已收下的数据无法撤回。

## 5. 重启、配额和数据保留

- 网关重启保留账户、VM 归属和记录。登录会话、VM Cookie 和 60 秒单次票据都保存在进程内。重启后重新登录，首次打开重新探测原 VM，页面在探测前显示 `stopped`。网关退出、登出和切换页面不会停止 Incus 实例。
- 工作台里的停止操作管理 VM 内的运行环境。停止外层 VM 由管理员在专用项目执行 `incus --project cage-tenant stop cage-<id>`。再次打开会启动原 VM，沿用其根盘。宿主重启后的 VM 自启动行为可在专用 Incus profile 配置 `boot.autostart`。
- 账户配额和全局配额计入所有持久 VM 记录，包含停机的 VM。`-parallel-starts` 只限制网关任务；Incus 守护进程的异步配备或启动可能在 CLI 超时后继续。超时后先用 `incus --force-local --project cage-tenant operation list` 和实例状态对账，再手动重试。对同一 VM 的请求加入网关已有任务，排队时状态保持 `starting`。管理员可停止空闲的外层 VM 释放内存，磁盘保持原样。宿主配置应关闭跨租户内存页合并，例如 KSM。更新系统目录里的镜像指纹只影响之后创建的 VM 记录。
- 网关确认上游连接故障或内部会话失效后，清除该工作台就绪缓存和未使用票据；从门户重新打开可触发探测及必要时重新握手。网关不重放写请求；如果响应中断，先检查上次操作是否已在来宾完成，再决定是否重试。工作台的嵌套 SSH 模式当前不受支持，普通业务 API 在切换后可能返回 `409`，请保留 VM 本地工作环境模式。
- 按同一恢复点备份 `/var/lib/cage-tenant` 与 Incus 实例、项目、存储池和相关镜像。`state.json` 保存密码哈希和 VM 所有权，`guest-account.json` 保存托管工作台账户的派生密钥。回滚或迁移前停网关并保管备份，保留 VM ID、实例名及 `user.cage-tenant.id` 绑定。一个状态目录只运行一个网关进程。
- v1 有 VM 记录时，必须用 `-legacy-debian-image <64 位原始指纹>` 明确迁移；不从当前系统目录、别名或替代镜像推断。指纹应来自可信旧部署档案或原实例的 `volatile.base_image`（并核实确为旧 Debian 模板）。已经迁移成空指纹的旧 v2 Debian 记录可用同一参数补绑，不覆盖任何非空绑定；未补绑时，已有实例经过校验仍可打开，未配备的记录不能创建 VM。无法确认原镜像时保留备份，不猜测指纹。
- VM 就绪和错误状态只存在于内存，不依赖状态文件写入。账户或 VM 记录写入若在原子替换前失败，修复存储后可重试；替换后若目录同步失败，网关保守阻止新登录和打开，先检查宿主存储和持久化状态，再重启。
- 验收使用两个独立门户账户，每个创建至少两台 VM。在同一门户切换，核对不同工作台数据，再以第二个账户确认第一个账户的 VM 不可访问。门户登出会撤销该次登录发放的票据、HTTP 流和 WebSocket。

## 6. 检查根盘复用与运行成本

同一门户的所有账号共用管理员维护的空白镜像目录。复用以完整镜像指纹为准，配备时所有账号使用同一个 Incus 项目、profile 和存储池。每台 VM 使用独立可写根盘，首次启动后生成的账号数据、项目文件和凭据均保存在该 VM 内。

让 ZFS 镜像 zvol 的 `volsize`、Incus 池的 `volume.size` 和 profile 根盘 `size` 一致，并确认模板镜像虚拟盘大小在该容量内。用两个不同门户账号各创建并打开一台使用同一镜像指纹的 VM，再检查它们的根盘。用其中一台写入测试文件，确认另一台读取不到该文件，并检查两个账号的清单各自只显示所属 VM。

```sh
incus --force-local storage show tenant-pool
incus --force-local storage info tenant-pool
incus --force-local --project cage-tenant storage volume list tenant-pool
sudo zfs list -t volume -o name,volsize,used,refer,origin,refreservation -r cage-zfs
```

同镜像的两个 VM 根盘应各自持有可写 zvol，并在 `ORIGIN` 显示同一镜像的 `@readonly` 快照。`VOLSIZE` 是来宾看到的标称虚拟容量，`REFER` 包含从快照继承的块，`USED` 还可能计入保留量。测试环境中，Incus 6.0、统一 10 GiB 的 ZFS 镜像和根盘获得了共同的 `@readonly` 来源。根盘容量不同的 ZFS 实例和同环境的 Btrfs 实例获得独占块。使用其他驱动、容量或 Incus 版本时，按现场测量安排磁盘预算。

在预检浏览器的 Network 面板记录第一次 `open` 到列表显示 `ready` 的时间。切换到已运行 VM 时，记录 `open` 至工作台首个文档响应的时间。只记耗时和状态码，避免导出含认证请求的 HAR。配备、切换和停止 VM 前后执行以下命令，对比 Incus 资源、进程数量与宿主可用内存。

```sh
incus --force-local --project cage-tenant info cage-preflight
incus --force-local --project cage-tenant list
free -h
ps -C qemu-system-x86_64 -o pid,rss,%cpu,comm
ps -C incus -o comm= | wc -l
```

每台 VM 独立复用一个 Cage 会话和 HTTP 连接池，最多保留 8 条空闲 guest-agent 连接，空闲 15 秒后关闭。同一 VM 的 HTTP、流和 WebSocket 共享 64 条同时请求的上限，另保留两条连接用于握手和健康检查。长连接由正常的读写回压控制，登录撤销会关闭该会话正在使用的流和 WebSocket。
