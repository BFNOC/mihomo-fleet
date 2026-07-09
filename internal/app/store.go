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

// errProfileNotFound, errPortUnavailable and errValidation are sentinel
// errors used to classify user-facing errors from the store/config layer
// (see controller.go's former isPortUnavailableError/isInstanceValidationError,
// which classified by substring-matching error text -- fragile, and any
// wording change would silently change HTTP status codes). Callers should
// use errors.Is(err, errPortUnavailable) etc.
//
// The concrete errors below deliberately do NOT use
// fmt.Errorf("...: %w", sentinel), because that appends the sentinel's own
// message to the error text. internal/app/web/app.js's errorLabels/
// errorPatterns match the *exact* historical error text (several via
// anchored ^...$ regexes) to localize these messages; appending text would
// silently break that matching. Instead each site below uses a small
// wrapper type (mirroring instanceNotFoundError above) that reports the
// original, unmodified message via Error() while still satisfying
// errors.Is via a custom Is method.
var (
	errProfileNotFound = errors.New("profile not found")
	errPortUnavailable = errors.New("port unavailable")
	errValidation      = errors.New("invalid instance configuration")
)

type profileNotFoundError struct {
	id string
}

func (err profileNotFoundError) Error() string {
	return fmt.Sprintf("profile %q not found", err.id)
}

func (err profileNotFoundError) Is(target error) bool {
	return target == errProfileNotFound
}

type portUnavailableError struct {
	msg string
}

func (err portUnavailableError) Error() string { return err.msg }

func (err portUnavailableError) Is(target error) bool { return target == errPortUnavailable }

type validationError struct {
	msg string
}

func (err validationError) Error() string { return err.msg }

func (err validationError) Is(target error) bool { return target == errValidation }

type Store struct {
	mu              sync.RWMutex
	dataDir         string
	storePath       string
	items           map[string]*Instance
	profiles        map[string]*Profile
	proxyGroupCache *profileProxyGroupCache
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
	ProxyBind       string
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
	ProxyBind      *string
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
		dataDir:         dataDir,
		storePath:       filepath.Join(dataDir, "instances.json"),
		items:           make(map[string]*Instance),
		profiles:        make(map[string]*Profile),
		proxyGroupCache: newProfileProxyGroupCache(),
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
		copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
		copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
	copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
		return nil, profileNotFoundError{id: id}
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
	// parseProfileProxyGroups only depends on fetched.Config, not on any
	// Store state, so it is run here, before the write lock is taken. This
	// is the single most expensive step of a subscription refresh (a full
	// YAML parse of a config that can be up to 16MB) and previously ran
	// while s.mu was held, blocking every other Store read/write (including
	// the UI's periodic GET /api/instances) for its duration. The nil check
	// mirrors the one further below so a nil fetched still surfaces the same
	// "profile not found"/URL-mismatch errors first when both conditions
	// apply, matching the original error precedence.
	var groups []ProfileProxyGroup
	if fetched != nil {
		groups, _ = parseProfileProxyGroups(fetched.Config, nil)
	}
	validSelections := profileSelectionSet(groups)

	s.mu.Lock()
	defer s.mu.Unlock()

	profile, ok := s.profiles[id]
	if !ok {
		return nil, profileNotFoundError{id: id}
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
			copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
			copy.Chain = append([]string{}, item.Chain...)
			copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
			itemSnapshots[item.ID] = copy
		}
	}
	if err := writeFileAtomic(profile.ConfigPath, []byte(fetched.Config), 0o600); err != nil {
		return nil, err
	}
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
		return "", profileNotFoundError{id: id}
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// profileProxyGroupCache caches the selection-independent proxy-group parse
// of a profile's config file (parseProfileProxyGroupsBase), keyed by the
// config's path plus its mtime/size at the time it was parsed. The UI polls
// GET /api/profiles/{id}/proxies roughly every 1.8s while the proxies tab of
// an open instance is active; without this cache each poll re-reads and
// re-parses the full (up to 16MB) subscription YAML even though the file
// only changes when PatchProfile or ApplySubscriptionFetchForURL rewrite it.
// Both of those go through writeFileAtomic (temp file + rename), which
// always changes mtime and typically size, so a stale entry is naturally
// invalidated the next time it is looked up -- no explicit invalidation call
// is needed.
type profileProxyGroupCache struct {
	mu      sync.Mutex
	entries map[string]profileProxyGroupCacheEntry
}

