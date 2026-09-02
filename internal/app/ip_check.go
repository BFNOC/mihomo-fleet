package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 出口 IP 检测：经实例的混合端口（HTTP 代理）请求一个回显 IP 的网址，把
// 响应解析成 IP 返回。默认网址可被请求体里的 url 覆盖，前端负责记住用户的选择。
const (
	defaultIPCheckURL = "https://api.ip.sb/ip"
	ipCheckTimeout    = 15 * time.Second
	ipCheckMaxBody    = 4 << 10
)

// instanceProxyDialHost picks a local address the controller itself can reach
// item's mixed port on: loopback whenever the bind covers it, otherwise the
// first bound address (which is one of this host's own interfaces).
func instanceProxyDialHost(item *Instance) string {
	addrs, err := parseProxyBindAddresses(item.ProxyBind)
	if err != nil || len(addrs) == 0 {
		return defaultProxyBind
	}
	for _, addr := range addrs {
		switch canonicalProxyBindHost(addr) {
		case "0.0.0.0", defaultProxyBind:
			return defaultProxyBind
		case "::":
			return "::1"
		}
	}
	return addrs[0]
}

// fetchInstanceIP requests target through item's mixed port and returns the IP
// the remote side reported. Plain-text bodies (ip.sb, ipify, icanhazip ...) are
// trimmed; a JSON object is read via its "ip" field.
func fetchInstanceIP(ctx context.Context, item *Instance, target string) (string, error) {
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(instanceProxyDialHost(item), strconv.Itoa(item.MixedPort))}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true},
		Timeout:   ipCheckTimeout,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	// ip.sb 等站点对浏览器 UA 返回 HTML 页面，对 curl UA 返回纯文本。
	req.Header.Set("User-Agent", "curl/8.0")
	req.Header.Set("Accept", "text/plain, application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ip check request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, ipCheckMaxBody))
	if err != nil {
		return "", fmt.Errorf("ip check request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ip check returned HTTP %d", resp.StatusCode)
	}
	return parseIPCheckBody(body)
}

func parseIPCheckBody(body []byte) (string, error) {
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "{") {
		var payload struct {
			IP string `json:"ip"`
		}
		if err := json.Unmarshal([]byte(text), &payload); err == nil && payload.IP != "" {
			text = strings.TrimSpace(payload.IP)
		}
	}
	if net.ParseIP(text) == nil {
		snippet := text
		if len(snippet) > 60 {
			snippet = snippet[:60] + "…"
		}
		return "", fmt.Errorf("ip check response is not an IP: %q", snippet)
	}
	return text, nil
}

func (c *Controller) handleIPCheck(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	target := strings.TrimSpace(req.URL)
	if target == "" {
		target = defaultIPCheckURL
	}
	if parsed, err := url.Parse(target); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, http.StatusBadRequest, errors.New("ip check URL must start with http:// or https://"))
		return
	}
	item, ok := c.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if c.manager.state(id) == nil {
		writeError(w, http.StatusConflict, errors.New("instance must be running to check ip"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ipCheckTimeout)
	defer cancel()
	started := time.Now()
	ip, err := fetchInstanceIP(ctx, item, target)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{
		"ip":        ip,
		"url":       target,
		"elapsedMs": time.Since(started).Milliseconds(),
	})
}
