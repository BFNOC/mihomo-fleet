package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const portSuggestMaxStart = 60535

var errInstanceNotFound = errors.New("instance not found")

type instanceNotFoundError struct {
	id string
}

func (err instanceNotFoundError) Error() string {
	return fmt.Sprintf("instance %q not found", err.id)
}

func (err instanceNotFoundError) Is(target error) bool {
	return target == errInstanceNotFound
}

type Store struct {
	mu        sync.RWMutex
	dataDir   string
	storePath string
	items     map[string]*Instance
	profiles  map[string]*Profile
}

type ProfilePatch struct {
	Name                  string
	Config                *string
	SubscriptionURL       *string
	AutoUpdate            *bool
	UpdateIntervalMinutes *int
}

type createInstanceOptions struct {
	Name            string
	ProfileID       string
	Config          string
	MixedPort       int
	ControllerPort  int
	MixedStart      int
	ControllerStart int
	Mode            string
	LocalProxies    string
	Chain           []string
	SelectedProxies map[string]string
	SelectedGroup   string
	SelectedProxy   string
}

type updateInstanceOptions struct {
	Name           string
	ProfileID      string
	Config         string
	MixedPort      int
	ControllerPort int
	Mode           string
	LocalProxies   *string
	Chain          *[]string
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "instances"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "profiles"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dataDir, 0o700)
	_ = os.Chmod(filepath.Join(dataDir, "instances"), 0o700)
	_ = os.Chmod(filepath.Join(dataDir, "profiles"), 0o700)
	s := &Store{
		dataDir:   dataDir,
		storePath: filepath.Join(dataDir, "instances.json"),
		items:     make(map[string]*Instance),
		profiles:  make(map[string]*Profile),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.storePath)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveLocked()
	}
	if err != nil {
		return err
	}
	var data storedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	for _, item := range data.Instances {
		copy := *item
		copy.Mode = instanceMode(copy.Mode)
		copy.Chain = normalizeChainNames(copy.Chain)
		copy.SelectedProxies = normalizeSelections(copy.SelectedProxies, copy.SelectedGroup, copy.SelectedProxy)
		s.items[item.ID] = &copy
	}
	for _, profile := range data.Profiles {
		copy := *profile
		s.profiles[profile.ID] = &copy
	}
	if len(s.profiles) == 0 && len(s.items) > 0 {
		for _, item := range s.items {
			if item.UserConfigPath == "" {
				continue
			}
			id := uniqueProfileID(item.Name+" profile", s.profiles)
			profile := &Profile{
				ID:         id,
				Name:       item.Name + " profile",
				ConfigPath: item.UserConfigPath,
				CreatedAt:  item.CreatedAt,
				UpdatedAt:  item.UpdatedAt,
			}
			item.ProfileID = id
			s.profiles[id] = profile
		}
		return s.saveLocked()
	}
	return nil
}

func (s *Store) ListProfiles() []*Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Profile, 0, len(s.profiles))
	for _, item := range s.profiles {
		out = append(out, cloneProfile(item))
	}
	return out
}

func (s *Store) GetProfile(id string) (*Profile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.profiles[id]
	if !ok {
		return nil, false
	}
	return cloneProfile(item), true
}

func (s *Store) List() []*Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Instance, 0, len(s.items))
	for _, item := range s.items {
		copy := *item
		copy.Chain = append([]string{}, item.Chain...)
		copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
		out = append(out, &copy)
	}
	return out
}

func (s *Store) Get(id string) (*Instance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	if !ok {
		return nil, false
	}
	copy := *item
	copy.Chain = append([]string{}, item.Chain...)
	copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
	return &copy, true
}

func (s *Store) CreateProfile(name, config string) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createProfileLocked(name, config)
}

