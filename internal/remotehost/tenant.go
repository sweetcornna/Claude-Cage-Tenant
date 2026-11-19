package remotehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	"unicode/utf8"
)

var (
	tenantDNSLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	tenantUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	tenantVMID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	tenantSystemID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

func canonicalTenantUser(user string) string {
	user = strings.ToLower(strings.TrimSpace(user))
	if tenantUsername.MatchString(user) {
		return user
	}
	return ""
}

func parseTenantPortal(raw string) (*url.URL, error) {
	invalid := failure("tenant_portal_invalid", "请输入多租户服务器的 HTTPS 门户地址（仅域名和可选端口）")
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || raw != "https://"+u.Host+u.Path || (u.Path != "" && u.Path != "/") {
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

func tenantWorkspaceURL(portal *url.URL, id, raw string) (string, string, error) {
	origin := "https://vm-" + id + "." + portal.Host
	ticket, ok := strings.CutPrefix(raw, origin+"/_tenant/enter#")
	if !tenantVMID.MatchString(id) || !ok || !instancePattern.MatchString(ticket) {
		return "", "", failure("tenant_workspace_invalid", "门户返回的虚拟机入口无效")
	}
	return origin, ticket, nil
}

type TenantSystem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type TenantVM struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	SystemLabel string `json:"system_label"`
	State       string `json:"state"`
}

type TenantCatalog struct {
	Portal  string         `json:"portal"`
	User    string         `json:"user"`
	Systems []TenantSystem `json:"systems"`
	VMs     []TenantVM     `json:"vms"`
}

type tenantLogin struct {
	portal *url.URL
	client *http.Client
	userID string
}

func tenantHTTPTransport() *http.Transport {
	// Keep HTTP/1.1 for WebSocket upgrades. TLS uses the system roots.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Transport{
		Protocols: protocols, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout: time.Minute, MaxIdleConns: 16, MaxIdleConnsPerHost: 8,
	}
}

func tenantRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func tenantAPIError(status int, code string) error {
	switch {
	case code == "invalid_credentials" && status == http.StatusUnauthorized:
		return failure("tenant_credentials", "门户账户或密码不正确")
	case status == http.StatusUnauthorized:
		return failure("tenant_session_expired", "门户会话已失效，请重新登录")
	case code == "vm_not_found" && status == http.StatusNotFound:
		return failure("tenant_vm_missing", "虚拟机不存在，或不属于这个门户账户")
	case code == "vm_limit" && status == http.StatusConflict:
		return failure("tenant_vm_limit", "已达到虚拟机配额")
	case status == http.StatusTooManyRequests:
		return failure("tenant_rate_limited", "门户请求过于频繁，请稍后重试")
	case status == http.StatusBadRequest:
		return failure("tenant_invalid_input", "门户拒绝了请求，请检查输入")
	case status == http.StatusServiceUnavailable:
		return failure("tenant_unavailable", "门户或虚拟机暂不可用，请稍后重试")
	default:
		return failure("tenant_request_failed", "门户请求未完成，请检查门户地址或联系管理员")
	}
}

// request is only used for the fixed portal API and the validated VM enter
// endpoint. Do not return upstream text, URLs, tokens, or transport errors.
func (l *tenantLogin) request(ctx context.Context, origin, method, path string, input, result any, statuses ...int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, failure("tenant_invalid_input", "无法创建门户请求")
		}
		defer clear(data)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return 0, failure("tenant_invalid_input", "无法创建门户请求")
	}
	req.GetBody = nil // Do not replay POSTs with credentials, tickets, or writes.
	req.Header.Set("Origin", origin)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin == l.portal.String() && l.userID != "" {
		req.Header.Set("X-Cage-Expected-User", l.userID)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, failure("tenant_network", "无法访问 HTTPS 门户或虚拟机，请检查网络与 TLS 证书")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return resp.StatusCode, failure("tenant_response_invalid", "门户响应无效")
	}
	defer clear(data)
	for _, code := range statuses {
		if resp.StatusCode == code {
			if result != nil && json.Unmarshal(data, result) != nil {
				return code, failure("tenant_response_invalid", "门户响应无效")
			}
			return code, nil
		}
	}
	var problem struct{ Error struct{ Code string } }
	_ = json.Unmarshal(data, &problem)
	return resp.StatusCode, tenantAPIError(resp.StatusCode, problem.Error.Code)
}

