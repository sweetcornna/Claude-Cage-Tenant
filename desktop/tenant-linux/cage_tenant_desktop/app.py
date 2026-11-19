"""The GTK 3 window: a setup page for the portal address, or the portal in WebKitGTK.

Import this module only through ``cage_tenant_desktop.launcher.main`` so the
environment is cleaned before WebKit loads.
"""

from __future__ import annotations

import os
import sys
import threading

import gi

gi.require_version("Gtk", "3.0")
gi.require_version("WebKit2", "4.1")
gi.require_version("Gdk", "3.0")
from gi.repository import Gdk, Gio, GLib, Gtk, WebKit2  # noqa: E402

from . import APP_ID, APP_NAME  # noqa: E402
from . import local_cage, policy  # noqa: E402
from .policy import ConnectAction  # noqa: E402
from .storage import PortalStore  # noqa: E402

# is_main_frame_main_resource(), which enforces the main-frame origin, is 2.40+.
MIN_WEBKIT = (2, 40)
VERSION = "0.0.0"


def webkit_version():
    return (WebKit2.get_major_version(), WebKit2.get_minor_version(), WebKit2.get_micro_version())


# The portal's warm Claude palette, for the native start and address pages.
PALETTES = {
    "light": {
        "bg": "#f5f3eb", "surface": "#fffdf8", "border": "#cfc9bb", "text": "#292621", "muted": "#6e685d",
        "accent": "#974129", "fill": "#d97757", "hover": "#cf6b4b", "on_fill": "#261713", "danger": "#a82d2d",
    },
    "dark": {
        "bg": "#22211e", "surface": "#2b2925", "border": "#575248", "text": "#f3efe5", "muted": "#b7b1a5",
        "accent": "#f1a58b", "fill": "#e48c70", "hover": "#ed9d83", "on_fill": "#261713", "danger": "#f27474",
    },
}

STYLE = """
headerbar.claude {{ background-image: none; background-color: {bg}; border-bottom: 1px solid {border}; box-shadow: none; }}
headerbar.claude .title, headerbar.claude .subtitle {{ color: {text}; }}
headerbar.claude button {{ background-image: none; background-color: transparent; border: 1px solid transparent;
  box-shadow: none; text-shadow: none; color: {text}; -gtk-icon-shadow: none; }}
headerbar.claude button:hover {{ background-color: {surface}; border-color: {border}; }}
headerbar.claude button:disabled {{ color: {muted}; }}
.claude-page, .claude-page viewport {{ background-color: {bg}; color: {text}; }}
.claude-page label {{ color: {text}; }}
.claude-page label.claude-title {{ font-size: 22px; font-weight: 600; }}
.claude-page label.claude-field {{ font-weight: 600; }}
.claude-page entry {{ background-image: none; background-color: {surface}; color: {text}; border: 1px solid {border};
  border-radius: 8px; padding: 6px 10px; min-height: 28px; box-shadow: none; }}
.claude-page entry:focus {{ border-color: {accent}; box-shadow: 0 0 0 1px {accent}; }}
.claude-page entry selection {{ background-color: {fill}; color: {on_fill}; }}
.claude-page button {{ background-image: none; background-color: {surface}; color: {text}; border: 1px solid {border};
  border-radius: 8px; padding: 6px 14px; min-height: 22px; font-weight: 600; box-shadow: none; text-shadow: none; }}
.claude-page button label {{ color: {text}; }}
.claude-page button:hover {{ background-color: {bg}; border-color: {fill}; }}
.claude-page button.claude-primary {{ background-color: {fill}; border-color: {fill}; }}
.claude-page button.claude-primary label {{ color: {on_fill}; }}
.claude-page button.claude-primary:hover {{ background-color: {hover}; border-color: {hover}; }}
.claude-page button.claude-large {{ min-height: 36px; border-radius: 10px; font-size: 15px; }}
.claude-page button:disabled {{ opacity: 0.5; }}
"""


