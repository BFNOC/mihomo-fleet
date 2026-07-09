package app

import "time"

const (
	InstanceModeRule        = "rule"
	InstanceModeGlobalChain = "global-chain"
)

const defaultUserConfig = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`

type Options struct {
	Bind       string
	Port       int
	DataDir    string
	MihomoPath string
	AppVersion string
}

type Instance struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ProfileID         string            `json:"profileId"`
	MixedPort         int               `json:"mixedPort"`
	ProxyBind         string            `json:"proxyBind,omitempty"`
	ControllerPort    int               `json:"controllerPort"`
	Secret            string            `json:"secret"`
	UserConfigPath    string            `json:"userConfigPath"`
	RuntimeConfigPath string            `json:"runtimeConfigPath"`
	Mode              string            `json:"mode,omitempty"`
	LocalProxies      string            `json:"localProxies,omitempty"`
	Chain             []string          `json:"chain,omitempty"`
	SelectedProxies   map[string]string `json:"selectedProxies,omitempty"`
	SelectedGroup     string            `json:"selectedGroup,omitempty"`
	SelectedProxy     string            `json:"selectedProxy,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	// ConfigUpdatedAt is the last time a mutation that actually affects the
	// generated runtime config (Mode, Chain, LocalProxies, Config content,
	// ports, ProxyBind, ProfileID) touched this instance -- unlike UpdatedAt,
	// which every mutation bumps, including SetSelection and SetError (N2,
	// docs/review-2026-07-11-fix-verification-round4.md). decorateStatus
	// (manager.go) compares this against the running process's start time to
	// derive PendingRestart; UpdatedAt was doing that job before and produced
	// false positives on every selection change. Old stores predating this
	// field load it as the zero value, which is never After() a start time,
	// so pre-existing instances simply report no pending restart until their
	// first config-affecting edit -- an acceptable, self-healing gap.
	ConfigUpdatedAt time.Time `json:"configUpdatedAt,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
}

type Profile struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	ConfigPath            string            `json:"configPath"`
	SubscriptionURL       string            `json:"subscriptionUrl,omitempty"`
	AutoUpdate            bool              `json:"autoUpdate"`
	UpdateIntervalMinutes int               `json:"updateIntervalMinutes,omitempty"`
	LastUpdatedAt         time.Time         `json:"lastUpdatedAt,omitempty"`
	LastUpdateError       string            `json:"lastUpdateError,omitempty"`
	HomeURL               string            `json:"homeUrl,omitempty"`
	SubscriptionInfo      *SubscriptionInfo `json:"subscriptionInfo,omitempty"`
	CreatedAt             time.Time         `json:"createdAt"`
	UpdatedAt             time.Time         `json:"updatedAt"`
}

type InstanceView struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ProfileID         string            `json:"profileId"`
	ProfileName       string            `json:"profileName,omitempty"`
	ProfileConfigPath string            `json:"profileConfigPath,omitempty"`
	MixedPort         int               `json:"mixedPort"`
	ProxyBind         string            `json:"proxyBind"`
	ControllerPort    int               `json:"controllerPort"`
	UserConfigPath    string            `json:"userConfigPath"`
	RuntimeConfigPath string            `json:"runtimeConfigPath"`
	Mode              string            `json:"mode"`
	LocalProxies      string            `json:"localProxies,omitempty"`
	Chain             []string          `json:"chain,omitempty"`
	SelectedProxies   map[string]string `json:"selectedProxies,omitempty"`
	SelectedGroup     string            `json:"selectedGroup,omitempty"`
	SelectedProxy     string            `json:"selectedProxy,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	LastError         string            `json:"lastError,omitempty"`
	Status            string            `json:"status"`
	PID               int               `json:"pid,omitempty"`
	// PendingRestart is true when this (running) instance's stored fields
	// have been updated more recently than the runtime config the live
	// process was actually launched from, i.e. a saved change (Mode/Chain/
	// LocalProxies/Config) has not taken effect yet and won't until the
	// instance is restarted (arch M5,
	// docs/review-2026-07-11-go-architecture.md). Always false/omitted for
	// stopped or starting instances.
	PendingRestart bool `json:"pendingRestart,omitempty"`
}

type storedData struct {
	Instances []*Instance `json:"instances"`
	Profiles  []*Profile  `json:"profiles"`
}

type SubscriptionInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

type ProfileProxyGroup struct {
	Name string   `json:"name"`
	Type string   `json:"type,omitempty"`
	All  []string `json:"all"`
	Now  string   `json:"now,omitempty"`
}

type SystemStatus struct {
	Bind         string `json:"bind"`
	Port         int    `json:"port"`
	DataDir      string `json:"dataDir"`
	AppVersion   string `json:"appVersion"`
	MihomoPath   string `json:"mihomoPath"`
	MihomoFound  bool   `json:"mihomoFound"`
	MihomoSource string `json:"mihomoSource"`
	Version      string `json:"version,omitempty"`
}
