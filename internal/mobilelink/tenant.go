package mobilelink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The phone signs in to a multi-tenant gateway portal (cmd/cage-tenant) with
// the account password, lists that account's virtual machines and opens one.
// Only the phone operation allowlist is then forwarded to the VM origin, with
// the VM's own host-only cookie. Neither the portal nor the VM cookie, the
// password nor the entry ticket ever reaches the WebView.

var (
	tenantDNSLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	tenantUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	tenantVMID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	tenantSecret   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const (
	tenantPortalCookie = "__Host-cage-tenant"
	tenantVMCookie     = "__Host-cage-workspace"
)

// TenantError carries a message that is safe to show on the phone. Upstream
// text, URLs, tickets and transport details are never included.
type TenantError struct {
	Code    string
	Message string
}

func (e *TenantError) Error() string { return e.Message }

func tenantFailure(code, message string) error { return &TenantError{code, message} }

func tenantCode(err error) string {
	var failure *TenantError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

// CanonicalTenantPortal accepts only an HTTPS origin with a DNS name and an
// optional port, the shape the gateway uses for its portal and VM subdomains.
func CanonicalTenantPortal(raw string) (string, error) {
	u, err := tenantPortal(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func tenantPortal(raw string) (*url.URL, error) {
	invalid := tenantFailure("tenant_portal_invalid", "请输入服务器门户的 HTTPS 地址，例如 https://cage.example.org")
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, invalid
	}
	name := strings.ToLower(u.Hostname())
	if len(name) > 217 || !strings.Contains(name, ".") || net.ParseIP(name) != nil || strings.HasSuffix(name, ".localhost") {
		return nil, invalid
	}
	for _, label := range strings.Split(name, ".") {
		if !tenantDNSLabel.MatchString(label) {
			return nil, invalid
		}
	}
	host := name
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || port != strconv.Itoa(number) {
			return nil, invalid
		}
		if number != 443 {
			host = net.JoinHostPort(name, port)
		}
	} else if !strings.EqualFold(u.Host, name) {
		return nil, invalid
	}
	return &url.URL{Scheme: "https", Host: host}, nil
}

// CanonicalTenantUser lowercases a portal user name and checks the gateway's rules.
func CanonicalTenantUser(user string) string {
	user = strings.ToLower(strings.TrimSpace(user))
	if tenantUsername.MatchString(user) {
		return user
	}
	return ""
}

type TenantVM struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	SystemLabel string `json:"system_label,omitempty"`
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
}

func newTenantTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 35 * time.Second,
		IdleConnTimeout: time.Minute, MaxIdleConns: 16, MaxIdleConnsPerHost: 8,
	}
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// TenantAccount keeps one portal sign-in for the phone. It signs in again with
// the remembered password when the gateway expires the portal session.
type TenantAccount struct {
	portal    *url.URL
	user      string
	transport *http.Transport

	mu       sync.Mutex
	password []byte
	client   *http.Client
	userID   string
	closed   bool
}

func LoginTenant(ctx context.Context, portal, username string, password []byte) (*TenantAccount, error) {
	return loginTenant(ctx, portal, username, password, newTenantTransport())
}

func loginTenant(ctx context.Context, portal, username string, password []byte, transport *http.Transport) (*TenantAccount, error) {
	origin, err := tenantPortal(portal)
	if err != nil {
		return nil, err
	}
	user := CanonicalTenantUser(username)
	if user == "" || len(password) > 128 || utf8.RuneCount(password) < 12 || !utf8.Valid(password) {
		return nil, tenantFailure("tenant_credentials", "请填写门户账户和密码（密码至少 12 个字符）")
	}
	a := &TenantAccount{portal: origin, user: user, transport: transport, password: bytes.Clone(password)}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.signInLocked(ctx); err != nil {
		clear(a.password)
		transport.CloseIdleConnections()
		return nil, err
	}
	return a, nil
}

func (a *TenantAccount) Portal() string { return a.portal.String() }
func (a *TenantAccount) User() string   { return a.user }

func (a *TenantAccount) signInLocked(ctx context.Context) error {
	if a.closed {
		return tenantFailure("tenant_closed", "已退出这台服务器")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: a.transport, Jar: jar, CheckRedirect: noRedirect}
	var result struct{ User struct{ ID, Username string } }
	_, err := tenantCall(ctx, client, a.portal.String(), "", http.MethodPost, "/api/tenant/login",
		map[string]string{"username": a.user, "password": string(a.password)}, &result, http.StatusOK)
	if err != nil {
		return err
	}
	if !tenantVMID.MatchString(result.User.ID) || result.User.Username != a.user || jarCookie(jar, a.portal, tenantPortalCookie) == nil {
		return tenantFailure("tenant_response_invalid", "门户未返回有效的登录会话")
	}
	a.client, a.userID = client, result.User.ID
	return nil
}

