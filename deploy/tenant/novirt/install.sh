#!/usr/bin/env bash
# Install the Super Cage gateway on a Linux host without hardware
# virtualization. Each workspace runs as an unprivileged Incus system
# container. Run as root from the extracted Super-Cage-NoVirt bundle.
set -euo pipefail

bundle=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
origin='' dev=false listen=127.0.0.1:8080
prefix=/opt/cage-tenant state_dir=/var/lib/cage-tenant
project=cage-tenant pool=cage-tenant pool_driver=dir pool_source=''
network=cagebr0 tenant_nic=true allow_lan=false
instances=20 cpu=4 memory=8GiB swap=8GiB disk=10GiB processes=16384
project_cpu=80 project_memory=160GiB project_disk=1TiB
base_image=images:debian/12 image_mirror='' debian_mirror='' rebuild_template=false
freeze_after=15m stop_after=72h idle_cpu=5 registration=false
install_incus=true nested_userns=false

usage() {
  cat <<'EOF'
用法：sudo ./install.sh --origin https://cage.example.org [选项]

  --origin URL            门户 HTTPS 地址（正式部署必填）
  --dev                   本机测试：门户为 http://cage.localhost:<端口>，不需要 TLS
  --listen IP:PORT        网关回环监听地址（默认 127.0.0.1:8080）
  --enable-registration   开放自助注册；首批账号注册完后不带此项再运行一次
  --instances N           工作区总数上限（默认 20）
  --cpu N --memory SIZE --swap SIZE --disk SIZE --processes N
                          每个工作区的上限（默认 4 / 8GiB / 8GiB / 10GiB / 16384）
  --project-cpu N --project-memory SIZE --project-disk SIZE
                          全部工作区合计上限（默认 80 / 160GiB / 1TiB）
  --storage-pool NAME     Incus 存储池（默认 cage-tenant，不存在时创建）
  --storage-driver NAME   新建存储池的驱动：dir、btrfs、zfs 或 lvm（默认 dir）
  --storage-source PATH   新建存储池的来源（块设备、已有 zpool 等，可选）
  --network NAME          工作区网络（默认 cagebr0，不存在时创建，带 NAT）
  --no-tenant-network     工作区不接网络（仍用该网络构建模板）
  --allow-lan             允许工作区访问宿主局域网和其他内网地址（默认禁止）
  --base-image REMOTE:ALIAS
                          模板的基础镜像（默认 images:debian/12）
  --image-mirror URL      images 远端的 simplestreams 镜像站，例如
                          https://mirrors.tuna.tsinghua.edu.cn/lxc-images/
  --debian-mirror URL     构建模板时使用的 Debian 软件源，例如 http://mirrors.ustc.edu.cn
  --rebuild-template      重新构建 cage-blank 模板
  --freeze-after DUR --stop-after DUR --idle-cpu-percent N
                          空闲冷却参数（默认 15m / 72h / 5）
  --skip-incus-install    不安装 Incus，只使用已安装的版本
  --allow-nested-userns   Ubuntu 23.10+：放开 apparmor_restrict_unprivileged_userns，
                          工作区内的 rootless 容器引擎需要；只在专用宿主上使用
EOF
}

die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
note() { printf '==> %s\n' "$*"; }

