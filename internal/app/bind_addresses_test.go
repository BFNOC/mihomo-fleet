package app

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestClassifyBindAddressKind(t *testing.T) {
	tests := []struct {
		name string
		ip   net.IP
		want string
	}{
		{name: "IPv4 loopback", ip: net.ParseIP("127.0.0.1"), want: "loopback"},
		{name: "IPv6 loopback", ip: net.ParseIP("::1"), want: "loopback"},
		{name: "IPv4 link-local", ip: net.ParseIP("169.254.1.2"), want: "linkLocal"},
		{name: "IPv6 link-local", ip: net.ParseIP("fe80::1"), want: "linkLocal"},
		{name: "RFC1918 10/8", ip: net.ParseIP("10.0.0.5"), want: "private"},
		{name: "RFC1918 172.16/12", ip: net.ParseIP("172.20.3.4"), want: "private"},
		{name: "RFC1918 192.168/16", ip: net.ParseIP("192.168.64.1"), want: "private"},
		{name: "IPv6 unique local fc00::/7", ip: net.ParseIP("fd12:3456:789a::1"), want: "private"},
		{name: "public IPv4", ip: net.ParseIP("203.0.113.9"), want: "public"},
		{name: "public IPv6", ip: net.ParseIP("2001:db8::1"), want: "public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyBindAddressKind(tt.ip); got != tt.want {
				t.Fatalf("classifyBindAddressKind(%v) = %q, want %q", tt.ip, got, tt.want)
			}
		})
	}
}

