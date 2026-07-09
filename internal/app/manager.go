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
	done    chan struct{} // closed by the wait goroutine once cmd.Wait() returns and procs[id] is cleared
}

// startAttempt tracks an in-flight StartContext call so a concurrent Stop/Delete
// can cancel it and wait for it to settle instead of racing on m.procs.
type startAttempt struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the StartContext call that owns it returns
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
	starts        map[string]*startAttempt
	reservedPorts map[int]string
	logs          map[string]*logBuffer
	deleting      map[string]bool
}

func NewManager(store *Store, mihomoPath string) *Manager {
	return &Manager{
		store:         store,
		mihomoPath:    mihomoPath,
		procs:         make(map[string]*processState),
		starting:      make(map[string]bool),
		starts:        make(map[string]*startAttempt),
		reservedPorts: make(map[int]string),
		logs:          make(map[string]*logBuffer),
		deleting:      make(map[string]bool),
	}
}

// BeginDelete marks id as being deleted so a concurrent StartContext call
// (e.g. from another client's POST .../start racing a DELETE) refuses to
// launch a process that would immediately become orphaned once the caller
// removes the instance record. Callers must pair this with EndDelete --
// ideally via defer, including on error paths -- or the instance can never
// start again.
func (m *Manager) BeginDelete(id string) {
	m.mu.Lock()
	m.deleting[id] = true
	m.mu.Unlock()
}

// EndDelete clears the delete-in-progress marker set by BeginDelete.
func (m *Manager) EndDelete(id string) {
	m.mu.Lock()
	delete(m.deleting, id)
	m.mu.Unlock()
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
		ProxyBind:         instanceProxyBind(item.ProxyBind),
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

	// startCtx lets a concurrent StopContext cancel this in-flight start. It is
	// never wired into the launched mihomo process itself (exec.Command, not
	// exec.CommandContext) so cancelling it after a successful cmd.Start() does
	// not kill the running instance.
	startCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	m.mu.Lock()
	if m.deleting[id] {
		m.mu.Unlock()
		return fmt.Errorf("instance %q is being deleted", id)
	}
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
	attempt := &startAttempt{cancel: cancel, done: make(chan struct{})}
	m.starting[id] = true
	m.starts[id] = attempt
	m.reservedPorts[item.ControllerPort] = id
	m.reservedPorts[item.MixedPort] = id
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		if m.starts[id] == attempt {
			delete(m.starts, id)
		}
		if m.reservedPorts[item.ControllerPort] == id {
			delete(m.reservedPorts, item.ControllerPort)
		}
		if m.reservedPorts[item.MixedPort] == id {
			delete(m.reservedPorts, item.MixedPort)
		}
		m.mu.Unlock()
		close(attempt.done)
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
	if err := m.testConfig(startCtx, item); err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("config test failed: " + err.Error())
		return err
	}

	// A concurrent Stop/Delete may have cancelled this attempt while the config
	// test was running. Re-check right before cmd.Start() so a cancelled or
	// deleted instance never actually launches a process.
	if err := startCtx.Err(); err != nil {
		m.log(id).Add("start aborted: " + err.Error())
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
	ps := &processState{cmd: cmd, started: time.Now().UTC(), logs: buf, done: make(chan struct{})}
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
		close(ps.done)
	}()
	go m.restoreSelection(item, buf)

	return nil
}

func (m *Manager) Stop(id string) error {
	return m.StopContext(context.Background(), id)
}

// StopContext stops the instance identified by id. It captures a single
// *processState snapshot and waits on its done channel (closed by the wait
// goroutine that owns that exact process), so a concurrent Start that
// replaces procs[id] with a new process can never be confused for the one
// being stopped, and no polling ticker is needed.
//
// If the instance is currently in its StartContext preparation window (no
// process registered yet), the in-flight start is cancelled and StopContext
// waits for it to settle before deciding whether there is anything left to
// stop.
func (m *Manager) StopContext(ctx context.Context, id string) error {
	ps := m.state(id)
	if ps == nil {
		settled, err := m.cancelAndAwaitStart(ctx, id)
		if err != nil {
			return err
		}
		if settled == nil {
			// Nothing was starting, or the start aborted before it ever
			// registered a process: nothing to stop.
			return nil
		}
		ps = settled
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	ps.logs.Add("stopping mihomo")
	_ = stopProcess(ps.cmd)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ps.done:
		return nil
	case <-time.After(3 * time.Second):
	}

	ps.logs.Add("force killing mihomo")
	if err := killProcess(ps.cmd); err != nil {
		// The process can exit on its own in the narrow window between the
		// 3s SIGTERM deadline firing (above) and this SIGKILL syscall -- it
		// was already dying from the earlier SIGTERM and got reaped a moment
		// sooner. killProcess then legitimately fails ("no such process")
		// even though the instance is not actually stuck. A brief
		// non-blocking-ish probe of ps.done disambiguates: if the wait
		// goroutine already observed the exit (or does within a short grace
		// window), this is not an error.
		select {
		case <-ps.done:
			return nil
		case <-time.After(50 * time.Millisecond):
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ps.done:
		return nil
	case <-time.After(1 * time.Second):
		return fmt.Errorf("process %q did not exit after force kill", id)
	}
}

// cancelAndAwaitStart cancels an in-flight StartContext call for id, if any,
// and waits (bounded) for it to settle. It returns the processState if the
// start won the race and registered a running process despite being
// cancelled (StopContext must then proceed to stop it), or nil if nothing
// was starting or the start aborted before launching a process.
func (m *Manager) cancelAndAwaitStart(ctx context.Context, id string) (*processState, error) {
	m.mu.Lock()
	attempt, ok := m.starts[id]
	m.mu.Unlock()
	if !ok {
		return nil, nil
	}
	attempt.cancel()
	select {
	case <-attempt.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("instance %q did not stop starting in time", id)
	}
	return m.state(id), nil
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

// Shutdown stops every running instance. Instances are stopped concurrently
// (one goroutine per id) so the total time is bounded by the slowest single
// instance's SIGTERM/SIGKILL grace period rather than growing linearly with
// the number of running instances.
//
// Before that, it cancels every in-flight StartContext attempt (m.starts) so
// an instance still in its preparation window does not launch mihomo
// after/while the application is shutting down, and waits (bounded by ctx)
// for each to settle. The procs snapshot below is taken after that wait so a
// start that still won the race and registered a process is included in the
// stop set.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	attempts := make([]*startAttempt, 0, len(m.starts))
	for _, attempt := range m.starts {
		attempt.cancel()
		attempts = append(attempts, attempt)
	}
	m.mu.Unlock()
	for _, attempt := range attempts {
		select {
		case <-attempt.done:
		case <-ctx.Done():
		}
	}

	ids := make([]string, 0)
	m.mu.RLock()
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(len(ids))
		for _, id := range ids {
			go func(id string) {
				defer wg.Done()
				_ = m.Stop(id)
			}(id)
		}
		wg.Wait()
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

// Busy reports whether id has a running process or is currently in its
// StartContext preparation window. Controller write guards that must reject
// changes while an instance cannot safely be mutated should use Busy instead
// of checking state() alone, since state() is nil for the entire starting
// window (writeRuntimeConfig/prepareGeodata/testConfig can take up to ~10s).
func (m *Manager) Busy(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.procs[id] != nil || m.starting[id]
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
		// The scanner stopped consuming the pipe (e.g. a line exceeded the
		// buffer limit). Keep draining so the child process never blocks
		// forever on a full pipe if it writes more output.
		_, _ = io.Copy(io.Discard, stream)
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
