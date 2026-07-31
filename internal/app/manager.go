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

	// controllerPort/mixedPort/proxyBind snapshot the exact values item's
	// runtime config was generated with when this process was launched (or
	// last successfully hot-reloaded -- see Manager.ReloadContext, which never
	// touches these three since it refuses to run at all when they would
	// change). ReloadContext compares them against item's *current* stored
	// values to decide whether a pending edit only touches proxies/rules
	// (safe to push live) or would also change what mihomo listens on (not
	// safe -- see errReloadNetworkChanged). Immutable after StartContext sets
	// them, so -- unlike started below -- reading them straight off the
	// pointer returned by state()/instanceRuntime() needs no extra copying.
	controllerPort int
	mixedPort      int
	proxyBind      string

	// stopRequested marks that this exact process was asked to exit by
	// StopContext -- a direct Stop, the Stop half of Restart (Stop then
	// Start), or the Stop that brackets BeginDelete/store.Delete -- before
	// its wait goroutine (StartContext's `go func() { cmd.Wait() ... }()`)
	// observed cmd.Wait() return. It is the crash watchdog's gating signal
	// for "the user asked for this exit": maybeAutoRestart only ever
	// schedules a relaunch when this is false. Set once, under m.mu, by
	// StopContext right before it signals the process (covering both the
	// process-already-running case and the race where a start that raced
	// against a concurrent Stop still wins and registers one, per
	// cancelAndAwaitStart) -- never cleared once set, since a fresh
	// StartContext call always creates a brand-new processState rather than
	// reusing this one.
	stopRequested bool
}

// startAttempt tracks an in-flight StartContext call so a concurrent Stop/Delete
// can cancel it and wait for it to settle instead of racing on m.procs.
type startAttempt struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the StartContext call that owns it returns
}

// watchdogState holds the crash watchdog's per-instance bookkeeping
// (manager.go's #2 feature). It is keyed independently of processState in
// Manager.watchdogs because it must outlive any single process: a crash
// replaces processState with nil (and a later relaunch creates a brand-new
// one), but RestartCount/LastExitReason/LastExitAt need to stay visible on
// the view across that replacement, and consecutive/cancelPending need to
// keep tracking the instance across however many relaunches its backoff
// sequence spans.
type watchdogState struct {
	// restartCount is a lifetime total of successful auto-restarts for this
	// instance (InstanceView.RestartCount) -- runtime evidence, per
	// PRODUCT.md's "explicit runtime evidence... should drive the UI"
	// principle. Unlike consecutive below, a manual Start/Restart never
	// resets it: it is a history of what the watchdog has done, not a
	// live gauge of backoff eligibility.
	restartCount int
	// consecutive counts crash+relaunch attempts since the last time this
	// instance either (a) was manually started/restarted (resetWatchdogLocked,
	// called from startContext) or (b) stayed up longer than
	// watchdogHealthyAfter after an auto-restart (watchHealthyRun). It drives
	// both the exponential backoff delay (watchdogBackoffDelay) and the
	// watchdogMaxRestarts give-up cap.
	consecutive    int
	lastExitReason string
	lastExitAt     time.Time
	// cancelPending cancels the context a currently-sleeping backoff timer
	// (runScheduledRestart) is waiting on. Non-nil only while a relaunch is
	// actually pending; StopContext (via markUserStopped) and
	// resetWatchdogLocked both call it (via cancelPendingRestartLocked) so a
	// user Stop or manual Start always wins a race against a scheduled
	// auto-restart, never the other way around. Ownership of this field is
	// gated by generation below -- only the goroutine whose captured
	// generation still matches may read or clear it; see runScheduledRestart.
	cancelPending context.CancelFunc
	// generation increments every time scheduleRestart arms a new backoff
	// attempt. Each runScheduledRestart goroutine captures the value at arm
	// time and compares it back against this field after waking from its
	// sleep: a mismatch means a *newer* attempt has since been armed (this
	// goroutine was simply descheduled long enough for another
	// crash-and-relaunch cycle to begin), so it must return immediately
	// without touching cancelPending (which now belongs to that newer
	// attempt) or relaunching -- otherwise a stale goroutine can clobber a
	// live successor's cancel handle, silently losing a Stop delivered
	// during the successor's own backoff (crash-watchdog concurrency review,
	// fix #2).
	generation int
	// userStopped is a persistent (survives across the processState that was
	// running when it was set) "the user asked this instance to stop" stamp,
	// set unconditionally by StopContext (markUserStopped) even when there is
	// currently no running process and no pending backoff for it to cancel --
	// closing the window where a crash's exit goroutine is between clearing
	// m.procs and scheduleRestart arming anything, and a concurrent Stop
	// would otherwise find nothing to act on and silently lose the race
	// (crash-watchdog concurrency review, fix #3). Checked by scheduleRestart
	// before it ever arms a backoff, and by runScheduledRestart's post-sleep
	// section; cleared only by a genuine subsequent (re)start
	// (resetWatchdogLocked), so it never lingers past the next time the
	// operator actually starts the instance again.
	userStopped bool
}

// backoffSleep is sleepWithContext (mihomo_api.go), indirected through a
// package-level var so manager_test.go can substitute a fake that reproduces
// the exact select{} tie-break race fix #1 (crash-watchdog concurrency
// review) defends against: select choosing the timer branch even though
// ctx.Done() became ready at the same instant, because a concurrent Stop
// cancelled the context right as the backoff naturally elapsed. Real
// sleepWithContext behavior is unaffected; only tests ever assign a
// different function here.
var backoffSleep = sleepWithContext

// errAlreadyRunning is startContext's internal signal that it hit its
// idempotent no-op path (m.procs[id] != nil || m.starting[id]) instead of
// actually launching a process. Every existing public entry point
// (StartContext, and therefore Start/StartAll/the controller's start action)
// converts this back to a plain nil -- already-running is still "success"
// for them, matching the pre-existing idempotent-start semantics documented
// on runBatch. runScheduledRestart is the one caller that needs to tell the
// two apart: crediting restartCount++ / spawning watchHealthyRun for an
// attempt that lost a race to a manual Start (and so performed no actual
// relaunch) would misreport bookkeeping manual Start's own
// resetWatchdogLocked call already reset out from under it (crash-watchdog
// concurrency review, fix #4's related nit).
var errAlreadyRunning = errors.New("instance already running or starting")

// errWatchdogUserStopped is startContext's internal signal that the crash
// watchdog's own relaunch (runScheduledRestart, resetWatchdog=false) found
// id stamped user-stopped while re-checking under m.mu, atomically with the
// starting-state transition a few lines below. This closes the race where a
// user's Stop lands between runScheduledRestart's own post-sleep userStopped
// check (which happens before startContext is even called) and this
// function actually acquiring m.mu: without this second check, that Stop
// would see no running process and no pending backoff to cancel (both
// already cleared by the time it runs) and report success, while the
// watchdog goes on to launch the process anyway (code review finding #1).
// Only runScheduledRestart's caller path ever produces or consumes this;
// every other startContext caller passes resetWatchdog=true and never sees
// it.
var errWatchdogUserStopped = errors.New("instance was stopped; auto-restart aborted")