func (s *Store) CreateSubscriptionProfile(name, subscriptionURL string, autoUpdate bool, intervalMinutes int, fetched *subscriptionFetchResult) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fetched == nil {
		return nil, errors.New("fetched subscription is required")
	}
	if name == "" {
		name = fetched.Name
	}
	if name == "" {
		name = "Remote subscription"
	}
	if intervalMinutes <= 0 {
		intervalMinutes = fetched.UpdateIntervalMinutes
	}
	intervalMinutes = normalizeSubscriptionInterval(intervalMinutes, autoUpdate)

	profile, err := s.createProfileRecordLocked(name, fetched.Config)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	profile.SubscriptionURL = subscriptionURL
	profile.AutoUpdate = autoUpdate
	profile.UpdateIntervalMinutes = intervalMinutes
	profile.LastUpdatedAt = now
	profile.LastUpdateError = ""
	profile.HomeURL = fetched.HomeURL
	profile.SubscriptionInfo = fetched.Info
	profile.UpdatedAt = now
	if err := s.saveLocked(); err != nil {
		delete(s.profiles, profile.ID)
		_ = os.RemoveAll(filepath.Dir(profile.ConfigPath))
		return nil, err
	}
	return cloneProfile(profile), nil
}

func (s *Store) UpdateProfile(id, name, config string) (*Profile, error) {
	var cfg *string
	if config != "" {
		cfg = &config
	}
	return s.PatchProfile(id, ProfilePatch{Name: name, Config: cfg})
}

func (s *Store) PatchProfile(id string, patch ProfilePatch) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, ok := s.profiles[id]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	original := cloneProfile(profile)
	originalURL := profile.SubscriptionURL
	var previousConfig []byte
	if patch.Config != nil {
		var err error
		previousConfig, err = os.ReadFile(profile.ConfigPath)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(profile.ConfigPath, []byte(*patch.Config), 0o600); err != nil {
			return nil, err
		}
	}
	if patch.Name != "" {
		profile.Name = patch.Name
	}
	if patch.SubscriptionURL != nil {
		profile.SubscriptionURL = strings.TrimSpace(*patch.SubscriptionURL)
	}
	if patch.SubscriptionURL != nil && profile.SubscriptionURL != originalURL {
		profile.LastUpdatedAt = time.Time{}
		profile.LastUpdateError = ""
		profile.HomeURL = ""
		profile.SubscriptionInfo = nil
		if profile.SubscriptionURL == "" {
			profile.AutoUpdate = false
			profile.UpdateIntervalMinutes = 0
		}
	}
	if patch.AutoUpdate != nil {
		profile.AutoUpdate = *patch.AutoUpdate
	}
	if patch.UpdateIntervalMinutes != nil {
		profile.UpdateIntervalMinutes = *patch.UpdateIntervalMinutes
	}
	if profile.SubscriptionURL != "" {
		profile.UpdateIntervalMinutes = normalizeSubscriptionInterval(profile.UpdateIntervalMinutes, profile.AutoUpdate)
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		*profile = *original
		if patch.Config != nil {
			_ = writeFileAtomic(profile.ConfigPath, previousConfig, 0o600)
		}
		return nil, err
	}
	return cloneProfile(profile), nil
}

func (s *Store) ApplySubscriptionFetch(id string, fetched *subscriptionFetchResult) (*Profile, error) {
	return s.ApplySubscriptionFetchForURL(id, "", fetched)
}

func (s *Store) ApplySubscriptionFetchForURL(id, expectedURL string, fetched *subscriptionFetchResult) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, ok := s.profiles[id]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	if profile.SubscriptionURL == "" {
		return nil, fmt.Errorf("profile %q is not a subscription profile", id)
	}
	if expectedURL != "" && profile.SubscriptionURL != expectedURL {
		return nil, fmt.Errorf("profile %q subscription URL changed during update", id)
	}
	if fetched == nil {
		return nil, errors.New("fetched subscription is required")
	}
	originalProfile := cloneProfile(profile)
	previousConfig, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return nil, err
	}
	itemSnapshots := make(map[string]Instance)
	for _, item := range s.items {
		if item.ProfileID == id {
			copy := *item
			copy.Chain = append([]string{}, item.Chain...)
			copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
			itemSnapshots[item.ID] = copy
		}
	}
	if err := writeFileAtomic(profile.ConfigPath, []byte(fetched.Config), 0o600); err != nil {
		return nil, err
	}
	groups, _ := parseProfileProxyGroups(fetched.Config, nil)
	validSelections := profileSelectionSet(groups)
	now := time.Now().UTC()
	if fetched.UpdateIntervalMinutes > 0 && profile.UpdateIntervalMinutes <= 0 {
		profile.UpdateIntervalMinutes = normalizeSubscriptionInterval(fetched.UpdateIntervalMinutes, profile.AutoUpdate)
	}
	profile.LastUpdatedAt = now
	profile.LastUpdateError = ""
	profile.HomeURL = fetched.HomeURL
	profile.SubscriptionInfo = fetched.Info
	profile.UpdatedAt = now
	for _, item := range s.items {
		if item.ProfileID == id && instanceMode(item.Mode) != InstanceModeGlobalChain {
			reconcileInstanceSelection(item, validSelections)
		}
	}
	if err := s.saveLocked(); err != nil {
		*profile = *originalProfile
		for itemID, snapshot := range itemSnapshots {
			if item, ok := s.items[itemID]; ok {
				restored := snapshot
				restored.SelectedProxies = cloneStringMap(snapshot.SelectedProxies)
				*item = restored
			}
		}
		_ = writeFileAtomic(profile.ConfigPath, previousConfig, 0o600)
		return nil, err
	}
	return cloneProfile(profile), nil
}

