package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// mihomoProxyRequestInfo carries the per-request values (arch L9) that a
// cached, port-keyed *httputil.ReverseProxy cannot close over directly: the
// same *ReverseProxy instance is reused across many different instances/
// requests over its lifetime, so its Director reads these from the request's
// context (stashed by handleMihomoProxy below) instead.
type mihomoProxyRequestInfo struct {
	targetPath string
	rawQuery   string
	secret     string
}

type mihomoProxyContextKey struct{}

// mihomoProxyFor returns a *httputil.ReverseProxy targeting
// 127.0.0.1:<port>, creating and caching one on first use (arch L9): the
// previous implementation constructed a brand new ReverseProxy (and Director
// closure) on every single request.
func (c *Controller) mihomoProxyFor(port int) *httputil.ReverseProxy {
	c.mihomoProxiesMu.Lock()
	defer c.mihomoProxiesMu.Unlock()
	if proxy, ok := c.mihomoProxies[port]; ok {
		return proxy
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.proxyTransport
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		info, _ := req.Context().Value(mihomoProxyContextKey{}).(mihomoProxyRequestInfo)
		req.URL.Path = info.targetPath
		req.URL.RawQuery = info.rawQuery
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+info.secret)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("mihomo controller unreachable: %w", err))
	}
	c.mihomoProxies[port] = proxy
	return proxy
}

func (c *Controller) handleMihomoProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mihomo/")
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	item, ok := c.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// arch L9: forwarding unconditionally here meant a stopped instance
	// whose ControllerPort had since been reused by an unrelated local
	// process would receive that process, not mihomo, complete with the
	// instance's controller secret in the Authorization header. Busy
	// (running or still starting) is the same guard the PUT handlers above
	// use to decide whether an instance's runtime state can be trusted.
	if !c.manager.Busy(id) {
		writeError(w, http.StatusConflict, fmt.Errorf("instance %q is not running", id))
		return
	}
	targetPath := "/"
	if len(parts) == 2 {
		targetPath = "/" + parts[1]
	}
	proxy := c.mihomoProxyFor(item.ControllerPort)
	info := mihomoProxyRequestInfo{targetPath: targetPath, rawQuery: r.URL.RawQuery, secret: item.Secret}
	r = r.WithContext(context.WithValue(r.Context(), mihomoProxyContextKey{}, info))
	proxy.ServeHTTP(w, r)
}
