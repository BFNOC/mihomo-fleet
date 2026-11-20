package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PortMapping pins one extra inbound port of an instance to one outbound proxy
// or proxy group. It is rendered as a mihomo `listeners:` entry of type mixed
// with `proxy: <Proxy>`, which mihomo resolves before any rule (tunnel.go
// resolveMetadata checks metadata.SpecialProxy first), so traffic entering
// Port always leaves through Proxy whatever the instance's rules or mode say.
//
// The instance's own mixed port is unaffected and keeps its normal routing;
// mappings are additive and off when the list is empty.
type PortMapping struct {
	Port  int    `json:"port"`
	Proxy string `json:"proxy"`
}

// maxPortMappings caps one instance's mappings. Every mapping is a listening
// socket per bind address, so the cap bounds what one save can ask mihomo to
// open; it is far above the handful of exits the feature was built for.
const maxPortMappings = 64

// portMappingReadyTimeout bounds how long startContext waits after launch for
// every mapped port to accept a connection. mihomo binds listeners only after
// parsing the whole config, and a listener that fails to bind is just logged
// (listener.go PatchInboundListeners) while the process keeps running, so
// without this wait a mapped port mihomo never bound would leave the instance
// looking healthy. It proves something accepts on the port, not that it is
// mihomo: a process grabbing the port between the pre-start probe and
// mihomo's bind would still pass, as would a lost UDP half. A var so tests
// can shorten it.
var portMappingReadyTimeout = 15 * time.Second