func loginTenant(ctx context.Context, portal *url.URL, username string, password []byte, transport *http.Transport) (*tenantLogin, error) {
	if canonicalTenantUser(username) == "" || len(password) > 128 || utf8.RuneCount(password) < 12 || !utf8.Valid(password) {
		return nil, failure("tenant_credentials", "请填写门户账户和密码（至少 12 个字符）")
	}
	jar, _ := cookiejar.New(nil)
	l := &tenantLogin{portal: portal, client: &http.Client{Transport: transport, Jar: jar, CheckRedirect: tenantRedirect}}
	var result struct{ User struct{ ID, Username string } }
	_, err := l.request(ctx, portal.String(), http.MethodPost, "/api/tenant/login",
		map[string]string{"username": username, "password": string(password)}, &result, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if !tenantVMID.MatchString(result.User.ID) || result.User.Username != username || tenantCookie(jar, portal, "__Host-cage-tenant") == nil {
		return nil, failure("tenant_response_invalid", "门户未返回有效的会话")
	}
	l.userID = result.User.ID
	return l, nil
}

func tenantCookie(jar http.CookieJar, origin *url.URL, name string) *http.Cookie {
	for _, cookie := range jar.Cookies(origin) {
		if cookie.Name == name && instancePattern.MatchString(cookie.Value) {
			return cookie
		}
	}
	return nil
}

func (l *tenantLogin) logout() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = l.request(ctx, l.portal.String(), http.MethodPost, "/api/tenant/logout", struct{}{}, nil, http.StatusNoContent)
}

func (l *tenantLogin) vms(ctx context.Context) ([]TenantVM, error) {
	result := struct {
		VMs []TenantVM `json:"vms"`
	}{VMs: []TenantVM{}}
	_, err := l.request(ctx, l.portal.String(), http.MethodGet, "/api/tenant/vms", nil, &result, http.StatusOK)
	return result.VMs, err
}

// BrowseTenant does not leave a logged-in portal session behind. Each form
// action gets its own jar, independent of other accounts and active workbenches.
func BrowseTenant(ctx context.Context, raw, username string, password []byte) (TenantCatalog, error) {
	portal, err := parseTenantPortal(raw)
	if err != nil {
		return TenantCatalog{}, err
	}
	transport := tenantHTTPTransport()
	defer transport.CloseIdleConnections()
	username = canonicalTenantUser(username)
	l, err := loginTenant(ctx, portal, username, password, transport)
	if err != nil {
		return TenantCatalog{}, err
	}
	defer l.logout()
	var systems struct {
		Systems []TenantSystem `json:"systems"`
	}
	if _, err := l.request(ctx, portal.String(), http.MethodGet, "/api/tenant/systems", nil, &systems, http.StatusOK); err != nil {
		return TenantCatalog{}, err
	}
	vms, err := l.vms(ctx)
	return TenantCatalog{Portal: portal.String(), User: username, Systems: systems.Systems, VMs: vms}, err
}

func CreateTenantVM(ctx context.Context, raw, username string, password []byte, name, system string) (TenantVM, error) {
	portal, err := parseTenantPortal(raw)
	if err != nil {
		return TenantVM{}, err
	}
	if name = strings.TrimSpace(name); name == "" || utf8.RuneCountInString(name) > 64 || hasControl(name) || !tenantSystemID.MatchString(system) {
		return TenantVM{}, failure("tenant_invalid_input", "请填写有效的虚拟机名称及系统")
	}
	transport := tenantHTTPTransport()
	defer transport.CloseIdleConnections()
	l, err := loginTenant(ctx, portal, canonicalTenantUser(username), password, transport)
	if err != nil {
		return TenantVM{}, err
	}
	defer l.logout()
	var result struct {
		VM TenantVM `json:"vm"`
	}
	_, err = l.request(ctx, portal.String(), http.MethodPost, "/api/tenant/vms",
		map[string]string{"name": name, "system_id": system}, &result, http.StatusCreated)
	if err == nil && !tenantVMID.MatchString(result.VM.ID) {
		err = failure("tenant_response_invalid", "门户未返回有效的虚拟机")
	}
	return result.VM, err
}

