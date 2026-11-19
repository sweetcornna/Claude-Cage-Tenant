using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace CageTenant;

/// <summary>
/// One window: a setup page for the portal address, or the portal itself in
/// WebView2. The WebView2 user data folder is per portal origin; switching or
/// clearing the server destroys the view and deletes that folder.
/// </summary>
internal sealed class MainForm : Form
{
    private const string AppName = "Cage Tenant";
    private readonly PortalStore store;
    private PortalOrigin? origin;
    private WebView2? web;
    private CoreWebView2Environment? environment;
    private bool busy;
    private bool cancelToPortal;
    private readonly ClaudePalette palette = ClaudeTheme.Current();

    private readonly Panel content = new() { Dock = DockStyle.Fill };
    private readonly TableLayoutPanel startPage;
    private readonly Button localButton = new() { Text = "本地安装(&L)" };
    private readonly Button remoteButton = new() { Text = "连接远程(&R)" };
    private readonly Label startStatus = new() { AutoSize = true, MaximumSize = new Size(420, 0), Margin = new Padding(0, 16, 0, 0), Visible = false };
    private readonly TableLayoutPanel setupPage;
    private readonly Label setupTitle = new() { AutoSize = true, Margin = new Padding(0, 0, 0, 12) };
    private readonly TextBox addressBox = new() { Dock = DockStyle.Fill, Margin = new Padding(0, 12, 0, 8) };
    private readonly Button connectButton = new() { Text = "连接", AutoSize = true };
    private readonly Button cancelButton = new() { Text = "取消", AutoSize = true };
    private readonly Button clearAddressButton = new() { Text = "清除已保存地址", AutoSize = true };
    private readonly Label statusLabel = new() { AutoSize = true, MaximumSize = new Size(600, 0), Margin = new Padding(0, 8, 0, 0) };

    private readonly Panel notice = new() { Dock = DockStyle.Top, Height = 40, Visible = false, Padding = new Padding(12, 6, 8, 6) };
    private readonly Label noticeLabel = new() { Dock = DockStyle.Fill, TextAlign = ContentAlignment.MiddleLeft, AutoEllipsis = true };
    private readonly Button noticeRetry = new() { Text = "重试", Dock = DockStyle.Right, AutoSize = true, Visible = false };
    private readonly Button noticeClose = new() { Text = "关闭", Dock = DockStyle.Right, AutoSize = true };

    private readonly ToolStripMenuItem reloadItem = new("重新加载(&R)") { ShortcutKeyDisplayString = "F5" };
    private readonly ToolStripMenuItem homeItem = new("回到门户首页(&H)");
    private readonly ToolStripMenuItem startItem = new("选择使用方式(&S)…");
    private readonly ToolStripMenuItem changeItem = new("更换服务器(&C)…");
    private readonly ToolStripMenuItem clearSessionItem = new("清除本服务器登录数据(&L)…");
    private readonly ToolStripMenuItem clearAddressItem = new("清除已保存地址(&D)…");

    public MainForm(PortalStore store)
    {
        this.store = store;
        Text = AppName;
        Font = SystemFonts.MessageBoxFont ?? Font;
        AutoScaleMode = AutoScaleMode.Dpi;
        ClientSize = new Size(1280, 820);
        MinimumSize = new Size(800, 560);
        StartPosition = FormStartPosition.CenterScreen;
        try { Icon = Icon.ExtractAssociatedIcon(Environment.ProcessPath!) ?? Icon; } catch (ArgumentException) { }

        content.BackColor = palette.Background;
        startPage = BuildStartPage();
        setupPage = BuildSetupPage();
        content.Controls.Add(startPage);
        content.Controls.Add(setupPage);
        notice.Controls.Add(noticeLabel);
        notice.Controls.Add(noticeRetry);
        notice.Controls.Add(noticeClose);
        noticeClose.Click += (_, _) => HideNotice();
        noticeRetry.Click += async (_, _) => await RetryAsync();
        var menu = BuildMenu();
        // Docking runs from the last control added: menu, then notice, then content fills the rest.
        Controls.Add(content);
        Controls.Add(notice);
        Controls.Add(menu);
        MainMenuStrip = menu;
        AcceptButton = connectButton;

        origin = store.Load();
        // Data from a server that is no longer configured is removed even if
        // an earlier deletion failed while WebView2 still held its files.
        store.RemoveSessionsExcept(origin);
        Shown += (_, _) => ShowStart();
    }