func jarCookie(jar http.CookieJar, origin *url.URL, name string) *http.Cookie {
	for _, cookie := range jar.Cookies(origin) {
		if cookie.Name == name && tenantSecret.MatchString(cookie.Value) {
			return cookie
		}
	}
	return nil
}

func tenantAPIError(status int, code string) error {
	switch {
	case code == "invalid_credentials" && status == http.StatusUnauthorized:
		return tenantFailure("tenant_credentials", "门户账户或密码不正确")
	case status == http.StatusUnauthorized:
		return tenantFailure("tenant_session_expired", "门户登录已失效，请重新登录")
	case code == "session_changed" && status == http.StatusConflict:
		return tenantFailure("tenant_session_expired", "门户登录已切换，请重新登录")
	case code == "vm_not_found" && status == http.StatusNotFound:
		return tenantFailure("tenant_vm_missing", "虚拟机不存在，或不属于这个门户账户")
	case code == "vm_limit" && status == http.StatusConflict:
		return tenantFailure("tenant_vm_limit", "已达到虚拟机数量上限")
	case status == http.StatusTooManyRequests:
		return tenantFailure("tenant_rate_limited", "门户请求过于频繁，请稍后重试")
	case status == http.StatusBadRequest:
		return tenantFailure("tenant_invalid_input", "门户拒绝了请求，请检查输入")
	case status == http.StatusServiceUnavailable:
		return tenantFailure("tenant_unavailable", "门户或虚拟机暂不可用，请稍后重试")
	default:
		return tenantFailure("tenant_request_failed", "门户请求未完成，请检查地址或联系管理员")
	}
}

// tenantCall is only used for the fixed portal API and the validated VM entry.
func tenantCall(ctx context.Context, client *http.Client, origin, expectedUser, method, path string, input, result any, statuses ...int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, tenantFailure("tenant_invalid_input", "无法创建门户请求")
		}
		defer clear(data)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return 0, tenantFailure("tenant_invalid_input", "无法创建门户请求")
	}
	req.GetBody = nil // Never replay credentials, tickets or writes.
	req.Header.Set("Origin", origin)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if expectedUser != "" {
		req.Header.Set("X-Cage-Expected-User", expectedUser)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, tenantFailure("tenant_network", "无法连接服务器门户，请检查网络、地址与 TLS 证书")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return resp.StatusCode, tenantFailure("tenant_response_invalid", "门户响应无效")
	}
	defer clear(data)
	for _, code := range statuses {
		if resp.StatusCode == code {
			if result != nil && json.Unmarshal(data, result) != nil {
				return code, tenantFailure("tenant_response_invalid", "门户响应无效")
			}
			return code, nil
		}
	}
	var problem struct{ Error struct{ Code string } }
	_ = json.Unmarshal(data, &problem)
	return resp.StatusCode, tenantAPIError(resp.StatusCode, problem.Error.Code)
}

// portal calls the portal API once and signs in again after an expired
// session. A 401 means the gateway did not act, so one retry is safe.
func (a *TenantAccount) portalCall(ctx context.Context, method, path string, input, result any, statuses ...int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, tenantFailure("tenant_closed", "已退出这台服务器")
	}
	status, err := tenantCall(ctx, a.client, a.portal.String(), a.userID, method, path, input, result, statuses...)
	if tenantCode(err) != "tenant_session_expired" {
		return status, err
	}
	if err := a.signInLocked(ctx); err != nil {
		return 0, err
	}
	return tenantCall(ctx, a.client, a.portal.String(), a.userID, method, path, input, result, statuses...)
}

func (a *TenantAccount) VMs(ctx context.Context) ([]TenantVM, error) {
	result := struct {
		VMs []TenantVM `json:"vms"`
	}{VMs: []TenantVM{}}
	if _, err := a.portalCall(ctx, http.MethodGet, "/api/tenant/vms", nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	vms := result.VMs[:0]
	for _, vm := range result.VMs {
		if tenantVMID.MatchString(vm.ID) {
			vms = append(vms, vm)
		}
	}
	return vms, nil
}

func validVMName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 64 && strings.IndexFunc(name, unicode.IsControl) < 0
}

// CreateVM only records the VM. The gateway prepares it the first time it is opened.
func (a *TenantAccount) CreateVM(ctx context.Context, name string) (TenantVM, error) {
	if name = strings.TrimSpace(name); !validVMName(name) {
		return TenantVM{}, tenantFailure("tenant_invalid_input", "虚拟机名称为 1–64 个字符")
	}
	var result struct {
		VM TenantVM `json:"vm"`
	}
	if _, err := a.portalCall(ctx, http.MethodPost, "/api/tenant/vms", map[string]string{"name": name}, &result, http.StatusCreated); err != nil {
		return TenantVM{}, err
	}
	if !tenantVMID.MatchString(result.VM.ID) {
		return TenantVM{}, tenantFailure("tenant_response_invalid", "门户未返回有效的虚拟机")
	}
	return result.VM, nil
}

// errTenantStarting reports a VM whose idempotent start task is still running.
var errTenantStarting = tenantFailure("tenant_starting", "虚拟机正在启动，请稍候…")