while (($#)); do
  case "$1" in
    --origin) origin=${2:?}; shift ;;
    --dev) dev=true ;;
    --listen) listen=${2:?}; shift ;;
    --enable-registration) registration=true ;;
    --instances) instances=${2:?}; shift ;;
    --cpu) cpu=${2:?}; shift ;;
    --memory) memory=${2:?}; shift ;;
    --swap) swap=${2:?}; shift ;;
    --disk) disk=${2:?}; shift ;;
    --processes) processes=${2:?}; shift ;;
    --project-cpu) project_cpu=${2:?}; shift ;;
    --project-memory) project_memory=${2:?}; shift ;;
    --project-disk) project_disk=${2:?}; shift ;;
    --storage-pool) pool=${2:?}; shift ;;
    --storage-driver) pool_driver=${2:?}; shift ;;
    --storage-source) pool_source=${2:?}; shift ;;
    --network) network=${2:?}; shift ;;
    --no-tenant-network) tenant_nic=false ;;
    --allow-lan) allow_lan=true ;;
    --base-image) base_image=${2:?}; shift ;;
    --image-mirror) image_mirror=${2:?}; shift ;;
    --debian-mirror) debian_mirror=${2:?}; shift ;;
    --rebuild-template) rebuild_template=true ;;
    --freeze-after) freeze_after=${2:?}; shift ;;
    --stop-after) stop_after=${2:?}; shift ;;
    --idle-cpu-percent) idle_cpu=${2:?}; shift ;;
    --skip-incus-install) install_incus=false ;;
    --allow-nested-userns) nested_userns=true ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数：$1" ;;
  esac
  shift
done