// Watchdog tunables. Package-level vars, not consts, so tests can shrink
// them (mirroring util.go's isPortFree / store_test.go's withPortFree
// pattern) instead of a real test run waiting out up to 30s of backoff or a
// 60s healthy-run window.
var (
	watchdogBaseBackoff  = 1 * time.Second
	watchdogMaxBackoff   = 30 * time.Second
	watchdogMaxRestarts  = 5
	watchdogHealthyAfter = 60 * time.Second
)

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
	// watchdogs holds the crash watchdog's per-instance bookkeeping (see
	// watchdogState's doc comment). Entries are created lazily
	// (watchdogFor) the first time an instance's process exits, and removed
	// by dropWatchdog once the instance itself is deleted -- mirroring
	// logs's lifecycle (arch L7 / conc L-1).
	watchdogs map[string]*watchdogState
	// lifecycle serializes a single instance's start/stop/reload against each
	// other -- one *sync.Mutex per instance id, never one lock for the whole
	// Manager, so unrelated instances never block on each other. Without
	// this, ReloadContext could capture ps := m.state(id), re-verify
	// m.procs[id] == ps under m.mu.RLock(), release that lock, and only then
	// do the actual work (writeRuntimeConfig, prepareGeodata,
	// reloadMihomoConfig): a concurrent StopContext, or a Restart's Stop then
	// Start, could land in the window between the re-check and that work,
	// leaving the runtime config written for a process that already exited
	// and reloadMihomoConfig's reload command (with this instance's
	// controller secret) sent to a controller port that may by then belong
	// to an unrelated process that grabbed the freed port. A one-shot
	// pointer re-check cannot close that window; only holding this mutex
	// across the whole check-then-work sequence, mutually exclusive with the
	// lifecycle transitions themselves, can.
	//
	// Not every caller holds it for its entire function body: startContext
	// and StopContext each have a short early phase that deliberately runs
	// without it. startContext's is only the initial store reads used for
	// validation and the m.mu-protected registration of m.starting/m.starts/
	// m.reservedPorts -- it acquires this lock immediately after that
	// registration, before writeRuntimeConfig/testConfig, then re-reads
	// item/profile under the lock so config generation, the config test, and
	// the eventual cmd.Start() all use one snapshot that no concurrent
	// ReloadContext (which holds this same lock for its whole body) can
	// replace out from under them -- without this, a watchdog relaunch could
	// write+test snapshot A, block on this lock while a ReloadContext
	// overwrote the same file with snapshot B, and then exec against B
	// having only ever tested A. StopContext's early phase resolves which
	// processState a Stop targets, including cancelling a same-id in-flight
	// start via the pre-existing startCtx/cancelAndAwaitStart mechanism --
	// that path never waits on this lock, so a Stop can still interrupt a
	// start that is blocked waiting for it, or already holding it (see each
	// function's own doc comment for the details). See lifecycleLock and its
	// callers.
	//
	// Entries are never removed: unlike watchdogs above, dropWatchdog
	// deliberately leaves m.lifecycle[id] in place (see its own comment for
	// why a delete here would be unsafe). The retained cost is one
	// zero-value sync.Mutex and one map entry per instance id this process
	// has ever touched, bounded by real instance creations.
	lifecycle map[string]*sync.Mutex
	// coreUpdating is armed for the duration of an in-flight mihomo core
	// binary swap (core_update.go's ApplyCoreUpdate, via
	// BeginCoreUpdate/EndCoreUpdate) and checked by startContext, so a
	// Start racing an in-flight update can never launch a process against
	// the binary mid-swap. Fleet-wide rather than per-instance (unlike
	// deleting above) since a core swap replaces the one binary every
	// instance execs, not just one instance's own files.
	coreUpdating bool
	// ctx/cancel bound restoreSelection's polling loop to the Manager's own
	// lifetime (conc L-3, docs/review-2026-07-11-go-concurrency-performance.md)
	// rather than the per-StartContext-call ctx, which is cancelled as soon
	// as StartContext itself returns (see StartContext's startCtx) -- long
	// before the background restoreSelection goroutine it kicks off is done.
	ctx    context.Context
	cancel context.CancelFunc
}

func NewManager(store *Store, mihomoPath string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:         store,
		mihomoPath:    mihomoPath,
		procs:         make(map[string]*processState),
		starting:      make(map[string]bool),
		starts:        make(map[string]*startAttempt),
		reservedPorts: make(map[int]string),
		logs:          make(map[string]*logBuffer),
		deleting:      make(map[string]bool),
		watchdogs:     make(map[string]*watchdogState),
		lifecycle:     make(map[string]*sync.Mutex),
		ctx:           ctx,
		cancel:        cancel,
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

// instanceRuntimeState is a starting/running snapshot for a single instance,
// read from Manager's maps under a single lock acquisition. Reading
// isStarting(id) and state(id) as two separate lock/unlock pairs (the
// previous implementation of both Views and View) left a window between the
// two reads where a start that just finished (starting cleared, procs not
// yet set, or vice versa) could be observed as neither starting nor running
// (testing L8 / conc L-5).
type instanceRuntimeState struct {
	starting bool
	ps       *processState
	// started copies ps.started at snapshot time, taken under m.mu.RLock
	// below. Unlike every other processState field, started can change after
	// publish (Manager.markReloaded bumps it forward on a successful hot
	// reload), so decorateStatus must not dereference the live ps.started
	// field lock-free the way it safely does for write-once fields like
	// ps.cmd -- that would be a genuine data race against markReloaded's
	// m.mu.Lock()'d write. Copying the value out here, under the same mutex
	// markReloaded writes under, is what makes decorateStatus's read safe.
	started time.Time
	// restartCount/lastExitReason/lastExitAt copy the instance's
	// watchdogState (if any) at snapshot time, under the same m.mu that
	// protects it -- see watchdogState's doc comment for why this data
	// outlives any single processState.
	restartCount   int
	lastExitReason string
	lastExitAt     time.Time
}

// runtimeSnapshot returns a starting/running snapshot for every instance
// Manager currently knows about, taken under a single m.mu.RLock. Views used
// to call isStarting(id) and state(id) independently per instance -- two
// RLock/RUnlock pairs per instance, 2N lock acquisitions for N instances --
// which this replaces with one lock acquisition total (conc L-5).
func (m *Manager) runtimeSnapshot() map[string]instanceRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]instanceRuntimeState, len(m.procs)+len(m.starting))
	for id := range m.starting {
		out[id] = instanceRuntimeState{starting: true}
	}
	for id, ps := range m.procs {
		entry := out[id]
		entry.ps = ps
		entry.started = ps.started
		out[id] = entry
	}
	for id, wd := range m.watchdogs {
		entry := out[id]
		entry.restartCount = wd.restartCount
		entry.lastExitReason = wd.lastExitReason
		entry.lastExitAt = wd.lastExitAt
		out[id] = entry
	}
	return out
}

// instanceRuntime returns id's starting/running snapshot, read atomically
// under a single m.mu.RLock (unlike the previous isStarting(id)+state(id)
// pair used by View, see instanceRuntimeState's doc comment).
func (m *Manager) instanceRuntime(id string) instanceRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := instanceRuntimeState{starting: m.starting[id], ps: m.procs[id]}
	if state.ps != nil {
		state.started = state.ps.started
	}
	if wd := m.watchdogs[id]; wd != nil {
		state.restartCount = wd.restartCount
		state.lastExitReason = wd.lastExitReason
		state.lastExitAt = wd.lastExitAt
	}
	return state
}