func (s *Store) SetProfileUpdateError(id, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if profile, ok := s.profiles[id]; ok {
		profile.LastUpdateError = message
		profile.UpdatedAt = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			log.Printf("save subscription update error failed for profile %s: %v", id, err)
		}
	}
}

func (s *Store) ReadProfileConfig(id string) (string, error) {
	profile, ok := s.GetProfile(id)
	if !ok {
		return "", fmt.Errorf("profile %q not found", id)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Store) Create(name, profileID, config string, mixedPort, controllerPort int) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.createInstanceLocked(createInstanceOptions{
		Name:            name,
		ProfileID:       profileID,
		Config:          config,
		MixedPort:       mixedPort,
		ControllerPort:  controllerPort,
		MixedStart:      28000,
		ControllerStart: 29000,
	})
}

func (s *Store) CreateWithOptions(opts createInstanceOptions) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if opts.MixedStart == 0 {
		opts.MixedStart = 28000
	}
	if opts.ControllerStart == 0 {
		opts.ControllerStart = 29000
	}
	return s.createInstanceLocked(opts)
}

func (s *Store) Clone(id, name string, mixedPort, controllerPort int) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	source, ok := s.items[id]
	if !ok {
		return nil, instanceNotFoundError{id: id}
	}
	if strings.TrimSpace(name) == "" {
		name = source.Name + " copy"
	}
	// 克隆复用配置档和已保存节点选择，但必须生成新的端口、secret 与运行配置。
	return s.createInstanceLocked(createInstanceOptions{
		Name:            name,
		ProfileID:       source.ProfileID,
		MixedPort:       mixedPort,
		ControllerPort:  controllerPort,
		MixedStart:      nextClonePortStart(source.MixedPort, 28000),
		ControllerStart: nextClonePortStart(source.ControllerPort, 29000),
		SelectedProxies: cloneStringMap(source.SelectedProxies),
		SelectedGroup:   source.SelectedGroup,
		SelectedProxy:   source.SelectedProxy,
		Mode:            source.Mode,
		LocalProxies:    source.LocalProxies,
		Chain:           append([]string{}, source.Chain...),
	})
}