type profileProxyGroupCacheEntry struct {
	modTime time.Time
	size    int64
	groups  []ProfileProxyGroup
}

func newProfileProxyGroupCache() *profileProxyGroupCache {
	return &profileProxyGroupCache{entries: make(map[string]profileProxyGroupCacheEntry)}
}

func (c *profileProxyGroupCache) get(path string, info os.FileInfo) ([]ProfileProxyGroup, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[path]
	if !ok || !entry.modTime.Equal(info.ModTime()) || entry.size != info.Size() {
		return nil, false
	}
	// Defensive copy: the cached slice must never be handed out by
	// reference, since callers are free to treat the result as theirs
	// (e.g. append to it) without corrupting the cached entry.
	return append([]ProfileProxyGroup(nil), entry.groups...), true
}

func (c *profileProxyGroupCache) put(path string, info os.FileInfo, groups []ProfileProxyGroup) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[path] = profileProxyGroupCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		groups:  append([]ProfileProxyGroup(nil), groups...),
	}
}

// ProfileProxyGroups returns the parsed proxy-groups for a profile's config
// (selection-independent: Now is each group's first candidate), using
// proxyGroupCache to avoid re-reading and re-parsing the config file when it
// has not changed since the last call. See profileProxyGroupCache's doc
// comment for why that cache needs no explicit invalidation hook.
func (s *Store) ProfileProxyGroups(id string) ([]ProfileProxyGroup, error) {
	profile, ok := s.GetProfile(id)
	if !ok {
		return nil, profileNotFoundError{id: id}
	}
	info, err := os.Stat(profile.ConfigPath)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.proxyGroupCache.get(profile.ConfigPath, info); ok {
		return cached, nil
	}
	raw, err := os.ReadFile(profile.ConfigPath)
	if err != nil {
		return nil, err
	}
	groups, err := parseProfileProxyGroupsBase(string(raw))
	if err != nil {
		return nil, err
	}
	s.proxyGroupCache.put(profile.ConfigPath, info, groups)
	return append([]ProfileProxyGroup(nil), groups...), nil
}

// ProfileProxyGroupsForInstance returns proxy-groups for a profile with the
// given instance's saved selection (or its global-chain plan) applied. The
// non-global-chain path -- by far the common case, and the one polled every
// 1.8s by the proxies panel -- goes through ProfileProxyGroups above and so
// benefits from its cache; only global-chain mode (whose result also depends
// on the instance's LocalProxies/Chain, not just the profile config) still
// reads and parses the config directly on every call.
func (s *Store) ProfileProxyGroupsForInstance(id string, item *Instance) ([]ProfileProxyGroup, error) {
	if item == nil || instanceMode(item.Mode) != InstanceModeGlobalChain {
		var selections map[string]string
		if item != nil {
			selections = normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
		}
		base, err := s.ProfileProxyGroups(id)
		if err != nil {
			return nil, err
		}
		return applyProfileProxySelections(base, selections), nil
	}
	config, err := s.ReadProfileConfig(id)
	if err != nil {
		return nil, err
	}
	return parseGlobalChainProxyGroups(config, item)
}

