package io.github.leexeo.cage.mobile;

import android.app.Activity;
import android.app.AlertDialog;
import android.app.Dialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.content.res.AssetManager;
import android.graphics.Typeface;
import android.graphics.drawable.Drawable;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.content.res.ColorStateList;
import android.view.Gravity;
import android.net.Uri;
import android.os.Bundle;
import android.os.Build;
import android.provider.MediaStore;
import android.text.InputType;
import android.text.TextUtils;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.webkit.CookieManager;
import android.webkit.MimeTypeMap;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.*;
import cagemobile.Cagemobile;
import cagemobile.Client;
import cagemobile.Tenant;
import java.io.*;
import java.time.Instant;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONObject;
import org.json.JSONArray;
import java.util.LinkedHashMap;
import java.util.Map;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import androidx.webkit.ScriptHandler;
import androidx.core.content.FileProvider;

public final class MainActivity extends Activity {
    private static final int PICK_INVITATION = 41;
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    // Server sign-in and VM starts can take minutes; keep them off the pairing worker.
    private final ExecutorService serverWorker = Executors.newCachedThreadPool();
    private ServerAccounts servers;
    private final Map<String, Tenant> tenants = new LinkedHashMap<>();
    private LinearLayout machineList;
    private EditText serverPortalField, serverUserField, serverPasswordField;
    private SharedPreferences trust;
    private HostCredentials credentials;
    private AppUpdate updates;
    private DeviceKey device;
    private String fingerprint = "";
    private Invitation invitation;
    private Invitation.Saved imported;
    private WebView browser;
    private final Map<String, ConnectedDevice> connections = new LinkedHashMap<>();
    private ConnectedDevice activeDevice;
    private boolean viewingWorkspace;
    private boolean openArchivedAfterConnect;
    private static final class ConnectedDevice {
        final String id;
        final String hostKey;
        final Client client;
        final Uri launch;
        WebView view;
        ScriptHandler script;
        String name;
        String account; // Set for a virtual machine opened from a Cage server account.
        ConnectedDevice(String hostKey, Client client, Uri launch, String name) {
            this.id=java.util.UUID.randomUUID().toString(); this.hostKey=hostKey;
            this.client=client; this.launch=launch; this.name=name;
        }
    }
    private enum Page { HOME, SETTINGS, HOST, RELAY, PAIR, CODE, SERVERS, MACHINES }
    private Page page = Page.HOME;
    private FrameLayout root;
    private LinearLayout form;
    private FrameLayout home;
    private View overlay;
    private EditText relayField, pinField;
    private TextView localPin, status, inviteStatus;
    private CheckBox verified;
    private Switch customRelay;
    private Button connect, forgetHost;
    private boolean busy;
    private int generation;
    private MobileTheme palette;
    private SharedPreferences appearance;
    private static final int SCAN = 42;
    private static final int PICK_FILES = 45, CAMERA_FOR_FILES = 46;
    // A pending <input type="file"> request from the workbench, and its camera photo.
    private ValueCallback<Uri[]> pendingFiles;
    private Intent pendingPicker;
    private File pendingPhoto;
    private boolean sharing;