// TestBuildBindAddressOptionsZoneAndOrdering covers the IPv6 link-local zone
// (an address must carry its source interface as a zone; anything else must
// not), the loopback/private/public/linkLocal/wildcard ordering with the
// wildcard always last, and alphabetical ordering by address within a kind.
func TestBuildBindAddressOptionsZoneAndOrdering(t *testing.T) {
	candidates := []bindAddressCandidate{
		{ip: net.ParseIP("fe80::1"), ifaceName: "en0"},
		{ip: net.ParseIP("203.0.113.9"), ifaceName: "eth0"},
		{ip: net.ParseIP("127.0.0.1"), ifaceName: "lo"},
		{ip: net.ParseIP("192.168.64.5"), ifaceName: "eth0"},
		{ip: net.ParseIP("192.168.1.2"), ifaceName: "eth1"},
		{ip: net.ParseIP("2001:db8::1"), ifaceName: "eth0"},
	}
	got := buildBindAddressOptions(candidates)

	want := []BindAddressOption{
		{Address: "127.0.0.1", Kind: "loopback", Interface: "lo"},
		{Address: "192.168.1.2", Kind: "private", Interface: "eth1"},
		{Address: "192.168.64.5", Kind: "private", Interface: "eth0"},
		{Address: "2001:db8::1", Kind: "public", Interface: "eth0"},
		{Address: "203.0.113.9", Kind: "public", Interface: "eth0"},
		{Address: "fe80::1%en0", Kind: "linkLocal", Interface: "en0"},
		{Address: "0.0.0.0", Kind: "wildcard"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildBindAddressOptions() = %#v, want %#v", got, want)
	}
}

// TestBuildBindAddressOptionsDedupesFirstOccurrenceWins covers dedupe by the
// same key normalization the picker compares on (canonicalProxyBindHost):
// the same address reachable from two interfaces (or written in different
// case/trailing-dot form) must appear only once, keeping the first
// occurrence's Interface.
func TestBuildBindAddressOptionsDedupesFirstOccurrenceWins(t *testing.T) {
	candidates := []bindAddressCandidate{
		{ip: net.ParseIP("192.168.1.2"), ifaceName: "eth0"},
		{ip: net.ParseIP("192.168.1.2"), ifaceName: "eth1"},
		{ip: net.ParseIP("127.0.0.1"), ifaceName: "lo0"},
		{ip: net.ParseIP("127.0.0.1"), ifaceName: "lo1"},
	}
	got := buildBindAddressOptions(candidates)
	// Two distinct addresses plus the wildcard -- not four entries.
	if len(got) != 3 {
		t.Fatalf("len(options) = %d, want 3 (deduped), got %#v", len(got), got)
	}
	for _, opt := range got {
		switch opt.Address {
		case "192.168.1.2":
			if opt.Interface != "eth0" {
				t.Fatalf("192.168.1.2 interface = %q, want eth0 (first occurrence)", opt.Interface)
			}
		case "127.0.0.1":
			if opt.Interface != "lo0" {
				t.Fatalf("127.0.0.1 interface = %q, want lo0 (first occurrence)", opt.Interface)
			}
		case "0.0.0.0":
		default:
			t.Fatalf("unexpected address in deduped output: %+v", opt)
		}
	}
}

func TestBuildBindAddressOptionsSkipsNoCandidatesYieldsOnlyWildcard(t *testing.T) {
	got := buildBindAddressOptions(nil)
	want := []BindAddressOption{{Address: "0.0.0.0", Kind: "wildcard"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildBindAddressOptions(nil) = %#v, want %#v", got, want)
	}
}

// TestHostBindAddressesSurviveNormalizeRoundTrip is the contract
// hostBindAddresses exists for: every address it returns must survive
// normalizeProxyBindAddress unchanged, since the UI feeds these straight
// into the proxyBind field. This runs against whatever real interfaces the
// test machine has, unlike the synthetic-candidate tests above.
func TestHostBindAddressesSurviveNormalizeRoundTrip(t *testing.T) {
	options := hostBindAddresses()
	if len(options) == 0 {
		t.Fatal("hostBindAddresses() returned no options at all (not even the wildcard)")
	}
	for _, opt := range options {
		got, err := normalizeProxyBindAddress(opt.Address)
		if err != nil {
			t.Fatalf("normalizeProxyBindAddress(%q) error: %v", opt.Address, err)
		}
		if got != opt.Address {
			t.Fatalf("normalizeProxyBindAddress(%q) = %q, want unchanged", opt.Address, got)
		}
	}
}

// TestHostBindAddressesWildcardLast covers the ordering contract end to end
// (not just buildBindAddressOptions' unit test above): the real
// hostBindAddresses() result must end with the 0.0.0.0 wildcard.
func TestHostBindAddressesWildcardLast(t *testing.T) {
	options := hostBindAddresses()
	last := options[len(options)-1]
	if last.Address != "0.0.0.0" || last.Kind != "wildcard" {
		t.Fatalf("last option = %+v, want the 0.0.0.0 wildcard", last)
	}
	for _, opt := range options[:len(options)-1] {
		if opt.Address == "0.0.0.0" {
			t.Fatalf("0.0.0.0 appeared before the end of the list: %+v", options)
		}
	}
}

// TestHostBindAddressesDeterministicAcrossCalls covers the "must not
// reshuffle between polls" requirement directly against the real
// enumeration path.
func TestHostBindAddressesDeterministicAcrossCalls(t *testing.T) {
	first := hostBindAddresses()
	second := hostBindAddresses()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("hostBindAddresses() is not deterministic across calls:\n%#v\n%#v", first, second)
	}
}

func TestHandleBindAddressesRejectsWrongMethod(t *testing.T) {
	c := newBatchTestController(t)
	req := httptest.NewRequest(http.MethodPost, "/api/system/bind-addresses", nil)
	rec := httptest.NewRecorder()
	c.handleBindAddresses(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleBindAddressesReturnsAddressesArray(t *testing.T) {
	c := newBatchTestController(t)
	req := httptest.NewRequest(http.MethodGet, "/api/system/bind-addresses", nil)
	rec := httptest.NewRecorder()
	c.handleBindAddresses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Addresses []BindAddressOption `json:"addresses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(payload.Addresses) == 0 {
		t.Fatal("expected at least the wildcard entry")
	}
}

// TestRouteSystemAndBindAddressesDoNotConflict verifies the claim that
// "/api/system" (an exact pattern, no trailing slash) and
// "/api/system/bind-addresses" can coexist as distinct registered patterns
// on the same mux without one shadowing the other.
func TestRouteSystemAndBindAddressesDoNotConflict(t *testing.T) {
	c := newBatchTestController(t)
	mux := http.NewServeMux()
	c.RegisterRoutes(mux)

	systemReq := httptest.NewRequest(http.MethodGet, "/api/system", nil)
	systemRec := httptest.NewRecorder()
	mux.ServeHTTP(systemRec, systemReq)
	if systemRec.Code != http.StatusOK {
		t.Fatalf("GET /api/system status = %d, want 200, body: %s", systemRec.Code, systemRec.Body.String())
	}
	var systemPayload SystemStatus
	if err := json.Unmarshal(systemRec.Body.Bytes(), &systemPayload); err != nil {
		t.Fatalf("decode /api/system: %v (body %s)", err, systemRec.Body.String())
	}

	bindReq := httptest.NewRequest(http.MethodGet, "/api/system/bind-addresses", nil)
	bindRec := httptest.NewRecorder()
	mux.ServeHTTP(bindRec, bindReq)
	if bindRec.Code != http.StatusOK {
		t.Fatalf("GET /api/system/bind-addresses status = %d, want 200, body: %s", bindRec.Code, bindRec.Body.String())
	}
	var bindPayload struct {
		Addresses []BindAddressOption `json:"addresses"`
	}
	if err := json.Unmarshal(bindRec.Body.Bytes(), &bindPayload); err != nil {
		t.Fatalf("decode /api/system/bind-addresses: %v (body %s)", err, bindRec.Body.String())
	}
	if len(bindPayload.Addresses) == 0 {
		t.Fatal("expected at least the wildcard entry from /api/system/bind-addresses")
	}
}
