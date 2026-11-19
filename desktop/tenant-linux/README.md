# Cage Tenant（Linux 客户端）

Cage Tenant 使用 GTK 3 和 WebKitGTK，在应用窗口中连接管理员部署的 HTTPS 门户。VM 运行在 [Linux Incus 服务端](../../deploy/tenant/README.md)。

## 安装

在使用 WebKitGTK 4.1（2.40 或更高版本）的 Debian 或 Ubuntu 桌面上，从 [发布页](https://github.com/sweetcornna/Claude-Cage-Tenant/releases) 下载 `.deb` 并校验 `SHA256SUMS` 中对应条目。此包通过发行版安装本机架构的依赖。使用 Ubuntu 24.04 arm64 验证。

```sh
sudo apt install ./cage-tenant-desktop_0.1.8_all.deb
cage-tenant-desktop
```

启动后选择“本地安装”或“连接远程”。本地安装会打开已安装的上游 Cage；尚未安装时，x86_64 的 Debian 或 Ubuntu 会把上游 Cage 0.1.8 的 `.deb` 包（约 121 MB）下载到“下载”文件夹，校验 SHA-256 后交给系统软件安装器，装好后可以删除。连接远程需要填写门户 HTTPS 地址。右上角菜单可以返回启动页、更换服务器、清除登录数据或清除已保存地址。配置保存在 `~/.config/cage-tenant-desktop`，WebKit 会话按门户源分别保存在 `~/.local/share/cage-tenant-desktop/sessions` 和 `~/.cache/cage-tenant-desktop/sessions`，并遵循 XDG 路径设置。更换或清除门户会销毁旧视图并清理对应会话。应用使用系统证书验证。每个 WebKit 会话接收内嵌 VM 的会话 Cookie，由门户的 CSP、iframe 沙箱与网关鉴权约束工作台访问。

## 构建与验证

```sh
bash desktop/tenant-linux/build-deb.sh
bash desktop/tenant-linux/container-test.sh
```

包默认输出到 `_output/tenant-linux/`。用 `CAGE_TENANT_LINUX_OUTPUT_DIR` 指定其他绝对路径。容器测试使用 Docker 和 Xvfb，覆盖安装、VM iframe 会话、门户切换、证书错误及卸载。