    private void rememberRelay(String relay) {
        trust.edit().putString("preferred-relay", relay).apply();
    }
    private String preferredRelay() {
        return trust.getString("preferred-relay", trust.getString("relay", Cagemobile.defaultRelay()));
    }
    private static String deviceModel() {
        String model = Build.MODEL.trim();
        String brand = Build.MANUFACTURER.trim();
        if (brand.isEmpty() || model.regionMatches(true, 0, brand, 0, brand.length())) return model;
        return Character.toUpperCase(brand.charAt(0)) + brand.substring(1) + " " + model;
    }
    private String selectedRelay() throws Exception {
        return customRelay.isChecked() ? Invitation.relay(relayField.getText().toString()) : Cagemobile.defaultRelay();
    }
    private void displayRelay(String value) {
        boolean custom = !Cagemobile.defaultRelay().equals(value);
        customRelay.setChecked(custom);
        relayField.setText(custom ? value : "");
        relayField.setVisibility(custom ? View.VISIBLE : View.GONE);
    }
    private void pairScan(String code) {
        if (busy) return;
        if (device == null) { status.setText("设备密钥尚未就绪，请稍后重试。"); return; }
        String relay = preferredRelay();
        int attempt = ++generation;
        setConnecting(true);
        status.setText("正在与电脑配对…");
        worker.execute(() -> {
            try {
                Invitation.Saved saved = new Invitation.Saved(new JSONObject(Cagemobile.pair(code, relay, device)));
                credentials.save(saved.relay, saved.pin, saved.lookup);
                rememberRelay(saved.relay);
                runOnUiThread(() -> {
                    if (generation != attempt) return;
                    invitation = null;
                    imported = null;
                    setConnecting(false);
                    connect();
                });
            } catch (Exception e) {
                runOnUiThread(() -> {
                    if (generation != attempt) return;
                    setConnecting(false);
                    status.setText("配对未完成。请确认两端使用同一中继，再用电脑上新生成的二维码或配对码重试。");
                });
            }
        });
    }

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        trust = getSharedPreferences("verified-host", MODE_PRIVATE);
        appearance = getSharedPreferences("appearance", MODE_PRIVATE);
        palette = MobileTheme.apply(this, themeMode());
        credentials = new HostCredentials(trust);
        servers = new ServerAccounts(getSharedPreferences("server-accounts", MODE_PRIVATE));
        updates = new AppUpdate(this, state);
        showSetup();
        if (state != null) {
            String restoredPage = state.getString("page", Page.HOME.name());
            Page restored = "ENVIRONMENT".equals(restoredPage) ? Page.HOME : Page.valueOf(restoredPage);
            if (restored == Page.PAIR || restored == Page.CODE) showPair(restored == Page.CODE);
            if (restored == Page.SETTINGS) showSettings(false);
            if (restored == Page.HOST || restored == Page.RELAY) showAdvanced(restored);
            if (restored == Page.SERVERS || restored == Page.MACHINES) showServers();
        }
        worker.execute(() -> {
            try {
                DeviceKey key = new DeviceKey();
                String pin = Cagemobile.fingerprint(key);
                runOnUiThread(() -> {
                    if (isDestroyed()) return;
                    device = key;
                    fingerprint = pin;
                    if (localPin != null) localPin.setText(pin);
                });
            } catch (Exception e) {
                runOnUiThread(() -> {
                    if (!isDestroyed()) status.setText("无法使用 Android Keystore 设备密钥。");
                });
            }
        });
    }
    @Override protected void onSaveInstanceState(Bundle saved) {
        super.onSaveInstanceState(saved);
        updates.saveState(saved);
        saved.putString("page", page.name());
    }

    private int dp(int value) { return Math.round(getResources().getDisplayMetrics().density * value); }
    private String themeMode() { return appearance.getString("theme", "system"); }
    private void insets(View root) {
        root.setOnApplyWindowInsetsListener((v, w) -> {
            v.setPadding(w.getSystemWindowInsetLeft(), w.getSystemWindowInsetTop(), w.getSystemWindowInsetRight(), w.getSystemWindowInsetBottom());
            return w.consumeSystemWindowInsets();
        });
        root.requestApplyInsets();
    }
    private LinearLayout column() {
        LinearLayout group = new LinearLayout(this);
        group.setOrientation(LinearLayout.VERTICAL);
        return group;
    }
    private GradientDrawable surface(int color, int radius, boolean outline) {
        GradientDrawable shape = new GradientDrawable();
        shape.setColor(color);
        shape.setCornerRadius(dp(radius));
        if (outline) shape.setStroke(dp(1), palette.line);
        return shape;
    }
    private Drawable pressable(int color, int radius, boolean outline) {
        return new RippleDrawable(ColorStateList.valueOf(palette.dark ? 0x14ffffff : 0x0d000000), surface(color, radius, outline), null);
    }
    private TextView text(String value, int size, int color) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(size);
        view.setTextColor(color);
        return view;
    }
    private TextView label(String value, int size) {
        TextView view = text(value, size, palette.muted);
        view.setPadding(0, dp(12), 0, dp(3));
        form.addView(view);
        return view;
    }
    private TextView title(String value, int size) {
        TextView view = text(value, size, palette.ink);
        view.setTypeface(MobileTheme.SERIF);
        if (Build.VERSION.SDK_INT >= 28) view.setAccessibilityHeading(true);
        return view;
    }
    private static void buttonRole(View view) {
        view.setFocusable(true);
        view.setAccessibilityDelegate(new View.AccessibilityDelegate() {
            @Override public void onInitializeAccessibilityNodeInfo(View host, android.view.accessibility.AccessibilityNodeInfo info) {
                super.onInitializeAccessibilityNodeInfo(host, info);
                info.setClassName(Button.class.getName());
            }
        });
    }
    // A hand-drawn doodle above a short line, for empty lists and onboarding.
    private void emptyState(LinearLayout parent, String doodle, String message) {
        LinearLayout box = column();
        box.setGravity(Gravity.CENTER_HORIZONTAL);
        box.setPadding(0, dp(12), 0, dp(8));
        box.addView(image("cage_doodle_" + doodle), dp(136), dp(102));
        TextView line = text(message, 15, palette.muted);
        line.setGravity(Gravity.CENTER);
        line.setPadding(dp(12), dp(10), dp(12), 0);
        box.addView(line, -1, -2);
        parent.addView(box, -1, -2);
    }
    private TextView chip(String value, int color) {
        TextView view = text(value, 12, color);
        view.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        view.setPadding(dp(10), dp(4), dp(10), dp(4));
        view.setBackground(surface((color & 0x00ffffff) | 0x24000000, 999, false));
        return view;
    }
    // A card row with a title, a quieter detail line and an optional state chip.
    private LinearLayout listRow(LinearLayout parent, String name, String detail, String state, int tone, View.OnClickListener action) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        row.setPadding(dp(16), dp(10), dp(14), dp(10));
        row.setMinimumHeight(dp(60));
        row.setBackground(pressable(palette.panel, 12, true));
        row.setOnClickListener(action);
        buttonRole(row);
        LinearLayout texts = column();
        TextView title = text(name, 16, palette.ink);
        title.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        title.setSingleLine(); title.setEllipsize(TextUtils.TruncateAt.END);
        texts.addView(title);
        if (!detail.isEmpty()) {
            TextView more = text(detail, 13, palette.muted);
            more.setSingleLine(); more.setEllipsize(TextUtils.TruncateAt.END);
            texts.addView(more);
        }
        row.addView(texts, new LinearLayout.LayoutParams(0, -2, 1));
        if (state != null) row.addView(chip(state, tone));
        row.setContentDescription(name + (detail.isEmpty() ? "" : "，" + detail) + (state == null ? "" : "，" + state));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, -2);
        lp.topMargin = dp(8);
        parent.addView(row, lp);
        return row;
    }
    private Button button(LinearLayout parent, String name, boolean primary, View.OnClickListener action) {
        Button view = new Button(this);
        view.setText(name);
        view.setAllCaps(false);
        view.setTextSize(16);
        view.setTextColor(primary ? palette.onAction : palette.ink);
        view.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        view.setStateListAnimator(null);
        view.setBackground(pressable(primary ? palette.action : palette.soft, 14, false));
        view.setOnClickListener(action);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, dp(52));
        lp.topMargin = dp(8);
        parent.addView(view, lp);
        return view;
    }
    private ImageButton icon(String resource, String name, View.OnClickListener action) {
        ImageButton view = new ImageButton(this);
        view.setImageResource(getResources().getIdentifier("cage_" + resource, "drawable", getPackageName()));
        view.setContentDescription(name);
        view.setColorFilter(palette.ink);
        view.setScaleType(ImageView.ScaleType.CENTER);
        view.setBackground(pressable(palette.soft, 24, false));
        view.setOnClickListener(action);
        return view;
    }
    private View connectComputerButton(View.OnClickListener action) {
        LinearLayout view = new LinearLayout(this);
        view.setOrientation(LinearLayout.HORIZONTAL);
        view.setGravity(Gravity.CENTER);
        view.setPadding(dp(16), 0, dp(16), 0);
        view.setBackground(pressable(palette.action, 14, false));
        view.setContentDescription("连接电脑");
        view.setOnClickListener(action);
        buttonRole(view);
        ImageView computer = image("cage_computer");
        computer.setColorFilter(palette.onAction);
        view.addView(computer, dp(22), dp(22));
        TextView title = text("连接电脑", 16, palette.onAction);
        title.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        title.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        LinearLayout.LayoutParams caption = new LinearLayout.LayoutParams(-2, -2);
        caption.setMarginStart(dp(10));
        view.addView(title, caption);
        return view;
    }
    private EditText field(String hint, String value) {
        EditText input = new EditText(this);
        input.setSingleLine();
        input.setTextSize(16);
        input.setTextColor(palette.ink);
        input.setHintTextColor(palette.muted);
        input.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        input.setHint(hint);
        input.setText(value);
        input.setBackground(surface(palette.soft, 12, false));
        input.setPadding(dp(14), 0, dp(14), 0);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, dp(52));
        lp.topMargin = dp(8);
        form.addView(input, lp);
        return input;
    }
    private void statusView() {
        status = label("", 14);
        status.setAccessibilityLiveRegion(View.ACCESSIBILITY_LIVE_REGION_POLITE);
        status.setMinHeight(dp(22));
    }
    private void layout(Page next) {
        page = next;
        overlay = null;
        root = new FrameLayout(this);
        root.setBackgroundColor(palette.bg);
        setContentView(root);
        insets(root);
        localPin = null;
    }
    private ImageView image(String resource) {
        ImageView img = new ImageView(this);
        img.setImageResource(getResources().getIdentifier(resource, "drawable", getPackageName()));
        img.setScaleType(ImageView.ScaleType.FIT_CENTER);
        img.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        return img;
    }
    private void showSetup() {
        viewingWorkspace = false;
        if (browser != null) browser.onPause();
        layout(Page.HOME);
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        root.addView(scroll, -1, -1);
        FrameLayout body = new FrameLayout(this);
        body.setMinimumHeight(dp(440));
        scroll.addView(body, new ScrollView.LayoutParams(-1, -2));
        home = body;
        FrameLayout.LayoutParams gear = new FrameLayout.LayoutParams(dp(48), dp(48), Gravity.TOP | Gravity.RIGHT);
        gear.setMargins(dp(18), dp(12), dp(18), 0);
        body.addView(icon("gear", "设置", v -> showSettings(false)), gear);

        form = column();
        form.setGravity(Gravity.CENTER_HORIZONTAL);
        LinearLayout homeForm = form;
        // Balance the larger brand/action gap so the logo stays in place (§3.6).
        form.setPadding(0, dp(24), 0, 0);
        FrameLayout.LayoutParams hero = new FrameLayout.LayoutParams(dp(300), -2, Gravity.CENTER);
        hero.leftMargin = dp(24);
        hero.rightMargin = dp(24);
        body.addView(form, hero);
        form.addView(image("cage_logo"), dp(96), dp(96));
        TextView title = title("Super Cage", 34);
        title.setGravity(Gravity.CENTER);
        title.setPadding(0, dp(10), 0, 0);
        form.addView(title, -2, -2);
        ImageView squiggle = image("cage_doodle_squiggle");
        squiggle.setScaleType(ImageView.ScaleType.FIT_XY);
        form.addView(squiggle, dp(150), dp(12));
        TextView subtitle = text("在手机上接着写代码", 16, palette.muted);
        subtitle.setGravity(Gravity.CENTER);
        subtitle.setPadding(0, dp(12), 0, dp(52));
        form.addView(subtitle, -1, -2);
        form.addView(connectComputerButton(v -> showPair(false)), new LinearLayout.LayoutParams(-1, dp(52)));
        Button server = button(form, "连接服务器", false, v -> showServers());
        server.setBackground(pressable(palette.panel, 14, true));
        ((LinearLayout.LayoutParams) server.getLayoutParams()).topMargin = dp(12);
        statusView();
        body.addOnLayoutChangeListener((view, left, top, right, bottom, oldLeft, oldTop, oldRight, oldBottom) -> {
            int height = bottom - top;
            if (height == oldBottom - oldTop && right - left == oldRight - oldLeft) return;
            hero.width = Math.min(dp(300), Math.max(0, right - left - dp(48)));
            homeForm.setLayoutParams(hero);
            int spacing = Math.min(dp(56), Math.max(dp(28), Math.round(height * 0.07f))) + dp(16);
            subtitle.setPadding(0, dp(12), 0, spacing);
        });
    }
    private void closeSheet() {
        if (busy) return;
        showSetup();
    }
    private void showPair(boolean code) {
        boolean opening = overlay == null;
        if (overlay != null) root.removeView(overlay);
        page = code ? Page.CODE : Page.PAIR;
        home.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS);
        home.setDescendantFocusability(android.view.ViewGroup.FOCUS_BLOCK_DESCENDANTS);
        FrameLayout layer = new FrameLayout(this);
        overlay = layer;
        root.addView(layer, -1, -1);
        View shade = new View(this);
        shade.setBackgroundColor(0x64000000);
        shade.setOnClickListener(v -> closeSheet());
        shade.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        shade.setFocusable(false);
        layer.addView(shade, -1, -1);
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(false);
        scroll.setBackground(surface(palette.panel, 20, false));
        scroll.setClickable(true);
        FrameLayout.LayoutParams sheet = new FrameLayout.LayoutParams(Math.min(getResources().getDisplayMetrics().widthPixels, dp(500)), -2,
            Gravity.BOTTOM | Gravity.CENTER_HORIZONTAL);
        sheet.setMargins(dp(8), dp(12), dp(8), dp(8));
        sheet.width = Math.min(sheet.width - dp(16), dp(500));
        layer.addView(scroll, sheet);
        form = column();
        form.setPadding(dp(20), dp(14), dp(20), dp(14));
        scroll.addView(form);
        FrameLayout heading = new FrameLayout(this);
        form.addView(heading, -1, dp(48));
        TextView title = title(code ? "配对码" : "连接电脑", 26);
        heading.addView(title, new FrameLayout.LayoutParams(-2, -2, Gravity.LEFT | Gravity.CENTER_VERTICAL));
        if (!code) {
            form.addView(image("cage_doodle_laptop"), -1, dp(108));
            label("在电脑的 Super Cage「设置 → 远程连接」中生成配对二维码或配对码。", 14);
            button(form, "扫码连接", true, v -> startActivityForResult(new Intent(this, ScanActivity.class), SCAN));
            button(form, "使用配对码", false, v -> showPair(true));
        } else {
            label("粘贴电脑上复制的一次性配对码。", 15);
            EditText manualCode = field("一次性配对码", "");
            button(form, "连接电脑", true, v -> {
                String value = manualCode.getText().toString().trim();
                if (value.isEmpty()) { status.setText("请粘贴配对码。"); return; }
                manualCode.setText("");
                android.view.inputmethod.InputMethodManager ime = (android.view.inputmethod.InputMethodManager)getSystemService(INPUT_METHOD_SERVICE);
                ime.hideSoftInputFromWindow(manualCode.getWindowToken(), 0);
                pairScan(value);
            });
            button(form, "返回配对方式", false, v -> showPair(false));
        }
        if (!code && !connections.isEmpty()) {
            label("已连接的设备", 15);
            label("长按设备可取消连接", 14);
            for (ConnectedDevice connected : connections.values()) {
                LinearLayout entry = listRow(form, connected.name, connected.account == null ? "电脑" : "服务器虚拟机", "进入", palette.clayInk, v -> enterDevice(connected));
                entry.setBackground(pressable(palette.soft, 12, false));
                entry.setOnLongClickListener(v -> {confirmDisconnect(connected); return true;});
            }
        }
        if (credentials.saved() && !code) {
            Button again = button(form, "重新连接已配对电脑", false, v -> connect());
            again.setTextColor(palette.clayInk);
            again.setBackground(pressable(palette.panel, 14, true));
        }
        statusView();
        form.setFocusableInTouchMode(true);
        form.requestFocus();
        if (opening && android.animation.ValueAnimator.areAnimatorsEnabled()) {
            shade.setAlpha(0f);
            scroll.getViewTreeObserver().addOnPreDrawListener(new android.view.ViewTreeObserver.OnPreDrawListener() {
                @Override public boolean onPreDraw() {
                    scroll.getViewTreeObserver().removeOnPreDrawListener(this);
                    if (overlay != layer) return true;
                    // Start below the viewport after measuring, before the first visible frame.
                    scroll.setTranslationY(scroll.getHeight() + sheet.bottomMargin);
                    scroll.animate().translationY(0).setDuration(300)
                        .setInterpolator(new android.view.animation.PathInterpolator(0.2f, 0f, 0f, 1f)).start();
                    shade.animate().alpha(1f).setDuration(200).start();
                    return true;
                }
            });
        }
    }

    private void settingsPage(Page next, String name, Runnable back) {
        layout(next);
        ScrollView scroll = new ScrollView(this);
        root.addView(scroll, -1, -1);
        form = column();
        form.setPadding(dp(22), dp(14), dp(22), dp(28));
        form.setFocusableInTouchMode(true);
        scroll.addView(form);
        form.addView(icon("back", "返回", v -> back.run()), dp(48), dp(48));
        TextView title = title(name, 32);
        title.setPadding(0, dp(22), 0, dp(14));
        form.addView(title);
    }
    private Button row(String title, View.OnClickListener listener) {
        Button b = button(form, title, false, listener);
        b.setGravity(Gravity.CENTER_VERTICAL | Gravity.START);
        b.setPadding(dp(20), 0, dp(16), 0);
        b.setBackground(pressable(palette.panel, 12, true));
        return b;
    }
    private static final String[] THEMES = {"system", "light", "dark"};
    private static final String[] THEME_NAMES = {"跟随系统", "浅色", "深色"};
    private void showSettings(boolean focusTheme) {
        settingsPage(Page.SETTINGS, "设置", this::showSetup);
        label("外观", 14);
        int index = Math.max(0, java.util.Arrays.asList(THEMES).indexOf(themeMode()));
        LinearLayout cards = new LinearLayout(this);
        LinearLayout.LayoutParams cardsSize = new LinearLayout.LayoutParams(-1, dp(100));
        cardsSize.topMargin = dp(6);
        form.addView(cards, cardsSize);
        View chooser = null;
        for (int i = 0; i < THEMES.length; i++) {
            String value = THEMES[i];
            boolean chosen = i == index;
            LinearLayout card = column();
            card.setGravity(Gravity.CENTER);
            GradientDrawable shape = surface(chosen ? palette.soft : palette.panel, 12, false);
            shape.setStroke(dp(chosen ? 2 : 1), chosen ? palette.clay : palette.line);
            card.setBackground(new RippleDrawable(ColorStateList.valueOf(palette.dark ? 0x14ffffff : 0x0d000000), shape, null));
            card.setContentDescription("主题：" + THEME_NAMES[i]);
            card.setSelected(chosen);
            buttonRole(card);
            card.setOnClickListener(v -> {
                appearance.edit().putString("theme", value).apply();
                palette = MobileTheme.apply(this, value);
                showSettings(!v.isInTouchMode());
            });
            LinearLayout art = new LinearLayout(this);
            art.setGravity(Gravity.CENTER);
            for (String doodle : "system".equals(value) ? new String[]{"sun", "moon"} : new String[]{"light".equals(value) ? "sun" : "moon"}) {
                LinearLayout.LayoutParams size = new LinearLayout.LayoutParams(dp(56), dp(42));
                if (art.getChildCount() > 0) size.setMarginStart(-dp(22));
                art.addView(image("cage_doodle_" + doodle), size);
            }
            card.addView(art);
            TextView name = text(THEME_NAMES[i], 14, palette.ink);
            name.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
            card.addView(name);
            LinearLayout.LayoutParams size = new LinearLayout.LayoutParams(0, -1, 1);
            if (i > 0) size.setMarginStart(dp(10));
            cards.addView(card, size);
            if (chosen) chooser = card;
        }
        label("远程连接", 14);
        row("已归档会话", v -> openArchivedSessions());
        row("中继设置", v -> showAdvanced(Page.RELAY));
        row("主机与指纹", v -> showAdvanced(Page.HOST));
        label("应用更新", 14);
        row("检查更新   ·   " + updates.currentVersion(), v -> updates.check(true));
        statusView();
        if (focusTheme && chooser != null) chooser.requestFocus();
    }

    private void showAdvanced(Page next) {
        boolean host = next == Page.HOST;
        settingsPage(next, host ? "主机与指纹" : "中继设置", () -> showSettings(false));
        verified = new CheckBox(this);
        customRelay = new Switch(this);
        customRelay.setText("使用自建中继");
        customRelay.setTextColor(palette.ink);
        customRelay.setTextSize(16);
        form.addView(customRelay, -1, dp(48));
        relayField = field("自建 VPS 的 HTTPS 地址", "");
        customRelay.setOnCheckedChangeListener((view, enabled) -> {
            relayField.setVisibility(enabled ? View.VISIBLE : View.GONE);
            verified.setChecked(false);
        });
        displayRelay(imported != null && host ? imported.relay : preferredRelay());
        if (!host) {
            label("关闭自建中继后，使用 Super Cage 官方中继。", 14);
            button(form, "保存中继设置", true, v -> {
                try { rememberRelay(selectedRelay()); status.setText("中继设置已保存。"); }
                catch (Exception e) { status.setText("请填写有效的 HTTPS 地址。"); }
            });
            statusView();
            return;
        }
        label("这台手机的指纹", 15);
        localPin = label(fingerprint.isEmpty() ? "正在读取设备密钥…" : fingerprint, 14);
        localPin.setTypeface(Typeface.MONOSPACE);
        localPin.setTextIsSelectable(true);
        button(form, "复制手机指纹", false, v -> {
            if (fingerprint.isEmpty()) return;
            ClipboardManager clipboard = (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
            clipboard.setPrimaryClip(ClipData.newPlainText("Super Cage 手机指纹", fingerprint));
            status.setText("手机指纹已复制，请在电脑上核对并信任。");
        });
        label("电脑指纹", 15);
        pinField = field("sha256:…", imported != null ? imported.pin : trust.getString("pin", ""));
        verified.setText("我已与电脑上独立显示的指纹核对");
        verified.setTextColor(palette.ink);
        form.addView(verified);
        TextWatcher changed = new TextWatcher() {
            public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            public void onTextChanged(CharSequence s, int start, int before, int count) { verified.setChecked(false); }
            public void afterTextChanged(Editable text) {}
        };
        pinField.addTextChangedListener(changed);
        relayField.addTextChangedListener(changed);
        button(form, "保存已核对的主机", true, v -> saveVerified());
        forgetHost = button(form, "忘记主机", false, v -> new AlertDialog.Builder(this)
            .setMessage("移除这台电脑的地址、指纹和重连凭据？")
            .setNegativeButton("取消", null).setPositiveButton("忘记主机", (dialog, which) -> forget()).show());
        forgetHost.setTextColor(palette.danger);
        forgetHost.setVisibility(trust.contains("pin") ? View.VISIBLE : View.GONE);
        inviteStatus = label(imported != null ? "请与电脑独立显示的指纹核对，再保存主机。" :
            invitation != null ? "已导入本次临时邀请。" :
            credentials.saved() ? "这台电脑已保存，可以直接重新连接。" : "导入电脑生成的连接文件或临时邀请。", 14);
        button(form, "导入连接文件", false, v -> {
            Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*");
            startActivityForResult(intent, PICK_INVITATION);
        });
        connect = button(form, credentials.saved() ? "重新连接" : "连接工作台", false, v -> connect());
        statusView();
    }

    private void saveVerified() {
        if (!verified.isChecked()) { status.setText("请先在电脑上核对指纹。"); return; }
        final String relay, pin;
        try { relay = selectedRelay(); pin = Invitation.pin(pinField.getText().toString()); }
        catch (Exception e) { status.setText("中继地址或电脑指纹格式无效。"); return; }
        Invitation.Saved record = imported;
        if (record != null && (!record.relay.equals(relay) || !record.pin.equals(pin))) { status.setText("填写内容与导入的连接文件不一致，请重新核对。"); return; }
        int attempt = ++generation;
        setConnecting(true);
        worker.execute(() -> {
            try {
                credentials.save(relay, pin, record == null ? null : record.lookup);rememberRelay(relay);
                runOnUiThread(() -> { if (generation == attempt) {
                    imported = null; verified.setChecked(false); setConnecting(false); forgetHost.setVisibility(View.VISIBLE);
                    connect.setText(credentials.saved() && invitation == null ? "重新连接" : "连接工作台");
                    if (credentials.saved() && invitation == null) inviteStatus.setText("这台电脑已保存，可以直接重新连接。");
                    status.setText(credentials.saved() ? "已保存这台电脑，可以重新连接。" : "已保存主机指纹。");
                } });
            } catch (Exception e) { runOnUiThread(() -> { if (generation == attempt) { setConnecting(false); status.setText("无法安全保存主机，请解锁手机后重试。"); } }); }
        });
    }
    private void forget() {
        int attempt = ++generation; setConnecting(true);
        worker.execute(() -> {
            try {
                credentials.forget();
                runOnUiThread(() -> { if (generation == attempt) { invitation=null; imported=null; setConnecting(false); showAdvanced(Page.HOST); status.setText("已忘记主机。"); } });
            } catch (Exception e) { runOnUiThread(() -> { if (generation == attempt) { setConnecting(false); status.setText("移除未完成，请重试。"); } }); }
        });
    }

    @Override protected void onActivityResult(int code, int result, Intent data) {
        super.onActivityResult(code, result, data);
        if (updates.onResult(code, result)) return;
        if (code == PICK_FILES) { deliverFiles(result, data); return; }
        if (code == SCAN) {
            if(result==RESULT_OK && data!=null) pairScan(data.getStringExtra("code"));
            else if(data!=null && data.hasExtra("error"))status.setText(data.getStringExtra("error"));
            return;
        }
        if (code != PICK_INVITATION || result != RESULT_OK || data == null || data.getData() == null) return;
        Uri file = data.getData();
        if (!"content".equals(file.getScheme())) { status.setText("请选择设备文件应用中的邀请。"); return; }
        int attempt = ++generation;
        invitation = null; imported = null; verified.setChecked(false); setConnecting(true);
        worker.execute(() -> {
            try (InputStream input = getContentResolver().openInputStream(file)) {
                JSONObject object = Invitation.read(input);
                Invitation.Saved saved = object.has("version") ? new Invitation.Saved(object) : null;
                Invitation item = saved == null ? Invitation.room(object) : null;
                runOnUiThread(() -> { if (attempt == generation) {
                    setConnecting(false); invitation = item; imported = saved; status.setText("");
                    if (saved != null) {
                        displayRelay(saved.relay); pinField.setText(saved.pin); verified.setChecked(false);
                        inviteStatus.setText("请与电脑独立显示的指纹核对，再保存主机。");
                    } else { inviteStatus.setText("已导入本次临时邀请。"); }
                    connect.setText("连接工作台");
                } });
            } catch (Exception e) {
                runOnUiThread(() -> { if (attempt == generation) { setConnecting(false); inviteStatus.setText("文件无效或临时邀请已过期，请重新导入。"); } });
            }
        });
    }

    private void connect() {
        connect(false);
    }
    private void connect(boolean archivedEntry) {
        if (busy) return;
        openArchivedAfterConnect = archivedEntry;
        final String relay, pin;
        try {
            relay = page == Page.HOST ? selectedRelay() : trust.getString("relay", preferredRelay());
            pin = Invitation.pin(page == Page.HOST ? pinField.getText().toString() : trust.getString("pin", ""));
        } catch (Exception e) { status.setText("请在「设置 → 主机与指纹」保存电脑指纹。"); return; }
        if ((page == Page.HOST && imported != null) || !credentials.matches(relay,pin)) { status.setText("请先核对并保存这台主机。"); return; }
        ConnectedDevice existing = connections.get(relay + "\n" + pin);
        if (existing != null) {
            enterDevice(existing);
            if (openArchivedAfterConnect) {
                openArchivedAfterConnect = false;
                browser.evaluateJavascript("window.dispatchEvent(new Event('cage-open-archives'))", null);
            }
            return;
        }
        if (device == null) { status.setText("设备密钥尚未就绪。"); return; }
        Invitation item = page == Page.HOST ? invitation : null;
        if (item == null && !credentials.saved()) { status.setText("请导入连接文件或有效的临时邀请。"); return; }
        if (item != null && !item.expiry.isAfter(Instant.now())) { invitation=null; status.setText("临时邀请已过期，请重新连接或导入新邀请。"); return; }
        invitation = null;
        if (item != null) inviteStatus.setText("本次邀请已用于连接。");
        int attempt = ++generation; setConnecting(true); status.setText("正在验证电脑并连接…");
        worker.execute(() -> {
            Client opened = null;
            try {
                String directory = extractUI();
                opened = item == null
                    ? Cagemobile.openSaved(relay, credentials.lookup(relay, pin), pin, device)
                    : Cagemobile.open(relay, item.room, item.token, pin, device);
                if (item == null) opened.reportDevice("android", deviceModel());
                String launch = opened.startUI(directory);
                Uri url = Uri.parse(launch);
                if (!"http".equals(url.getScheme()) || !"127.0.0.1".equals(url.getHost()) || url.getPort()<1 || url.getUserInfo()!=null) throw new IOException();
                Client connected = opened;
                runOnUiThread(() -> {
                    if (attempt != generation || isDestroyed()) { close(connected); return; }
                    setConnecting(false);
                    ConnectedDevice next = new ConnectedDevice(relay + "\n" + pin, connected, url,
                        "电脑 " + (connections.size() + 1));
                    connections.put(next.hostKey, next);
                    createBrowser(next);
                    enterDevice(next);
                    broadcastDevices();
                });
            } catch (Exception e) {
                close(opened);
                runOnUiThread(() -> { if (attempt == generation) {
                    setConnecting(false); if (page == Page.HOST) connect.setText(credentials.saved() ? "重新连接" : "连接工作台");
                    status.setText(item == null ? "连接未完成。请确认电脑上的连接服务正在运行，再点重新连接。若已更换凭据，请导入新的连接文件。" : "连接未完成。请核对双方指纹与网络，再导入新邀请。");
                } });
            }
        });
    }
    private void setConnecting(boolean connecting) {
        busy = connecting;
        enableActions(root, !connecting);
    }
    private static void enableActions(View view, boolean enabled) {
        if (view instanceof android.view.ViewGroup) {
            android.view.ViewGroup group = (android.view.ViewGroup)view;
            for (int i = 0; i < group.getChildCount(); i++) enableActions(group.getChildAt(i), enabled);
            // Card rows and the connect button are clickable layouts.
            if (view instanceof LinearLayout && view.isClickable()) {
                view.setEnabled(enabled);
                view.setAlpha(enabled ? 1f : 0.5f);
            }
        } else if (view.isClickable() || view instanceof EditText) view.setEnabled(enabled);
    }
    private synchronized String extractUI() throws IOException {
        File root = new File(getCacheDir(), "cage-mobile-ui");
        copyAssets(getAssets(), "mobile-ui", root);
        return root.getAbsolutePath();
    }
    private static void copyAssets(AssetManager assets, String path, File dest) throws IOException {
        String[] children = assets.list(path);
        if (children == null) throw new IOException("missing bundled assets");
        if (children.length == 0) {
            try (InputStream input = assets.open(path); OutputStream output = new FileOutputStream(dest)) {
                byte[] bytes = new byte[16384]; int n;
                while ((n=input.read(bytes))!=-1) output.write(bytes,0,n);
            }
            return;
        }
        if (!dest.isDirectory() && !dest.mkdirs()) throw new IOException("asset storage unavailable");
        for (String child: children) copyAssets(assets,path+"/"+child,new File(dest,child));
    }
    private static boolean allowed(Uri url, int port) {
        return "http".equals(url.getScheme()) && "127.0.0.1".equals(url.getHost()) && url.getPort() == port && url.getUserInfo() == null;
    }
    private JSONObject deviceSnapshot(ConnectedDevice current) {
        JSONObject state = new JSONObject();
        JSONArray devices = new JSONArray();
        try {
            for (ConnectedDevice connected : connections.values()) {
                JSONObject item = new JSONObject();
                item.put("id", connected.id); item.put("name", connected.name);
                item.put("kind", connected.account == null ? "computer" : "tenant"); devices.put(item);
            }
            state.put("current", current.id); state.put("devices", devices);
        } catch (org.json.JSONException impossible) {throw new IllegalStateException(impossible);}
        return state;
    }
    private String deviceScript(ConnectedDevice current) {
        return "window.__cageDevices=" + deviceSnapshot(current).toString() +
            ";window.dispatchEvent(new CustomEvent('cage-devices',{detail:window.__cageDevices}));";
    }
    private void broadcastDevices() {
        for (ConnectedDevice connected : connections.values()) {
            if (connected.script != null) connected.script.remove();
            if (WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT))
                connected.script = WebViewCompat.addDocumentStartJavaScript(connected.view, deviceScript(connected),
                    java.util.Collections.singleton("http://127.0.0.1:" + connected.launch.getPort()));
            if (connected.view.getUrl() != null && allowed(Uri.parse(connected.view.getUrl()), connected.launch.getPort()))
                connected.view.evaluateJavascript(deviceScript(connected), null);
        }
    }
    private void deviceCommand(ConnectedDevice source, String json) {
        // The largest command is share-file: 8 MiB of file data as base64 plus its name and type.
        if (!connections.containsValue(source) || isDestroyed() || json.length() > (12 << 20)) return;
        try {
            JSONObject command = new JSONObject(json);
            String action = command.optString("action");
            if ("name".equals(action)) {
                if (source.account != null) return; // A server VM keeps its portal name.
                String name = command.optString("name").trim();
                if (!name.isEmpty() && name.length() <= 120 && !name.equals(source.name)) {
                    source.name = name; broadcastDevices();
                }
                return;
            }
            if (!viewingWorkspace || source != activeDevice) return;
            if ("home".equals(action)) showSetup();
            if ("open-url".equals(action)) openExternal(command.optString("url"));
            if ("haptic".equals(action)) haptic(source.view, command.optString("style"));
            if ("share-file".equals(action)) shareFile(command.optString("name"), command.optString("mime"), command.optString("data"));
        } catch (org.json.JSONException ignored) {}
    }
    private static void haptic(View view, String style) {
        int effect;
        switch (style) {
            case "light": effect = HapticFeedbackConstants.CLOCK_TICK; break;
            case "medium": effect = HapticFeedbackConstants.VIRTUAL_KEY; break;
            case "success": effect = Build.VERSION.SDK_INT >= 30 ? HapticFeedbackConstants.CONFIRM : HapticFeedbackConstants.VIRTUAL_KEY; break;
            case "warning": effect = Build.VERSION.SDK_INT >= 30 ? HapticFeedbackConstants.REJECT : HapticFeedbackConstants.LONG_PRESS; break;
            default: return;
        }
        view.performHapticFeedback(effect);
    }
    private static void prune(File directory, long age) {
        File[] old = directory.listFiles();
        if (old == null) return;
        for (File item : old) {
            if (System.currentTimeMillis() - item.lastModified() < age) continue;
            if (item.isDirectory()) prune(item, 0);
            item.delete();
        }
    }
    // Writes a validated workbench export to the app cache and opens the system share sheet.
    private void shareFile(String name, String mime, String data) {
        if (sharing) return;
        sharing = true;
        serverWorker.execute(() -> {
            try {
                cagemobile.SharedFile file = Cagemobile.decodeSharedFile(name, mime, data);
                File shared = new File(getCacheDir(), "shared");
                prune(shared, 60 * 60 * 1000);
                File directory = new File(shared, java.util.UUID.randomUUID().toString());
                String fileName = file.getName();
                String extension = MimeTypeMap.getSingleton().getExtensionFromMimeType(file.getMime());
                if (fileName.lastIndexOf('.') <= 0 && extension != null) fileName += "." + extension;
                File output = new File(directory, fileName);
                if (!directory.mkdirs()) throw new IOException();
                try (OutputStream stream = new FileOutputStream(output)) {
                    byte[] bytes = file.getData();
                    if (bytes != null) stream.write(bytes);
                }
                Uri uri = FileProvider.getUriForFile(this, getPackageName() + ".updates", output);
                Intent send = new Intent(Intent.ACTION_SEND).setType(file.getMime()).putExtra(Intent.EXTRA_STREAM, uri)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
                send.setClipData(ClipData.newRawUri(output.getName(), uri));
                runOnUiThread(() -> {
                    sharing = false;
                    if (isDestroyed()) return;
                    try { startActivity(Intent.createChooser(send, null)); }
                    catch (android.content.ActivityNotFoundException e) { Toast.makeText(this, "没有可以接收这个文件的应用。", Toast.LENGTH_LONG).show(); }
                });
            } catch (Exception e) {
                String message = e.getMessage();
                runOnUiThread(() -> {
                    sharing = false;
                    if (!isDestroyed()) Toast.makeText(this, message == null || message.isEmpty() || message.length() > 200 ||
                        !message.matches("[\\p{IsHan}].*") ? "无法分享这个文件。" : message, Toast.LENGTH_LONG).show();
                });
            }
        });
    }
    // <input type="file">: the system picker with the page's accept types, plus the camera for images.
    private void chooseFiles(ValueCallback<Uri[]> callback, WebChromeClient.FileChooserParams params) {
        if (pendingFiles != null) pendingFiles.onReceiveValue(null);
        pendingFiles = callback;
        java.util.List<String> types = new java.util.ArrayList<>();
        for (String accept : params.getAcceptTypes()) {
            for (String part : accept.split(",")) {
                String type = part.trim().toLowerCase(java.util.Locale.ROOT);
                if (type.startsWith(".")) type = MimeTypeMap.getSingleton().getMimeTypeFromExtension(type.substring(1));
                if (type != null && type.matches("[a-z0-9.+-]+/[a-z0-9.+*-]+") && !types.contains(type)) types.add(type);
            }
        }
        Intent pick = new Intent(Intent.ACTION_GET_CONTENT).addCategory(Intent.CATEGORY_OPENABLE)
            .setType(types.size() == 1 ? types.get(0) : "*/*");
        if (types.size() > 1) pick.putExtra(Intent.EXTRA_MIME_TYPES, types.toArray(new String[0]));
        if (params.getMode() == WebChromeClient.FileChooserParams.MODE_OPEN_MULTIPLE) pick.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
        boolean images = types.isEmpty();
        for (String type : types) images |= type.startsWith("image/") || type.equals("*/*");
        pendingPicker = pick;
        boolean camera = images && getPackageManager().hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY);
        boolean granted = checkSelfPermission(android.Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED;
        // Only the capture attribute asks for the camera; otherwise it is offered once already allowed.
        if (camera && params.isCaptureEnabled() && !granted) {
            requestPermissions(new String[]{android.Manifest.permission.CAMERA}, CAMERA_FOR_FILES);
            return;
        }
        launchPicker(camera && granted, params.isCaptureEnabled());
    }
    private void launchPicker(boolean camera, boolean captureOnly) {
        Intent pick = pendingPicker;
        pendingPicker = null;
        if (pick == null || pendingFiles == null) return;
        pendingPhoto = null;
        Intent photo = null;
        if (camera) {
            try {
                File captures = new File(getCacheDir(), "captures");
                prune(captures, 24 * 60 * 60 * 1000);
                if (captures.isDirectory() || captures.mkdirs()) {
                    File output = new File(captures, "photo-" + System.currentTimeMillis() + ".jpg");
                    Uri uri = FileProvider.getUriForFile(this, getPackageName() + ".updates", output);
                    photo = new Intent(MediaStore.ACTION_IMAGE_CAPTURE).putExtra(MediaStore.EXTRA_OUTPUT, uri)
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
                    photo.setClipData(ClipData.newRawUri(output.getName(), uri));
                    pendingPhoto = output;
                }
            } catch (RuntimeException e) { photo = null; pendingPhoto = null; }
        }
        Intent launch = Intent.createChooser(pick, null);
        if (photo != null && captureOnly) launch = photo;
        else if (photo != null) launch.putExtra(Intent.EXTRA_INITIAL_INTENTS, new Intent[]{photo});
        try { startActivityForResult(launch, PICK_FILES); }
        catch (android.content.ActivityNotFoundException e) {
            if (launch == photo) { pendingPicker = pick; launchPicker(false, false); return; }
            deliverFiles(RESULT_CANCELED, null);
        }
    }
    private void deliverFiles(int result, Intent data) {
        ValueCallback<Uri[]> callback = pendingFiles;
        File photo = pendingPhoto;
        pendingFiles = null; pendingPhoto = null;
        if (callback == null) return;
        Uri[] files = null;
        if (result == RESULT_OK) {
            if (photo != null && photo.length() > 0) files = new Uri[]{FileProvider.getUriForFile(this, getPackageName() + ".updates", photo)};
            else if (data != null && data.getClipData() != null) {
                files = new Uri[data.getClipData().getItemCount()];
                for (int i = 0; i < files.length; i++) files[i] = data.getClipData().getItemAt(i).getUri();
            } else if (data != null && data.getData() != null) files = new Uri[]{data.getData()};
        }
        callback.onReceiveValue(files);
    }
    @Override public void onRequestPermissionsResult(int code, String[] names, int[] results) {
        super.onRequestPermissionsResult(code, names, results);
        if (code != CAMERA_FOR_FILES) return;
        boolean granted = results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED;
        launchPicker(granted, granted);
    }
    private void createBrowser(ConnectedDevice connected) {
        WebView view = new WebView(this);
        connected.view = view;
        // Same as the page background, so loading never flashes white.
        view.setBackgroundColor(palette.bg);
        WebView.setWebContentsDebuggingEnabled(false);
        WebSettings settings = view.getSettings(); settings.setJavaScriptEnabled(true); settings.setAllowFileAccess(false); settings.setAllowContentAccess(false);
        settings.setUserAgentString(settings.getUserAgentString() + " CageMobile/1");
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW); settings.setCacheMode(WebSettings.LOAD_NO_CACHE);
        CookieManager.getInstance().setAcceptThirdPartyCookies(view,false);
        final int port = connected.launch.getPort();
        java.util.Set<String> origins = java.util.Collections.singleton("http://127.0.0.1:" + port);
        if (WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) {
            WebViewCompat.addWebMessageListener(view, "CageDevices", origins, (web, message, origin, mainFrame, reply) -> {
                if (message.getType() == androidx.webkit.WebMessageCompat.TYPE_STRING && mainFrame &&
                        allowed(origin, port) && web.getUrl() != null && allowed(Uri.parse(web.getUrl()), port))
                    runOnUiThread(() -> deviceCommand(connected, message.getData()));
            });
        }
        if (WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT))
            connected.script = WebViewCompat.addDocumentStartJavaScript(view, deviceScript(connected), origins);
        view.setWebViewClient(new WebViewClient() {
            @Override public void onPageFinished(WebView w, String url) {
                if (allowed(Uri.parse(url), port)) w.evaluateJavascript(deviceScript(connected), null);
            }
            @Override public boolean shouldOverrideUrlLoading(WebView w, WebResourceRequest r) {
                String command = r.getUrl().toString();
                if (r.isForMainFrame() && r.hasGesture() && w.getUrl() != null && allowed(Uri.parse(w.getUrl()), port)) {
                    if (command.equals("cage://home")) { w.post(() -> showSetup()); return true; }
                }
                return !allowed(r.getUrl(),port);
            }
            @Override public WebResourceResponse shouldInterceptRequest(WebView w, WebResourceRequest r) {
                if (allowed(r.getUrl(),port)) return null;
                return new WebResourceResponse("text/plain","UTF-8",403,"Forbidden",java.util.Collections.emptyMap(),new ByteArrayInputStream(new byte[0]));
            }
        });
        view.setWebChromeClient(new WebChromeClient() {
            @Override public boolean onShowFileChooser(WebView w, ValueCallback<Uri[]> callback, FileChooserParams params) {
                if (w != browser || !viewingWorkspace || w.getUrl() == null || !allowed(Uri.parse(w.getUrl()), port)) return false;
                chooseFiles(callback, params);
                return true;
            }
        });
        Uri launch = connected.launch;
        if (openArchivedAfterConnect) {
            launch = launch.buildUpon().appendQueryParameter("page", "archived").build();
            openArchivedAfterConnect = false;
        }
        view.loadUrl(launch.toString());
    }
    private void openArchivedSessions() {
        if (activeDevice != null && connections.containsValue(activeDevice)) {
            enterDevice(activeDevice);
            browser.evaluateJavascript("window.dispatchEvent(new Event('cage-open-archives'))", null);
        } else {
            connect(true);
        }
    }
    private void enterDevice(ConnectedDevice selected) {
        if (!connections.containsValue(selected)) return;
        if (browser != null && browser != selected.view) browser.onPause();
        activeDevice = selected; browser = selected.view; viewingWorkspace = true;
        LinearLayout pane = column();
        pane.setBackgroundColor(palette.bg);
        browser.setBackgroundColor(palette.bg);
        if (browser.getParent() instanceof android.view.ViewGroup)
            ((android.view.ViewGroup)browser.getParent()).removeView(browser);
        pane.addView(browser, new LinearLayout.LayoutParams(-1,0,1));
        setContentView(pane); insets(pane); browser.onResume(); broadcastDevices();
    }
    private void confirmDisconnect(ConnectedDevice selected) {
        Dialog dialog = new Dialog(this);
        LinearLayout panel = column(); panel.setPadding(dp(22),dp(18),dp(22),dp(22));
        panel.setBackground(surface(palette.panel,20,false));
        TextView title = title("取消与“" + selected.name + "”的连接？",22); panel.addView(title);
        TextView explanation = text("设备将从当前连接列表移除，已保存的配对信息仍然保留。",14,palette.muted);
        explanation.setPadding(0,dp(12),0,dp(12)); panel.addView(explanation);
        button(panel,"取消连接",false,v -> {dialog.dismiss(); disconnectDevice(selected);}).setTextColor(palette.danger);
        button(panel,"继续连接",false,v -> dialog.dismiss());
        dialog.setContentView(panel);
        android.view.Window window = dialog.getWindow();
        if (window != null) {
            window.setBackgroundDrawableResource(android.R.color.transparent);
            window.setGravity(Gravity.BOTTOM);
            window.setLayout(-1,-2);
        }
        dialog.show();
        if (window != null) window.setLayout(-1,-2);
    }
    private static void close(Client c) { if (c!=null) try { c.close(); } catch (Exception ignored) { } }
    private void disconnectDevice(ConnectedDevice connected) {
        if (connections.remove(connected.hostKey) == null) return;
        if (connected.script != null) connected.script.remove();
        connected.view.stopLoading();
        if (connected.view.getParent() instanceof android.view.ViewGroup)
            ((android.view.ViewGroup)connected.view.getParent()).removeView(connected.view);
        connected.view.destroy();
        worker.execute(() -> close(connected.client));
        if (activeDevice == connected) {
            browser = null; activeDevice = null;
            if (viewingWorkspace) showSetup();
        }
        if (page == Page.PAIR) showPair(false);
        broadcastDevices();
    }
    private void endSession() {
        ++generation; invitation = null;
        for (ConnectedDevice connected : new java.util.ArrayList<>(connections.values())) {
            if (connected.script != null) connected.script.remove();
            connected.view.stopLoading(); connected.view.destroy();
            worker.execute(() -> close(connected.client));
        }
        connections.clear(); browser = null; activeDevice = null;
    }
    @Override protected void onPause() {
        updates.pause();
        if (viewingWorkspace && browser != null) browser.onPause();
        super.onPause();
    }
    @Override protected void onResume() {
        super.onResume();
        updates.resume();
        if (viewingWorkspace && browser != null) browser.onResume();
    }
    @Override public void onBackPressed() {
        if (busy && page == Page.MACHINES) { ++generation; setConnecting(false); status.setText("已取消。"); return; }
        if (busy) return;
        if (viewingWorkspace && browser != null && browser.canGoBack()) browser.goBack();
        else if (viewingWorkspace) showSetup();
        else if (page == Page.CODE) showPair(false);
        else if (page == Page.RELAY || page == Page.HOST) showSettings(false);
        else if (page == Page.MACHINES) showServers();
        else if (page != Page.HOME) showSetup();
        else super.onBackPressed();
    }
    @Override public void onDestroy() {
        updates.close(); endSession(); worker.shutdown();
        for (Tenant tenant : tenants.values()) serverWorker.execute(tenant::close);
        tenants.clear(); serverWorker.shutdown();
        super.onDestroy();
    }

    // Super Cage servers: sign in to a multi-tenant portal account and open its VMs in the phone workbench.
    private void showServers() {
        settingsPage(Page.SERVERS, "连接服务器", this::showSetup);
        java.util.List<ServerAccounts.Account> saved = servers.list();
        if (saved.isEmpty()) emptyState(form, "server", "还没有服务器。添加一个，随时回到你的工作区。");
        else {
            label("登录 Super Cage 服务器的门户账户，打开你的虚拟机。", 14);
            label("已保存的服务器（长按可忘记）", 14);
            for (ServerAccounts.Account account : saved) {
                LinearLayout entry = listRow(form, account.user, account.host(), null, 0, v -> signInSaved(account));
                entry.setOnLongClickListener(v -> { confirmForgetServer(account); return true; });
            }
        }
        label(saved.isEmpty() ? "服务器账户" : "登录其他账户", 14);
        serverPortalField = field("服务器地址，例如 https://cage.example.org", "");
        serverPortalField.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        serverUserField = field("账户", "");
        serverPasswordField = field("密码", "");
        serverPasswordField.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        CheckBox remember = new CheckBox(this);
        remember.setText("在这台手机上记住密码");
        remember.setTextColor(palette.ink);
        remember.setChecked(true);
        form.addView(remember);
        button(form, "登录", true, v -> {
            String portal = serverPortalField.getText().toString(), user = serverUserField.getText().toString();
            String password = serverPasswordField.getText().toString();
            if (portal.trim().isEmpty() || user.trim().isEmpty() || password.isEmpty()) { status.setText("请填写服务器地址、账户和密码。"); return; }
            serverPasswordField.setText("");
            android.view.inputmethod.InputMethodManager ime = (android.view.inputmethod.InputMethodManager)getSystemService(INPUT_METHOD_SERVICE);
            ime.hideSoftInputFromWindow(serverPasswordField.getWindowToken(), 0);
            signInServer(portal, user, password, remember.isChecked());
        });
        statusView();
    }
    private void failServer(int attempt, Exception e, String fallback) {
        String message = e.getMessage();
        runOnUiThread(() -> { if (attempt == generation && !isDestroyed()) {
            setConnecting(false);
            status.setText(message == null || message.isEmpty() || message.length() > 200 ? fallback : message);
        } });
    }
    private void signInServer(String portal, String user, String password, boolean remember) {
        if (busy) return;
        int attempt = ++generation; setConnecting(true); status.setText("正在登录服务器…");
        serverWorker.execute(() -> {
            try {
                Tenant tenant = Cagemobile.tenantLogin(portal, user, password);
                ServerAccounts.Account account = new ServerAccounts.Account(tenant.portal(), tenant.user());
                boolean stored;
                try { servers.save(account, remember ? password : null); stored = true; } catch (Exception e) { stored = false; }
                boolean saved = stored;
                runOnUiThread(() -> {
                    if (attempt != generation || isDestroyed()) { serverWorker.execute(tenant::close); return; }
                    setConnecting(false);
                    Tenant old = tenants.put(account.id(), tenant);
                    if (old != null && old != tenant) serverWorker.execute(old::close);
                    showMachines(account);
                    if (!saved) status.setText("已登录，但无法在这台手机上保存服务器。");
                });
            } catch (Exception e) { failServer(attempt, e, "登录未完成，请检查网络和账户。"); }
        });
    }
    private void signInSaved(ServerAccounts.Account account) {
        if (busy) return;
        if (tenants.containsKey(account.id())) { showMachines(account); return; }
        String password = servers.password(account);
        if (password == null) {
            serverPortalField.setText(account.portal); serverUserField.setText(account.user);
            serverPasswordField.requestFocus(); status.setText("请输入 " + account.user + " 的密码。");
            return;
        }
        signInServer(account.portal, account.user, password, true);
    }
    private static String serverKey(ServerAccounts.Account account, String vm) { return "server\n" + account.id() + "\n" + vm; }
    private static String stateLabel(String state) {
        switch (state) {
            case "ready": return "运行中";
            case "starting": return "正在启动";
            case "error": return "启动失败";
            default: return "已停止";
        }
    }
    private int stateTone(String state) {
        switch (state) {
            case "ready": return palette.ok;
            case "starting": return palette.clayInk;
            case "error": return palette.danger;
            default: return palette.muted;
        }
    }
    private void showMachines(ServerAccounts.Account account) {
        settingsPage(Page.MACHINES, "虚拟机", this::showServers);
        label(account.user + " @ " + account.host(), 14);
        machineList = column();
        form.addView(machineList);
        button(form, "新建虚拟机", true, v -> askMachineName(account));
        button(form, "刷新", false, v -> refreshMachines(account));
        button(form, "退出登录", false, v -> new AlertDialog.Builder(this).setTitle("退出登录？")
            .setMessage("将关闭这个账户已打开的虚拟机工作台。虚拟机会继续在服务器上运行。")
            .setNegativeButton("取消", null).setPositiveButton("退出登录", (dialog, which) -> { signOutServer(account); showServers(); }).show())
            .setTextColor(palette.danger);
        label("打开的虚拟机显示 Claude Code 项目、会话和工具审批。远程浏览器与 Claude Desktop 需要扫码连接电脑。", 13);
        statusView();
        refreshMachines(account);
    }
    private void refreshMachines(ServerAccounts.Account account) {
        Tenant tenant = tenants.get(account.id());
        if (tenant == null || busy) return;
        int attempt = ++generation; setConnecting(true); status.setText("正在读取虚拟机…");
        serverWorker.execute(() -> {
            try {
                JSONArray vms = new JSONArray(tenant.virtualMachines());
                runOnUiThread(() -> {
                    if (attempt != generation || page != Page.MACHINES || isDestroyed()) return;
                    setConnecting(false);
                    renderMachines(account, vms);
                    status.setText("");
                });
            } catch (Exception e) { failServer(attempt, e, "无法读取虚拟机列表。"); }
        });
    }
    private void renderMachines(ServerAccounts.Account account, JSONArray vms) {
        machineList.removeAllViews();
        if (vms.length() == 0) emptyState(machineList, "terminal", "还没有虚拟机。新建一台，开始你的工作区。");
        for (int i = 0; i < vms.length(); i++) {
            JSONObject vm = vms.optJSONObject(i);
            if (vm == null) continue;
            String id = vm.optString("id"), name = vm.optString("name"), state = vm.optString("state");
            boolean open = connections.containsKey(serverKey(account, id));
            listRow(machineList, name, vm.optString("system_label"), open ? "已打开" : stateLabel(state),
                open ? palette.clayInk : stateTone(state), v -> openMachine(account, id, name, state));
        }
    }
    private void askMachineName(ServerAccounts.Account account) {
        EditText input = new EditText(this);
        input.setSingleLine();
        input.setHint("名称");
        FrameLayout box = new FrameLayout(this);
        box.setPadding(dp(22), dp(4), dp(22), 0);
        box.addView(input);
        new AlertDialog.Builder(this).setTitle("新建虚拟机").setMessage("服务器为新虚拟机准备空白环境，首次打开需要几分钟。")
            .setView(box).setNegativeButton("取消", null)
            .setPositiveButton("新建并打开", (dialog, which) -> createMachine(account, input.getText().toString().trim())).show();
    }
    private void createMachine(ServerAccounts.Account account, String name) {
        Tenant tenant = tenants.get(account.id());
        if (tenant == null || busy || name.isEmpty()) return;
        int attempt = ++generation; setConnecting(true); status.setText("正在新建虚拟机…");
        serverWorker.execute(() -> {
            try {
                JSONObject vm = new JSONObject(tenant.createVirtualMachine(name));
                runOnUiThread(() -> {
                    if (attempt != generation || isDestroyed()) return;
                    setConnecting(false);
                    openMachine(account, vm.optString("id"), vm.optString("name", name), vm.optString("state"));
                });
            } catch (Exception e) { failServer(attempt, e, "无法新建虚拟机。"); }
        });
    }
    private void openMachine(ServerAccounts.Account account, String id, String name, String state) {
        Tenant tenant = tenants.get(account.id());
        if (tenant == null || busy) return;
        ConnectedDevice existing = connections.get(serverKey(account, id));
        if (existing != null) { enterDevice(existing); return; }
        int attempt = ++generation; setConnecting(true);
        status.setText("ready".equals(state) ? "正在打开虚拟机…" : "正在启动虚拟机，首次启动可能需要几分钟。按返回键可取消。");
        serverWorker.execute(() -> {
            Client opened = null;
            try {
                String directory = extractUI();
                opened = tenant.openVirtualMachine(id);
                Uri url = Uri.parse(opened.startUI(directory));
                if (!"http".equals(url.getScheme()) || !"127.0.0.1".equals(url.getHost()) || url.getPort()<1 || url.getUserInfo()!=null) throw new IOException();
                Client connected = opened;
                runOnUiThread(() -> {
                    if (attempt != generation || isDestroyed()) { close(connected); return; }
                    setConnecting(false);
                    ConnectedDevice next = new ConnectedDevice(serverKey(account, id), connected, url, name);
                    next.account = account.id();
                    connections.put(next.hostKey, next);
                    createBrowser(next);
                    enterDevice(next);
                    broadcastDevices();
                });
            } catch (Exception e) { close(opened); failServer(attempt, e, "无法打开虚拟机，请稍后重试。"); }
        });
    }
    // Signing out revokes the server's VM sessions, so their workbenches close too.
    private void signOutServer(ServerAccounts.Account account) {
        ++generation;
        for (ConnectedDevice connected : new java.util.ArrayList<>(connections.values()))
            if (account.id().equals(connected.account)) disconnectDevice(connected);
        Tenant tenant = tenants.remove(account.id());
        if (tenant != null) serverWorker.execute(tenant::close);
    }
    private void confirmForgetServer(ServerAccounts.Account account) {
        new AlertDialog.Builder(this).setTitle("忘记这台服务器？")
            .setMessage("将从这台手机移除 " + account.user + " @ " + account.host() + " 和保存的密码。")
            .setNegativeButton("取消", null).setPositiveButton("忘记", (dialog, which) -> {
                signOutServer(account);
                try { servers.forget(account); } catch (Exception e) { status.setText("移除未完成，请重试。"); return; }
                showServers();
                status.setText("已忘记这台服务器。");
            }).show();
    }
    private void openExternal(String raw) {
        Uri url = Uri.parse(raw);
        if (raw.length() > 16384 || url.getHost() == null || url.getUserInfo() != null ||
            !("https".equals(url.getScheme()) || "http".equals(url.getScheme()))) return;
        try { startActivity(new Intent(Intent.ACTION_VIEW, url).addCategory(Intent.CATEGORY_BROWSABLE)); }
        catch (android.content.ActivityNotFoundException ignored) {}
    }
}
