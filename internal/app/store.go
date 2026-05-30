package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	mu        sync.RWMutex
	dataDir   string
	storePath string
	items     map[string]*Instance
	profiles  map[string]*Profile
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
		copy := *item
		out = append(out, &copy)
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
	copy := *item
	return &copy, true
}

func (s *Store) List() []*Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Instance, 0, len(s.items))
	for _, item := range s.items {
		copy := *item
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
	return &copy, true
}

func (s *Store) CreateProfile(name, config string) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createProfileLocked(name, config)
}

func (s *Store) UpdateProfile(id, name, config string) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.profiles[id]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	if name != "" {
		profile.Name = name
	}
	if config != "" {
		if err := writeFileAtomic(profile.ConfigPath, []byte(config), 0o600); err != nil {
			return nil, err
		}
	}
	profile.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *profile
	return &copy, nil
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

	now := time.Now().UTC()
	id := uniqueID(name, s.items)
	used := s.usedPortsLocked("")
	if mixedPort == 0 {
		mixedPort = allocatePort(28000, used)
	} else if used[mixedPort] || !isPortFree(mixedPort) {
		return nil, fmt.Errorf("mixed proxy port %d is unavailable", mixedPort)
	}
	if controllerPort == 0 {
		controllerPort = allocatePort(29000, used)
	} else if used[controllerPort] || !isPortFree(controllerPort) {
		return nil, fmt.Errorf("controller port %d is unavailable", controllerPort)
	}
	if mixedPort == 0 || controllerPort == 0 {
		return nil, errors.New("unable to allocate local ports")
	}
	if profileID == "" {
		profile, err := s.createProfileLocked(name+" profile", config)
		if err != nil {
			return nil, err
		}
		profileID = profile.ID
	}
	profile, ok := s.profiles[profileID]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", profileID)
	}
	secret, err := randomToken()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dataDir, "instances", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	if config == "" {
		config = defaultUserConfig
	}
	item := &Instance{
		ID:                id,
		Name:              name,
		ProfileID:         profileID,
		MixedPort:         mixedPort,
		ControllerPort:    controllerPort,
		Secret:            secret,
		UserConfigPath:    profile.ConfigPath,
		RuntimeConfigPath: filepath.Join(dir, "config.runtime.yaml"),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if item.Name == "" {
		item.Name = id
	}
	s.items[item.ID] = item
	if err := s.saveLocked(); err != nil {
		delete(s.items, item.ID)
		return nil, err
	}
	copy := *item
	return &copy, nil
}

func (s *Store) Update(id, name, profileID, config string, mixedPort, controllerPort int) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	if name != "" {
		item.Name = name
	}
	if profileID != "" {
		profile, ok := s.profiles[profileID]
		if !ok {
			return nil, fmt.Errorf("profile %q not found", profileID)
		}
		if item.ProfileID != profileID {
			item.SelectedGroup = ""
			item.SelectedProxy = ""
			item.ProfileID = profileID
			item.UserConfigPath = profile.ConfigPath
		}
	}
	used := s.usedPortsLocked(id)
	if mixedPort > 0 {
		if used[mixedPort] || !isPortFree(mixedPort) {
			return nil, fmt.Errorf("mixed proxy port %d is unavailable", mixedPort)
		}
		item.MixedPort = mixedPort
	}
	if controllerPort > 0 {
		if used[controllerPort] || !isPortFree(controllerPort) {
			return nil, fmt.Errorf("controller port %d is unavailable", controllerPort)
		}
		item.ControllerPort = controllerPort
	}
	if config != "" {
		profile, ok := s.profiles[item.ProfileID]
		if !ok {
			return nil, fmt.Errorf("profile %q not found", item.ProfileID)
		}
		if err := writeFileAtomic(profile.ConfigPath, []byte(config), 0o600); err != nil {
			return nil, err
		}
		profile.UpdatedAt = time.Now().UTC()
	}
	item.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *item
	return &copy, nil
}

func (s *Store) SetSelection(id, group, proxy string) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", id)
	}
	item.SelectedGroup = group
	item.SelectedProxy = proxy
	item.UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *item
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

func (s *Store) createProfileLocked(name, config string) (*Profile, error) {
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
	if err := s.saveLocked(); err != nil {
		delete(s.profiles, profile.ID)
		return nil, err
	}
	copy := *profile
	return &copy, nil
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
