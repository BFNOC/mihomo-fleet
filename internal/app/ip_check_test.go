package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestParseIPCheckBody(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4\n":                       "1.2.3.4",
		"  2001:db8::1 ":                  "2001:db8::1",
		`{"ip":"5.6.7.8","country":"SG"}`: "5.6.7.8",
	}
	for body, want := range cases {
		got, err := parseIPCheckBody([]byte(body))
		if err != nil || got != want {
			t.Fatalf("parseIPCheckBody(%q) = %q, %v; want %q", body, got, err, want)
		}
	}
	if _, err := parseIPCheckBody([]byte("<html>403</html>")); err == nil || !strings.Contains(err.Error(), "not an IP") {
		t.Fatalf("html body: err = %v, want not-an-IP error", err)
	}
}

func TestInstanceProxyDialHost(t *testing.T) {
	cases := map[string]string{
		"":                      "127.0.0.1",
		"0.0.0.0":               "127.0.0.1",
		"192.168.1.5,127.0.0.1": "127.0.0.1",
		"192.168.1.5":           "192.168.1.5",
		"::":                    "::1",
		"192.168.1.5,10.0.0.2":  "192.168.1.5",
	}
	for bind, want := range cases {
		if got := instanceProxyDialHost(&Instance{ProxyBind: bind}); got != want {
			t.Fatalf("instanceProxyDialHost(%q) = %q, want %q", bind, got, want)
		}
	}
}

// fetchInstanceIP must go through the mixed port as an HTTP proxy: a stub
// proxy that answers every absolute-URL GET with a fixed IP proves the
// request was routed via the port rather than dialed directly.
func TestFetchInstanceIPUsesMixedPortAsProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.Host != "ip.example" {
			http.Error(w, "not proxied: "+r.RequestURI, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("9.9.9.9\n"))
	}))
	defer proxy.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	mixedPort, _ := strconv.Atoi(port)
	item := &Instance{MixedPort: mixedPort, ProxyBind: "127.0.0.1"}
	ip, err := fetchInstanceIP(context.Background(), item, "http://ip.example/ip")
	if err != nil || ip != "9.9.9.9" {
		t.Fatalf("fetchInstanceIP = %q, %v; want 9.9.9.9 via proxy", ip, err)
	}
}