// enter asks the portal for an entry ticket once and exchanges it for the VM
// cookie. While the VM is starting it returns errTenantStarting.
func (a *TenantAccount) enter(ctx context.Context, id string) (string, *http.Cookie, error) {
	if !tenantVMID.MatchString(id) {
		return "", nil, tenantFailure("tenant_vm_missing", "虚拟机编号无效")
	}
	var result struct {
		VM  TenantVM `json:"vm"`
		URL string   `json:"workspace_url"`
	}
	status, err := a.portalCall(ctx, http.MethodPost, "/api/tenant/vms/"+id+"/open", struct{}{}, &result, http.StatusOK, http.StatusAccepted)
	if err != nil {
		return "", nil, err
	}
	if status == http.StatusAccepted {
		if result.VM.State == "error" {
			return "", nil, tenantFailure("tenant_start_failed", "虚拟机未能启动，请重试或联系管理员")
		}
		return "", nil, errTenantStarting
	}
	origin := "https://vm-" + id + "." + a.portal.Host
	ticket, ok := strings.CutPrefix(result.URL, origin+"/_tenant/enter#")
	if !ok || !tenantSecret.MatchString(ticket) {
		return "", nil, tenantFailure("tenant_workspace_invalid", "门户返回的虚拟机入口无效")
	}
	// A separate jar: nothing the portal or another VM sets authenticates this one.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: a.transport, Jar: jar, CheckRedirect: noRedirect}
	if _, err := tenantCall(ctx, client, origin, "", http.MethodPost, "/_tenant/enter", map[string]string{"ticket": ticket}, nil, http.StatusNoContent); err != nil {
		return "", nil, err
	}
	vmURL, _ := url.Parse(origin)
	cookie := jarCookie(jar, vmURL, tenantVMCookie)
	if cookie == nil {
		return "", nil, tenantFailure("tenant_response_invalid", "虚拟机未返回有效的会话")
	}
	return origin, &http.Cookie{Name: cookie.Name, Value: cookie.Value}, nil
}

// OpenWorkspace waits for a starting VM and enters it. Each attempt joins the
// gateway's idempotent start task instead of starting another one.
func (a *TenantAccount) OpenWorkspace(ctx context.Context, id string, poll time.Duration) (*TenantWorkspace, error) {
	for {
		origin, cookie, err := a.enter(ctx, id)
		if err == nil {
			return &TenantWorkspace{account: a, id: id, origin: origin, cookie: cookie}, nil
		}
		if err != errTenantStarting {
			return nil, err
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, tenantFailure("tenant_start_timeout", "等待虚拟机启动超时，请稍后重新打开")
		case <-timer.C:
		}
	}
}

// Close signs out of the portal, which also revokes every VM cookie from this sign-in.
func (a *TenantAccount) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	clear(a.password)
	if a.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = tenantCall(ctx, a.client, a.portal.String(), a.userID, http.MethodPost, "/api/tenant/logout", struct{}{}, nil, http.StatusNoContent)
		cancel()
	}
	a.transport.CloseIdleConnections()
}

// TenantWorkspace forwards the phone allowlist to one VM origin.
type TenantWorkspace struct {
	account *TenantAccount
	id      string
	origin  string

	mu     sync.Mutex
	cookie *http.Cookie
}

func (w *TenantWorkspace) Origin() string { return w.origin }

type workspaceTransport struct{ w *TenantWorkspace }

func (t workspaceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.w.mu.Lock()
	cookie := t.w.cookie
	t.w.mu.Unlock()
	req.AddCookie(cookie)
	return t.w.account.transport.RoundTrip(req)
}

func textReply(status int, message string) *Reply {
	return &Reply{Status: status, ContentType: "text/plain; charset=utf-8", Body: []byte(message)}
}

// Do sends one allowlisted operation. A read refused because the sign-in
// expired or the VM was stopped while idle enters the VM again; a mutation is
// never resent, so the phone reads the result before trying again.
func (w *TenantWorkspace) Do(ctx context.Context, method, path string, body []byte) (*Reply, error) {
	reply, err := request(ctx, workspaceTransport{w}, w.origin, method, path, body, true)
	if err != nil || method != http.MethodGet || (reply.Status != http.StatusUnauthorized && reply.Status != http.StatusServiceUnavailable) {
		return reply, err
	}
	origin, cookie, err := w.account.enter(ctx, w.id)
	switch {
	case err == errTenantStarting:
		return textReply(http.StatusServiceUnavailable, "虚拟机已在空闲时停机，正在重新启动，请稍候再读取。"), nil
	case err != nil:
		return textReply(http.StatusServiceUnavailable, err.Error()), nil
	case origin != w.origin:
		return textReply(http.StatusBadGateway, "门户返回的虚拟机入口无效"), nil
	}
	w.mu.Lock()
	w.cookie = cookie
	w.mu.Unlock()
	return request(ctx, workspaceTransport{w}, w.origin, method, path, body, true)
}
