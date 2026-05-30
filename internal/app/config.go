package app

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
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
	for _, key := range []string{
		"port",
		"socks-port",
		"redir-port",
		"tproxy-port",
		"external-controller-tls",
		"external-controller-cors-allow-origins",
	} {
		delete(cfg, key)
	}
	cfg["mixed-port"] = item.MixedPort
	cfg["external-controller"] = fmt.Sprintf("127.0.0.1:%d", item.ControllerPort)
	cfg["secret"] = item.Secret
	cfg["allow-lan"] = false
	cfg["bind-address"] = "127.0.0.1"

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(item.RuntimeConfigPath, out, 0o600)
}