func (s *Store) Create(name, profileID, config string, mixedPort, controllerPort int) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.createInstanceLocked(createInstanceOptions{
		Name:            name,
		ProfileID:       profileID,
		Config:          config,
		MixedPort:       mixedPort,
		ProxyBind:       defaultProxyBind,
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
		ProxyBind:       source.ProxyBind,
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
	proxyBind, err := normalizeProxyBind(opts.ProxyBind)
	if err != nil {
		// normalizeProxyBind lives in proxy_bind.go and returns a plain
		// error; wrap it here (rather than at its definition) so its
		// message text -- matched verbatim by app.js's errorPatterns --
		// stays unchanged while still classifying as errValidation.
		return nil, validationError{msg: err.Error()}
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
		return nil, validationError{msg: "mixed and controller ports must differ"}
	}
	if opts.MixedPort == 0 {
		opts.MixedPort = allocatePort(opts.MixedStart, used)
	} else if used[opts.MixedPort] || !isPortFree(opts.MixedPort) {
		return nil, portUnavailableError{msg: fmt.Sprintf("mixed proxy port %d is unavailable", opts.MixedPort)}
	}
	used[opts.MixedPort] = true
	if opts.ControllerPort == 0 {
		opts.ControllerPort = allocatePort(opts.ControllerStart, used)
	} else if used[opts.ControllerPort] || !isPortFree(opts.ControllerPort) {
		return nil, portUnavailableError{msg: fmt.Sprintf("controller port %d is unavailable", opts.ControllerPort)}
	}
	if opts.MixedPort == 0 || opts.ControllerPort == 0 {
		return nil, portUnavailableError{msg: "unable to allocate local ports"}
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
		return nil, profileNotFoundError{id: opts.ProfileID}
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
		ProxyBind:         proxyBind,
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
	copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
			return nil, profileNotFoundError{id: opts.ProfileID}
		}
		if item.ProfileID != opts.ProfileID {
			nextProfileID = opts.ProfileID
			nextUserConfigPath = profile.ConfigPath
			clearSelection = true
		}
	}
	nextMixedPort := item.MixedPort
	nextProxyBind := instanceProxyBind(item.ProxyBind)
	nextControllerPort := item.ControllerPort
	if opts.MixedPort > 0 {
		nextMixedPort = opts.MixedPort
	}
	if opts.ControllerPort > 0 {
		nextControllerPort = opts.ControllerPort
	}
	if opts.ProxyBind != nil {
		var err error
		nextProxyBind, err = normalizeProxyBind(*opts.ProxyBind)
		if err != nil {
			// See the matching comment in createInstanceLocked: wrap here
			// (not in proxy_bind.go) to keep the message text app.js
			// matches unchanged while classifying as errValidation.
			return nil, validationError{msg: err.Error()}
		}
	}
	if nextMixedPort == nextControllerPort {
		// Mirror createInstanceLocked's equal-ports check: this is a
		// validation failure (400), not a port-availability conflict (409).
		// See H1 in REVIEW-2026-07-04.md -- this path previously returned
		// portUnavailableError, which HTTP-mapped to 409 even though the
		// create path's identical check already returned validationError
		// (400) for the same condition.
		return nil, validationError{msg: "mixed and controller ports must differ"}
	}
	used := s.usedPortsLocked(id)
	if opts.MixedPort > 0 {
		if used[opts.MixedPort] || !isPortFree(opts.MixedPort) {
			return nil, portUnavailableError{msg: fmt.Sprintf("mixed proxy port %d is unavailable", opts.MixedPort)}
		}
		used[opts.MixedPort] = true
	}
	if opts.ControllerPort > 0 {
		if used[opts.ControllerPort] || !isPortFree(opts.ControllerPort) {
			return nil, portUnavailableError{msg: fmt.Sprintf("controller port %d is unavailable", opts.ControllerPort)}
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
	item.ProxyBind = nextProxyBind
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
			return nil, profileNotFoundError{id: item.ProfileID}
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
	copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
	copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
	copy.Chain = append([]string{}, item.Chain...)
	copy.SelectedProxies = cloneStringMap(item.SelectedProxies)
	return &copy, nil
}

func (s *Store) SetError(id, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.items[id]; ok && item.LastError != message {
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
		return "", profileNotFoundError{id: item.ProfileID}
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
		copy.ProxyBind = instanceProxyBind(copy.ProxyBind)
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
