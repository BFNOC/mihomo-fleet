package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type processState struct {
	cmd     *exec.Cmd
	started time.Time
	logs    *logBuffer
}

type InstanceBatchError struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type InstanceBatchResult struct {
	Total   int                  `json:"total"`
	Success int                  `json:"success"`
	Failed  int                  `json:"failed"`
	Errors  []InstanceBatchError `json:"errors,omitempty"`
}

type Manager struct {
	mu            sync.RWMutex
	store         *Store
	mihomoPath    string
	procs         map[string]*processState
	starting      map[string]bool
	reservedPorts map[int]string
	logs          map[string]*logBuffer
}

func NewManager(store *Store, mihomoPath string) *Manager {
	return &Manager{
		store:         store,
		mihomoPath:    mihomoPath,
		procs:         make(map[string]*processState),
		starting:      make(map[string]bool),
		reservedPorts: make(map[int]string),
		logs:          make(map[string]*logBuffer),
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
		Mode:              instanceMode(item.Mode),
		LocalProxies:      item.LocalProxies,
		Chain:             append([]string{}, item.Chain...),
		SelectedProxies:   cloneStringMap(item.SelectedProxies),
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
	return m.StartContext(context.Background(), id)
}

func (m *Manager) StartContext(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	// reservedPorts 只覆盖启动准备窗口；已运行实例仍由持久化端口唯一性和系统 bind 结果兜底。
	if owner := m.reservedPorts[item.ControllerPort]; owner != "" && owner != id {
		m.mu.Unlock()
		err := fmt.Errorf("controller port %d is already in use", item.ControllerPort)
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	if owner := m.reservedPorts[item.MixedPort]; owner != "" && owner != id {
		m.mu.Unlock()
		err := fmt.Errorf("mixed proxy port %d is already in use", item.MixedPort)
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	m.starting[id] = true
	m.reservedPorts[item.ControllerPort] = id
	m.reservedPorts[item.MixedPort] = id
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		if m.reservedPorts[item.ControllerPort] == id {
			delete(m.reservedPorts, item.ControllerPort)
		}
		if m.reservedPorts[item.MixedPort] == id {
			delete(m.reservedPorts, item.MixedPort)
		}
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
	preparedGeodata, err := m.prepareGeodata(item)
	if err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("geodata prepare failed: " + err.Error())
		return err
	}
	if len(preparedGeodata) > 0 {
		m.log(id).Add("geodata ready: " + strings.Join(preparedGeodata, ", "))
	}
	needsGeodata := configGeodataNeeds(profile)
	if needsGeodata.site && !hasPreparedGeodata(preparedGeodata, "GeoSite.dat") {
		m.log(id).Add("GeoSite.dat not found locally; mihomo may try to download it")
	}
	if needsGeodata.ip && !hasPreparedGeodata(preparedGeodata, "GeoIP.dat") {
		m.log(id).Add("GeoIP.dat not found locally; mihomo may try to download it")
	}
	if err := m.testConfig(ctx, item); err != nil {
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
	return m.StopContext(context.Background(), id)
}

func (m *Manager) StopContext(ctx context.Context, id string) error {
	ps := m.state(id)
	if ps == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ps.logs.Add("stopping mihomo")
	_ = stopProcess(ps.cmd)

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			ps.logs.Add("force killing mihomo")
			if err := killProcess(ps.cmd); err != nil {
				return err
			}
			return m.waitAfterForceKill(ctx, id, ticker)
		case <-ticker.C:
			if m.state(id) == nil {
				return nil
			}
		}
	}
}

func (m *Manager) waitAfterForceKill(ctx context.Context, id string, ticker *time.Ticker) error {
	killDeadline := time.NewTimer(1 * time.Second)
	defer killDeadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-killDeadline.C:
			return fmt.Errorf("process %q did not exit after force kill", id)
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

// StartAll 批量启动所有实例；单个实例失败只记录到结果中，后续实例会继续尝试。
// 已运行或正在启动的实例会沿用 StartContext 的幂等语义并计为成功。
func (m *Manager) StartAll(ctx context.Context) InstanceBatchResult {
	return m.runBatch(ctx, m.StartContext)
}

// StopAll 批量关闭所有实例；未运行实例沿用 Stop 的幂等语义并计为成功。
func (m *Manager) StopAll(ctx context.Context) InstanceBatchResult {
	return m.runBatch(ctx, m.StopContext)
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

func (m *Manager) runBatch(ctx context.Context, action func(context.Context, string) error) InstanceBatchResult {
	items := m.store.List()
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	order := make(map[string]int, len(items))
	for i, item := range items {
		order[item.ID] = i
	}

	result := InstanceBatchResult{Total: len(items)}
	if len(items) == 0 {
		return result
	}

	type outcome struct {
		id   string
		name string
		err  error
	}

	workers := min(4, len(items))
	jobs := make(chan *Instance)
	outcomes := make(chan outcome, len(items))
	var wg sync.WaitGroup

	// 批量操作采用有限并发，避免多个 mihomo 配置测试或进程退出等待同时压满本机资源。
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				outcomes <- outcome{
					id:   item.ID,
					name: item.Name,
					err:  action(ctx, item.ID),
				}
			}
		}()
	}

	for _, item := range items {
		jobs <- item
	}
	close(jobs)
	wg.Wait()
	close(outcomes)

	for out := range outcomes {
		if out.err != nil {
			result.Failed++
			result.Errors = append(result.Errors, InstanceBatchError{
				ID:    out.id,
				Name:  out.name,
				Error: out.err.Error(),
			})
			continue
		}
		result.Success++
	}
	sort.Slice(result.Errors, func(i, j int) bool {
		return order[result.Errors[i].ID] < order[result.Errors[j].ID]
	})
	return result
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

func (m *Manager) testConfig(ctx context.Context, item *Instance) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
	selections := normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
	if len(selections) == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending := 0
		for group, proxy := range selections {
			if err := putMihomoProxy(item, group, proxy); err != nil {
				pending++
			} else {
				buf.Add(fmt.Sprintf("restored proxy selection %s -> %s", group, proxy))
				delete(selections, group)
			}
		}
		if pending == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	for group, proxy := range selections {
		buf.Add(fmt.Sprintf("proxy selection restore timed out for %s -> %s", group, proxy))
	}
}
