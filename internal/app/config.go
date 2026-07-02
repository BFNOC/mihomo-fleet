package app

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	globalChainSelectGroupName = "节点选择"
	globalChainRelayGroupName  = "代理链"
)

func writeRuntimeConfig(item *Instance, profile *Profile) error {
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if len(raw) == 0 {
		cfg = make(map[string]any)
	} else if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse user config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	cleanRuntimeConfig(cfg)
	if instanceMode(item.Mode) == InstanceModeGlobalChain {
		if err := applyGlobalChainConfig(cfg, item); err != nil {
			return err
		}
	}
	if err := applyRuntimeFields(cfg, item); err != nil {
		return err
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(item.RuntimeConfigPath, out, 0o600)
}

func cleanRuntimeConfig(cfg map[string]any) {
	for _, key := range []string{
		"port",
		"socks-port",
		"redir-port",
		"tproxy-port",
		"external-controller-tls",
		"external-controller-cors-allow-origins",
		"external-ui",
		"external-ui-name",
		"external-ui-url",
		"listeners",
		"tun",
	} {
		delete(cfg, key)
	}
}

func applyRuntimeFields(cfg map[string]any, item *Instance) error {
	cfg["external-controller"] = fmt.Sprintf("127.0.0.1:%d", item.ControllerPort)
	cfg["secret"] = item.Secret
	return applyProxyBindFields(cfg, item)
}

func applyProxyBindFields(cfg map[string]any, item *Instance) error {
	addrs, err := parseProxyBindAddresses(item.ProxyBind)
	if err != nil {
		return err
	}
	if len(addrs) == 1 {
		cfg["mixed-port"] = item.MixedPort
		delete(cfg, "listeners")
		addr := addrs[0]
		if addr == defaultProxyBind {
			cfg["allow-lan"] = false
			cfg["bind-address"] = defaultProxyBind
			return nil
		}
		cfg["allow-lan"] = true
		cfg["bind-address"] = mihomoBindAddress(addr)
		return nil
	}

	delete(cfg, "mixed-port")
	delete(cfg, "bind-address")
	cfg["allow-lan"] = true
	listeners := make([]any, 0, len(addrs))
	for index, addr := range addrs {
		listeners = append(listeners, map[string]any{
			"name":   fmt.Sprintf("fleet-mixed-%d", index+1),
			"type":   "mixed",
			"port":   item.MixedPort,
			"listen": addr,
			"udp":    true,
		})
	}
	cfg["listeners"] = listeners
	return nil
}

func applyGlobalChainConfig(cfg map[string]any, item *Instance) error {
	proxies, inlineNames, err := proxyItemsAndNames(cfg["proxies"])
	if err != nil {
		return err
	}
	localProxies, localNames, err := parseLocalProxyItems(item.LocalProxies)
	if err != nil {
		return err
	}
	providerNames := proxyProviderNames(cfg["proxy-providers"])
	if len(inlineNames) == 0 && len(localNames) == 0 && len(providerNames) == 0 {
		return fmt.Errorf("global-chain mode requires proxies, proxy-providers, or local proxies")
	}

	for _, name := range inlineNames {
		if name == globalChainSelectGroupName || name == globalChainRelayGroupName {
			return fmt.Errorf("proxy name %q conflicts with generated global-chain group", name)
		}
	}
	inlineKnown := stringSet(inlineNames)
	for _, name := range localNames {
		if inlineKnown[name] {
			return fmt.Errorf("local proxy name %q conflicts with profile proxy", name)
		}
	}
	plan, err := buildGlobalChainPlan(item, inlineNames, localNames, providerNames)
	if err != nil {
		return err
	}

	for _, proxy := range localProxies {
		proxies = append(proxies, proxy)
	}
	applyDialerProxyChain(proxies, plan)
	if len(proxies) > 0 {
		cfg["proxies"] = proxies
	} else {
		delete(cfg, "proxies")
	}
	if plan.selectDialer != "" && len(providerNames) > 0 {
		applyProviderDialerProxy(cfg["proxy-providers"], plan.selectDialer)
	}

	selectGroup := map[string]any{
		"name": globalChainSelectGroupName,
		"type": "select",
	}
	if len(plan.selectNames) > 0 {
		selectGroup["proxies"] = plan.selectNames
	}
	if len(providerNames) > 0 {
		selectGroup["use"] = providerNames
	}

	cfg["mode"] = "rule"
	cfg["proxy-groups"] = []any{selectGroup}
	cfg["rules"] = []string{fmt.Sprintf("MATCH,%s", plan.matchTarget)}
	for _, key := range []string{"rule-providers", "sub-rules", "script"} {
		delete(cfg, key)
	}
	return nil
}

type globalChainPlan struct {
	chain        []string
	matchTarget  string
	proxyDialers map[string]string
	selectDialer string
	selectNames  []string
	usesSelect   bool
}

func buildGlobalChainPlan(item *Instance, inlineNames, localNames, providerNames []string) (globalChainPlan, error) {
	chain := normalizeChainNames(item.Chain)
	if len(chain) == 0 {
		chain = []string{globalChainSelectGroupName}
		if len(localNames) > 0 && (len(inlineNames) > 0 || len(providerNames) > 0) {
			chain = append(append([]string{}, localNames...), globalChainSelectGroupName)
		}
	}
	for _, name := range chain {
		if name == globalChainRelayGroupName {
			return globalChainPlan{}, fmt.Errorf("chain cannot reference generated relay group %q", name)
		}
	}

	proxyNames := stringSet(append(append([]string{}, inlineNames...), localNames...))
	seen := make(map[string]bool, len(chain))
	proxyDialers := make(map[string]string, len(chain))
	chainProxies := make(map[string]bool, len(chain))
	usesSelect := false
	for index, name := range chain {
		if seen[name] {
			return globalChainPlan{}, fmt.Errorf("chain contains duplicate member %q", name)
		}
		seen[name] = true
		if name == globalChainSelectGroupName {
			usesSelect = true
			continue
		}
		if !proxyNames[name] {
			return globalChainPlan{}, fmt.Errorf("chain references unknown proxy or group %q", name)
		}
		chainProxies[name] = true
		if index == 0 {
			proxyDialers[name] = ""
		}
	}

	selectDialer := ""
	for index := 1; index < len(chain); index++ {
		dialer := chain[index-1]
		name := chain[index]
		if name == globalChainSelectGroupName {
			selectDialer = dialer
			continue
		}
		proxyDialers[name] = dialer
	}

	selectNames := make([]string, 0, len(inlineNames)+len(localNames))
	for _, name := range append(append([]string{}, inlineNames...), localNames...) {
		if !usesSelect || !chainProxies[name] {
			selectNames = append(selectNames, name)
		}
	}
	selectNames = dedupeStrings(selectNames)
	if usesSelect && len(selectNames) == 0 && len(providerNames) == 0 {
		return globalChainPlan{}, fmt.Errorf("global-chain mode has no selectable proxy after chain members")
	}
	return globalChainPlan{
		chain:        chain,
		matchTarget:  chain[len(chain)-1],
		proxyDialers: proxyDialers,
		selectDialer: selectDialer,
		selectNames:  selectNames,
		usesSelect:   usesSelect,
	}, nil
}

func applyDialerProxyChain(proxies []any, plan globalChainPlan) {
	if len(plan.proxyDialers) == 0 && plan.selectDialer == "" {
		return
	}
	selectNames := stringSet(plan.selectNames)
	for _, item := range proxies {
		proxy, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := proxyName(item)
		if dialer, ok := plan.proxyDialers[name]; ok {
			if dialer == "" {
				delete(proxy, "dialer-proxy")
			} else {
				proxy["dialer-proxy"] = dialer
			}
			continue
		}
		if plan.selectDialer != "" && selectNames[name] {
			proxy["dialer-proxy"] = plan.selectDialer
		}
	}
}

func applyProviderDialerProxy(value any, dialer string) {
	providers, ok := value.(map[string]any)
	if !ok {
		return
	}
	for _, value := range providers {
		provider, ok := value.(map[string]any)
		if !ok {
			continue
		}
		override, _ := provider["override"].(map[string]any)
		if override == nil {
			override = make(map[string]any)
		}
		override["dialer-proxy"] = dialer
		provider["override"] = override
	}
}

func profileProxyGroupsForInstance(config string, item *Instance) ([]ProfileProxyGroup, error) {
	if item == nil || instanceMode(item.Mode) != InstanceModeGlobalChain {
		var selections map[string]string
		if item != nil {
			selections = normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
		}
		return parseProfileProxyGroups(config, selections)
	}
	return parseGlobalChainProxyGroups(config, item)
}

func parseGlobalChainProxyGroups(config string, item *Instance) ([]ProfileProxyGroup, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, fmt.Errorf("parse profile config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	_, inlineNames, err := proxyItemsAndNames(cfg["proxies"])
	if err != nil {
		return nil, err
	}
	_, localNames, err := parseLocalProxyItems(item.LocalProxies)
	if err != nil {
		return nil, err
	}
	providerNames := proxyProviderNames(cfg["proxy-providers"])
	plan, err := buildGlobalChainPlan(item, inlineNames, localNames, providerNames)
	if err != nil {
		return nil, err
	}
	selections := normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
	group := ProfileProxyGroup{
		Name: globalChainSelectGroupName,
		Type: "select",
		All:  plan.selectNames,
		Now:  selectionForGroup(globalChainSelectGroupName, plan.selectNames, selections),
	}
	if len(providerNames) > 0 && len(plan.selectNames) == 0 {
		group.Now = ""
	}
	return []ProfileProxyGroup{group}, nil
}

func proxyItemsAndNames(value any) ([]any, []string, error) {
	if value == nil {
		return nil, nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("proxies must be a list")
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := proxyName(item)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return append([]any{}, items...), dedupeStrings(names), nil
}

func parseLocalProxyItems(raw string) ([]any, []string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, nil
	}
	var parsed []map[string]any
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, nil, fmt.Errorf("parse local proxies: %w", err)
	}
	if len(parsed) == 0 {
		return nil, nil, nil
	}
	items := make([]any, 0, len(parsed))
	names := make([]string, 0, len(parsed))
	seen := make(map[string]bool, len(parsed))
	for index, item := range parsed {
		nameValue, ok := item["name"].(string)
		name := strings.TrimSpace(nameValue)
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("local proxy %d is missing name", index+1)
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("local proxy name %q is duplicated", name)
		}
		if name == globalChainSelectGroupName || name == globalChainRelayGroupName {
			return nil, nil, fmt.Errorf("local proxy name %q conflicts with generated global-chain group", name)
		}
		item["name"] = name
		seen[name] = true
		items = append(items, item)
		names = append(names, name)
	}
	return items, names, nil
}

func proxyName(item any) string {
	proxy, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := proxy["name"].(string)
	return strings.TrimSpace(name)
}

func proxyProviderNames(value any) []string {
	providers, ok := value.(map[string]any)
	if !ok || len(providers) == 0 {
		return nil
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		if strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	sort.Strings(names)
	return names
}

func normalizeChainNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out[value] = true
		}
	}
	return out
}

func instanceMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "", InstanceModeRule:
		return InstanceModeRule
	case InstanceModeGlobalChain:
		return InstanceModeGlobalChain
	default:
		return InstanceModeRule
	}
}

func normalizeInstanceMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", InstanceModeRule:
		return InstanceModeRule, nil
	case InstanceModeGlobalChain:
		return InstanceModeGlobalChain, nil
	default:
		return "", fmt.Errorf("instance mode %q is invalid", mode)
	}
}
