package tenant

import (
	"net/http"
	"net/http/httputil"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Hashed workbench assets under /assets/ never change under their name, so a
// browser may keep them. Everything else the gateway serves stays
// "Cache-Control: no-store", and a workbench that does not say otherwise (an
// older one answers no-store here too) keeps the assets uncacheable as well.
const (
	assetPrefix = "/assets/"
	// maxAssetAge caps the lifetime a workbench may grant, one year.
	maxAssetAge = 365 * 24 * 60 * 60
)

var (
	assetEncodings = regexp.MustCompile(`^[A-Za-z0-9*;=.,\s-]{1,128}$`)
	assetETag      = regexp.MustCompile(`^(?:W/)?"[\x21\x23-\x7e]{0,126}"$`)
)

func assetRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(r.URL.Path, assetPrefix) && len(r.Header.Values("Upgrade")) == 0
}

// cacheAssets changes how proxy treats a request for a hashed asset: the
// browser's Accept-Encoding reaches the workbench, so a pre-compressed asset
// travels compressed instead of being decoded by the gateway's own transport,
// and the workbench's cache policy and validators reach the browser. reply is
// the header of the response the gateway sends; it already holds no-store.
func cacheAssets(proxy *httputil.ReverseProxy, r *http.Request, reply http.Header) {
	if !assetRequest(r) {
		return
	}
	rewrite, modify := proxy.Rewrite, proxy.ModifyResponse
	proxy.Rewrite = func(request *httputil.ProxyRequest) {
		rewrite(request)
		if encodings := strings.Join(request.In.Header.Values("Accept-Encoding"), ","); assetEncodings.MatchString(encodings) {
			request.Out.Header.Set("Accept-Encoding", encodings)
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		guest := response.Header
		if err := modify(response); err != nil {
			return err
		}
		forwardAssetCaching(response, guest, reply)
		return nil
	}
}

func forwardAssetCaching(response *http.Response, guest, reply http.Header) {
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		return
	}
	control, ok := assetCacheControl(guest.Values("Cache-Control"))
	if !ok {
		return
	}
	// ReverseProxy adds the headers of the response to the ones already set,
	// so the gateway's no-store has to go.
	reply.Del("Cache-Control")
	headers := response.Header
	headers.Set("Cache-Control", control)
	if etag := guest.Get("ETag"); assetETag.MatchString(etag) {
		headers.Set("ETag", etag)
	}
	if modified, err := http.ParseTime(guest.Get("Last-Modified")); err == nil {
		headers.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	}
	headers.Set("Vary", "Accept-Encoding")
}

// assetCacheControl turns the workbench's Cache-Control into one the gateway
// forwards. Only a small set of directives is understood; anything else, and
// any no-store, means the asset stays uncacheable. The browser may keep the
// asset but a shared cache may not, so public becomes private.
func assetCacheControl(values []string) (string, bool) {
	var directives []string
	for _, directive := range strings.Split(strings.Join(values, ","), ",") {
		directive = strings.ToLower(strings.TrimSpace(directive))
		switch {
		case directive == "public":
			directive = "private"
		case directive == "private", directive == "immutable", directive == "no-cache", directive == "must-revalidate":
		case strings.HasPrefix(directive, "max-age="):
			digits := strings.TrimPrefix(directive, "max-age=")
			if digits == "" || strings.ContainsFunc(digits, func(char rune) bool { return char < '0' || char > '9' }) {
				return "", false
			}
			seconds, err := strconv.ParseUint(digits, 10, 63)
			if err != nil {
				// Too large for 63 bits: a very long lifetime, capped below.
				seconds = maxAssetAge
			}
			directive = "max-age=" + strconv.FormatUint(min(seconds, maxAssetAge), 10)
		default:
			return "", false
		}
		if !slices.Contains(directives, directive) {
			directives = append(directives, directive)
		}
	}
	return strings.Join(directives, ", "), len(directives) != 0
}