    private MenuStrip BuildMenu()
    {
        var portal = new ToolStripMenuItem("门户(&P)");
        reloadItem.Click += async (_, _) => await RetryAsync();
        homeItem.Click += (_, _) => { if (origin != null) web?.CoreWebView2?.Navigate(origin.Url.AbsoluteUri); };
        startItem.Click += (_, _) => ShowStart();
        changeItem.Click += (_, _) => ShowSetup(changing: true);
        clearSessionItem.Click += async (_, _) => await ClearSessionAsync();
        clearAddressItem.Click += async (_, _) => await ClearAddressAsync();
        var exit = new ToolStripMenuItem("退出(&X)");
        exit.Click += (_, _) => Close();
        portal.DropDownItems.AddRange([reloadItem, homeItem, new ToolStripSeparator(), startItem, changeItem, clearSessionItem,
            clearAddressItem, new ToolStripSeparator(), exit]);
        var help = new ToolStripMenuItem("帮助(&H)");
        var about = new ToolStripMenuItem("关于 Cage Tenant(&A)");
        about.Click += (_, _) => MessageBox.Show(this,
            $"Cage Tenant {Application.ProductVersion}\n\n" +
            "MIT License. Copyright (c) 2026 Sylvain Zimmer, Claude Cage contributors.",
            AppName, MessageBoxButtons.OK, MessageBoxIcon.Information);
        help.DropDownItems.Add(about);
        var menu = new MenuStrip { Dock = DockStyle.Top };
        menu.Items.AddRange([portal, help]);
        return menu;
    }