[[ $(id -u) == 0 ]] || die '请用 root 运行（sudo ./install.sh ...）'
[[ -d /run/systemd/system ]] || die '需要 systemd'
[[ -f /sys/fs/cgroup/cgroup.controllers ]] || die '需要 cgroup v2'
[[ -x "$bundle/cage-tenant" && -x "$bundle/cage" && -f "$bundle/ui/tenant/index.html" ]] || die '请在解压后的 Super-Cage-NoVirt 目录中运行'
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) die "不支持的架构：$(uname -m)" ;;
esac
[[ "$bundle" == *"-linux-$arch" ]] || die "这个安装包不是 $arch 版本"
[[ "$listen" =~ ^127\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+$ ]] || die '--listen 必须是 127.x.x.x:端口'
port=${listen##*:}
if $dev; then
  origin="http://cage.localhost:$port"
else
  [[ "$origin" =~ ^https://[a-z0-9.-]+(:[0-9]+)?$ ]] || die '请用 --origin 指定门户的 HTTPS 地址，例如 https://cage.example.org（本机测试用 --dev）'
fi
for name in "$project" "$pool" "$network"; do
  [[ "$name" =~ ^[a-z][a-z0-9-]{0,14}$ ]] || die "名称不合法：$name"
done
[[ "$project" != default ]] || die '请使用专用 Incus 项目'

# 1. Incus
version_ge() { [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" == "$2" ]]; }
if ! command -v incus >/dev/null; then
  $install_incus || die '没有找到 Incus；去掉 --skip-incus-install 让脚本安装'
  . /etc/os-release
  case "$ID" in
    debian|ubuntu) ;;
    *) die "自动安装 Incus 只支持 Debian 和 Ubuntu，当前是 $PRETTY_NAME；请先自行安装 Incus 6.0 LTS，再加 --skip-incus-install" ;;
  esac
  [[ -n "${VERSION_CODENAME:-}" ]] || die "无法识别 $PRETTY_NAME 的版本代号；请先自行安装 Incus 6.0 LTS，再加 --skip-incus-install"
  note '安装 Incus 6.0 LTS（Zabbly 软件源）'
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -q
  apt-get install -y -q curl gnupg ca-certificates
  # Any release Zabbly builds for works; ask its archive instead of keeping a list.
  curl -fsS --retry 3 -o /dev/null "https://pkgs.zabbly.com/incus/lts-6.0/dists/$VERSION_CODENAME/Release" ||
    die "Zabbly 没有为 $PRETTY_NAME（$VERSION_CODENAME）提供 Incus 6.0 LTS；请先自行安装，再加 --skip-incus-install"
  install -d -m 0755 /etc/apt/keyrings
  key=$(mktemp)
  curl -fsSL --retry 3 https://pkgs.zabbly.com/key.asc -o "$key"
  # The fingerprint Zabbly publishes for its package signing key.
  gpg --show-keys --with-colons "$key" | grep -qx 'fpr:::::::::4EFC590696CB15B87C73A3AD82CC8797C838DCFD:' ||
    die 'Zabbly 软件源密钥指纹不符，已停止'
  install -m 0644 "$key" /etc/apt/keyrings/zabbly.asc
  rm -f "$key"
  cat > /etc/apt/sources.list.d/zabbly-incus-lts-6.0.sources <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/lts-6.0
Suites: $VERSION_CODENAME
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF
  apt-get update -q
  apt-get install -y -q incus
fi
incus_bin=$(command -v incus)
I() { incus --force-local "$@"; }
P() { incus --force-local --project "$project" "$@"; }
for _ in $(seq 30); do I info >/dev/null 2>&1 && break; sleep 1; done
server_version=$(I query /1.0 | sed -n 's/.*"server_version": *"\([^"]*\)".*/\1/p' | head -1)
[[ -n "$server_version" ]] || die 'Incus 服务没有响应'
version_ge "$server_version" 6.0.0 || die "Incus $server_version 太旧，需要 6.0 或更新"
version_ge "$server_version" 6.0.7 || note "提示：Incus $server_version 低于 6.0.7，工作区内的 rootless 容器引擎可能无法创建容器（见部署指南第 8 节）"

# 2. Kernel settings. Unprivileged containers share their mapped root's
# inotify limit on the host, and systemd needs several instances per
# container, so the usual default of 128 runs out after a few workspaces.
note '设置内核参数'
{
  echo 'fs.inotify.max_user_instances = 1048576'
  echo 'fs.inotify.max_user_watches = 1048576'
  echo 'kernel.keys.maxkeys = 2000'
  echo 'vm.max_map_count = 262144'
  if $nested_userns && [[ -e /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]]; then
    echo 'kernel.apparmor_restrict_unprivileged_userns = 0'
  fi
} > /etc/sysctl.d/60-cage-tenant.conf
sysctl -q -p /etc/sysctl.d/60-cage-tenant.conf
if [[ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)" == 1 ]]; then
  note '提示：本机限制无特权用户命名空间；工作区内的 rootless 容器引擎需要 --allow-nested-userns'
fi
if [[ $(awk '/^SwapTotal:/ {print $2}' /proc/meminfo) == 0 ]]; then
  note '提示：本机没有 swap。冻结的工作区不占 CPU，但内存无法换出（见部署指南第 7 节）'
fi

# 3. Storage, network and the dedicated project.
# No "incus admin init": the template and every workspace use the pool and
# network created here, and its default bridge fails on hosts where Incus
# cannot find a free IPv6 subnet.
if ! I storage show "$pool" >/dev/null 2>&1; then
  note "创建存储池 $pool（$pool_driver）"
  if [[ -n "$pool_source" ]]; then I storage create "$pool" "$pool_driver" source="$pool_source"
  else I storage create "$pool" "$pool_driver"; fi
fi
if ! I network show "$network" >/dev/null 2>&1; then
  note "创建网络 $network"
  I network create "$network" ipv4.address=auto ipv4.nat=true ipv6.address=none
fi
# Workspace isolation lives in a host nftables table rather than an Incus
# network ACL. An ACL also drops the flows a transparent proxy on the host
# (Clash or Mihomo TUN with auto-redirect) redirects to the bridge address,
# which cuts every workspace off the Internet. The guard drops forwarded
# traffic to private, loopback, link-local and multicast ranges and all IPv6,
# and any workspace connection to the host itself except DHCP, DNS and flows
# the host redirected. It loads before Incus starts instances at boot.
acl="$network-egress"
if [[ "$(I network get "$network" security.acls 2>/dev/null)" == "$acl" ]]; then
  I network unset "$network" security.acls
  I network unset "$network" security.acls.default.egress.action 2>/dev/null || true
  I network unset "$network" security.acls.default.ingress.action 2>/dev/null || true
fi
I network acl delete "$acl" >/dev/null 2>&1 || true
guard=/etc/cage-tenant/guard.nft guard_unit=/etc/systemd/system/cage-tenant-guard.service
if ! $allow_lan; then
  command -v nft >/dev/null || { apt-get update -q && apt-get install -y -q nftables; }
  nft_bin=$(command -v nft)
  install -d -m 0755 /etc/cage-tenant
  cat > "$guard" <<EOF
#!$nft_bin -f
# Written by Super Cage install.sh: isolate workspaces on $network.
table inet cage_tenant_guard
delete table inet cage_tenant_guard
table inet cage_tenant_guard {
	chain input {
		type filter hook input priority filter - 1; policy accept;
		iifname != "$network" accept
		udp dport { 53, 67 } accept
		tcp dport 53 accept
		ct status dnat accept
		icmp type { destination-unreachable, time-exceeded, parameter-problem } accept
		drop
	}
	chain forward {
		type filter hook forward priority filter - 1; policy accept;
		iifname "$network" ip daddr { 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4 } drop
		iifname "$network" meta nfproto ipv6 drop
	}
}
EOF
  cat > "$guard_unit" <<EOF
[Unit]
Description=Super Cage workspace network isolation (nftables)
DefaultDependencies=no
Wants=network-pre.target
Before=network-pre.target incus.service incus-startup.service cage-tenant.service shutdown.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$nft_bin -f $guard
ExecStop=$nft_bin delete table inet cage_tenant_guard

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable cage-tenant-guard.service >/dev/null 2>&1
  systemctl restart cage-tenant-guard.service
else
  systemctl disable --now cage-tenant-guard.service >/dev/null 2>&1 || true
  rm -f "$guard" "$guard_unit"
  systemctl daemon-reload
fi
if ! I project show "$project" >/dev/null 2>&1; then
  note "创建项目 $project"
  nic_rules=(-c restricted.devices.nic=block)
  if $tenant_nic; then nic_rules=(-c restricted.devices.nic=managed -c "restricted.networks.access=$network"); fi
  I project create "$project" \
    -c features.images=true -c features.profiles=true \
    -c restricted=true -c restricted.devices.disk=managed "${nic_rules[@]}" \
    -c restricted.containers.nesting=allow \
    -c restricted.containers.lowlevel=allow -c restricted.virtual-machines.lowlevel=allow \
    -c "limits.containers=$instances" -c limits.virtual-machines=0 \
    -c "limits.cpu=$project_cpu" -c "limits.memory=$project_memory" -c "limits.disk=$project_disk"
  P profile device add default root disk path=/ pool="$pool" size="$disk"
  if $tenant_nic; then P profile device add default eth0 nic network="$network" name=eth0; fi
  P profile set default limits.cpu="$cpu" limits.memory="$memory" limits.memory.swap="$swap" \
    limits.processes="$processes" security.nesting=true
else
  note "沿用已有项目 $project 和它的 profile"
fi

# 4. The blank workspace template: Debian with systemd, socat and Cage's
# control service. It holds no account, key or user data.
if $rebuild_template || ! P image alias list --format csv | grep -q '^cage-blank,'; then
  note '构建空白工作区模板 cage-blank（需要几分钟）'
  if [[ -n "$image_mirror" ]]; then
    install -d -m 0700 "${INCUS_CONF:-$HOME/.config/incus}"
    I remote remove cage-images >/dev/null 2>&1 || true
    I remote add cage-images "$image_mirror" --protocol=simplestreams --public
    base_image="cage-images:${base_image#*:}"
  fi
  build="cage-template-$(date +%s)"
  cleanup() { I delete -f "$build" >/dev/null 2>&1 || true; }
  trap cleanup EXIT
  I launch --quiet "$base_image" "$build" --no-profiles -s "$pool" -n "$network"
  inside() { I exec "$build" -- "$@"; }
  mirror_host=deb.debian.org
  if [[ -n "$debian_mirror" ]]; then mirror_host=${debian_mirror#*://}; mirror_host=${mirror_host%%[:/]*}; fi
  ready=false
  for _ in $(seq 90); do
    if inside sh -c 'ip -4 route | grep -q ^default && getent hosts "$1" >/dev/null' sh "$mirror_host"; then ready=true; break; fi
    sleep 1
  done
  $ready || die "模板构建容器没有网络（检查网络 $network 的 NAT 和 DNS）"
  if [[ -n "$debian_mirror" ]]; then
    inside sh -c 'sed -i "s#http://deb.debian.org#$1#g" /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null; true' sh "$debian_mirror"
  fi
  inside sh -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -y -q --no-install-recommends socat ca-certificates coreutils'
  inside sh -c 'id cage >/dev/null 2>&1 || useradd --create-home --uid 1000 --shell /bin/bash cage'
  I file push --quiet "$bundle/cage" "$build/usr/local/bin/cage" --mode 0755 --uid 0 --gid 0
  I file push --quiet "$bundle/cage-serve.service" "$build/etc/systemd/system/cage-serve.service" --mode 0644 --uid 0 --gid 0
  inside systemctl enable cage-serve.service
  inside sh -c '
    if [ -n "$1" ]; then sed -i "s#$1#http://deb.debian.org#g" /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null; fi
    apt-get clean; rm -rf /var/lib/apt/lists/*
    rm -rf /home/cage/.claude-cage /root/.bash_history
    rm -f /etc/ssh/ssh_host_*
    rm -rf /var/log/journal/*; find /var/log -type f -exec truncate -s 0 {} +
    rm -f /var/lib/dbus/machine-id; : > /etc/machine-id' sh "$debian_mirror"
  I stop "$build"
  fingerprint=$(I publish "$build" 2>&1 | sed -n 's/.*fingerprint: *\([0-9a-f]\{64\}\).*/\1/p')
  [[ "$fingerprint" =~ ^[0-9a-f]{64}$ ]] || die '模板发布失败'
  export_dir=$(mktemp -d)
  I image export "local:$fingerprint" "$export_dir/cage-blank" >/dev/null
  I image delete "local:$fingerprint"
  P image alias delete cage-blank >/dev/null 2>&1 || true
  P image import "$export_dir"/cage-blank* --alias cage-blank >/dev/null
  rm -rf "$export_dir"
  cleanup
  trap - EXIT
fi

# 5. The gateway service.
note "安装网关到 $prefix"
install -d -m 0755 "$prefix" "$prefix/ui/tenant"
install -m 0755 "$bundle/cage-tenant" "$prefix/cage-tenant.new"
mv -f "$prefix/cage-tenant.new" "$prefix/cage-tenant"
install -m 0644 "$bundle"/ui/tenant/* "$prefix/ui/tenant/"
install -d -m 0700 "$state_dir"
args=(-listen "$listen" -state-dir "$state_dir" -ui-dir "$prefix/ui/tenant"
  -incus-bin "$incus_bin" -incus-project "$project" -incus-profile default
  -incus-instance-type container -incus-image cage-blank
  -freeze-after "$freeze_after" -stop-after "$stop_after" -idle-cpu-percent "$idle_cpu")
if $tenant_nic; then args+=(-incus-network "$network"); fi
if $dev; then args+=(-dev); else args+=(-origin "$origin"); fi
if $registration; then args+=(-enable-preview-registration); fi
cat > /etc/systemd/system/cage-tenant.service <<EOF
[Unit]
Description=Super Cage gateway (Incus system containers)
After=network-online.target incus.service incus.socket
Wants=network-online.target

[Service]
ExecStart=$prefix/cage-tenant ${args[*]}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable cage-tenant.service >/dev/null
systemctl restart cage-tenant.service
sleep 3
systemctl is-active --quiet cage-tenant.service || { journalctl -u cage-tenant.service -n 30 --no-pager; die '网关没有启动'; }

cat <<EOF

Super Cage 无虚拟化版已安装。
  门户：$origin
  网关：$listen（systemd 服务 cage-tenant，日志 journalctl -u cage-tenant）
  Incus 项目：$project，模板：cage-blank，网络：$network
EOF
if ! $dev; then
  host=${origin#https://}
  echo "下一步：把 $host 和 *.$host 指向同一个 HTTPS 反向代理，转发到 http://$listen 并开启 WebSocket（见 deploy/tenant/README.md 第 4 节）。"
fi
if $registration; then
  echo '自助注册已开放。首批账号注册完后，不带 --enable-registration 再运行一次本脚本。'
fi
