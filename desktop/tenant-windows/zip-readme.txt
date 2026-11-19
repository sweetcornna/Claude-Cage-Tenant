Cage Tenant @VERSION@ for Windows x64 (unsigned build)
=======================================================

Cage Tenant is the Windows client for a Cage multi-tenant portal. It shows
the portal - sign-up, sign-in, creating and switching virtual machines, and
each VM's workbench - inside its own window, using Microsoft Edge WebView2.
The portal and the VMs run on a Linux Incus server that an administrator
deploys separately (deploy/tenant/README.md). This program runs no VMs.

Cage Tenant 是 Cage 多租户门户的 Windows 客户端，在自己的窗口中用 WebView2 显示
门户（注册、登录、创建和切换虚拟机、工作台）。门户和虚拟机运行在管理员另行部署的
Linux Incus 服务器上，本程序不在本机运行虚拟机。

Requirements / 运行要求
- Windows 10 or 11, x64.
- Microsoft Edge WebView2 Runtime (Evergreen). Windows 11 includes it. If it
  is missing, the app says so; install the Evergreen Runtime from Microsoft.
- No .NET installation is needed (self-contained).

Install / 安装
1. Extract the whole "Cage Tenant" folder, for example to
   %LOCALAPPDATA%\Programs\Cage Tenant.
2. Run CageTenant.exe and choose 本地安装 (Local Install) or 连接远程 (Connect
   Remotely). Local Install opens the upstream Cage app, or downloads its
   0.1.8 installer and verifies the SHA-256 before running it. For Connect
   Remotely, enter the HTTPS portal address from your administrator, for
   example https://cage.example.org, and click 连接 (Connect).

This preview has no Authenticode signature. Windows SmartScreen may warn before
first launch. Verify the ZIP against SHA256SUMS from the same release.
预览版未签名，首次运行时 SmartScreen 可能提示。请用同一次发布的 SHA256SUMS 核对。

What it stores / 保存的数据
- %LOCALAPPDATA%\Cage Tenant\portal-origin.txt - the portal address only.
- %LOCALAPPDATA%\Cage Tenant\Sessions\<id>\ - the WebView2 session (cookies,
  site storage, cache) for that one portal. The app never reads it. Changing
  the server or "清除本服务器登录数据" deletes it.
- %LOCALAPPDATA%\Cage Tenant\Downloads\ - the upstream installer that Local
  Install downloaded and verified (about 485 MB). Safe to delete afterwards.
No account names, passwords or cookies are read or saved by the app itself;
WebView2 password saving and autofill are turned off.

Menu 门户 / Portal
- 重新加载: reload.          - 回到门户首页: open the portal root.
- 更换服务器…: enter another address (deletes the old server's session).
- 清除本服务器登录数据…: sign out everywhere on this PC for this server.
- 清除已保存地址…: forget the address and delete its session.

Uninstall / 卸载
Delete the "Cage Tenant" folder and %LOCALAPPDATA%\Cage Tenant.

Security behaviour
- Only https:// portal roots are accepted; no IP addresses, credentials,
  paths, queries or fragments.
- The main window only shows pages from that exact origin. VM workbenches
  load in the portal's iframes as the portal allows.
- The portal cannot open pop-ups, new windows or external applications; nothing is
  opened in an external browser.
- Certificate errors stop the load; there is no way to continue past them.
- No script injection, host objects, web messages or DevTools.

MIT License - see LICENSE.txt.
Copyright (c) 2026 Sylvain Zimmer, Claude Cage contributors.