def prefers_dark() -> bool:
    settings = Gtk.Settings.get_default()
    if settings is not None:
        if settings.props.gtk_application_prefer_dark_theme or "dark" in (settings.props.gtk_theme_name or "").lower():
            return True
    source = Gio.SettingsSchemaSource.get_default()
    schema = source.lookup("org.gnome.desktop.interface", True) if source else None
    if schema is not None and schema.has_key("color-scheme"):
        return Gio.Settings.new("org.gnome.desktop.interface").get_string("color-scheme") == "prefer-dark"
    return False


def install_style() -> dict:
    palette = PALETTES["dark" if prefers_dark() else "light"]
    provider = Gtk.CssProvider()
    provider.load_from_data(STYLE.format(**palette).encode("utf-8"))
    Gtk.StyleContext.add_provider_for_screen(Gdk.Screen.get_default(), provider, Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
    return palette


class PortalView:
    """One WebKit view bound to one portal origin and its own data folders.

    No user scripts, no script message handlers, no developer extras: the
    page gets no native bridge. TLS errors always fail the load.
    """

    def __init__(self, window: "TenantWindow", origin, data_dir: str, cache_dir: str):
        self.window = window
        self.origin = origin
        self.manager = WebKit2.WebsiteDataManager(base_data_directory=data_dir, base_cache_directory=cache_dir)
        self.manager.set_tls_errors_policy(WebKit2.TLSErrorsPolicy.FAIL)
        self.manager.set_persistent_credential_storage_enabled(False)
        self.context = WebKit2.WebContext.new_with_website_data_manager(self.manager)
        # GTK's default rejects cookies first set by an embedded document.
        # The VM needs to set its host-only session cookie after ticket exchange.
        # This policy applies only to this portal's isolated WebKit profile.
        cookies = self.manager.get_cookie_manager()
        cookies.set_persistent_storage(os.path.join(data_dir, "cookies.sqlite"), WebKit2.CookiePersistentStorage.SQLITE)
        cookies.set_accept_policy(WebKit2.CookieAcceptPolicy.ALWAYS)
        self.context.connect("download-started", self._on_download_started)
        self.view = WebKit2.WebView.new_with_context(self.context)
        settings = self.view.get_settings()
        settings.set_enable_developer_extras(False)
        settings.set_javascript_can_open_windows_automatically(False)
        settings.set_javascript_can_access_clipboard(False)
        settings.set_allow_file_access_from_file_urls(False)
        settings.set_allow_universal_access_from_file_urls(False)
        settings.set_allow_top_navigation_to_data_urls(False)
        settings.set_disable_web_security(False)
        settings.set_enable_write_console_messages_to_stdout(False)
        self.view.connect("decide-policy", self._on_decide_policy)
        self.view.connect("create", self._on_create)
        self.view.connect("load-changed", self._on_load_changed)
        self.view.connect("load-failed", self._on_load_failed)
        self.view.connect("load-failed-with-tls-errors", self._on_tls_error)
        self.view.connect("permission-request", self._on_permission_request)
        self.view.connect("context-menu", self._on_context_menu)
        self.view.connect("web-process-terminated", self._on_web_process_terminated)
        self.view.connect("notify::title", lambda *_: window.update_title())

    def load_portal(self):
        self.view.load_uri(self.origin.url)

    # ---- policy ----------------------------------------------------------

    def _on_decide_policy(self, view, decision, kind):
        if kind == WebKit2.PolicyDecisionType.NEW_WINDOW_ACTION:
            uri = decision.get_navigation_action().get_request().get_uri()
            decision.ignore()
            self.window.notice("已阻止打开新窗口：" + policy.describe(uri), error=False)
            return True
        if kind == WebKit2.PolicyDecisionType.NAVIGATION_ACTION:
            # The decision does not say which frame navigates, so subframes
            # (the VM workbench) are left to the portal's CSP and sandbox.
            # Only schemes that no frame should load, such as file: or an
            # external application's scheme, are refused here.
            uri = decision.get_navigation_action().get_request().get_uri()
            if not policy.is_web_navigation(uri):
                decision.ignore()
                self.window.notice("已阻止打开外部地址：" + policy.describe(uri), error=False)
                return True
            return False
        if kind == WebKit2.PolicyDecisionType.RESPONSE:
            # The authoritative main-frame rule: a main-frame document from
            # any other origin is never committed.
            if decision.is_main_frame_main_resource():
                uri = decision.get_response().get_uri() or decision.get_request().get_uri()
                if not policy.is_portal_document(uri, self.origin):
                    decision.ignore()
                    self.window.notice("已阻止主窗口离开门户：" + policy.describe(uri), error=False)
                    return True
            return False
        return False

    def _on_create(self, view, action):
        # Popups and target=_blank never open a window (or an external browser).
        return None

    def _on_load_changed(self, view, event):
        uri = view.get_uri()
        if event in (WebKit2.LoadEvent.STARTED, WebKit2.LoadEvent.REDIRECTED):
            if uri and not policy.is_portal_document(uri, self.origin):
                view.stop_loading()
                self.window.notice("已阻止主窗口离开门户：" + policy.describe(uri), error=False)
        elif event == WebKit2.LoadEvent.COMMITTED and policy.is_portal_document(uri, self.origin):
            self.window.clear_error_notice()
        self.window.update_title()

    def _on_load_failed(self, view, event, uri, error):
        if error.matches(WebKit2.network_error_quark(), WebKit2.NetworkError.CANCELLED) or error.matches(
            WebKit2.policy_error_quark(), WebKit2.PolicyError.FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE
        ):
            return True
        if error.matches(WebKit2.network_error_quark(), WebKit2.NetworkError.UNKNOWN_PROTOCOL) or error.matches(
            WebKit2.policy_error_quark(), WebKit2.PolicyError.CANNOT_SHOW_URI
        ):
            self.window.notice("无法在本窗口打开该地址：" + policy.describe(uri), error=False)
            return True
        self.window.notice("门户加载失败：" + (error.message or "未知错误"), error=True, retry=True)
        return True

    def _on_tls_error(self, view, uri, certificate, errors):
        # Never allow_tls_certificate_for_host(): there is no way past this.
        self.window.notice(
            "无法验证门户的 HTTPS 证书，已停止加载。请联系管理员检查证书；本应用不提供忽略证书的选项。",
            error=True,
            retry=True,
        )
        return True

    def _on_permission_request(self, view, request):
        clipboard = getattr(WebKit2, "ClipboardPermissionRequest", None)
        if clipboard is not None and isinstance(request, clipboard):
            self.window.ask_clipboard(request)
            return True
        request.deny()
        return True

    def _on_context_menu(self, view, menu, event, hit):
        for item in list(menu.get_items()):
            name = item.get_stock_action().value_nick.upper().replace("-", "_")
            if policy.removable_context_actions([name]):
                menu.remove(item)
        return False

    def _on_web_process_terminated(self, view, reason):
        self.window.notice("页面进程已退出。点击“重试”重新加载。", error=True, retry=True)

    def _on_download_started(self, context, download):
        def finished(d):
            path = d.get_destination()
            if path and path.startswith("file://"):
                path = GLib.filename_from_uri(path)[0]
            self.window.notice("下载完成：" + (path or "已保存"), error=False)

        def failed(d, error):
            if not error.matches(WebKit2.download_error_quark(), WebKit2.DownloadError.CANCELLED_BY_USER):
                self.window.notice("下载失败：" + (error.message or "未知错误"), error=True)

        download.connect("finished", finished)
        download.connect("failed", failed)

    # ---- teardown --------------------------------------------------------

    def destroy(self, clear: bool, done):
        """Destroy the view; with ``clear``, first clear all website data
        through WebKit. Calls ``done(cleared)`` once the view is gone."""

        def finish(cleared):
            parent = self.view.get_parent()
            if parent is not None:
                parent.remove(self.view)
            self.view.destroy()
            self.view = None
            self.context = None
            self.manager = None
            done(cleared)

        if not clear:
            finish(False)
            return

        def cleared_cb(manager, result):
            try:
                ok = manager.clear_finish(result)
            except GLib.Error:
                ok = False
            finish(ok)

        self.view.stop_loading()
        self.manager.clear(WebKit2.WebsiteDataTypes.ALL, 0, None, cleared_cb)


class TenantWindow(Gtk.ApplicationWindow):
    def __init__(self, app: Gtk.Application, store: PortalStore):
        super().__init__(application=app, title=APP_NAME)
        self.store = store
        self.origin = store.load()
        # Remove data of servers that are no longer configured, even if an
        # earlier deletion failed.
        store.remove_sessions_except(self.origin)
        self.portal = None
        self.busy = False
        self.cancel_to_portal = False
        self.palette = install_style()
        self.set_default_size(1280, 820)
        self.set_size_request(800, 560)
        self.set_icon_name(APP_ID)

        header = Gtk.HeaderBar(show_close_button=True, title=APP_NAME)
        header.get_style_context().add_class("claude")
        self.set_titlebar(header)
        self.header = header
        reload_button = Gtk.Button.new_from_icon_name("view-refresh-symbolic", Gtk.IconSize.BUTTON)
        reload_button.set_tooltip_text("重新加载 (F5)")
        reload_button.set_action_name("win.reload")
        menu_button = Gtk.MenuButton(tooltip_text="菜单 (F10)")
        menu_button.set_image(Gtk.Image.new_from_icon_name("open-menu-symbolic", Gtk.IconSize.BUTTON))
        menu_button.set_menu_model(self._menu_model())
        self.menu_button = menu_button
        header.pack_end(menu_button)
        header.pack_end(reload_button)

        self._add_actions()

        self.info = Gtk.InfoBar(show_close_button=True, revealed=False)
        self.info.set_no_show_all(True)
        self.info_label = Gtk.Label(xalign=0, wrap=True, selectable=True)
        self.info.get_content_area().add(self.info_label)
        self.info_retry = self.info.add_button("重试", 1)
        self.info.connect("response", self._on_info_response)
        self.info_is_error = False

        self.stack = Gtk.Stack(transition_type=Gtk.StackTransitionType.NONE)
        self.stack.add_named(self._start_page(), "start")
        self.stack.add_named(self._setup_page(), "setup")
        self.portal_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        self.stack.add_named(self.portal_box, "portal")

        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        box.pack_start(self.info, False, False, 0)
        box.pack_start(self.stack, True, True, 0)
        self.add(box)
        self.show_all()
        self.show_start()

    # ---- building --------------------------------------------------------

    def _menu_model(self):
        menu = Gio.Menu()
        page = Gio.Menu()
        page.append("重新加载", "win.reload")
        page.append("回到门户首页", "win.home")
        menu.append_section(None, page)
        server = Gio.Menu()
        server.append("选择使用方式…", "win.start")
        server.append("更换服务器…", "win.change-server")
        server.append("清除本服务器登录数据…", "win.clear-session")
        server.append("清除已保存地址…", "win.clear-address")
        menu.append_section(None, server)
        app = Gio.Menu()
        app.append("关于 Cage Tenant", "win.about")
        app.append("退出", "app.quit")
        menu.append_section(None, app)
        return menu

    def _add_actions(self):
        self.actions = {}
        for name, handler in (
            ("reload", lambda *_: self.retry()),
            ("home", lambda *_: self.portal and self.portal.load_portal()),
            ("start", lambda *_: self.show_start()),
            ("change-server", lambda *_: self.show_setup(changing=True)),
            ("clear-session", lambda *_: self.clear_session()),
            ("clear-address", lambda *_: self.clear_address()),
            ("about", lambda *_: self.show_about()),
            ("menu", lambda *_: self.menu_button.set_active(not self.menu_button.get_active())),
        ):
            action = Gio.SimpleAction.new(name, None)
            action.connect("activate", handler)
            self.add_action(action)
            self.actions[name] = action

    def _start_page(self):
        page = Gtk.Box()
        page.get_style_context().add_class("claude-page")
        outer = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER)
        outer.set_spacing(16)
        page.pack_start(outer, True, True, 0)
        icon = Gtk.Image.new_from_icon_name(APP_ID, Gtk.IconSize.DIALOG)
        icon.set_pixel_size(64)
        title = Gtk.Label(label="选择使用方式")
        title.get_style_context().add_class("claude-title")
        buttons = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=12, homogeneous=True)
        self.local_button = Gtk.Button(label="本地安装(_L)", use_underline=True)
        self.local_button.connect("clicked", lambda *_: self.open_local())
        self.remote_button = Gtk.Button(label="连接远程(_R)", use_underline=True)
        self.remote_button.connect("clicked", lambda *_: self.show_setup(False))
        self.remote_button.get_style_context().add_class("claude-primary")
        for button in (self.local_button, self.remote_button):
            button.get_style_context().add_class("claude-large")
            button.set_size_request(180, 48)
            buttons.pack_start(button, True, True, 0)
        self.start_status = Gtk.Label(wrap=True, max_width_chars=48, selectable=True, justify=Gtk.Justification.CENTER)
        self.start_status.set_no_show_all(True)
        for widget in (icon, title, buttons, self.start_status):
            outer.pack_start(widget, False, False, 0)
        outer.child_set_property(title, "padding", 4)
        return page

    def _setup_page(self):
        outer = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, halign=Gtk.Align.CENTER, valign=Gtk.Align.CENTER)
        outer.set_size_request(560, -1)
        outer.set_spacing(12)
        outer.set_margin_start(24)
        outer.set_margin_end(24)
        self.setup_title = Gtk.Label(xalign=0)
        self.setup_title.get_style_context().add_class("claude-title")
        self.address = Gtk.Entry(placeholder_text="https://cage.example.org", activates_default=True)
        address_label = Gtk.Label(label="门户地址", xalign=0)
        address_label.get_style_context().add_class("claude-field")
        address_label.set_mnemonic_widget(self.address)
        self.address.set_max_length(4096)
        self.address.set_input_purpose(Gtk.InputPurpose.URL)
        self.address.connect("changed", lambda *_: self.set_status(None))
        buttons = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self.connect_button = Gtk.Button(label="连接", can_default=True)
        self.connect_button.get_style_context().add_class("claude-primary")
        self.connect_button.connect("clicked", lambda *_: self.connect_portal())
        self.cancel_button = Gtk.Button(label="取消")
        self.cancel_button.connect("clicked", lambda *_: self._cancel_setup())
        self.clear_button = Gtk.Button(label="清除已保存地址")
        self.clear_button.connect("clicked", lambda *_: self.clear_address())
        for button in (self.connect_button, self.cancel_button, self.clear_button):
            buttons.pack_start(button, False, False, 0)
        self.status = Gtk.Label(xalign=0, wrap=True, max_width_chars=60, selectable=True)
        for widget in (self.setup_title, address_label, self.address, buttons, self.status):
            outer.pack_start(widget, False, False, 0)
        scroller = Gtk.ScrolledWindow(hscrollbar_policy=Gtk.PolicyType.NEVER)
        scroller.get_style_context().add_class("claude-page")
        scroller.add(outer)
        return scroller

    # ---- pages -----------------------------------------------------------

    def show_setup(self, changing: bool):
        changing = changing and self.origin is not None
        self.setup_title.set_text("更换服务器" if changing else "连接远程")
        self.address.set_text(self.origin.origin if self.origin else "")
        self.cancel_to_portal = changing and self.portal is not None
        self.cancel_button.set_label("取消" if self.cancel_to_portal else "返回")
        self.cancel_button.set_visible(True)
        self.set_status(None)
        self.stack.set_visible_child_name("setup")
        self.connect_button.grab_default()
        self.address.grab_focus()
        self.update_commands()
        self.update_title()

    def _cancel_setup(self):
        if self.cancel_to_portal and self.origin is not None and self.portal is not None:
            self.stack.set_visible_child_name("portal")
            self.portal.view.grab_focus()
            self.update_commands()
        else:
            self.show_start()

    def show_start(self):
        self.set_start_status(None)
        self.hide_notice()
        self.stack.set_visible_child_name("start")
        self.remote_button.grab_focus()
        self.update_commands()
        self.update_title()

    def set_start_status(self, message, error=False):
        if not message:
            self.start_status.set_text("")
            self.start_status.hide()
            return
        color = self.palette["danger"] if error else self.palette["muted"]
        self.start_status.set_markup("<span foreground='%s'>%s</span>" % (color, GLib.markup_escape_text(message)))
        self.start_status.show()

    def open_local(self):
        if self.busy:
            return
        if local_cage.installed():
            try:
                local_cage.launch()
                self.set_start_status(None)
            except local_cage.LocalCageError as e:
                self.set_start_status(str(e), error=True)
            return
        if not local_cage.supported():
            self.set_start_status("本地安装需要 x86_64 的 Debian 或 Ubuntu。", error=True)
            return
        self._set_busy(True)
        self.set_start_status("正在下载本地安装包…")
        # A visible folder: the snap App Center that opens .deb files on Ubuntu
        # cannot read hidden folders such as ~/.cache.
        folder = GLib.get_user_special_dir(GLib.UserDirectory.DIRECTORY_DOWNLOAD) or os.path.join(self.store.cache_dir, "local")

        def work():
            path, error = None, "无法打开本地 Cage，请重试。"
            try:
                path, error = local_cage.prepare_installer(folder), None
            except local_cage.LocalCageError as e:
                error = str(e)
            except Exception:
                pass
            finally:
                # Always hand back to the main loop so the buttons come back.
                GLib.idle_add(done, path, error)

        def done(path, error):
            self._set_busy(False)
            if error:
                self.set_start_status(error, error=True)
                return False
            try:
                Gio.AppInfo.launch_default_for_uri(Gio.File.new_for_path(path).get_uri(), None)
                self.set_start_status("已打开安装包，请在软件安装器中完成安装。")
            except GLib.Error:
                self.set_start_status("无法打开安装包：%s" % path, error=True)
            return False

        threading.Thread(target=work, daemon=True).start()

    def show_portal(self):
        if self.origin is None:
            self.show_setup(False)
            return
        if self.portal is None:
            try:
                data_dir, cache_dir = self.store.session_dirs(self.origin, create=True)
            except OSError as e:
                self.show_setup(False)
                self.set_status("无法创建会话文件夹：%s" % e.strerror, error=True)
                return
            self.portal = PortalView(self, self.origin, data_dir, cache_dir)
            self.portal_box.pack_start(self.portal.view, True, True, 0)
            self.portal.view.show()
            self.portal.load_portal()
        self.stack.set_visible_child_name("portal")
        self.portal.view.grab_focus()
        self.update_commands()
        self.update_title()

    def update_title(self):
        host = self.origin.host if self.origin else None
        self.set_title(f"{APP_NAME} — {host}" if host else APP_NAME)
        self.header.set_title(APP_NAME)
        self.header.set_subtitle(self.origin.origin if self.origin else None)

    def on_start_page(self) -> bool:
        return self.stack.get_visible_child_name() == "start"

    def update_commands(self):
        # Reload and home never connect from the start page.
        ready = not self.busy and not self.on_start_page()
        live = ready and self.portal is not None
        self.actions["reload"].set_enabled(live or (ready and self.origin is not None))
        self.actions["home"].set_enabled(live)
        self.actions["clear-session"].set_enabled(not self.busy and self.origin is not None)
        self.actions["clear-address"].set_enabled(not self.busy and self.origin is not None)
        self.actions["change-server"].set_enabled(not self.busy)
        self.connect_button.set_sensitive(not self.busy)
        self.clear_button.set_sensitive(not self.busy and self.origin is not None)
        self.cancel_button.set_sensitive(not self.busy)
        self.local_button.set_sensitive(not self.busy)
        self.remote_button.set_sensitive(not self.busy)
        self.actions["start"].set_enabled(not self.busy)

    def set_status(self, message, error=False):
        if not message:
            self.status.set_text("")
            self.status.hide()
            return
        color = self.palette["danger"] if error else self.palette["muted"]
        self.status.set_markup("<span foreground='%s'>%s</span>" % (color, GLib.markup_escape_text(message)))
        self.status.show()

    def notice(self, message, error, retry=False):
        self.info_label.set_text(message)
        self.info.set_message_type(Gtk.MessageType.ERROR if error else Gtk.MessageType.INFO)
        self.info_retry.set_visible(retry)
        self.info_is_error = error
        self.info.show()
        self.info.set_revealed(True)

    def clear_error_notice(self):
        if self.info_is_error:
            self.hide_notice()

    def hide_notice(self):
        self.info.set_revealed(False)
        self.info.hide()
        self.info_is_error = False

    def _on_info_response(self, bar, response):
        self.hide_notice()
        if response == 1:
            self.retry()

    # ---- commands --------------------------------------------------------

    def connect_portal(self):
        if self.busy:
            return
        plan = policy.plan_connect(self.address.get_text(), self.origin)
        if plan.action == ConnectAction.REJECT:
            self.set_status(plan.message, error=True)
            self.address.grab_focus()
        elif plan.action == ConnectAction.KEEP:
            self.show_portal()
        elif plan.action == ConnectAction.CONNECT:
            if self._save(plan.origin):
                self.store.remove_sessions_except(plan.origin)
                self.show_portal()
        else:
            old = self.origin
            self.confirm(
                f"切换到 {plan.origin.origin} 会关闭当前门户，并删除 {old.origin} 在本机的登录数据。继续吗？",
                "切换(_S)",
                lambda: self._switch(old, plan.origin),
            )

    def _switch(self, old, new):
        def after(cleared):
            removed = self.store.remove_session(old)
            self._set_busy(False)
            if not self._save(new):
                self.show_setup(False)
                return
            self.hide_notice()
            if not removed:
                self.notice("旧服务器的部分会话文件未能删除，下次启动时会再次删除。", error=False)
            self.show_portal()

        self._set_busy(True)
        self._destroy_portal(True, after)

    def _save(self, origin):
        try:
            self.store.save(origin)
        except OSError as e:
            self.set_status("无法保存门户地址：%s" % (e.strerror or e), error=True)
            return False
        self.origin = origin
        return True

    def clear_session(self):
        if self.busy or self.origin is None:
            return

        def go():
            origin = self.origin
            # Reopen only when the portal was on screen; from the start or
            # setup page, stay there.
            reopen = self.stack.get_visible_child_name() == "portal"

            def after(cleared):
                removed = self.store.remove_session(origin)
                self._set_busy(False)
                if removed or cleared:
                    self.notice("已清除本服务器的登录数据。", error=False)
                else:
                    self.notice("未能删除登录数据。请关闭 Cage Tenant 后重新打开再试。", error=True)
                if reopen:
                    if removed or cleared:
                        self.show_portal()
                    else:
                        self.show_setup(False)
                elif self.cancel_to_portal:
                    self.cancel_to_portal = False
                    self.cancel_button.set_label("返回")

            self._set_busy(True)
            self._destroy_portal(True, after)

        self.confirm(
            f"将关闭门户并删除 {self.origin.origin} 在本机的登录数据（Cookie、站点存储和缓存），之后需要重新登录。继续吗？",
            "清除(_C)",
            go,
        )

    def clear_address(self):
        if self.busy or self.origin is None:
            return

        def go():
            origin = self.origin

            def after(cleared):
                removed = self.store.remove_session(origin)
                self._set_busy(False)
                try:
                    self.store.clear()
                except OSError as e:
                    self.notice("无法删除已保存的地址：%s" % (e.strerror or e), error=True)
                    return
                self.origin = None
                self.hide_notice()
                self.show_setup(False)
                self.set_status(
                    "已清除保存的地址和登录数据。" if removed else "已清除保存的地址；部分会话文件未能删除，下次启动时会再次删除。"
                )

            self._set_busy(True)
            self._destroy_portal(True, after)

        self.confirm(f"将删除已保存的门户地址 {self.origin.origin}，并删除该服务器在本机的登录数据。继续吗？", "清除(_C)", go)

    def retry(self):
        if self.busy or self.origin is None or self.on_start_page():
            return
        self.hide_notice()
        if self.portal is not None:
            if self.portal.view.get_uri() and policy.is_portal_document(self.portal.view.get_uri(), self.origin):
                self.portal.view.reload()
            else:
                self.portal.load_portal()
        else:
            self.show_portal()

    def _destroy_portal(self, clear, done):
        portal = self.portal
        self.portal = None
        if portal is None:
            done(False)
            return
        portal.destroy(clear, done)

    def _set_busy(self, busy):
        self.busy = busy
        self.update_commands()

    def confirm(self, message, accept_label, on_accept):
        dialog = Gtk.MessageDialog(
            transient_for=self,
            modal=True,
            message_type=Gtk.MessageType.WARNING,
            buttons=Gtk.ButtonsType.NONE,
            text=message,
        )
        dialog.add_button("取消(_N)", Gtk.ResponseType.CANCEL)
        dialog.add_button(accept_label, Gtk.ResponseType.ACCEPT).get_style_context().add_class("destructive-action")
        dialog.set_default_response(Gtk.ResponseType.CANCEL)

        def response(d, r):
            d.destroy()
            if r == Gtk.ResponseType.ACCEPT:
                on_accept()

        dialog.connect("response", response)
        dialog.show()

    def ask_clipboard(self, request):
        dialog = Gtk.MessageDialog(
            transient_for=self,
            modal=True,
            message_type=Gtk.MessageType.QUESTION,
            buttons=Gtk.ButtonsType.NONE,
            text="页面请求读取剪贴板内容。允许本次读取吗？",
        )
        dialog.add_button("拒绝(_D)", Gtk.ResponseType.REJECT)
        dialog.add_button("允许(_A)", Gtk.ResponseType.ACCEPT)
        dialog.set_default_response(Gtk.ResponseType.REJECT)

        def response(d, r):
            d.destroy()
            request.allow() if r == Gtk.ResponseType.ACCEPT else request.deny()

        dialog.connect("response", response)
        dialog.show()

    def show_about(self):
        dialog = Gtk.AboutDialog(
            transient_for=self,
            modal=True,
            program_name=APP_NAME,
            version=VERSION,
            logo_icon_name=APP_ID,
            comments="WebKitGTK %d.%d.%d" % webkit_version(),
            license_type=Gtk.License.MIT_X11,
            copyright="Copyright (c) 2026 Sylvain Zimmer, Claude Cage contributors",
        )
        dialog.connect("response", lambda d, r: d.destroy())
        dialog.show()


