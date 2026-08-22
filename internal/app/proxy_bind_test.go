package app

import (
	"errors"
	"net"
	"reflect"
	"strconv"
	"testing"
)

func TestParseProxyBindAddresses(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "default",
			want: []string{"127.0.0.1"},
		},
		{
			name: "multiple addresses",
			raw:  "127.0.0.1, 192.168.64.1,127.0.0.1",
			want: []string{"127.0.0.1", "192.168.64.1"},
		},
		{
			name: "all alias",
			raw:  "all",
			want: []string{"0.0.0.0"},
		},
		{
			name: "bracketed IPv6",
			raw:  "[::1]",
			want: []string{"::1"},
		},
		{
			name: "localhost normalizes to loopback",
			raw:  "localhost,127.0.0.1",
			want: []string{"127.0.0.1"},
		},
		{
			name: "IPv6 zones stay distinct",
			raw:  "fe80::1%en0,fe80::1%en1",
			want: []string{"fe80::1%en0", "fe80::1%en1"},
		},
		{
			name: "IPv4 wildcard covers specific IPv4 binds",
			raw:  "0.0.0.0,127.0.0.1,192.168.64.1",
			want: []string{"0.0.0.0"},
		},
		{
			name: "IPv4 and IPv6 wildcards",
			raw:  "0.0.0.0,::",
			want: []string{"0.0.0.0", "::"},
		},
		{
			name:    "reject port",
			raw:     "127.0.0.1:28000",
			wantErr: true,
		},
		{
			name:    "reject bracketed IPv6 with port",
			raw:     "[::1]:28000",
			wantErr: true,
		},
		{
			name:    "reject hostname",
			raw:     "example.test",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProxyBindAddresses(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseProxyBindAddresses(%q) expected error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProxyBindAddresses(%q) error: %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseProxyBindAddresses(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

// withProxyBindProbe swaps the bind probe for the duration of a test. Same
// serialization caveat as withPortFree (store_test.go): it replaces a
// package-level var, so a test using it must not run with t.Parallel().
func withProxyBindProbe(t *testing.T, fn func(address string) error) {
	t.Helper()
	original := proxyBindListenProbe
	proxyBindListenProbe = fn
	t.Cleanup(func() { proxyBindListenProbe = original })
}

func TestCheckProxyBindAvailableProbesEveryAddress(t *testing.T) {
	var probed []string
	withProxyBindProbe(t, func(address string) error {
		probed = append(probed, address)
		return nil
	})

	if err := checkProxyBindAvailable("127.0.0.1,::1", 28001); err != nil {
		t.Fatalf("checkProxyBindAvailable() error = %v, want nil", err)
	}
	want := []string{"127.0.0.1:28001", "[::1]:28001"}
	if !reflect.DeepEqual(probed, want) {
		t.Fatalf("probed = %#v, want %#v", probed, want)
	}
}

// The backup-restored-elsewhere case: the stored address is not one this host
// has, so the failure must name the address rather than blame the port.
func TestCheckProxyBindAvailableReportsMissingHostAddress(t *testing.T) {
	withProxyBindProbe(t, func(string) error {
		return errors.New("bind: cannot assign requested address")
	})

	// 192.0.2.0/24 is TEST-NET-1 (RFC 5737): reserved for documentation, so no
	// real interface can legitimately carry it and hostBindAddresses() will
	// never list it on the machine running this test.
	err := checkProxyBindAvailable("192.0.2.10", 28001)
	if err == nil {
		t.Fatal("expected an error for an address this host does not have")
	}
	const want = `proxy bind address "192.0.2.10" is not available on this host`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// A wildcard bind can only ever fail for a busy port, so it must not be
// reported as a missing address.
func TestCheckProxyBindAvailableReportsBusyPortForWildcard(t *testing.T) {
	withProxyBindProbe(t, func(string) error {
		return errors.New("bind: address already in use")
	})

	err := checkProxyBindAvailable("0.0.0.0", 28001)
	if err == nil {
		t.Fatal("expected an error for a busy wildcard bind")
	}
	const want = "mixed proxy port 28001 is already in use"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// An address the host really does have still routes to the port-conflict
// message. Sourced from hostBindAddresses() so it holds on any machine.
func TestCheckProxyBindAvailableReportsBusyPortForLocalAddress(t *testing.T) {
	withProxyBindProbe(t, func(string) error {
		return errors.New("bind: address already in use")
	})

	options := hostBindAddresses()
	if len(options) == 0 {
		t.Skip("no host addresses to test against")
	}
	addr := options[0].Address
	if addr == "0.0.0.0" {
		t.Skip("only the wildcard is available on this host")
	}
	err := checkProxyBindAvailable(addr, 28001)
	if err == nil {
		t.Fatalf("expected an error for %q", addr)
	}
	const want = "mixed proxy port 28001 is already in use"
	if err.Error() != want {
		t.Fatalf("error for %q = %q, want %q", addr, err.Error(), want)
	}
}

// TCP free but UDP taken must fail the check: a mixed listener serves both, so
// letting this through starts an instance whose UDP half never comes up while
// the loopback controller still reports it healthy.
func TestProxyBindListenProbeRejectsBusyUDPPort(t *testing.T) {
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind a udp port here")
	}
	defer packet.Close()
	addr := packet.LocalAddr().String()
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	// The TCP half of the same port is deliberately left free.
	tcp, err := net.Listen("tcp", addr)
	if err == nil {
		tcp.Close()
	} else {
		t.Skipf("tcp %s is not free, so this would not isolate the udp check", addr)
	}
	if err := checkProxyBindAvailable("127.0.0.1", port); err == nil {
		t.Fatalf("checkProxyBindAvailable(127.0.0.1, %d) = nil, want a conflict for the busy udp half", port)
	}
}

// mixedPortFreeOn answers the Store's save-time question, and must not turn an
// invalid bind string into a port conflict -- that error has its own message.
func TestMixedPortFreeOnSeparatesUnavailableFromInvalid(t *testing.T) {
	withProxyBindProbe(t, func(string) error { return errors.New("bind: address already in use") })
	if mixedPortFreeOn("127.0.0.1", 28001) {
		t.Fatal("mixedPortFreeOn = true for an address whose port is held")
	}
	if !mixedPortFreeOn("not-an-address", 28001) {
		t.Fatal("mixedPortFreeOn = false for a malformed bind string; that is the address validator's error to report")
	}
}