    private TableLayoutPanel CenteredPage(int width, Control body)
    {
        var outer = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 3, RowCount = 3, AutoScroll = true, BackColor = palette.Background };
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 50));
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Absolute, width));
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 50));
        outer.RowStyles.Add(new RowStyle(SizeType.Percent, 40));
        outer.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        outer.RowStyles.Add(new RowStyle(SizeType.Percent, 60));
        outer.Controls.Add(body, 1, 1);
        return outer;
    }

    private TableLayoutPanel BuildStartPage()
    {
        var body = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, AutoSize = true, Padding = new Padding(10) };
        var title = new Label
        {
            AutoSize = true, Text = "选择使用方式", ForeColor = palette.Text, Anchor = AnchorStyles.None,
            Font = new Font(Font.FontFamily, Font.Size * 1.8f, FontStyle.Bold), Margin = new Padding(0, 0, 0, 24),
        };
        ClaudeTheme.Secondary(localButton, palette, large: true);
        ClaudeTheme.Primary(remoteButton, palette, large: true);
        localButton.Margin = new Padding(0, 0, 12, 0);
        remoteButton.Margin = new Padding(0);
        localButton.Click += async (_, _) => await OpenLocalAsync();
        remoteButton.Click += (_, _) => ShowSetup(changing: false);
        var choices = new FlowLayoutPanel { AutoSize = true, WrapContents = false, Anchor = AnchorStyles.None, Margin = new Padding(0) };
        choices.Controls.AddRange([localButton, remoteButton]);
        startStatus.Anchor = AnchorStyles.None;
        foreach (var control in new Control[] { title, choices, startStatus })
        {
            body.RowStyles.Add(new RowStyle(SizeType.AutoSize));
            body.Controls.Add(control);
        }
        return CenteredPage(440, body);
    }

    private TableLayoutPanel BuildSetupPage()
    {
        var outer = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 3, RowCount = 3, AutoScroll = true, BackColor = palette.Background };
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 50));
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Absolute, 620));
        outer.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 50));
        outer.RowStyles.Add(new RowStyle(SizeType.Percent, 35));
        outer.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        outer.RowStyles.Add(new RowStyle(SizeType.Percent, 65));

        var form = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, AutoSize = true, Padding = new Padding(10) };
        setupTitle.Font = new Font(Font.FontFamily, Font.Size * 1.6f, FontStyle.Bold);
        setupTitle.ForeColor = palette.Text;
        var addressLabel = new Label { AutoSize = true, Text = "门户地址", ForeColor = palette.Text, Font = new Font(Font, FontStyle.Bold) };
        addressBox.BackColor = palette.Surface;
        addressBox.ForeColor = palette.Text;
        addressBox.BorderStyle = BorderStyle.FixedSingle;
        addressBox.Font = new Font(Font.FontFamily, Font.Size * 1.15f);
        ClaudeTheme.Primary(connectButton, palette);
        ClaudeTheme.Secondary(cancelButton, palette);
        ClaudeTheme.Secondary(clearAddressButton, palette);
        connectButton.Margin = cancelButton.Margin = clearAddressButton.Margin = new Padding(0, 0, 8, 0);
        addressBox.AccessibleName = addressLabel.Text;
        addressBox.PlaceholderText = "https://cage.example.org";
        addressBox.MaxLength = 4096;
        var buttons = new FlowLayoutPanel { AutoSize = true, FlowDirection = FlowDirection.LeftToRight, WrapContents = false, Margin = new Padding(0) };
        buttons.Controls.AddRange([connectButton, cancelButton, clearAddressButton]);
        connectButton.Click += async (_, _) => await ConnectAsync();
        cancelButton.Click += (_, _) =>
        {
            if (cancelToPortal && origin != null && web != null) ShowWeb();
            else ShowStart();
        };
        clearAddressButton.Click += async (_, _) => await ClearAddressAsync();
        addressBox.TextChanged += (_, _) => SetStatus(null, false);
        foreach (var control in new Control[] { setupTitle, addressLabel, addressBox, buttons, statusLabel })
        {
            form.RowStyles.Add(new RowStyle(SizeType.AutoSize));
            form.Controls.Add(control);
        }
        outer.Controls.Add(form, 1, 1);
        return outer;
    }

    // ---- Pages -------------------------------------------------------------

    private void ShowSetup(bool changing)
    {
        setupTitle.Text = changing && origin != null ? "更换服务器" : "连接远程";
        addressBox.Text = origin?.Origin ?? "";
        cancelToPortal = changing && origin != null && web != null;
        cancelButton.Text = cancelToPortal ? "取消" : "返回";
        cancelButton.Visible = true;
        SetStatus(null, false);
        startPage.Visible = false;
        setupPage.Visible = true;
        setupPage.BringToFront();
        AcceptButton = connectButton;
        if (web != null) web.Visible = false;
        UpdateCommands();
        addressBox.Focus();
        addressBox.SelectAll();
    }

    private void ShowStart()
    {
        SetStartStatus(null, false);
        HideNotice();
        setupPage.Visible = false;
        if (web != null) web.Visible = false;
        startPage.Visible = true;
        startPage.BringToFront();
        AcceptButton = remoteButton;
        UpdateCommands();
        remoteButton.Focus();
    }

    private void ShowWeb()
    {
        startPage.Visible = false;
        setupPage.Visible = false;
        if (web != null)
        {
            web.Visible = true;
            web.BringToFront();
            web.Focus();
        }
        UpdateCommands();
    }

    private async Task ShowPortalAsync()
    {
        if (origin == null) { ShowSetup(false); return; }
        Text = $"{AppName} — {origin.Host}";
        if (web == null) await StartWebViewAsync(origin);
        if (web != null) ShowWeb();
        else ShowSetup(changing: false);
    }

    private void UpdateCommands()
    {
        // Reload and home act on the visible portal only, never from the start page.
        var live = !busy && origin != null && web?.CoreWebView2 != null && web.Visible;
        reloadItem.Enabled = live;
        homeItem.Enabled = live;
        clearSessionItem.Enabled = !busy && origin != null;
        clearAddressItem.Enabled = !busy && origin != null;
        changeItem.Enabled = !busy;
        connectButton.Enabled = !busy;
        clearAddressButton.Enabled = !busy && origin != null;
        cancelButton.Enabled = !busy;
        localButton.Enabled = !busy;
        remoteButton.Enabled = !busy;
        startItem.Enabled = !busy;
    }

    private void SetStartStatus(string? message, bool error)
    {
        startStatus.Text = message ?? "";
        startStatus.Visible = message != null;
        startStatus.ForeColor = error ? palette.Danger : palette.SecondaryText;
    }

    private async Task OpenLocalAsync()
    {
        if (busy) return;
        await RunBusyAsync(async () =>
        {
            SetStartStatus("正在准备本地 Cage…", false);
            try
            {
                SetStartStatus(await LocalCageLauncher.OpenAsync(), false);
            }
            catch (LocalCageException e)
            {
                SetStartStatus(e.Message, true);
            }
            catch (Exception e) when (e is IOException or UnauthorizedAccessException)
            {
                // Network errors arrive as LocalCageException; these come from the disk.
                SetStartStatus("无法保存安装包，请检查磁盘空间后重试。", true);
            }
            catch (Exception)
            {
                SetStartStatus("无法打开本地 Cage，请重试。", true);
            }
        });
    }

    private void SetStatus(string? message, bool error)
    {
        statusLabel.Text = message ?? "";
        statusLabel.Visible = message != null;
        statusLabel.ForeColor = error ? palette.Danger : palette.SecondaryText;
    }

    private void ShowNotice(string message, bool error, bool retry = false)
    {
        noticeLabel.Text = message;
        notice.BackColor = error ? palette.DangerSoft : palette.Surface;
        noticeLabel.ForeColor = error ? palette.Danger : palette.Text;
        noticeRetry.Visible = retry;
        notice.Visible = true;
    }

    private void HideNotice() => notice.Visible = false;

    // ---- Commands ----------------------------------------------------------

    private async Task ConnectAsync()
    {
        if (busy) return;
        var plan = WebPolicy.PlanConnect(addressBox.Text, origin);
        switch (plan.Action)
        {
            case ConnectAction.Reject:
                SetStatus(plan.Message, true);
                addressBox.Focus();
                return;
            case ConnectAction.Keep:
                await RunBusyAsync(ShowPortalAsync);
                return;
            case ConnectAction.Switch:
                if (!Confirm($"切换到 {plan.Origin!.Origin} 会关闭当前门户，并删除 {origin!.Origin} 在本机的登录数据。继续吗？")) return;
                await RunBusyAsync(async () =>
                {
                    var old = origin!;
                    await DestroyWebViewAsync(clearData: true);
                    var removed = await RemoveSessionAsync(old);
                    if (!Save(plan.Origin!)) return;
                    if (!removed) ShowNotice("旧服务器的会话文件仍被占用，已清除其浏览数据；下次启动时会删除剩余文件。", false);
                    await ShowPortalAsync();
                });
                return;
            case ConnectAction.Connect:
                await RunBusyAsync(async () =>
                {
                    if (!Save(plan.Origin!)) return;
                    store.RemoveSessionsExcept(plan.Origin);
                    await ShowPortalAsync();
                });
                return;
        }
    }

    private bool Save(PortalOrigin next)
    {
        try
        {
            store.Save(next);
            origin = next;
            return true;
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException)
        {
            SetStatus("无法保存门户地址：" + e.Message, true);
            return false;
        }
    }

    private async Task ClearSessionAsync()
    {
        if (busy || origin == null) return;
        if (!Confirm($"将关闭门户并删除 {origin.Origin} 在本机的登录数据（Cookie、站点存储和缓存），之后需要重新登录。继续吗？")) return;
        var reopen = web?.Visible == true;
        await RunBusyAsync(async () =>
        {
            var current = origin!;
            var cleared = await DestroyWebViewAsync(clearData: true);
            if (!await RemoveSessionAsync(current))
            {
                if (!cleared)
                {
                    ShowNotice("登录数据仍被占用，未能删除。请关闭 Cage Tenant 后重新打开再试。", true);
                    return;
                }
                ShowNotice("会话文件仍被占用，已通过 WebView2 清除全部浏览数据。", false);
            }
            else
            {
                ShowNotice("已清除本服务器的登录数据。", false);
            }
            // Reopen only when the portal was on screen; from the start or
            // setup page, stay there.
            if (reopen) await ShowPortalAsync();
            else
            {
                cancelToPortal = false;
                cancelButton.Text = "返回";
            }
        });
    }

    private async Task ClearAddressAsync()
    {
        if (busy || origin == null) return;
        if (!Confirm($"将删除已保存的门户地址 {origin.Origin}，并删除该服务器在本机的登录数据。继续吗？")) return;
        await RunBusyAsync(async () =>
        {
            var current = origin!;
            await DestroyWebViewAsync(clearData: true);
            var removed = await RemoveSessionAsync(current);
            try { store.Clear(); }
            catch (Exception e) when (e is IOException or UnauthorizedAccessException)
            {
                ShowNotice("无法删除已保存的地址：" + e.Message, true);
                return;
            }
            origin = null;
            Text = AppName;
            HideNotice();
            if (!removed) ShowNotice("会话文件仍被占用，已清除其浏览数据；下次启动时会删除剩余文件。", false);
            ShowSetup(changing: false);
            SetStatus("已清除保存的地址和登录数据。", false);
        });
    }

    private async Task RetryAsync()
    {
        if (busy || origin == null || startPage.Visible) return;
        HideNotice();
        if (web?.CoreWebView2 != null)
        {
            web.CoreWebView2.Reload();
            return;
        }
        await RunBusyAsync(async () =>
        {
            await DestroyWebViewAsync(clearData: false);
            await ShowPortalAsync();
        });
    }

    private async Task RunBusyAsync(Func<Task> action)
    {
        busy = true;
        UpdateCommands();
        UseWaitCursor = true;
        try { await action(); }
        finally
        {
            busy = false;
            UseWaitCursor = false;
            UpdateCommands();
        }
    }

    private bool Confirm(string message) =>
        MessageBox.Show(this, message, AppName, MessageBoxButtons.OKCancel, MessageBoxIcon.Warning, MessageBoxDefaultButton.Button2) == DialogResult.OK;

    // ---- WebView2 ----------------------------------------------------------

    private async Task StartWebViewAsync(PortalOrigin target)
    {
        var folder = store.SessionFolder(target);
        CoreWebView2Environment env;
        WebView2 view;
        try
        {
            Directory.CreateDirectory(folder);
            // No extra browser arguments, no extensions, no single sign-on with
            // the Windows account. TLS stays with WebView2's own validation.
            var options = new CoreWebView2EnvironmentOptions
            {
                AdditionalBrowserArguments = "",
                AllowSingleSignOnUsingOSPrimaryAccount = false,
                AreBrowserExtensionsEnabled = false,
            };
            env = await CoreWebView2Environment.CreateAsync(null, folder, options);
            view = new WebView2 { Dock = DockStyle.Fill, Visible = false };
            content.Controls.Add(view);
            await view.EnsureCoreWebView2Async(env);
        }
        catch (WebView2RuntimeNotFoundException)
        {
            ShowNotice("未找到 Microsoft Edge WebView2 Runtime。Windows 11 通常已自带；请从 Microsoft 官方安装 WebView2 Runtime（常青版）后重启 Cage Tenant。", true);
            return;
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException or System.Runtime.InteropServices.COMException or InvalidOperationException)
        {
            foreach (var stray in content.Controls.OfType<WebView2>().ToList()) { content.Controls.Remove(stray); stray.Dispose(); }
            ShowNotice("无法启动 WebView2：" + e.Message, true, retry: true);
            return;
        }
        Configure(view.CoreWebView2, target);
        web = view;
        environment = env;
        view.CoreWebView2.Navigate(target.Url.AbsoluteUri);
    }

    private void Configure(CoreWebView2 core, PortalOrigin target)
    {
        var settings = core.Settings;
        // No native bridge: no host objects, no web messages, no DevTools.
        settings.AreHostObjectsAllowed = false;
        settings.IsWebMessageEnabled = false;
        settings.AreDevToolsEnabled = false;
        // WebView2 must not offer to store portal or workbench passwords.
        settings.IsPasswordAutosaveEnabled = false;
        settings.IsGeneralAutofillEnabled = false;
        settings.IsSwipeNavigationEnabled = false;
        settings.IsStatusBarEnabled = true;
        settings.IsZoomControlEnabled = true;
        settings.AreDefaultContextMenusEnabled = true;
        settings.AreDefaultScriptDialogsEnabled = true;
        settings.IsBuiltInErrorPageEnabled = true;

        // Main frame only. Subframe navigations (FrameNavigationStarting) are
        // deliberately not filtered: the portal's CSP and iframe sandbox decide
        // which VM origins may load.
        core.NavigationStarting += (_, e) =>
        {
            if (WebPolicy.IsPortalDocument(e.Uri, target)) return;
            e.Cancel = true;
            ShowNotice("已阻止主窗口离开门户：" + Describe(e.Uri), false);
        };
        core.NewWindowRequested += (_, e) =>
        {
            e.Handled = true;
            ShowNotice("已阻止打开新窗口：" + Describe(e.Uri), false);
        };
        core.LaunchingExternalUriScheme += (_, e) =>
        {
            e.Cancel = true;
            ShowNotice("已阻止打开外部应用。", false);
        };
        // Never continue past a certificate error, and never show WebView2's
        // interstitial that could offer to.
        core.ServerCertificateErrorDetected += (_, e) => e.Action = CoreWebView2ServerCertificateErrorAction.Cancel;
        core.PermissionRequested += (_, e) =>
        {
            e.State = e.PermissionKind switch
            {
                // WebView2 shows its own prompt; the portal delegates clipboard
                // access only to the exact VM origin.
                CoreWebView2PermissionKind.ClipboardRead when WebPolicy.IsPortalFamily(e.Uri, target) => CoreWebView2PermissionState.Default,
                CoreWebView2PermissionKind.MultipleAutomaticDownloads => CoreWebView2PermissionState.Default,
                _ => CoreWebView2PermissionState.Deny,
            };
        };
        core.ContextMenuRequested += (_, e) =>
        {
            for (var i = e.MenuItems.Count - 1; i >= 0; i--)
            {
                var name = e.MenuItems[i].Name;
                if (name.Contains("NewWindow", StringComparison.OrdinalIgnoreCase) || name is "share" or "inspectElement")
                    e.MenuItems.RemoveAt(i);
            }
        };
        core.NavigationCompleted += (_, e) =>
        {
            if (e.IsSuccess || e.WebErrorStatus == CoreWebView2WebErrorStatus.OperationCanceled) return;
            ShowNotice(DescribeError(e.WebErrorStatus), true, retry: true);
        };
        core.ProcessFailed += (_, e) =>
            ShowNotice(e.ProcessFailedKind == CoreWebView2ProcessFailedKind.BrowserProcessExited
                ? "WebView2 已退出。点击“重试”重新打开门户。"
                : "页面进程意外退出。点击“重试”重新加载。", true, retry: true);
        core.DocumentTitleChanged += (_, _) => UpdateCommands();
    }

    /// <summary>
    /// Closes the view and waits for its browser process to exit so the
    /// session folder can be deleted. Returns true if browsing data was cleared
    /// through WebView2 first.
    /// </summary>
    private async Task<bool> DestroyWebViewAsync(bool clearData)
    {
        var view = web;
        var env = environment;
        web = null;
        environment = null;
        if (view == null) return false;
        var cleared = false;
        if (clearData && view.CoreWebView2 != null)
        {
            try
            {
                await view.CoreWebView2.Profile.ClearBrowsingDataAsync();
                cleared = true;
            }
            catch (Exception e) when (e is System.Runtime.InteropServices.COMException or InvalidOperationException) { }
        }
        var exited = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        void Exited(object? sender, CoreWebView2BrowserProcessExitedEventArgs args) => exited.TrySetResult();
        if (env != null) env.BrowserProcessExited += Exited;
        content.Controls.Remove(view);
        view.Dispose();
        if (env != null)
        {
            await Task.WhenAny(exited.Task, Task.Delay(TimeSpan.FromSeconds(8)));
            env.BrowserProcessExited -= Exited;
        }
        return cleared;
    }

    private async Task<bool> RemoveSessionAsync(PortalOrigin target)
    {
        for (var attempt = 0; attempt < 10; attempt++)
        {
            if (store.RemoveSession(target)) return true;
            await Task.Delay(300);
        }
        return false;
    }

    private static string Describe(string? uri)
    {
        if (!Uri.TryCreate(uri, UriKind.Absolute, out var parsed)) return "未知地址";
        var text = parsed.IsDefaultPort || parsed.Port < 0 ? $"{parsed.Scheme}://{parsed.Host}" : $"{parsed.Scheme}://{parsed.Host}:{parsed.Port}";
        if (parsed.Host.Length == 0) text = parsed.Scheme + ":";
        return text.Length > 100 ? text[..100] + "…" : text;
    }

    private static string DescribeError(CoreWebView2WebErrorStatus status) => status switch
    {
        CoreWebView2WebErrorStatus.CertificateCommonNameIsIncorrect or CoreWebView2WebErrorStatus.CertificateExpired
            or CoreWebView2WebErrorStatus.CertificateRevoked or CoreWebView2WebErrorStatus.CertificateIsInvalid
            or CoreWebView2WebErrorStatus.ClientCertificateContainsErrors
            => "无法验证门户的 HTTPS 证书，已停止加载。请联系管理员检查证书；本应用不提供忽略证书的选项。",
        CoreWebView2WebErrorStatus.HostNameNotResolved => "找不到门户域名。请检查地址或网络。",
        CoreWebView2WebErrorStatus.CannotConnect or CoreWebView2WebErrorStatus.ServerUnreachable
            or CoreWebView2WebErrorStatus.ConnectionReset or CoreWebView2WebErrorStatus.ConnectionAborted
            or CoreWebView2WebErrorStatus.Disconnected or CoreWebView2WebErrorStatus.Timeout
            => "无法连接到门户服务器。请检查网络，或联系管理员确认服务已启动。",
        _ => $"门户加载失败（{status}）。",
    };

    protected override async void OnFormClosing(FormClosingEventArgs e)
    {
        base.OnFormClosing(e);
        if (e.Cancel || web == null) return;
        // Let WebView2 flush its own session files before the process exits.
        e.Cancel = true;
        Hide();
        await DestroyWebViewAsync(clearData: false);
        Close();
    }
}
