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
	// (running or still starting) is too loose a guard for this specific
	// path: while an instance is only "starting", its controller port may
	// not be listening yet at all, or the exact same reused-port hazard
	// above could still be squatting on it during that preparation window --
	// so this requires a *confirmed* running process (state, not Busy)
	// before ever forwarding a request at it (finding #3, code review). The
	// PUT handlers in instance_handler.go still use Busy on purpose, since
	// blocking an edit for the whole starting window is the guard they
	// actually want.
	//
	// This narrows the window but cannot close it: the process can exit at any
	// instant, including between this check and the proxy's own dial below, or
	// after a successful dial. No check-then-connect sequence is atomic
	// against the port being freed and re-bound, so no lock fixes this. What
	// IS closed here is the far wider window where a *known-stopped*
	// instance's port was forwarded to unconditionally.
	//
	// The residual is a consequence of addressing instances by localhost TCP
	// port, not of anything mihomo forces: it also supports
	// external-controller-unix / external-controller-pipe (see config.go's
	// strip list), whose per-process, never-reused addresses would remove the
	// reused-port hazard entirely. That migration is deliberately deferred --
	// see docs/known-limitations.md for the full assessment and plan.
	if c.manager.state(id) == nil {
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
