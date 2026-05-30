package app

import "time"

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
}

type Instance struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	ProfileID         string    `json:"profileId"`
	MixedPort         int       `json:"mixedPort"`
	ControllerPort    int       `json:"controllerPort"`
	Secret            string    `json:"secret"`
	UserConfigPath    string    `json:"userConfigPath"`
	RuntimeConfigPath string    `json:"runtimeConfigPath"`
	SelectedGroup     string    `json:"selectedGroup,omitempty"`
	SelectedProxy     string    `json:"selectedProxy,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	LastError         string    `json:"lastError,omitempty"`
}

type Profile struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ConfigPath string    `json:"configPath"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type InstanceView struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	ProfileID         string    `json:"profileId"`
	ProfileName       string    `json:"profileName,omitempty"`
	ProfileConfigPath string    `json:"profileConfigPath,omitempty"`
	MixedPort         int       `json:"mixedPort"`
	ControllerPort    int       `json:"controllerPort"`
	UserConfigPath    string    `json:"userConfigPath"`
	RuntimeConfigPath string    `json:"runtimeConfigPath"`
	SelectedGroup     string    `json:"selectedGroup,omitempty"`
	SelectedProxy     string    `json:"selectedProxy,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	LastError         string    `json:"lastError,omitempty"`
	Status            string    `json:"status"`
	PID               int       `json:"pid,omitempty"`
}

type storedData struct {
	Instances []*Instance `json:"instances"`
	Profiles  []*Profile  `json:"profiles"`
}

type SystemStatus struct {
	Bind         string `json:"bind"`
	Port         int    `json:"port"`
	DataDir      string `json:"dataDir"`
	MihomoPath   string `json:"mihomoPath"`
	MihomoFound  bool   `json:"mihomoFound"`
	MihomoSource string `json:"mihomoSource"`
	Version      string `json:"version,omitempty"`
}