// decorateStatus fills in view's Status/PID/PendingRestart from item and
// snap. This is the single copy of status-derivation logic Views and View
// both call (testing L8, docs/review-2026-07-11-testing-quality.md); it
// previously existed twice, once in each.
func decorateStatus(view *InstanceView, item *Instance, snap instanceRuntimeState) {
	switch {
	case snap.starting:
		view.Status = "starting"
	case snap.ps != nil:
		view.Status = "running"
		if snap.ps.cmd.Process != nil {
			view.PID = snap.ps.cmd.Process.Pid
		}
		// arch M5 (docs/review-2026-07-11-go-architecture.md): Mode/Chain/
		// LocalProxies/Config edits on a running instance are persisted
		// immediately by the store, but only take effect on the runtime
		// config the process was actually launched with (StartContext's
		// writeRuntimeConfig call, at snap.ps.started) -- so report that
		// drift instead of silently implying the change is already live.
		// N2 (docs/review-2026-07-11-fix-verification-round4.md): this used
		// to compare item.UpdatedAt, which every store mutation bumps
		// (including SetSelection and SetError) -- so selecting a node on a
		// running instance permanently flipped this true even though the
		// selection was already applied live via putMihomoProxy. Compare
		// ConfigUpdatedAt instead, which only the mutations that actually
		// change the generated runtime config touch.
		if item.ConfigUpdatedAt.After(snap.started) {
			view.PendingRestart = true
		}
	case item.LastError != "":
		view.Status = "error"
	}
	// Crash-watchdog evidence is sticky (never cleared by a later successful
	// restart or a status change) and shown regardless of the current
	// status, so an operator who missed the crash live can still see that it
	// happened -- see watchdogState's doc comment.
	view.RestartCount = snap.restartCount
	view.LastExitReason = snap.lastExitReason
	view.LastExitAt = snap.lastExitAt
}

func (m *Manager) Views() []InstanceView {
	items := m.store.List()
	snapshot := m.runtimeSnapshot()
	views := make([]InstanceView, 0, len(items))
	for _, item := range items {
		profile, _ := m.store.GetProfile(item.ProfileID)
		view := viewFor(item, profile, "stopped", 0)
		decorateStatus(&view, item, snapshot[item.ID])
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
	decorateStatus(&view, item, m.instanceRuntime(id))
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
		AutoRestart:       item.AutoRestart,
	}
	if profile != nil {
		view.ProfileName = profile.Name
		view.ProfileConfigPath = profile.ConfigPath
		view.UserConfigPath = profile.ConfigPath
	}
	return view
}

// lifecycleLock returns (creating on first use) the *sync.Mutex that
// serializes id's start/stop/reload transitions against each other -- see
// the lifecycle field's doc comment for why this exists. It takes m.mu only
// for the map access itself and never while holding the returned mutex:
// m.mu must never be held while acquiring a lifecycle mutex, since callers
// (startContext, StopContext, ReloadContext) go on to acquire m.mu
// themselves while holding it -- the only order this codebase allows is
// lifecycle -> m.mu, never the reverse.
func (m *Manager) lifecycleLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu := m.lifecycle[id]
	if mu == nil {
		mu = &sync.Mutex{}
		m.lifecycle[id] = mu
	}
	return mu
}

func (m *Manager) Start(id string) error {
	return m.StartContext(context.Background(), id)
}

func (m *Manager) StartContext(ctx context.Context, id string) error {
	err := m.startContext(ctx, id, true)
	if errors.Is(err, errAlreadyRunning) {
		// Every public entry point treats "already running/starting" as a
		// successful no-op, matching runBatch's documented idempotent-start
		// semantics -- only runScheduledRestart needs to tell this apart
		// from an actual fresh launch (errAlreadyRunning's doc comment).
		return nil
	}
	return err
}