// portMappingDial is the seam tests replace for the post-launch readiness check.
var portMappingDial = func(ctx context.Context, address string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// tcpListenProbe is the TCP-only sibling of proxyBindListenProbe, used for a
// mapping whose listener has UDP disabled: an unrelated process holding only
// the UDP half of that port must not block it.
var tcpListenProbe = func(address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return ln.Close()
}

func clonePortMappings(in []PortMapping) []PortMapping {
	if len(in) == 0 {
		return nil
	}
	return append([]PortMapping(nil), in...)
}

func portMappingsEqual(a, b []PortMapping) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// portMappingUDP is whether the generated mapping listeners carry UDP. In
// rule mode they match the fleet's own mixed listeners (`udp: true`). In
// global-chain mode they follow ChainUDP: that mode rejects UDP through its
// generated NETWORK,UDP,REJECT rule, and a listener's `proxy:` bypasses every
// rule, so enabling UDP on a mapping there would let through exactly what the
// main port refuses.
func portMappingUDP(mode string, chainUDP bool) bool {
	if instanceMode(mode) == InstanceModeGlobalChain {
		return chainUDP
	}
	return true
}

// normalizePortMappings trims target names and checks everything that does not
// need the profile or the host: count, port range, empty target, duplicate
// ports, and collisions with this instance's own mixed/controller ports
// (a zero mixed/controller port means "not yet allocated" and is skipped).
func normalizePortMappings(in []PortMapping, mixedPort, controllerPort int) ([]PortMapping, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxPortMappings {
		return nil, validationError{msg: fmt.Sprintf("too many port mappings (%d, max %d)", len(in), maxPortMappings)}
	}
	out := make([]PortMapping, 0, len(in))
	seen := make(map[int]int, len(in))
	for i, m := range in {
		// Not trimmed: mihomo matches a listener's proxy against the name
		// exactly as the config writes it, padding included.
		if m.Port < 1 || m.Port > 65535 {
			return nil, validationError{msg: fmt.Sprintf("port mapping #%d: port %d is out of range (1-65535)", i+1, m.Port)}
		}
		if strings.TrimSpace(m.Proxy) == "" {
			return nil, validationError{msg: fmt.Sprintf("port mapping #%d (port %d): proxy name is empty", i+1, m.Port)}
		}
		if prev, ok := seen[m.Port]; ok {
			return nil, validationError{msg: fmt.Sprintf("port mapping port %d is duplicated (#%d and #%d)", m.Port, prev, i+1)}
		}
		if mixedPort > 0 && m.Port == mixedPort {
			return nil, validationError{msg: fmt.Sprintf("port mapping port %d equals this instance's mixed proxy port", m.Port)}
		}
		if controllerPort > 0 && m.Port == controllerPort {
			return nil, validationError{msg: fmt.Sprintf("port mapping port %d equals this instance's controller port", m.Port)}
		}
		seen[m.Port] = i + 1
		out = append(out, m)
	}
	return out, nil
}

// portMappingTargetNames is the set a mapping may point at: the top-level
// proxies and proxy groups of the final runtime config. mihomo resolves a
// listener's `proxy` against exactly that table (built-ins aside, which the
// fleet does not offer); proxy-provider members are not in it.
func portMappingTargetNames(cfg map[string]any) []string {
	var names []string
	for _, key := range []string{"proxies", "proxy-groups"} {
		items, _ := cfg[key].([]any)
		for _, item := range items {
			// The raw name, not proxyName()'s trimmed one: mihomo matches the
			// listener's proxy against the name exactly as written.
			entry, _ := item.(map[string]any)
			if name, _ := entry["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// checkPortMappingTargets refuses a mapping whose target is not in the final
// runtime config. mihomo itself accepts such a config (`mihomo -t` passes) and
// only fails each connection with "proxy X not found", so this is the only
// place the mistake can surface before traffic does.
func checkPortMappingTargets(cfg map[string]any, mappings []PortMapping) error {
	if len(mappings) == 0 {
		return nil
	}
	names := portMappingTargetNames(cfg)
	known := stringSet(names)
	for _, m := range mappings {
		if known[m.Proxy] {
			continue
		}
		msg := fmt.Sprintf("port mapping %d -> %q references unknown proxy or group; available: %s", m.Port, m.Proxy, summarizeNames(names, 20))
		if providers, ok := cfg["proxy-providers"].(map[string]any); ok && len(providers) > 0 {
			msg += "; proxy-provider nodes cannot be mapped directly, map a proxy group instead"
		}
		return portMappingTargetError{msg: msg}
	}
	return nil
}

// portMappingTargetError is the "unknown target" refusal: a validation error
// (errors.Is errValidation), typed so a shared-profile edit can tell it apart
// from an instance's unrelated config problems (checkDependentPortMappingsLocked).
type portMappingTargetError struct{ msg string }

func (e portMappingTargetError) Error() string        { return e.msg }
func (e portMappingTargetError) Is(target error) bool { return target == errValidation }

func summarizeNames(names []string, limit int) string {
	if len(names) == 0 {
		return "(none)"
	}
	shown := names
	if len(shown) > limit {
		shown = shown[:limit]
	}
	// Quoted only when a name has surrounding spaces, which are otherwise
	// invisible in the message and are exactly what makes a match fail.
	parts := make([]string, len(shown))
	for i, name := range shown {
		if strings.TrimSpace(name) != name {
			name = strconv.Quote(name)
		}
		parts[i] = name
	}
	if len(names) <= limit {
		return strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%s, ... (%d total)", strings.Join(parts, ", "), len(names))
}

// portMappingListeners renders one mihomo listener per mapping and bind
// address. `listen` is always explicit: mihomo defaults a listener to 0.0.0.0,
// which would expose a loopback-only instance on every interface.
// checkPortMappingBind refuses a bind list mapping listeners cannot reproduce:
// a wildcard alongside a specific address of the other family
// (`0.0.0.0,::1`). A wildcard listener is dual-stack in Go, so it would also
// open the other family on every address, while separate listeners would
// collide with it. Only checked when the instance has mappings.
func checkPortMappingBind(bind string, mappings []PortMapping) error {
	if len(mappings) == 0 {
		return nil
	}
	addrs, err := parseProxyBindAddresses(bind)
	if err != nil {
		return nil // reported by the bind check itself
	}
	wildcard := ""
	for _, addr := range addrs {
		if host := canonicalProxyBindHost(addr); host == "0.0.0.0" || host == "::" {
			wildcard = addr
		}
	}
	if wildcard == "" {
		return nil
	}
	for _, addr := range addrs {
		if host := canonicalProxyBindHost(addr); host != "0.0.0.0" && host != "::" {
			return validationError{msg: fmt.Sprintf("port mappings cannot listen on proxy bind %q: the wildcard %s would also open every address of the other family than %s; use only wildcards or only specific addresses", bind, wildcard, addr)}
		}
	}
	return nil
}

// portMappingListenAddrs is where mapping listeners go: the bind addresses,
// except that a wildcard takes the whole list. mihomo listens through Go's
// net.Listen("tcp", ...), and a wildcard there is dual-stack, so any second
// address on the same port (`0.0.0.0,::`, or `::,127.0.0.1`) would fail with
// "address already in use" -- which the readiness check would then report as
// a failed start. The fleet's own fleet-mixed-N listeners predate this and
// are left as they are.
func portMappingListenAddrs(addrs []string) []string {
	for _, addr := range addrs {
		if host := canonicalProxyBindHost(addr); host == "0.0.0.0" || host == "::" {
			return []string{addr}
		}
	}
	return addrs
}

func portMappingListeners(mappings []PortMapping, addrs []string, udp bool) []any {
	addrs = portMappingListenAddrs(addrs)
	out := make([]any, 0, len(mappings)*len(addrs))
	for _, m := range mappings {
		for index, addr := range addrs {
			name := fmt.Sprintf("fleet-map-%d", m.Port)
			if len(addrs) > 1 {
				name = fmt.Sprintf("%s-%d", name, index+1)
			}
			out = append(out, map[string]any{
				"name":   name,
				"type":   "mixed",
				"port":   m.Port,
				"listen": addr,
				"udp":    udp,
				"proxy":  m.Proxy,
			})
		}
	}
	return out
}

// validatePortMappingTargets runs the same config generation start uses,
// without writing anything, so save time refuses what start would refuse.
func validatePortMappingTargets(item *Instance, profileConfig []byte) error {
	if len(item.PortMappings) == 0 {
		return nil
	}
	if _, _, err := buildRuntimeConfig(item, profileConfig); err != nil {
		if errors.Is(err, errValidation) {
			return err
		}
		return validationError{msg: err.Error()}
	}
	return nil
}

// checkDependentPortMappingsLocked refuses a new config for profileID that
// would stop another instance with port mappings from generating its runtime
// config (exceptID is the instance making the edit, checked by its own
// caller): a dropped mapping target, or a dropped chain member checked before
// it. Only breakage this edit introduces counts -- an instance whose config
// already failed under the current file (previous) is start's business.
// Subscription refreshes cannot be refused like this; those surface at start.
// Caller holds s.mu.
func (s *Store) checkDependentPortMappingsLocked(profileID string, previous, raw []byte, exceptID string) error {
	for _, item := range s.items {
		if item.ProfileID != profileID || item.ID == exceptID || len(item.PortMappings) == 0 {
			continue
		}
		if _, _, err := buildRuntimeConfig(item, previous); err != nil {
			continue
		}
		if _, _, err := buildRuntimeConfig(item, raw); err != nil {
			return validationError{msg: fmt.Sprintf("config would break instance %q: %v", item.Name, err)}
		}
	}
	return nil
}

// checkPortMappingAvailable probes one mapping's port on every bind address,
// with the transports its listener will actually open. The two failure causes
// are reported apart, like checkProxyBindAvailable does for the mixed port.
func checkPortMappingAvailable(bind string, m PortMapping, udp bool) error {
	addrs, err := parseProxyBindAddresses(bind)
	if err != nil {
		return err
	}
	probe := tcpListenProbe
	if udp {
		probe = proxyBindListenProbe
	}
	for _, addr := range addrs {
		if err := probe(net.JoinHostPort(addr, strconv.Itoa(m.Port))); err != nil {
			if !proxyBindAddressOnHost(addr) {
				return proxyBindUnavailableError{msg: fmt.Sprintf("proxy bind address %q is not available on this host", addr)}
			}
			return proxyBindUnavailableError{msg: fmt.Sprintf("port mapping port %d (-> %q) is already in use on %s: %v", m.Port, m.Proxy, addr, err)}
		}
	}
	return nil
}

// onHostBindAddresses keeps the addresses of bind this host has, joined like
// bind ("" when none). An unparsable bind is returned as is: it is rejected
// elsewhere with its own message.
func onHostBindAddresses(bind string) string {
	addrs, err := parseProxyBindAddresses(bind)
	if err != nil {
		return bind
	}
	kept := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if proxyBindAddressOnHost(addr) {
			kept = append(kept, addr)
		}
	}
	return strings.Join(kept, ",")
}

// portMappingReadyAddress is where the readiness check connects for one
// listener: a wildcard bind is reached through loopback of the same family.
func portMappingReadyAddress(addr string, port int) string {
	switch canonicalProxyBindHost(addr) {
	case "0.0.0.0":
		addr = "127.0.0.1"
	case "::":
		addr = "::1"
	}
	return net.JoinHostPort(addr, strconv.Itoa(port))
}

// mihomoListenersApplied reports whether item's mihomo has got past patching
// its named listeners, from its own controller (so another process holding a
// mapped port cannot fake it). updateListeners (executor.go) patches the named
// listeners first and only then calls SetAllowLan and ReCreateMixed, and
// GET /configs reports both: with one bind address the fleet's config has a
// top-level mixed-port, which reads non-zero only once recreated; with several
// it sets allow-lan true, which reads false (mihomo's initial value) until
// set. A var so tests can replace it.
var mihomoListenersApplied = func(ctx context.Context, item *Instance) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(item.ControllerPort)+"/configs", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+item.Secret)
	res, err := mihomoAPIClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("controller GET /configs returned %s", res.Status)
	}
	var general struct {
		MixedPort int  `json:"mixed-port"`
		AllowLan  bool `json:"allow-lan"`
	}
	if err := json.NewDecoder(res.Body).Decode(&general); err != nil {
		return false, "", fmt.Errorf("controller GET /configs: %w", err)
	}
	addrs, err := parseProxyBindAddresses(item.ProxyBind)
	if err != nil {
		return false, "", err
	}
	if len(addrs) == 1 {
		return general.MixedPort == item.MixedPort, fmt.Sprintf("controller reports mixed-port=%d, want %d", general.MixedPort, item.MixedPort), nil
	}
	return general.AllowLan, fmt.Sprintf("controller reports allow-lan=%t, want true", general.AllowLan), nil
}

// waitPortMappingsReady first waits for mihomo itself to report that it has
// patched its listeners (applied, see mihomoListenersApplied), and only then
// dials every mapped endpoint -- so an early connection to some other process
// that briefly held a port, before mihomo even tried to bind it, never counts.
// After the last endpoint answers it waits one more tick, counted from then,
// and confirms mihomo logged no bind failure for a mapping listener
// (bindFailure, see isPortMappingBindError): the error is logged before the
// applied signal flips, and the tick lets it reach the log reader.
//
// Endpoints are dialed per requested address family: `0.0.0.0,::` collapses
// to one listener, which on a host whose IPv6 sockets do not map IPv4 serves
// only one family -- dialing both loopbacks catches that.
//
// It gives up when the process exits (exited closes), ctx ends, or
// portMappingReadyTimeout passes -- one deadline for the whole wait, every
// dial included. Errors name each endpoint still down and the last error seen.
//
// Residual gap: with `log-level: silent` mihomo logs nothing, so a mapping it
// failed to bind (port taken, or only its UDP half) passes once another
// process accepts on that port.
func waitPortMappingsReady(ctx context.Context, bind string, mappings []PortMapping, exited <-chan struct{}, bindFailure func() string, applied func(context.Context) (bool, string, error)) error {
	addrs, err := parseProxyBindAddresses(bind)
	if err != nil {
		return err
	}
	pending := make(map[string]string) // address -> "port -> proxy"
	for _, m := range mappings {
		for _, addr := range addrs {
			pending[portMappingReadyAddress(addr, m.Port)] = fmt.Sprintf("%d -> %q", m.Port, m.Proxy)
		}
	}
	lastErr := make(map[string]error, len(pending))
	appliedState := "mihomo has not reported its listeners applied yet"
	confirmed := false
	var settleAt time.Time
	waitCtx, cancel := context.WithTimeout(ctx, portMappingReadyTimeout)
	defer cancel()
	describe := func() string {
		if !confirmed {
			return appliedState + "; mappings " + describePortMappings(mappings)
		}
		return describePendingMappings(pending, lastErr)
	}
	stopped := func() error {
		select {
		case <-exited:
			return fmt.Errorf("mihomo exited before port mappings were ready: %s", describe())
		default:
		}
		if line := bindFailure(); line != "" {
			return fmt.Errorf("mihomo could not bind a port mapping: %s", line)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("port mapping check interrupted: %w; not yet listening: %s", ctx.Err(), describe())
		}
		if waitCtx.Err() != nil {
			return fmt.Errorf("port mappings not listening after %s: %s", portMappingReadyTimeout, describe())
		}
		return nil
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := stopped(); err != nil {
			return err
		}
		if !confirmed {
			ok, observed, err := applied(waitCtx)
			switch {
			case err != nil:
				appliedState = "mihomo listener state unknown: " + err.Error()
			case ok:
				confirmed = true
			default:
				appliedState = "mihomo has not finished applying its listeners (" + observed + ")"
			}
		}
		if confirmed {
			for address := range pending {
				if err := stopped(); err != nil {
					return err
				}
				dialCtx, cancelDial := context.WithTimeout(waitCtx, time.Second)
				err := portMappingDial(dialCtx, address)
				cancelDial()
				if err == nil {
					delete(pending, address)
					continue
				}
				lastErr[address] = err
			}
			if len(pending) == 0 {
				if settleAt.IsZero() {
					settleAt = time.Now().Add(100 * time.Millisecond)
				} else if !time.Now().Before(settleAt) {
					if err := stopped(); err != nil {
						return err
					}
					return nil
				}
			}
		}
		select {
		case <-waitCtx.Done():
		case <-exited:
		case <-tick.C:
		}
	}
}

// isPortMappingBindError reports whether a mihomo log line says a fleet
// mapping listener failed to bind: listener.go PatchInboundListeners logs
// "Listener <name> listen err: <err>" and keeps running.
func isPortMappingBindError(line string) bool {
	return strings.Contains(line, "Listener fleet-map-") && strings.Contains(line, "listen err")
}

func describePendingMappings(pending map[string]string, lastErr map[string]error) string {
	parts := make([]string, 0, len(pending))
	for address, label := range pending {
		part := fmt.Sprintf("%s on %s", label, address)
		if err := lastErr[address]; err != nil {
			part += fmt.Sprintf(" (%v)", err)
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// portMappingRuntimeNotes reads the runtime config just written and returns
// log lines a user would otherwise have to dig out of it: the hops a mapping
// actually takes when its target carries dialer-proxy (global-chain mode makes
// chain members do), and a warning when log-level silent blinds the readiness
// check's bind-error scan. Best effort: an unreadable file yields nothing.
func portMappingRuntimeNotes(runtimeConfigPath string, mappings []PortMapping) []string {
	raw, err := os.ReadFile(runtimeConfigPath)
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if yaml.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	var notes []string
	if level, _ := cfg["log-level"].(string); strings.EqualFold(level, "silent") {
		notes = append(notes, "warning: log-level is silent, so a port mapping that mihomo fails to bind (or binds without its UDP half) cannot be detected at start")
	}
	dialers := make(map[string]string)
	items, _ := cfg["proxies"].([]any)
	for _, item := range items {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		if dialer, _ := entry["dialer-proxy"].(string); name != "" && dialer != "" {
			dialers[name] = dialer
		}
	}
	for _, m := range mappings {
		var hops []string
		seen := map[string]bool{m.Proxy: true}
		for next := dialers[m.Proxy]; next != "" && !seen[next]; next = dialers[next] {
			seen[next] = true
			hops = append([]string{next}, hops...)
		}
		if len(hops) > 0 {
			notes = append(notes, fmt.Sprintf("port mapping %d -> %s goes through %s", m.Port, m.Proxy, strings.Join(append(hops, m.Proxy), " > ")))
		}
	}
	return notes
}

func describePortMappingsOrNone(mappings []PortMapping) string {
	if len(mappings) == 0 {
		return "none"
	}
	return describePortMappings(mappings)
}

func describePortMappings(mappings []PortMapping) string {
	parts := make([]string, 0, len(mappings))
	for _, m := range mappings {
		parts = append(parts, fmt.Sprintf("%d -> %s", m.Port, m.Proxy))
	}
	return strings.Join(parts, ", ")
}