class TenantApplication(Gtk.Application):
    def __init__(self, store: PortalStore, unique: bool):
        flags = getattr(Gio.ApplicationFlags, "DEFAULT_FLAGS", Gio.ApplicationFlags.FLAGS_NONE)
        if not unique:
            flags |= Gio.ApplicationFlags.NON_UNIQUE
        super().__init__(application_id=APP_ID, flags=flags)
        self.store = store
        self.window = None

    def do_startup(self):
        Gtk.Application.do_startup(self)
        quit_action = Gio.SimpleAction.new("quit", None)
        quit_action.connect("activate", lambda *_: self.quit())
        self.add_action(quit_action)
        self.set_accels_for_action("app.quit", ["<Primary>q"])
        self.set_accels_for_action("win.reload", ["F5", "<Primary>r"])
        self.set_accels_for_action("win.change-server", ["<Primary>l"])
        self.set_accels_for_action("win.clear-session", ["<Primary><Shift>Delete"])
        self.set_accels_for_action("win.menu", ["F10"])

    def do_activate(self):
        if self.window is None:
            self.window = TenantWindow(self, self.store)
        self.window.present()


def session_bus_available(environ=os.environ) -> bool:
    if environ.get("DBUS_SESSION_BUS_ADDRESS"):
        return True
    runtime = environ.get("XDG_RUNTIME_DIR")
    return bool(runtime) and os.path.exists(os.path.join(runtime, "bus"))


def run(version: str) -> int:
    global VERSION
    VERSION = version
    if webkit_version()[:2] < MIN_WEBKIT:
        sys.stderr.write("Cage Tenant needs WebKitGTK %d.%d or newer (found %d.%d.%d).\n" % (MIN_WEBKIT + webkit_version()))
        return 1
    GLib.set_prgname(APP_ID)
    GLib.set_application_name(APP_NAME)
    app = TenantApplication(PortalStore.for_environment(), unique=session_bus_available())
    return app.run([sys.argv[0]])
