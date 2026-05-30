package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type processState struct {
	cmd     *exec.Cmd
	started time.Time
	logs    *logBuffer
}

type Manager struct {
	mu         sync.RWMutex
	store      *Store
	mihomoPath string
	procs      map[string]*processState
	starting   map[string]bool
	logs       map[string]*logBuffer
}

func NewManager(store *Store, mihomoPath string) *Manager {
	return &Manager{
		store:      store,
		mihomoPath: mihomoPath,
		procs:      make(map[string]*processState),
		starting:   make(map[string]bool),
		logs:       make(map[string]*logBuffer),
	}
}

func (m *Manager) Views() []InstanceView {
	items := m.store.List()
	views := make([]InstanceView, 0, len(items))
	for _, item := range items {
		profile, _ := m.store.GetProfile(item.ProfileID)
		view := viewFor(item, profile, "stopped", 0)
		if m.isStarting(item.ID) {
			view.Status = "starting"
		} else if ps := m.state(item.ID); ps != nil {
			view.Status = "running"
			if ps.cmd.Process != nil {
				view.PID = ps.cmd.Process.Pid
			}
		} else if item.LastError != "" {
			view.Status = "error"
		}
		views = append(views, view)
	}
	return views
}

func (m *Manager) View(id string) (InstanceView, bool) {
	item, ok := m.store.Get(id)
	if !ok {
		return InstanceView{}, false
	}
	profile, _ := m.store.GetProfile(item.ProfileID)
	view := viewFor(item, profile, "stopped", 0)
	if m.isStarting(id) {
		view.Status = "starting"
	} else if ps := m.state(id); ps != nil {
		view.Status = "running"
		if ps.cmd.Process != nil {
			view.PID = ps.cmd.Process.Pid
		}
	} else if item.LastError != "" {
		view.Status = "error"
	}
	return view, true
}

func viewFor(item *Instance, profile *Profile, status string, pid int) InstanceView {
	view := InstanceView{
		ID:                item.ID,
		Name:              item.Name,
		ProfileID:         item.ProfileID,
		MixedPort:         item.MixedPort,
		ControllerPort:    item.ControllerPort,
		UserConfigPath:    item.UserConfigPath,
		RuntimeConfigPath: item.RuntimeConfigPath,
		SelectedGroup:     item.SelectedGroup,
		SelectedProxy:     item.SelectedProxy,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
		LastError:         item.LastError,
		Status:            status,
		PID:               pid,
	}
	if profile != nil {
		view.ProfileName = profile.Name
		view.ProfileConfigPath = profile.ConfigPath
		view.UserConfigPath = profile.ConfigPath
	}
	return view
}

func (m *Manager) Start(id string) error {
	item, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	profile, ok := m.store.GetProfile(item.ProfileID)
	if !ok {
		return fmt.Errorf("profile %q not found", item.ProfileID)
	}
	m.mu.Lock()
	if m.procs[id] != nil || m.starting[id] {
		m.mu.Unlock()
		return nil
	}
	m.starting[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		m.mu.Unlock()
	}()
	if m.mihomoPath == "" {
		err := errors.New("mihomo binary not found. Install mihomo or start with -mihomo /path/to/mihomo")
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	if !isPortFree(item.ControllerPort) {
		err := fmt.Errorf("controller port %d is already in use", item.ControllerPort)
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	if !isPortFree(item.MixedPort) {
		err := fmt.Errorf("mixed proxy port %d is already in use", item.MixedPort)
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	if err := writeRuntimeConfig(item, profile); err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	if err := m.testConfig(item); err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("config test failed: " + err.Error())
		return err
	}
	m.store.SetError(id, "")

	cmd := exec.Command(m.mihomoPath, "-d", filepath.Dir(item.RuntimeConfigPath), "-f", item.RuntimeConfigPath)
	prepareCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}

	buf := m.log(id)
	ps := &processState{cmd: cmd, started: time.Now().UTC(), logs: buf}
	m.mu.Lock()
	m.procs[id] = ps
	m.mu.Unlock()

	buf.Add(fmt.Sprintf("started mihomo pid=%d", cmd.Process.Pid))
	go captureLines(buf, "stdout", stdout)
	go captureLines(buf, "stderr", stderr)
	go func() {
		err := cmd.Wait()
		if err != nil {
			m.store.SetError(id, err.Error())
			buf.Add("exited: " + err.Error())
		} else {
			m.store.SetError(id, "")
			buf.Add("exited cleanly")
		}
		m.mu.Lock()
		if m.procs[id] == ps {
			delete(m.procs, id)
		}
		m.mu.Unlock()
	}()
	go m.restoreSelection(item, buf)

	return nil
}

func (m *Manager) Stop(id string) error {
	ps := m.state(id)
	if ps == nil {
		return nil
	}
	ps.logs.Add("stopping mihomo")
	_ = stopProcess(ps.cmd)

	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			ps.logs.Add("force killing mihomo")
			if err := killProcess(ps.cmd); err != nil {
				return err
			}
			killDeadline := time.After(1 * time.Second)
			for {
				select {
				case <-killDeadline:
					return fmt.Errorf("process %q did not exit after force kill", id)
				case <-ticker.C:
					if m.state(id) == nil {
						return nil
					}
				}
			}
		case <-ticker.C:
			if m.state(id) == nil {
				return nil
			}
		}
	}
}

func (m *Manager) Restart(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	return m.Start(id)
}

func (m *Manager) Logs(id string) []string {
	return m.log(id).Lines()
}

func (m *Manager) Shutdown(ctx context.Context) {
	ids := make([]string, 0)
	m.mu.RLock()
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			_ = m.Stop(id)
		}
	}()

	select {
	case <-ctx.Done():
		m.mu.RLock()
		for _, ps := range m.procs {
			_ = killProcess(ps.cmd)
		}
		m.mu.RUnlock()
	case <-done:
	}
}

func (m *Manager) state(id string) *processState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.procs[id]
}

func (m *Manager) isStarting(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.starting[id]
}

func (m *Manager) log(id string) *logBuffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := m.logs[id]
	if buf == nil {
		buf = newLogBuffer(1000)
		m.logs[id] = buf
	}
	return buf
}

func captureLines(buf *logBuffer, name string, stream io.Reader) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		buf.Add(name + ": " + scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		buf.Add(name + " log scan failed: " + err.Error())
	}
}

func (m *Manager) testConfig(item *Instance) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.mihomoPath, "-t", "-d", filepath.Dir(item.RuntimeConfigPath), "-f", item.RuntimeConfigPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := string(out)
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("mihomo config test failed: %s", message)
	}
	return nil
}

func (m *Manager) restoreSelection(item *Instance, buf *logBuffer) {
	if item.SelectedGroup == "" || item.SelectedProxy == "" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := putMihomoProxy(item, item.SelectedGroup, item.SelectedProxy); err == nil {
			buf.Add(fmt.Sprintf("restored proxy selection %s -> %s", item.SelectedGroup, item.SelectedProxy))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	buf.Add(fmt.Sprintf("proxy selection restore timed out for %s -> %s", item.SelectedGroup, item.SelectedProxy))
}