func (l *tenantLogin) open(ctx context.Context, id string, poll time.Duration, phase func(string)) (string, error) {
	for {
		var result struct {
			URL string `json:"workspace_url"`
		}
		status, err := l.request(ctx, l.portal.String(), http.MethodPost, "/api/tenant/vms/"+id+"/open", struct{}{}, &result, http.StatusOK, http.StatusAccepted)
		if err != nil || status == http.StatusOK {
			return result.URL, err
		}
		phase("tenant_start")
		for {
			timer := time.NewTimer(poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", failure("tenant_start_timeout", "等待虚拟机启动超时，请稍后重新连接")
			case <-timer.C:
			}
			vms, err := l.vms(ctx)
			if err != nil {
				return "", err
			}
			var state string
			for _, vm := range vms {
				if vm.ID == id {
					state = vm.State
					break
				}
			}
			if state == "ready" {
				break
			}
			if state == "error" || state == "stopped" {
				return "", failure("tenant_start_failed", "虚拟机未能启动，请重试或联系管理员")
			}
			if state != "starting" {
				return "", failure("tenant_vm_missing", "未找到当前账户的虚拟机")
			}
		}
	}
}

func connectTenant(lifetime context.Context, p HostProfile, secrets ConnectSecrets, phase func(string)) (*Session, error) {
	return connectTenantWithTransport(lifetime, p, secrets, phase, tenantHTTPTransport(), 2*time.Second)
}

func connectTenantWithTransport(lifetime context.Context, p HostProfile, secrets ConnectSecrets, phase func(string), transport *http.Transport, poll time.Duration) (*Session, error) {
	portal, err := parseTenantPortal(p.Portal)
	if err != nil || !tenantVMID.MatchString(p.VMID) || secrets.Deploy {
		return nil, failure("tenant_profile_invalid", "多租户连接配置无效")
	}
	ctx, stop := context.WithTimeout(lifetime, 10*time.Minute)
	defer stop()
	phase("tenant_login")
	login, err := loginTenant(ctx, portal, p.User, secrets.Passphrase, transport)
	clear(secrets.Passphrase)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			login.logout()
			transport.CloseIdleConnections()
		}
	}()
	raw, err := login.open(ctx, p.VMID, poll, phase)
	if err != nil {
		return nil, err
	}
	origin, ticket, err := tenantWorkspaceURL(portal, p.VMID, raw)
	if err != nil {
		return nil, err
	}
	phase("tenant_enter")
	// Separate cookie jar from the portal. Nothing scoped to the login host
	// or set later by a workspace is used as workspace authentication.
	jar, _ := cookiejar.New(nil)
	enter := &tenantLogin{portal: portal, client: &http.Client{Transport: transport, Jar: jar, CheckRedirect: tenantRedirect}}
	if _, err := enter.request(ctx, origin, http.MethodPost, "/_tenant/enter", map[string]string{"ticket": ticket}, nil, http.StatusNoContent); err != nil {
		return nil, err
	}
	vmURL, _ := url.Parse(origin)
	workspaceCookie := tenantCookie(jar, vmURL, "__Host-cage-workspace")
	if workspaceCookie == nil {
		return nil, failure("tenant_response_invalid", "虚拟机未返回有效的会话")
	}
	life, cancel := context.WithCancel(lifetime)
	var once sync.Once
	bound := &sessionTransport{base: transport, tenantCookie: workspaceCookie, origin: origin, ctx: life}
	s := &Session{Origin: origin, Transport: bound, Context: life, Manifest: PublicManifest{
		APIProtocol: APIProtocol, InstanceID: fmt.Sprintf("%x", sha256.Sum256([]byte(portal.String()+"/"+p.VMID))),
		Platform: "linux", Runtime: "incus", Capabilities: map[string]bool{"client_project_share": false},
	}}
	s.close = func() error {
		cancel()
		once.Do(func() { login.logout(); transport.CloseIdleConnections() })
		return nil
	}
	bound.invalidate = func() error { cancel(); return nil }
	s.Client = &http.Client{Transport: bound, CheckRedirect: tenantRedirect}
	complete = true
	return s, nil
}
