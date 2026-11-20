# Cage Tenant 无虚拟化版

每个工作区是一个 Incus 无特权系统容器，宿主不需要 KVM。网关、门户、账号隔离和空闲冷却与标准版相同，网关以 `-incus-instance-type container` 运行。原理和手动配置见[部署指南第 8 节](../README.md#8-无硬件虚拟化的宿主incus-系统容器)。

## 要求

- Debian 12 或更新、Ubuntu 22.04 或更新（Zabbly 提供 Incus 6.0 LTS 软件包的版本，例如 Ubuntu 26.04），amd64 或 arm64，systemd 和 cgroup v2，root 权限。
- 建议开启 swap。冻结的工作区靠它释放内存；没有 swap 时只停止占用 CPU。
- 安装时能访问 `pkgs.zabbly.com`（未装 Incus 时）、`images.linuxcontainers.org` 和 Debian 软件源，或用 `--image-mirror`、`--debian-mirror` 指定镜像站。
- 门户需要一个域名及其通配子域，例如 `cage.example.org` 和 `*.cage.example.org`，由 HTTPS 反向代理转发到网关。

## 安装

```sh
tar -xzf Cage-Tenant-NoVirt-<版本>-linux-amd64.tar.gz
cd Cage-Tenant-NoVirt-<版本>-linux-amd64
sudo ./install.sh --origin https://cage.example.org --enable-registration
```

脚本依次完成：

1. 没有 Incus 时，从 Zabbly 软件源安装 Incus 6.0 LTS，并核对软件源签名密钥的指纹。已安装的 Incus 低于 6.0.7 时给出提示：工作区内的 rootless 容器引擎需要 6.0.7 以上。
2. 写入 `/etc/sysctl.d/60-cage-tenant.conf`，调高 inotify、内核密钥和内存映射上限。无特权容器的 root 映射到宿主的同一个 uid，共用 inotify 实例上限，默认的 128 只够少量容器启动 systemd。加 `--allow-nested-userns` 时还会在 Ubuntu 23.10+ 放开无特权用户命名空间限制。
3. 创建存储池 `cage-tenant`（默认 `dir` 驱动）、带 NAT 的网络 `cagebr0` 和受限项目 `cage-tenant`。每个工作区默认 4 CPU、8 GiB 内存、8 GiB swap、10 GiB 根盘、16384 个进程，总数 20 个。宿主 nftables 规则（`cage-tenant-guard` 服务，`/etc/cage-tenant/guard.nft`，开机时先于 Incus 加载）丢弃工作区发往私有地址、CGNAT、链路本地、组播和回环地址的流量以及全部 IPv6，并拒绝工作区连接宿主本身，只放行网桥自带的 DHCP、DNS 和宿主透明代理重定向的连接；工作区只能访问公网。宿主运行 Clash/Mihomo 等带 auto-redirect 的 TUN 代理时，工作区流量会经它出去。加 `--allow-lan` 可取消这些限制。
4. 用 `images:debian/12` 构建空白模板 `cage-blank`：安装 socat，创建 `cage` 账户，放入安装包里的 Linux 版 Cage 和 `cage-serve.service`，清空机器 ID、日志和软件源缓存后发布到项目。模板里没有账号、密钥或用户数据。
5. 把网关和门户装到 `/opt/cage-tenant`，状态目录 `/var/lib/cage-tenant`（`0700`），注册并启动 systemd 服务 `cage-tenant`，监听 `127.0.0.1:8080`。

`./install.sh --help` 列出全部选项。重复运行是安全的：已有的存储池、网络和项目保持不变，模板只在缺失或加 `--rebuild-template` 时重建，网关的程序和服务参数每次更新。

## 安装之后

- **域名和 TLS**：把门户域名和通配子域指向同一个 HTTPS 反向代理，转发到 `http://127.0.0.1:8080` 并开启 WebSocket，见[部署指南第 4 节](../README.md#4-配置-dnstls-和反代)。
- **关闭注册**：首批账号注册完后，不带 `--enable-registration` 再运行一次安装脚本。
- **本机试用**：`sudo ./install.sh --dev` 把门户设为 `http://cage.localhost:8080`，不需要证书。在自己电脑上 `ssh -L 8080:127.0.0.1:8080 <服务器>` 后用浏览器打开。
- **升级**：解压新版本的安装包，用同样的参数运行其中的 `install.sh`。已有工作区和数据保留。
- **查看状态**：`systemctl status cage-tenant`、`journalctl -u cage-tenant`、`incus --project cage-tenant list`。

## 隔离和限制

- 工作区与宿主共用内核，隔离弱于虚拟机。请使用专用宿主并及时更新内核。网关拒绝特权容器、`raw.*` 等配置，并要求每个实例的资源和设备与项目 profile 一致。
- 同一网络上的工作区之间可以互相访问对方开放的端口。Cage 的控制端口只监听工作区内的回环地址，不受影响。
- 卸载时依次停止并删除 `cage-tenant` 服务、项目内的实例和镜像、项目、网络和存储池，停用并删除 `cage-tenant-guard` 服务和 `/etc/cage-tenant/guard.nft`，再删除 `/opt/cage-tenant`、`/var/lib/cage-tenant` 和 `/etc/sysctl.d/60-cage-tenant.conf`。删除实例会删除其中的全部数据。

## 验证范围

2026-11-19 在 Lima 中的 Debian 12.15 arm64 虚拟机上从全新系统运行安装脚本。虚拟机内核 6.1，没有 `/dev/kvm`，4 CPU、6 GiB 内存、4 GiB swap，参数为 `--dev`、清华 images 镜像和中科大 Debian 源。脚本从 Zabbly 装好 Incus 6.0.7，构建并导入模板，启动网关，全程约 3 分钟。之后用 2 个工作区验证：

- 每个工作区约 4.6 秒就绪，经网关访问工作台返回正常。
- 工作区能访问公网；访问宿主所在局域网的连接被丢弃；同一网桥上的工作区之间可以互通。
- 空闲 1 分钟后冻结，每个工作区的常驻内存从约 110 MiB 降到 3.3 MiB，下一个请求约 55 毫秒内恢复。空闲 3 分钟后停机，重新打开约 2.5 秒就绪。
- 再次运行脚本时，已有的存储池、网络、项目和模板保持不变。

2026-11-20 在同一台测试机上用 0.1.12 的脚本升级：原有的 Incus 网络 ACL 被移除，改由 nftables 规则隔离；工作区仍能访问公网，连接宿主、局域网网关、网桥地址和元数据地址均被拦截。重启规则服务和重复运行脚本后规则保持不变；加 `--allow-lan` 运行会删除规则，不加时再运行会重新写入。同样的 nftables 规则也用在一台 Ubuntu 26.04 amd64 宿主（标准版，宿主运行 Mihomo TUN 透明代理）上，工作区经代理正常上网，访问宿主和局域网被拦截。

amd64 安装包没有在实机上运行过。

**工作区内的 Cage 停在首次配置。** 没有 KVM 时，Cage 无法在工作区内启动虚拟机。它的工作环境需要预装在模板里的 Cage 云工作环境，即 rootless 容器引擎加完整的工作环境镜像，目前只支持 amd64。安装脚本还不会构建这部分，它在容器租户中也没有验证过。因此本版能部署门户、账号、工作区和空闲冷却；要在工作区里运行 Claude，需要按[部署指南第 2 节第 3 步](../README.md#2-制作空白-vm-镜像)自行准备包含云工作环境的模板，并导入为项目内的 `cage-blank`。
