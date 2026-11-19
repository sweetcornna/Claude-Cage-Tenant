Cage Tenant — Claude Cage 多租户门户的 macOS 客户端

安装
1. 将 Cage Tenant.app 拖入 Applications（应用程序）。
2. 打开 Cage Tenant，选择“本地安装”或“连接远程”。
   本地安装会打开已安装的 Cage，或在 Apple Silicon Mac 上下载并打开上游 Cage 的安装包。
   连接远程时，输入管理员提供的 HTTPS 门户地址，然后点击“打开门户”。
   之前保存的地址会自动填入。

它做什么、不做什么
- 只保存经过校验的门户根地址（HTTPS、域名、无路径）。登录状态只保存在内存中，
  关闭门户窗口、更换门户或退出应用后清除。应用不读取账户、密码或 Cookie。
- 门户窗口只显示门户自身的页面和它内嵌的虚拟机工作台（vm-<id>.<门户域名>）。
  跳转到其他网站、弹出窗口、下载和选择文件上传都会被阻止，并在窗口底部提示。
- 证书由系统按默认规则校验，不能绕过。看到证书错误时，请联系门户管理员。
- 没有自动更新。应用运行在 App Sandbox 中，只有发起网络连接的权限。
- 不在 Mac 上运行 Incus 或虚拟机，也不替换原版 Cage。两者名称和标识不同，可以同时安装。

系统要求：macOS 14 或更新版本，Apple Silicon 或 Intel。
卸载：删除 Cage Tenant.app。如需同时删除保存的地址，再删除
~/Library/Containers/io.github.sweetcornna.claude-cage.tenant。

本软件以 MIT 许可证发布，见 LICENSE.txt。

Drag Cage Tenant.app into Applications and open it. Choose "本地安装" (Local
Install) to open the upstream Cage app or its installer on Apple Silicon.
Choose "连接远程" (Connect Remotely), enter the HTTPS address from your
administrator, and choose "打开门户" (Open Portal).
It stores only the validated portal address. The sign-in session is kept in
memory and ends when the portal window closes, the portal changes or the app
quits; the app never reads accounts, passwords or cookies. The window stays on
the portal and its workspace frames: other sites, pop-ups, downloads and file
uploads are blocked. Certificates use the system's default validation.
No auto-update; App Sandbox with outgoing network access only. It runs no
virtual machines on the Mac and does not replace Cage.
Requires macOS 14 or later on Apple Silicon or Intel. MIT License (LICENSE.txt).
