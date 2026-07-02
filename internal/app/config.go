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
	applyRuntimeFields(cfg, item)

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

func applyRuntimeFields(cfg map[string]any, item *Instance) {
	cfg["mixed-port"] = item.MixedPort
	cfg["external-controller"] = fmt.Sprintf("127.0.0.1:%d", item.ControllerPort)
	cfg["secret"] = item.Secret
	cfg["allow-lan"] = false
	cfg["bind-address"] = "127.0.0.1"
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

	known := make(map[string]bool)
	for _, name := range inlineNames {
		if name == globalChainSelectGroupName || name == globalChainRelayGroupName {
			return fmt.Errorf("proxy name %q conflicts with generated global-chain group", name)
		}
		known[name] = true
	}
	for _, name := range localNames {
		if known[name] {
			return fmt.Errorf("local proxy name %q conflicts with profile proxy", name)
		}
		known[name] = true
	}

	for _, proxy := range localProxies {
		proxies = append(proxies, proxy)
	}
	if len(proxies) > 0 {
		cfg["proxies"] = proxies
	} else {
		delete(cfg, "proxies")
	}

	selectNames := append(append([]string{}, inlineNames...), localNames...)
	selectGroup := map[string]any{
		"name": globalChainSelectGroupName,
		"type": "select",
	}
	if len(selectNames) > 0 {
		selectGroup["proxies"] = selectNames
	}
	if len(providerNames) > 0 {
		selectGroup["use"] = providerNames
	}

	known[globalChainSelectGroupName] = true
	chain := normalizeChainNames(item.Chain)
	if len(chain) == 0 {
		chain = append(append([]string{}, localNames...), globalChainSelectGroupName)
	}
	for _, name := range chain {
		if name == globalChainRelayGroupName {
			return fmt.Errorf("chain cannot reference generated relay group %q", name)
		}
		if !known[name] {
			return fmt.Errorf("chain references unknown proxy or group %q", name)
		}
	}

	relayGroup := map[string]any{
		"name":    globalChainRelayGroupName,
		"type":    "relay",
		"proxies": chain,
	}
	cfg["mode"] = "rule"
	cfg["proxy-groups"] = []any{selectGroup, relayGroup}
	cfg["rules"] = []string{fmt.Sprintf("MATCH,%s", globalChainRelayGroupName)}
	for _, key := range []string{"rule-providers", "sub-rules", "script"} {
		delete(cfg, key)
	}
	return nil
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
	selectNames := dedupeStrings(append(append([]string{}, inlineNames...), localNames...))
	selections := normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
	groups := []ProfileProxyGroup{{
		Name: globalChainSelectGroupName,
		Type: "select",
		All:  selectNames,
		Now:  selectionForGroup(globalChainSelectGroupName, selectNames, selections),
	}}
	if len(providerNames) > 0 && len(selectNames) == 0 {
		groups[0].Now = ""
	}
	chain := normalizeChainNames(item.Chain)
	if len(chain) == 0 {
		chain = append(append([]string{}, localNames...), globalChainSelectGroupName)
	}
	groups = append(groups, ProfileProxyGroup{
		Name: globalChainRelayGroupName,
		Type: "relay",
		All:  chain,
	})
	return groups, nil
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