func (s *Store) createInstanceLocked(opts createInstanceOptions) (*Instance, error) {
	now := time.Now().UTC()
	id := uniqueID(opts.Name, s.items)
	mode, err := normalizeInstanceMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	if mode == InstanceModeGlobalChain {
		if _, _, err := parseLocalProxyItems(opts.LocalProxies); err != nil {
			return nil, err
		}
	}
	used := s.usedPortsLocked("")
	if opts.MixedStart == 0 {
		opts.MixedStart = 28000
	}
	if opts.ControllerStart == 0 {
		opts.ControllerStart = 29000
	}
	if opts.MixedPort > 0 && opts.MixedPort == opts.ControllerPort {
		return nil, errors.New("mixed and controller ports must differ")
	}
	if opts.MixedPort == 0 {
		opts.MixedPort = allocatePort(opts.MixedStart, used)
	} else if used[opts.MixedPort] || !isPortFree(opts.MixedPort) {
		return nil, fmt.Errorf("mixed proxy port %d is unavailable", opts.MixedPort)
	}
	used[opts.MixedPort] = true
	if opts.ControllerPort == 0 {
		opts.ControllerPort = allocatePort(opts.ControllerStart, used)
	} else if used[opts.ControllerPort] || !isPortFree(opts.ControllerPort) {
		return nil, fmt.Errorf("controller port %d is unavailable", opts.ControllerPort)
	}
	if opts.MixedPort == 0 || opts.ControllerPort == 0 {
		return nil, errors.New("unable to allocate local ports")
	}
	var createdProfile *Profile
	if opts.ProfileID == "" {
		profile, err := s.createProfileRecordLocked(opts.Name+" profile", opts.Config)
		if err != nil {
			return nil, err
		}
		createdProfile = profile
		opts.ProfileID = profile.ID
	}
	cleanupCreatedProfile := func() {
		if createdProfile != nil {
			delete(s.profiles, createdProfile.ID)
			_ = os.RemoveAll(filepath.Dir(createdProfile.ConfigPath))
		}
	}
	profile, ok := s.profiles[opts.ProfileID]
	if !ok {
		cleanupCreatedProfile()
		return nil, fmt.Errorf("profile %q not found", opts.ProfileID)
	}
	secret, err := randomToken()
	if err != nil {
		cleanupCreatedProfile()
		return nil, err
	}
	dir := filepath.Join(s.dataDir, "instances", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		cleanupCreatedProfile()
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	item := &Instance{
		ID:                id,
		Name:              opts.Name,
		ProfileID:         opts.ProfileID,
		MixedPort:         opts.MixedPort,
		ControllerPort:    opts.ControllerPort,
		Secret:            secret,
		UserConfigPath:    profile.ConfigPath,
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		Mode:              mode,
		LocalProxies:      opts.LocalProxies,
		Chain:             normalizeChainNames(opts.Chain),
		SelectedProxies:   cloneStringMap(opts.SelectedProxies),
		SelectedGroup:     opts.SelectedGroup,
		SelectedProxy:     opts.SelectedProxy,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if item.Name == "" {
		item.Name = id
	}
	s.items[item.ID] = item
	if err := s.saveLocked(); err != nil {
		delete(s.items, item.ID)
		_ = os.RemoveAll(dir)
		cleanupCreatedProfile()
		return nil, err
	}
	copy := *item
	copy.Chain = append([]string{}, item.Chain...)
	copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
	return &copy, nil
}

func nextClonePortStart(sourcePort, fallback int) int {
	if sourcePort > 0 && sourcePort < 65535 {
		return sourcePort + 1
	}
	return fallback
}

func (s *Store) SuggestPorts() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	used := s.usedPortsLocked("")
	// 端口建议从当前已用端口之后继续往后扫，尽量保持新实例端口成段增长。
	mixedStart := 28000
	controllerStart := 29000
	for _, item := range s.items {
		if item.MixedPort >= mixedStart && item.MixedPort <= portSuggestMaxStart {
			mixedStart = item.MixedPort + 1
		}
		if item.ControllerPort >= controllerStart && item.ControllerPort <= portSuggestMaxStart {
			controllerStart = item.ControllerPort + 1
		}
	}
	mixed := allocatePort(mixedStart, used)
	controller := allocatePort(controllerStart, used)
	return mixed, controller
}

func (s *Store) Update(id, name, profileID, config string, mixedPort, controllerPort int) (*Instance, error) {
	return s.UpdateWithOptions(id, updateInstanceOptions{
		Name:           name,
		ProfileID:      profileID,
		Config:         config,
		MixedPort:      mixedPort,
		ControllerPort: controllerPort,
	})
}

func (s *Store) UpdateWithOptions(id string, opts updateInstanceOptions) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	nextName := item.Name
	if opts.Name != "" {
		nextName = opts.Name
	}
	nextProfileID := item.ProfileID
	nextUserConfigPath := item.UserConfigPath
	clearSelection := false
	if opts.ProfileID != "" {
		profile, ok := s.profiles[opts.ProfileID]
		if !ok {
			return nil, fmt.Errorf("profile %q not found", opts.ProfileID)
		}
		if item.ProfileID != opts.ProfileID {
			nextProfileID = opts.ProfileID
			nextUserConfigPath = profile.ConfigPath
			clearSelection = true
		}
	}
	nextMixedPort := item.MixedPort
	nextControllerPort := item.ControllerPort
	if opts.MixedPort > 0 {
		nextMixedPort = opts.MixedPort
	}
	if opts.ControllerPort > 0 {
		nextControllerPort = opts.ControllerPort
	}
	if nextMixedPort == nextControllerPort {
		return nil, fmt.Errorf("controller port %d is unavailable", nextControllerPort)
	}
	used := s.usedPortsLocked(id)
	if opts.MixedPort > 0 {
		if used[opts.MixedPort] || !isPortFree(opts.MixedPort) {
			return nil, fmt.Errorf("mixed proxy port %d is unavailable", opts.MixedPort)
		}
		used[opts.MixedPort] = true
	}
	if opts.ControllerPort > 0 {
		if used[opts.ControllerPort] || !isPortFree(opts.ControllerPort) {
			return nil, fmt.Errorf("controller port %d is unavailable", opts.ControllerPort)
		}
		used[opts.ControllerPort] = true
	}
	nextMode := item.Mode
	if opts.Mode != "" {
		mode, err := normalizeInstanceMode(opts.Mode)
		if err != nil {
			return nil, err
		}
		nextMode = mode
	}
	nextLocalProxies := item.LocalProxies
	if opts.LocalProxies != nil {
		nextLocalProxies = *opts.LocalProxies
	}
	if nextMode == InstanceModeGlobalChain {
		if _, _, err := parseLocalProxyItems(nextLocalProxies); err != nil {
			return nil, err
		}
	}
	snapshot := *item
	snapshot.Chain = append([]string{}, item.Chain...)
	snapshot.SelectedProxies = cloneStringMap(item.SelectedProxies)
	item.Name = nextName
	if clearSelection {
		item.SelectedGroup = ""
		item.SelectedProxy = ""
		item.SelectedProxies = nil
	}
	item.ProfileID = nextProfileID
	item.UserConfigPath = nextUserConfigPath
	item.MixedPort = nextMixedPort
	item.ControllerPort = nextControllerPort
	item.Mode = nextMode
	item.LocalProxies = nextLocalProxies
	if opts.Chain != nil {
		item.Chain = normalizeChainNames(*opts.Chain)
	}
	var previousConfig []byte
	if opts.Config != "" {
		profile, ok := s.profiles[item.ProfileID]
		if !ok {
			*item = snapshot
			item.Chain = append([]string{}, snapshot.Chain...)
			item.SelectedProxies = cloneStringMap(snapshot.SelectedProxies)
			return nil, fmt.Errorf("profile %q not found", item.ProfileID)
		}
		var err error
		previousConfig, err = os.ReadFile(profile.ConfigPath)
		if err != nil {
			*item = snapshot
			item.Chain = append([]string{}, snapshot.Chain...)
			item.SelectedProxies = cloneStringMap(snapshot.SelectedProxies)
			return nil, err
		}
		if err := writeFileAtomic(profile.ConfigPath, []byte(opts.Config), 0o600); err != nil {
			*item = snapshot
			item.Chain = append([]string{}, snapshot.Chain...)
			item.SelectedProxies = cloneStringMap(snapshot.SelectedProxies)
			return nil, err
		}
		profile.UpdatedAt = time.Now().UTC()
	}
	item.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		*item = snapshot
		item.Chain = append([]string{}, snapshot.Chain...)
		item.SelectedProxies = cloneStringMap(snapshot.SelectedProxies)
		if opts.Config != "" && previousConfig != nil {
			profile, ok := s.profiles[nextProfileID]
			if ok {
				if rollbackErr := writeFileAtomic(profile.ConfigPath, previousConfig, 0o600); rollbackErr != nil {
					log.Printf("rollback profile config %s failed: %v", nextProfileID, rollbackErr)
				}
			}
		}
		return nil, err
	}
	copy := *item
	copy.Chain = append([]string{}, item.Chain...)
	copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
	return &copy, nil
}