// startContext is StartContext's actual implementation. resetWatchdog is
// true for every caller except the crash watchdog's own relaunch
// (runScheduledRestart): a manual Start/Restart is exactly the "operator
// took control" signal that should give an instance a fresh run of
// consecutive backoff attempts (resetWatchdogLocked), but the watchdog's own
// relaunch call must not reset the very counter it just incremented to
// decide this relaunch should happen at all.
func (m *Manager) startContext(ctx context.Context, id string, resetWatchdog bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	item, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	_, ok = m.store.GetProfile(item.ProfileID)
	if !ok {
		return fmt.Errorf("profile %q not found", item.ProfileID)
	}

	// startCtx lets a concurrent StopContext cancel this in-flight start
	// without needing to wait for the lifecycle lock acquired further down:
	// StopContext's cancelAndAwaitStart path (see its own doc comment)
	// cancels startCtx and waits on attempt.done directly, never on this
	// call's lifecycle lock, so a Stop racing a start that is blocked waiting
	// for -- or already holding -- that lock can still interrupt it promptly.
	// It is never wired into the launched mihomo process itself
	// (exec.Command, not exec.CommandContext) so cancelling it after a
	// successful cmd.Start() does not kill the running instance.
	startCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	m.mu.Lock()
	if m.coreUpdating {
		m.mu.Unlock()
		return errors.New("mihomo core binary is being updated; retry once the update finishes")
	}
	if m.deleting[id] {
		m.mu.Unlock()
		return fmt.Errorf("instance %q is being deleted", id)
	}
	if m.procs[id] != nil || m.starting[id] {
		m.mu.Unlock()
		return errAlreadyRunning
	}
	if resetWatchdog {
		// A manual Start/Restart about to actually launch a process: cancel
		// any auto-restart backoff currently pending for id (so this doesn't
		// race a scheduled relaunch into a duplicate/late Start) and give the
		// consecutive-restart counter a fresh start -- see startContext's own
		// doc comment and resetWatchdogLocked's.
		m.resetWatchdogLocked(id)
	} else if wd := m.watchdogs[id]; wd != nil && wd.userStopped {
		// The watchdog's own relaunch (runScheduledRestart): re-check
		// userStopped here, atomically with the starting-state transition
		// below, so a Stop that lands between runScheduledRestart's own
		// (earlier, unlocked-in-between) check and this call can never lose
		// the race -- see errWatchdogUserStopped's doc comment (finding #1,
		// code review).
		m.mu.Unlock()
		return errWatchdogUserStopped
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
	// Captured before the cleanup defer below is installed. The defer's
	// closure captures variables, not the values they held at defer time --
	// and item is reassigned further down (once the lifecycle lock is held)
	// when this call re-reads the store. If the defer read
	// item.ControllerPort/item.MixedPort directly, a reassigned item would
	// silently change which ports it un-reserves on cleanup. Using these
	// dedicated locals instead keeps the defer tied to the ports actually
	// reserved above, regardless of what item later becomes.
	reservedController := item.ControllerPort
	reservedMixed := item.MixedPort
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		if m.starts[id] == attempt {
			delete(m.starts, id)
		}
		if m.reservedPorts[reservedController] == id {
			delete(m.reservedPorts, reservedController)
		}
		if m.reservedPorts[reservedMixed] == id {
			delete(m.reservedPorts, reservedMixed)
		}
		m.mu.Unlock()
		close(attempt.done)
	}()

	// Acquire the lifecycle lock now -- right after the attempt is
	// registered above (so a concurrent StopContext can still find and
	// cancel it via cancelAndAwaitStart/startCtx while this call sits queued
	// on the lock; see startCtx's doc comment) but before any config
	// generation below. Taking it here, rather than right before
	// cmd.Start() as this used to do, is what closes the race that let a
	// concurrent ReloadContext (which holds this same lock for its entire
	// body, and writes the same runtime config file) overwrite the config
	// this call had just tested, in the window between the test and the
	// launch -- mihomo would then exec against a config nobody tested. See
	// the lifecycle field's doc comment.
	lock := m.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()

	// A concurrent Stop may have cancelled this attempt, or a concurrent
	// Delete/edit/ReloadContext may have changed the world, while this call
	// waited for the lock above (ReloadContext holds it for its whole body).
	// Re-validate everything the preparation phase below depends on against
	// a fresh snapshot, and re-read item/profile themselves, so config
	// generation, the config test, and the eventual launch all use one
	// snapshot that no concurrent Reload can replace out from under them.
	if err := startCtx.Err(); err != nil {
		return err
	}
	if m.isDeleting(id) {
		return fmt.Errorf("instance %q is being deleted", id)
	}
	// Stamped BEFORE the store re-read below, not after it and not after
	// cmd.Start(). ps.started is what decorateStatus compares ConfigUpdatedAt
	// against to decide PendingRestart, so anything committed after this
	// instant must compare as newer. Reading the store first would leave every
	// edit landing between the read and the stamp looking already-applied
	// while the config generated below predates it -- silent drift with no UI
	// signal. Stamping first can only err the safe way (a redundant
	// "restart pending" hint), which is the same trade ReloadContext's
	// reloadStart makes; see markReloaded.
	startedAt := time.Now().UTC()

	item, ok = m.store.Get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	profile, ok := m.store.GetProfile(item.ProfileID)
	if !ok {
		return fmt.Errorf("profile %q not found", item.ProfileID)
	}
	// Defensive, not an expected path: the PUT handler's Busy guard already
	// rejects port edits while m.starting[id] is set, so item's ports should
	// be unable to change while this call sits blocked above. Guard anyway
	// rather than silently launching against ports that differ from the
	// ones reserved in m.reservedPorts (and that the cleanup defer above
	// will un-reserve).
	if item.ControllerPort != reservedController || item.MixedPort != reservedMixed {
		return fmt.Errorf("instance %q ports changed while starting", id)
	}

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
	parsedConfig, err := writeRuntimeConfig(item, profile)
	if err != nil {
		m.store.SetError(id, err.Error())
		m.log(id).Add("start failed: " + err.Error())
		return err
	}
	// arch M3: dns.listen is intentionally not stripped by cleanRuntimeConfig
	// (it may be a deliberate single-instance choice), but two instances
	// sharing this profile would both try to bind it -- warn in this
	// instance's own log rather than silently letting that surface only as
	// an opaque bind failure (or worse, silent DNS misbehavior) later.
	if configHasDNSListen(parsedConfig) {
		m.log(id).Add("warning: profile config sets dns.listen; if this profile is shared by another instance, they may conflict binding the same DNS listen address")
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
	// conc L-6: reuse the config writeRuntimeConfig already parsed instead of
	// reading and YAML-parsing profile.ConfigPath a second time.
	needsGeodata := configGeodataNeeds(parsedConfig)
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
	ps := &processState{
		cmd:     cmd,
		started: startedAt,
		logs:    buf,
		done:    make(chan struct{}),
		// Snapshot exactly the values writeRuntimeConfig just generated the
		// runtime config from, so a later ReloadContext call can tell whether
		// item's stored fields have since drifted from what this process is
		// actually listening on (see processState's doc comment).
		controllerPort: item.ControllerPort,
		mixedPort:      item.MixedPort,
		proxyBind:      instanceProxyBind(item.ProxyBind),
	}
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
		stopRequested := ps.stopRequested
		if m.procs[id] == ps {
			delete(m.procs, id)
		}
		m.mu.Unlock()
		close(ps.done)

		// Crash watchdog: decide whether this exit was unexpected (not a
		// user Stop/Restart/delete) and, if so, whether id's AutoRestart
		// flag calls for a relaunch. See maybeAutoRestart's doc comment for
		// the exact gating.
		m.maybeAutoRestart(id, err, stopRequested)
	}()
	// m.ctx (not startCtx) bounds this goroutine: startCtx is cancelled by the
	// deferred cancel() as soon as StartContext itself returns, moments after
	// this line runs, which would abort restoreSelection immediately. m.ctx
	// instead lives for the Manager's whole lifetime and is only cancelled by
	// Shutdown (conc L-3).
	go m.restoreSelection(m.ctx, item, ps, buf)

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
	// Stamp id as user-stopped and cancel any crash-watchdog backoff
	// currently pending for it, both unconditionally and up front -- see
	// markUserStopped's doc comment (crash-watchdog concurrency review, fix
	// #3) for the race this closes: an instance mid-backoff, or between a
	// crash being observed and scheduleRestart arming anything, has no
	// registered process and no in-flight start attempt (m.procs/m.starting
	// are both empty for it), so neither of the two branches below would
	// ever reach it, and a plain cancel-if-pending call can find nothing to
	// cancel even though the watchdog is about to relaunch it moments later.
	m.markUserStopped(id)

	// Resolving ps -- including the cancelAndAwaitStart branch, which
	// cancels and waits (bounded, up to 15s) on an in-flight startContext
	// preparation window -- deliberately happens before the lifecycle lock
	// below is acquired. startContext itself only takes that lock for its
	// own late, process-registering phase (see its doc comment), precisely
	// so this cancellation path is never stuck queued behind a whole
	// in-flight start it is trying to cancel.
	ps := m.state(id)
	if ps == nil {
		settled, err := m.cancelAndAwaitStart(ctx, id)
		if err != nil {
			return err
		}
		ps = settled
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Nothing running, nothing starting, and no such instance: return before
	// lifecycleLock so an id the store has never heard of never allocates an
	// m.lifecycle entry. Nothing removes those entries (see dropWatchdog for
	// why removal is unsafe), so without this gate a stop/delete loop over
	// invalid ids grows the map without bound -- the same shape as the
	// watchdogs leak markUserStopped already guards against. When ps is
	// non-nil there IS a live process to stop regardless of what the store
	// says, so the gate deliberately only covers the no-process case.
	if ps == nil {
		if _, ok := m.store.Get(id); !ok {
			return nil
		}
	}

	// Serialize the rest of this call -- including the "nothing to stop"
	// return just below -- against a concurrent startContext (a fresh
	// registration for this id) or ReloadContext for the same id; see the
	// lifecycle field's doc comment.
	//
	// The nothing-to-stop case must take this lock too, not return early
	// above it. The DELETE handler brackets store.Delete and the instance
	// directory removal with a Stop, and a process that crashed a moment
	// earlier leaves nothing for that Stop to find -- so returning before the
	// lock let the delete run while a ReloadContext holding the lock was
	// still mid-flight, whose prepareGeodata then re-created the very
	// directory the delete had just removed (an orphaned instance dir with
	// no store record). Taking the lock makes the delete wait for that reload
	// to finish instead.
	//
	// Safe to hold across the wait on ps.done further down: that channel is
	// closed by startContext's wait goroutine, which runs independently and
	// never itself needs this lock (only the maybeAutoRestart call it makes
	// afterward can lead back into startContext, on yet another goroutine),
	// so holding this lock here cannot block on anything that in turn needs
	// it. cancelAndAwaitStart above deliberately runs BEFORE the lock for the
	// same reason -- it must be able to interrupt an in-flight start that is
	// itself holding this lock.
	lock := m.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()

	if ps == nil {
		// Nothing was running, and nothing was starting (or the start aborted
		// before it ever registered a process).
		return nil
	}

	// Re-check after the wait: acquiring the lock above can block for as long
	// as a concurrent reload or start takes, and a caller whose deadline
	// expired in that window must not still go on to signal the process.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Mark this exact process as intentionally stopped before signaling it,
	// so its wait goroutine's maybeAutoRestart call never mistakes this for
	// a crash -- see processState.stopRequested's doc comment.
	m.mu.Lock()
	ps.stopRequested = true
	m.mu.Unlock()

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

// watchdogFor returns id's watchdogState, creating an empty one on first
// use. Caller must hold m.mu.
func (m *Manager) watchdogFor(id string) *watchdogState {
	wd := m.watchdogs[id]
	if wd == nil {
		wd = &watchdogState{}
		m.watchdogs[id] = wd
	}
	return wd
}

// cancelPendingRestartLocked cancels id's pending auto-restart backoff, if
// any (runScheduledRestart's sleep will observe its context cancelled and
// return without relaunching). Caller must hold m.mu.
func (m *Manager) cancelPendingRestartLocked(id string) {
	if wd := m.watchdogs[id]; wd != nil && wd.cancelPending != nil {
		wd.cancelPending()
		wd.cancelPending = nil
	}
}

// markUserStopped stamps id's watchdogState as user-stopped and cancels any
// currently-pending auto-restart backoff, both under one lock acquisition.
// Called unconditionally at the top of StopContext -- even when there is
// currently no running process and no pending backoff to cancel, which is
// exactly the shape of the race fix #3 (crash-watchdog concurrency review)
// closes: a crash's exit goroutine can be in the window between removing the
// dead process from m.procs and scheduleRestart arming a backoff, during
// which neither m.state(id) nor wd.cancelPending has anything for a
// concurrent Stop to find. The stamp is checked by scheduleRestart before it
// ever arms a backoff, and again by runScheduledRestart's post-sleep section
// (in case a Stop lands in the narrower window between arming and waking);
// it is cleared only by a genuine subsequent (re)start (resetWatchdogLocked),
// so it does not linger and block auto-restart after the operator manually
// starts the instance again.
func (m *Manager) markUserStopped(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Don't allocate a watchdogs entry for an id the store has no instance
	// for: dropWatchdog only ever runs for an instance the store actually
	// had (via the DELETE handler), so an entry created here for an unknown
	// id would never be cleaned up, and repeated Stop calls against
	// invalid/already-deleted ids would grow m.watchdogs without bound
	// (finding #7, code review).
	//
	// The check runs INSIDE m.mu, not before acquiring it: a delete
	// concurrent with this call can otherwise complete entirely -- including
	// its dropWatchdog and EndDelete -- between an outside-the-lock check and
	// the watchdogFor below, re-creating exactly the orphaned entry the check
	// exists to prevent. m.deleting is checked alongside it for the narrower
	// case of a delete still in flight, whose store row may not be gone yet.
	//
	// Lock ordering m.mu -> store.mu is safe and cannot deadlock: Store holds
	// no reference to Manager and never calls back into it, so the reverse
	// order does not exist anywhere in this package.
	if _, ok := m.store.Get(id); !ok || m.deleting[id] {
		return
	}
	wd := m.watchdogFor(id)
	wd.userStopped = true
	m.cancelPendingRestartLocked(id)
}

// resetWatchdogLocked cancels any pending auto-restart backoff for id (see
// cancelPendingRestartLocked), zeroes its consecutive-restart counter, and
// clears the userStopped stamp markUserStopped may have set. Called from
// startContext when resetWatchdog is true: a manual Start or Restart is the
// "operator took control" signal that should let an instance run through a
// fresh full backoff sequence on its next crash, rather than carrying
// forward however far a previous crash loop had already gotten toward
// watchdogMaxRestarts -- or remaining permanently suppressed by a stop from
// before this fresh start. Caller must hold m.mu.
func (m *Manager) resetWatchdogLocked(id string) {
	m.cancelPendingRestartLocked(id)
	if wd := m.watchdogs[id]; wd != nil {
		wd.consecutive = 0
		wd.userStopped = false
	}
}

// dropWatchdog discards id's crash-watchdog bookkeeping and cancels any
// backoff still pending for it. Mirrors dropLogs's doc comment (arch L7 /
// conc L-1): without this, m.watchdogs[id] would outlive the deleted
// instance forever. The controller's DELETE handler calls this alongside
// dropLogs after store.Delete succeeds -- BeginDelete/isDeleting is already
// in effect for the whole delete handler, so any watchdog goroutine still in
// flight at this point would refuse to relaunch on its own even without this
// call (see maybeAutoRestart/runScheduledRestart's isDeleting checks); this
// makes that immediate instead of leaving a goroutine to wake up, check, and
// exit on its own later.
func (m *Manager) dropWatchdog(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelPendingRestartLocked(id)
	delete(m.watchdogs, id)
	// m.lifecycle[id] is deliberately NOT deleted here, unlike watchdogs above.
	// lifecycleLock returns the mutex POINTER and the caller locks it after
	// m.mu has been released, so a delete here has a window in which one
	// goroutine holds the old mutex (fetched, not yet locked) while a later
	// caller for the same id gets a freshly-created second mutex from
	// lifecycleLock -- two "mutually exclusive" sections running at once for
	// one instance, which is exactly the guarantee this lock exists to make.
	// Reference counting would close that, but the thing being retained is a
	// zero-value sync.Mutex plus one map entry per instance id this process
	// has ever touched -- entries are only ever created for ids that passed an
	// existence check, so this is bounded by real instance creations, not by
	// anything a caller can drive with invalid ids (see markUserStopped for
	// the case where that distinction mattered).
}

// maybeAutoRestart is called from the exit goroutine of every process this
// Manager ever launches (startContext), right after it has recorded the
// exit's outcome and cleared m.procs/closed ps.done. It decides whether the
// crash watchdog should relaunch id, refusing whenever:
//
//   - stopRequested is true: this exact process was asked to stop by
//     StopContext -- a direct Stop, the Stop half of Restart, or the Stop
//     that BeginDelete/DELETE brackets around store.Delete. This is the
//     watchdog's primary "the user asked for this exit" signal; see
//     processState.stopRequested's doc comment.
//   - id is mid-delete (m.isDeleting). Checked independently of
//     stopRequested as defense in depth: BeginDelete's contract only
//     promises the marker is set for the duration of the delete handler, not
//     that every path to it necessarily also set stopRequested first.
//   - id's current AutoRestart flag -- re-read from the store here, not any
//     snapshot taken when this process was originally launched, since an
//     edit can toggle it while the process was running -- is false.
func (m *Manager) maybeAutoRestart(id string, exitErr error, stopRequested bool) {
	if stopRequested || m.isDeleting(id) {
		return
	}
	current, ok := m.store.Get(id)
	if !ok || !current.AutoRestart {
		return
	}
	// "exited cleanly" (the unconditional buf.Add a few lines up in
	// startContext) is a plain log line and stays as-is; this is the
	// distinct string surfaced as crash-watchdog evidence
	// (InstanceView.LastExitReason), which needs to read as unexpected --
	// exit status 0 with nobody having asked for it is still the watchdog's
	// business, not a benign shutdown.
	reason := "exited unexpectedly with status 0"
	if exitErr != nil {
		reason = exitErr.Error()
	}
	m.scheduleRestart(id, reason)
}

// watchdogBackoffDelay returns the exponential backoff delay for the given
// 1-indexed consecutive attempt number: base * 2^(attempt-1), capped at max.
// base/max are passed in rather than read directly from
// watchdogBaseBackoff/watchdogMaxBackoff so every call site reads those
// package vars exactly once, synchronously, at a point already proven safe
// against a test's var-swapping (see scheduleRestart's doc comment).
func watchdogBackoffDelay(base, max time.Duration, attempt int) time.Duration {
	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= max {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}

// scheduleRestart records id's crash evidence (LastExitReason/LastExitAt --
// visible on the view immediately, even before any relaunch is attempted)
// and, unless id has been stamped user-stopped (userStopped -- see
// markUserStopped's doc comment and fix #3, crash-watchdog concurrency
// review) or the consecutive-restart cap (watchdogMaxRestarts) was just
// reached, schedules a relaunch after an exponential backoff delay in a new
// goroutine. Called from maybeAutoRestart (which has already confirmed this
// exit was unexpected and AutoRestart is on) and, recursively, from
// runScheduledRestart when a relaunch attempt itself fails (fix #4) -- so a
// failed relaunch still consumes an attempt instead of silently stopping the
// recovery with backoff budget left.
func (m *Manager) scheduleRestart(id, reason string) {
	m.mu.Lock()
	wd := m.watchdogFor(id)
	if wd.userStopped {
		// A Stop raced in between the exit being observed
		// (maybeAutoRestart's stopRequested snapshot) and this call, or
		// between one failed relaunch attempt and this recursive retry.
		// Treat exactly like a user-intended exit: do not record this as
		// crash evidence and do not restart.
		m.mu.Unlock()
		return
	}
	wd.lastExitReason = reason
	wd.lastExitAt = time.Now().UTC()
	wd.consecutive++
	attempt := wd.consecutive
	// Every watchdog tunable this call and the goroutines it spawns need is
	// read exactly once, right here, while m.mu is held -- not later, and
	// not directly inside runScheduledRestart/watchHealthyRun's own
	// goroutines. Those goroutines can legitimately still be sleeping (up to
	// watchdogMaxBackoff, or watchdogHealthyAfter for watchHealthyRun -- both
	// many seconds in production) long after whatever test or caller
	// triggered this crash has moved on; reading the package-level vars
	// there directly raced against a test's own var-swapping cleanup
	// (withWatchdogTiming, manager_test.go) with no synchronization linking
	// the two. Capturing the values here, under m.mu -- the same mutex every
	// caller that inspects wd's fields (including every test assertion this
	// package makes) already synchronizes through -- and threading them
	// through as plain parameters from here on closes that race entirely.
	maxRestarts := watchdogMaxRestarts
	baseBackoff := watchdogBaseBackoff
	maxBackoff := watchdogMaxBackoff
	healthyAfter := watchdogHealthyAfter
	if attempt > maxRestarts {
		m.mu.Unlock()
		msg := fmt.Sprintf("auto-restart: giving up after %d consecutive crashes; last exit: %s", maxRestarts, reason)
		m.store.SetError(id, msg)
		m.log(id).Add(msg)
		return
	}
	wd.generation++
	myGen := wd.generation
	backoffCtx, cancel := context.WithCancel(m.ctx)
	wd.cancelPending = cancel
	m.mu.Unlock()

	delay := watchdogBackoffDelay(baseBackoff, maxBackoff, attempt)
	m.log(id).Add(fmt.Sprintf("crashed (%s); auto-restart attempt %d/%d in %s", reason, attempt, maxRestarts, delay))

	go m.runScheduledRestart(id, wd, backoffCtx, delay, attempt, myGen, healthyAfter)
}

// runScheduledRestart sleeps out delay (cancellable by ctx -- a Stop calling
// markUserStopped, a manual Start's resetWatchdogLocked, or Shutdown
// cancelling m.ctx, whichever this particular backoffCtx was derived from)
// and, if it completes normally and id is still eligible (not mid-delete,
// still AutoRestart, not user-stopped), relaunches it via startContext -- the
// exact same path a normal Start uses -- with resetWatchdog=false so this
// relaunch does not reset the very counters that led to it. It runs entirely
// on its own goroutine, holding m.mu only for brief bookkeeping updates, so
// calling back into startContext (which takes m.mu itself) cannot deadlock.
//
// myGen is the generation scheduleRestart minted when it armed this exact
// attempt (wd.generation at that time). The very first thing this function
// does after waking is compare myGen back against wd.generation: a mismatch
// means a *newer* attempt has since been armed (this goroutine was simply
// descheduled long enough for another crash-and-relaunch cycle to begin),
// so wd.cancelPending now belongs to that newer attempt and must not be
// touched, and this stale goroutine must not relaunch either -- otherwise it
// can clobber a live successor's cancel handle, silently losing a Stop
// delivered during the successor's own backoff (fix #2, crash-watchdog
// concurrency review). Only the current (matching-generation) goroutine may
// act past that point.
func (m *Manager) runScheduledRestart(id string, wd *watchdogState, ctx context.Context, delay time.Duration, attempt, myGen int, healthyAfter time.Duration) {
	err := backoffSleep(ctx, delay)

	m.mu.Lock()
	if wd.generation != myGen {
		// Superseded by a newer attempt -- not our cancelPending to touch,
		// and not our place to log (a deleted instance's log buffer must
		// not be lazily resurrected by a stale goroutine either).
		m.mu.Unlock()
		return
	}
	wd.cancelPending = nil
	stopped := wd.userStopped
	// ctx.Err() is rechecked here, under the lock, even though backoffSleep
	// already returned: select{} can pick its timer branch even when
	// ctx.Done() became ready at the very same instant a concurrent Stop
	// cancelled it, so err == nil alone does not prove nothing raced in at
	// the wake instant (fix #1, crash-watchdog concurrency review).
	cancelled := ctx.Err() != nil
	m.mu.Unlock()

	if stopped || err != nil || cancelled {
		// A deleted instance's log buffer must not be lazily resurrected by
		// this abort path (m.log(id) otherwise recreates it on any
		// reference) -- best-effort skip while id is mid-delete, matching
		// isDeleting's own best-effort contract elsewhere in this file.
		if !m.isDeleting(id) {
			if stopped {
				m.log(id).Add("auto-restart aborted: instance was stopped")
			} else {
				m.log(id).Add("auto-restart cancelled during backoff")
			}
		}
		return
	}
	if m.isDeleting(id) {
		return
	}
	current, ok := m.store.Get(id)
	if !ok || !current.AutoRestart {
		return
	}
	if err := m.startContext(m.ctx, id, false); err != nil {
		if errors.Is(err, errAlreadyRunning) {
			// A manual Start (or another relaunch) already won the race and
			// is the one actually running the process now; its own
			// resetWatchdogLocked call already owns this instance's
			// watchdog state. Crediting restartCount++ / spawning
			// watchHealthyRun here would misreport an attempt this call
			// never actually performed.
			m.log(id).Add("auto-restart skipped: instance is already running")
			return
		}
		if errors.Is(err, errWatchdogUserStopped) {
			// A Stop landed between this function's own userStopped check
			// above and startContext's atomic re-check under m.mu (finding
			// #1, code review) -- treat exactly like the stopped path above:
			// no crash evidence, no relaunch.
			m.log(id).Add("auto-restart aborted: instance was stopped")
			return
		}
		// Feed the failure back through scheduleRestart so it consumes a
		// consecutive-restart attempt and eventually reaches the give-up
		// path/message, instead of silently going quiet with backoff budget
		// still left (fix #4, crash-watchdog concurrency review).
		m.scheduleRestart(id, "restart failed: "+err.Error())
		return
	}
	m.mu.Lock()
	wd.restartCount++
	m.mu.Unlock()
	m.log(id).Add("auto-restart succeeded")
	go m.watchHealthyRun(id, wd, attempt, healthyAfter)
}

// watchHealthyRun resets id's consecutive-restart counter once its
// just-relaunched process has stayed up for healthyAfter -- the "the crash
// loop is actually over" signal, rather than resetting eagerly right after a
// relaunch merely starts without erroring immediately. It takes no action
// (and does not reset) if the process it is watching exits, or is no longer
// the current one (m.procs[id] != ps -- e.g. a subsequent crash-and-relaunch,
// or a manual Stop/Start racing in), by the time healthyAfter elapses.
func (m *Manager) watchHealthyRun(id string, wd *watchdogState, attempt int, healthyAfter time.Duration) {
	ps := m.state(id)
	if ps == nil {
		return
	}
	select {
	case <-ps.done:
		return
	case <-m.ctx.Done():
		return
	case <-time.After(healthyAfter):
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[id] == ps && wd.consecutive == attempt {
		wd.consecutive = 0
	}
}

// errReloadNetworkChanged is returned by ReloadContext when id's mixed port,
// controller port, or proxy bind address have changed since its process was
// launched (or last successfully hot-reloaded). Applying such a change is
// exactly the "listener" class of edit mihomo's own PUT /configs only
// recreates when the request asks it to force-recreate listeners (see
// reloadMihomoConfig's doc comment in mihomo_api.go for why this codebase
// never passes that flag) -- and even if it did, Fleet's own HTTP client
// would be left pointed at a now-stale ControllerPort/MixedPort/ProxyBind the
// moment mihomo actually rebound. Silently letting that through would either
// no-op the change or strand the controller connection, so ReloadContext
// refuses outright instead and the caller (controller.go's handleReload)
// reports that a restart is required -- never that the change hot-applied.
var errReloadNetworkChanged = errors.New("reload cannot apply a port or proxy bind change; restart the instance instead")

// reloadGenerationError wraps a failure regenerating id's runtime config
// locally (writeRuntimeConfig, prepareGeodata) -- before ReloadContext ever
// contacts mihomo. handleReload (controller.go) tells this apart from
// reloadMihomoConfig's upstream failures via errors.As, since the two
// deserve different HTTP statuses: a broken profile/local-proxy/global-chain
// config is a 422 (the instance's current stored fields don't produce a
// valid config at all), not a 502 (reserved for reloadMihomoConfig's
// failures -- an actual downstream mihomo rejection or network error).
type reloadGenerationError struct {
	err error
}

func (e reloadGenerationError) Error() string { return e.err.Error() }
func (e reloadGenerationError) Unwrap() error { return e.err }

// isDeleting reports whether id is mid-delete (BeginDelete/EndDelete,
// controller.go's DELETE handler) -- ReloadContext's guard against reloading
// into a process that is already being torn down.
func (m *Manager) isDeleting(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deleting[id]
}

// Reload is ReloadContext bound to context.Background(); see ReloadContext's
// doc comment for the full contract.
func (m *Manager) Reload(id string) error {
	return m.ReloadContext(context.Background(), id)
}

// ReloadContext regenerates id's runtime config from its current stored
// fields and profile (via config.go's writeRuntimeConfig -- the exact same
// generator StartContext uses, so the reloaded config is byte-for-byte what
// a fresh start would have produced), links any geodata files the new
// config needs (prepareGeodata, the same call StartContext makes -- without
// it, a profile edit that adds the first GEOSITE/GEOIP rule would have
// mihomo try to download the data file from scratch inside
// reloadMihomoConfig's 5s budget instead of finding it already linked), and
// asks its already-running mihomo process to apply the result in place via
// reloadMihomoConfig (mihomo_api.go), without restarting the process. On
// success it also re-applies the instance's saved proxy selections the same
// way StartContext does (restoreSelection) -- a profile without
// store-selected-node persistence otherwise resets every group to its
// default selection on any config apply, leaving Fleet's own
// SelectedProxies stale.
//
// It only ever applies to an instance that is already running: an id with no
// registered process (stopped, or still in its starting window) returns an
// "instance ... is not running" error instead of starting one -- reload is
// not a start. It also refuses outright while id is mid-delete (isDeleting)
// -- best-effort, like StartContext's identical m.deleting check, not a
// fully closed race (there is no "reloading" set the way m.starting tracks
// an in-flight Start), but it closes the common case of a reload landing on
// an instance that is already being stopped/removed.
//
// It is intentionally narrower than Start/Restart in what it will apply: if
// item's ControllerPort, MixedPort, or (normalized) ProxyBind differ from the
// values captured when the process actually launched (processState.
// controllerPort/mixedPort/proxyBind, set by StartContext and left untouched
// by every previous successful reload), it returns errReloadNetworkChanged
// without writing anything or contacting mihomo -- see that var's doc
// comment. Every other edit PendingRestart currently flags (Mode, Chain,
// LocalProxies, profile config content, ProfileID) only changes the
// generated YAML's proxies/rules/groups, which mihomo's PUT /configs applies
// unconditionally regardless of the force flag (confirmed against mihomo's
// hub/executor/executor.go ApplyConfig: only listener recreation is gated on
// force) -- and mihomo's own executor.ParseWithPath validates the file and
// rejects a broken config before ApplyConfig ever runs, so this deliberately
// does not also shell out to a local "mihomo -t" the way StartContext does;
// that would just re-validate the same file with the same parser a second
// time.
//
// reloadStart is captured before item is even read from the store, and is
// what markReloaded stamps onto the process's tracked started time -- NOT
// the wall-clock time this function returns. The subscription auto-update
// scheduler (and any other store mutation that bumps ConfigUpdatedAt) can
// land at any moment, including the window this function spends inside
// writeRuntimeConfig/prepareGeodata/reloadMihomoConfig (the last of which
// alone budgets 5s). Stamping with a "finished" timestamp would then let a
// mutation that landed *during* that window -- after the bytes this call
// actually pushed were already read -- get silently marked "applied" even
// though it wasn't. Stamping with reloadStart instead means such a mutation's
// ConfigUpdatedAt still lands after the recorded started time, so
// PendingRestart correctly keeps flagging it: a false positive (the safe
// direction), never a silently lost update. See markReloaded's doc comment
// for the rest of this.
//
// Reload failures (config/geodata generation, or mihomo rejecting the
// reload) are logged to id's log buffer and returned to the caller, but
// deliberately never go through m.store.SetError: the process is still
// running fine on its previous config, and SetError's LastError is shown by
// the UI even while status is "running" (InstanceDetail.vue's metaText /
// DashboardInstances.vue's isBad) -- setting it here would misreport a
// healthy, still-running instance as failed.
func (m *Manager) ReloadContext(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Serializes this whole check-then-work sequence against a concurrent
	// startContext or StopContext for the same id -- see the lifecycle
	// field's doc comment for the bug this closes: without it, the
	// m.procs[id] == ps re-check below and the actual work that follows
	// (writeRuntimeConfig, prepareGeodata, reloadMihomoConfig) are two
	// separate critical sections with a gap between them, and a Stop or
	// Restart landing in that gap makes the re-check meaningless.
	lock := m.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()

	// Re-check after the wait: acquiring the lock above can block for as long
	// as a concurrent start or stop takes, and a caller whose deadline expired
	// in that window must not still go on to rewrite the runtime config and
	// push it into the process.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Captured before any store read below -- see this function's doc comment
	// and markReloaded's for why this, not time.Now() after the reload
	// succeeds, is what gets stamped onto ps.started.
	reloadStart := time.Now().UTC()

	if m.isDeleting(id) {
		return fmt.Errorf("instance %q is being deleted", id)
	}
	ps := m.state(id)
	if ps == nil {
		return fmt.Errorf("instance %q is not running", id)
	}
	item, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	profile, ok := m.store.GetProfile(item.ProfileID)
	if !ok {
		// Typed (store.go's profileNotFoundError), not a bare fmt.Errorf like
		// StartContext's identical check above: handleReload classifies this
		// via errors.Is(err, errProfileNotFound) into a 404, which needs the
		// typed wrapper to actually match.
		return profileNotFoundError{id: item.ProfileID}
	}
	if item.ControllerPort != ps.controllerPort || item.MixedPort != ps.mixedPort || instanceProxyBind(item.ProxyBind) != ps.proxyBind {
		return errReloadNetworkChanged
	}

	// Re-verify id's process hasn't been swapped out from under this call.
	// The lifecycle lock held above is what actually closes the window a
	// concurrent StopContext or Restart (Stop then Start) could otherwise
	// land in between this check and the work below (finding #2, code
	// review) -- neither can run for this id until this call returns. What
	// remains here is a cheap correctness assertion against a process that
	// exited on its own (a crash): that path never goes through StopContext
	// and so never takes the lifecycle lock, and can clear m.procs[id]
	// (and let a watchdog relaunch replace it) at any moment, lock or no
	// lock.
	//
	// The crash path cannot be brought under this lock: StopContext holds it
	// while waiting on ps.done, which only the exit goroutine closes, so
	// making that goroutine take the lock deadlocks Stop outright. Two
	// consequences are accepted rather than papered over:
	//
	//   - A watchdog relaunch's own writeRuntimeConfig can interleave with the
	//     one below. Both go through writeFileAtomic (config.go) and both
	//     derive their bytes from the same stored item+profile, so the loser's
	//     temp file is simply discarded -- no torn or blended config is
	//     reachable, only a redundant write.
	//   - reloadMihomoConfig below may reach a controller port whose process
	//     died a moment ago. That is the same check-then-connect residual
	//     handleMihomoProxy documents (mihomo_proxy.go): no locking makes the
	//     send atomic with the liveness check while instances are addressed by
	//     a reusable localhost TCP port. See docs/known-limitations.md.
	m.mu.RLock()
	current := m.procs[id]
	m.mu.RUnlock()
	if current != ps {
		return fmt.Errorf("instance %q is not running", id)
	}

	buf := m.log(id)
	if _, err := writeRuntimeConfig(item, profile); err != nil {
		buf.Add("reload failed: " + err.Error())
		return reloadGenerationError{err: err}
	}
	if _, err := m.prepareGeodata(item); err != nil {
		buf.Add("reload geodata prepare failed: " + err.Error())
		return reloadGenerationError{err: err}
	}
	if err := reloadMihomoConfig(ctx, item); err != nil {
		buf.Add("reload failed: " + err.Error())
		return err
	}
	buf.Add("reloaded runtime config without restart")
	m.markReloaded(id, ps, reloadStart)
	// Mirrors StartContext's own restoreSelection call: re-apply saved
	// selections against the freshly reloaded config, since a profile without
	// store-selected-node persistence resets group selections to their
	// default on any config apply. mihomo's API is already up here (this
	// function just talked to it via reloadMihomoConfig above), so this
	// converges on its first attempt; it still runs in the background, like
	// Start's call, so the reload response never waits on it.
	go m.restoreSelection(m.ctx, item, ps, buf)
	return nil
}

// markReloaded records that id's already-running process just had a fresh
// runtime config pushed into it via a successful ReloadContext call, without
// restarting the process. It stamps ps.started with reloadStart -- captured
// by ReloadContext before it ever read item/profile from the store, not
// time.Now() at this point -- and only when both:
//
//   - m.procs[id] == ps still holds: the process ReloadContext generated
//     this config for is still the one running. If it died and a fresh Start
//     replaced it mid-reload, that new process already has its own started
//     time from StartContext, which this must not stomp with a stamp from an
//     older reload that no longer describes what it's running.
//   - reloadStart is strictly after ps.started's current value: monotonic
//     progress only. Two overlapping ReloadContext calls can finish out of
//     the order they started in; if a later-starting, faster call already
//     advanced ps.started past this (earlier-starting, slower) call's own
//     reloadStart, applying the earlier stamp would regress PendingRestart's
//     tracked time backward and could wrongly clear it for an edit the
//     faster reload never actually saw.
//
// See ReloadContext's doc comment for why reloadStart -- not a timestamp
// taken here, after the reload has already finished -- is what closes the
// lost-update window between reading item and finishing the reload.
func (m *Manager) markReloaded(id string, ps *processState, reloadStart time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[id] == ps && reloadStart.After(ps.started) {
		ps.started = reloadStart
	}
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

// dropLogs discards id's log buffer (arch L7 / conc L-1,
// docs/review-2026-07-11-go-architecture.md and
// docs/review-2026-07-11-go-concurrency-performance.md): without this,
// m.logs[id] outlived the instance itself once deleted -- nothing ever
// removed the map entry, so a long-running fleet that frequently creates and
// deletes instances would slowly accumulate abandoned (up to 1000-line)
// buffers. The controller's DELETE handler calls this after store.Delete
// succeeds. m.log(id) lazily recreates an empty buffer if id is ever
// referenced again (e.g. a slug reused by a brand new instance), so calling
// this is always safe.
func (m *Manager) dropLogs(id string) {
	m.mu.Lock()
	delete(m.logs, id)
	m.mu.Unlock()
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
	// Cancel every in-flight restoreSelection goroutine (conc L-3) up front;
	// safe to call more than once (main.go currently calls Shutdown twice --
	// once explicitly, once via a deferred cleanup -- and context.CancelFunc
	// is idempotent).
	m.cancel()

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

// AnyRunning reports whether ANY instance currently has a running process or
// is in its StartContext preparation window (same "busy" definition as
// Busy, just fleet-wide), returning one such instance's id for use in an
// error message. Used to gate the mihomo core binary swap
// (core_update.go): every running instance's process holds the on-disk
// binary open (mmap'd/exec'd), so replacing that file out from under them
// is exactly the "binary-in-use" hazard the security review calls out --
// refuse the swap outright rather than let os.Rename either fail
// unpredictably (Windows) or silently succeed while a stale process image
// keeps running against ordinary rename semantics (unix). Geodata swaps
// deliberately do NOT gate on this: mihomo does not hold geoip.dat/
// geosite.dat open the way it holds its own executable image, and swapping
// the data-dir source file never touches a running instance's own
// directory copy (geodata.go links/copies into each instance dir, so a
// rename of the shared source only affects what the *next* prepareGeodata
// picks up).
func (m *Manager) AnyRunning() (id string, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for instanceID := range m.starting {
		return instanceID, true
	}
	for instanceID := range m.procs {
		return instanceID, true
	}
	return "", false
}

// BeginCoreUpdate atomically (a) refuses if any instance is currently
// running/starting -- the same precondition AnyRunning alone checks -- and
// (b) if so, arms coreUpdating so startContext refuses every Start for as
// long as the caller holds the gate. Doing both under one m.mu.Lock is what
// actually closes the TOCTOU a standalone "if AnyRunning() { refuse }"
// followed by a separate write would leave: without this, an instance could
// start in the gap between the check and the flag being set, exactly the
// gap core_update.go's ApplyCoreUpdate needs closed for its multi-minute
// download+verify+swap. Callers MUST pair a successful call with
// EndCoreUpdate on every exit path (defer).
func (m *Manager) BeginCoreUpdate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for instanceID := range m.starting {
		return fmt.Errorf("instance %q is starting", instanceID)
	}
	for instanceID := range m.procs {
		return fmt.Errorf("instance %q is running", instanceID)
	}
	if m.coreUpdating {
		return errors.New("a core update is already in progress")
	}
	m.coreUpdating = true
	return nil
}

// EndCoreUpdate releases the gate BeginCoreUpdate armed. Safe to call even
// when BeginCoreUpdate never actually armed it (idempotent no-op), so a
// caller can unconditionally defer this right after checking
// BeginCoreUpdate's error without an extra branch.
func (m *Manager) EndCoreUpdate() {
	m.mu.Lock()
	m.coreUpdating = false
	m.mu.Unlock()
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

// restoreSelection re-applies item's saved proxy selections against the
// mihomo controller that was just launched, retrying for up to 5s while the
// process finishes bringing its API up. ctx bounds the whole call by the
// Manager's lifetime (see StartContext's call site); ps lets it notice the
// process has already exited (conc L-3,
// docs/review-2026-07-11-go-concurrency-performance.md) instead of spending
// the rest of the 5s window firing requests at a port nothing is listening
// on anymore.
func (m *Manager) restoreSelection(ctx context.Context, item *Instance, ps *processState, buf *logBuffer) {
	selections := normalizeSelections(item.SelectedProxies, item.SelectedGroup, item.SelectedProxy)
	if len(selections) == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ps.done:
			return
		case <-ctx.Done():
			return
		default:
		}
		pending := 0
		for group, proxy := range selections {
			if err := putMihomoProxy(ctx, item, group, proxy); err != nil {
				pending++
			} else {
				buf.Add(fmt.Sprintf("restored proxy selection %s -> %s", group, proxy))
				delete(selections, group)
			}
		}
		if pending == 0 {
			return
		}
		if err := sleepWithContext(ctx, 200*time.Millisecond); err != nil {
			return
		}
	}
	for group, proxy := range selections {
		buf.Add(fmt.Sprintf("proxy selection restore timed out for %s -> %s", group, proxy))
	}
}
