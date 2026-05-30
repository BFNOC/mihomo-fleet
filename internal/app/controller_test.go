package app

import "testing"

func TestAllowedHostStrictLoopback(t *testing.T) {
	c := &Controller{}
	allowed := []string{"127.0.0.1:47890", "localhost:47890", "[::1]:47890", "[::1]"}
	for _, host := range allowed {
		if !c.allowedHost(host) {
			t.Fatalf("expected host %q to be allowed", host)
		}
	}

	blocked := []string{"attacker.test:47890", "[::1].attacker.test:47890", "127.0.0.1.attacker:47890", ""}
	for _, host := range blocked {
		if c.allowedHost(host) {
			t.Fatalf("expected host %q to be blocked", host)
		}
	}
}