func (s *Store) SetSelection(id, group, proxy string) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	if item.SelectedProxies == nil {
		item.SelectedProxies = make(map[string]string)
	}
	item.SelectedProxies[group] = proxy
	item.SelectedGroup = group
	item.SelectedProxy = proxy
	item.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *item
	copy.Chain = append([]string{}, item.Chain...)
	copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
	return &copy, nil
}

func (s *Store) SetError(id, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.items[id]; ok {
		item.LastError = message
		item.UpdatedAt = time.Now().UTC()
		_ = s.saveLocked()
	}
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return nil
	}
	delete(s.items, id)
	if err := s.saveLocked(); err != nil {
		s.items[id] = item
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, "instances", id))
}

func (s *Store) ReadUserConfig(id string) (string, error) {
	item, ok := s.Get(id)
	if !ok {
		return "", fmt.Errorf("instance %q not found", id)
	}
	profile, ok := s.GetProfile(item.ProfileID)
	if !ok {
		return "", fmt.Errorf("profile %q not found", item.ProfileID)
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Store) saveLocked() error {
	data := storedData{Instances: make([]*Instance, 0, len(s.items))}
	for _, item := range s.items {
		copy := *item
		copy.Chain = append([]string{}, item.Chain...)
		copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
		data.Instances = append(data.Instances, &copy)
	}
	data.Profiles = make([]*Profile, 0, len(s.profiles))
	for _, item := range s.profiles {
		copy := *item
		data.Profiles = append(data.Profiles, &copy)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.storePath, raw, 0o600)
}

func normalizeSelections(selections map[string]string, group, proxy string) map[string]string {
	if len(selections) > 0 {
		return cloneStringMap(selections)
	}
	if group == "" || proxy == "" {
		return nil
	}
	return map[string]string{group: proxy}
}

func cloneProfile(in *Profile) *Profile {
	if in == nil {
		return nil
	}
	copy := *in
	if in.SubscriptionInfo != nil {
		info := *in.SubscriptionInfo
		copy.SubscriptionInfo = &info
	}
	return &copy
}

func profileSelectionSet(groups []ProfileProxyGroup) map[string]map[string]bool {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]map[string]bool, len(groups))
	for _, group := range groups {
		names := make(map[string]bool, len(group.All))
		for _, name := range group.All {
			names[name] = true
		}
		out[group.Name] = names
	}
	return out
}

