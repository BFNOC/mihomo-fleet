package app

import (
	"reflect"
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
