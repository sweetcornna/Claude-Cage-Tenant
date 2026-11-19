package tenant

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	vmIDPattern   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	secretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	dnsLabel      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type site struct {
	origin      string
	scheme      string
	host        string
	name        string
	portSuffix  string
	loginCookie string
	vmCookie    string
}

func parseSite(origin string, dev bool) (site, error) {
	invalid := errors.New("origin must be a canonical HTTPS DNS origin, or http://cage.localhost:<port> in development")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || origin != parsed.Scheme+"://"+parsed.Host {
		return site{}, invalid
	}
	name, port := parsed.Hostname(), parsed.Port()
	result := site{origin: origin, scheme: parsed.Scheme, host: parsed.Host, name: name,
		loginCookie: "__Host-cage-tenant", vmCookie: "__Host-cage-workspace"}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || port != strconv.Itoa(number) {
			return site{}, invalid
		}
		result.portSuffix = ":" + port
	}
	if parsed.Host != name+result.portSuffix {
		return site{}, invalid
	}
	if dev {
		if name != "cage.localhost" || parsed.Scheme != "http" || port == "" || port == "80" {
			return site{}, invalid
		}
		result.loginCookie, result.vmCookie = "cage_tenant", "cage_workspace"
		return result, nil
	}
	if parsed.Scheme != "https" || port == "443" || len(name) > 217 || !strings.Contains(name, ".") || net.ParseIP(name) != nil || strings.HasSuffix(name, ".localhost") {
		return site{}, invalid
	}
	labels := strings.Split(name, ".")
	for _, label := range labels {
		if !dnsLabel.MatchString(label) {
			return site{}, invalid
		}
	}
	return result, nil
}

func (s site) vmOrigin(id string) string {
	return s.scheme + "://vm-" + id + "." + s.name + s.portSuffix
}

func (s site) vmID(host string) string {
	id, ok := strings.CutPrefix(host, "vm-")
	if !ok {
		return ""
	}
	id, ok = strings.CutSuffix(id, "."+s.name+s.portSuffix)
	if !ok || !vmIDPattern.MatchString(id) {
		return ""
	}
	return id
}