func reconcileInstanceSelection(item *Instance, valid map[string]map[string]bool) {
	if item == nil || len(valid) == 0 {
		return
	}
	for group, proxy := range item.SelectedProxies {
		if !valid[group][proxy] {
			delete(item.SelectedProxies, group)
		}
	}
	if item.SelectedGroup != "" && !valid[item.SelectedGroup][item.SelectedProxy] {
		item.SelectedGroup = ""
		item.SelectedProxy = ""
		groups := make([]string, 0, len(item.SelectedProxies))
		for group := range item.SelectedProxies {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		for _, group := range groups {
			proxy := item.SelectedProxies[group]
			item.SelectedGroup = group
			item.SelectedProxy = proxy
			break
		}
	}
	if len(item.SelectedProxies) == 0 {
		item.SelectedProxies = nil
	}
}

func (s *Store) createProfileLocked(name, config string) (*Profile, error) {
	profile, err := s.createProfileRecordLocked(name, config)
	if err != nil {
		return nil, err
	}
	if err := s.saveLocked(); err != nil {
		delete(s.profiles, profile.ID)
		_ = os.RemoveAll(filepath.Dir(profile.ConfigPath))
		return nil, err
	}
	return cloneProfile(profile), nil
}

func (s *Store) createProfileRecordLocked(name, config string) (*Profile, error) {
	now := time.Now().UTC()
	if name == "" {
		name = "Default profile"
	}
	if config == "" {
		config = defaultUserConfig
	}
	id := uniqueProfileID(name, s.profiles)
	dir := filepath.Join(s.dataDir, "profiles", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	profile := &Profile{
		ID:         id,
		Name:       name,
		ConfigPath: filepath.Join(dir, "config.yaml"),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := writeFileAtomic(profile.ConfigPath, []byte(config), 0o600); err != nil {
		return nil, err
	}
	s.profiles[profile.ID] = profile
	return profile, nil
}

func (s *Store) usedPortsLocked(exceptID string) map[int]bool {
	used := make(map[int]bool)
	for _, item := range s.items {
		if item.ID == exceptID {
			continue
		}
		used[item.MixedPort] = true
		used[item.ControllerPort] = true
	}
	return used
}
