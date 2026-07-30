package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeMihomo builds a shell script that stands in for the real mihomo
// binary in tests. It tolerates the two invocation shapes Manager uses:
//   - config test: "-t -d <dir> -f <path>" — exits 0 after testDelaySeconds,
//     mirroring how manager.go's testConfig probes a config without binding
//     any ports.
//   - real start: "-d <dir> -f <path>" — behaves like a long-running process
//     that either exits on SIGTERM (respondsToTerm) or ignores it until
//     force-killed (stubborn), matching the two branches of StopContext.
//
// On a real start it also touches a "<instanceDir>/.fake-mihomo-ready"
// marker right after installing its TERM trap, so tests can wait for the
// trap to actually be in place before sending a signal instead of racing
// cmd.Start() returning against the shell finishing its setup.
func writeFakeMihomo(t *testing.T, respondsToTerm bool, testDelaySeconds int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mihomo")
	trap := "trap '' TERM"
	if respondsToTerm {
		trap = "trap 'exit 0' TERM"
	}
	script := fmt.Sprintf(`#!/bin/sh
instance_dir=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-d" ]; then
    instance_dir="$arg"
  fi
  if [ "$arg" = "-t" ]; then
    sleep %d
    exit 0
  fi
  prev="$arg"
done
%s
if [ -n "$instance_dir" ]; then
  : > "$instance_dir/.fake-mihomo-ready"
fi
while true; do
  sleep 1
done
`, testDelaySeconds, trap)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// waitForFakeMihomoReady blocks until the fake mihomo process for item has
// installed its TERM trap (see writeFakeMihomo), so a subsequent Stop()
// exercises the intended graceful/stubborn signal-handling path instead of
// racing the shell's own startup.
func waitForFakeMihomoReady(t *testing.T, item *Instance) {
	t.Helper()
	marker := filepath.Join(filepath.Dir(item.RuntimeConfigPath), ".fake-mihomo-ready")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fake mihomo never became ready (missing %s)", marker)
}

// writeCrashingFakeMihomo builds a fake mihomo that crashes (exit 1, no
// signal involved -- an "unexpected exit" the watchdog must react to) on its
// first crashCount real-start invocations, then behaves like
// writeFakeMihomo(t, true, 0)'s shape (a normal long-running process that
// exits cleanly on SIGTERM) on every invocation after that. Each real-start
// invocation is counted via a ".start-count" file inside the instance
// directory, since the watchdog relaunches the exact same binary path and a
// fresh shell process cannot otherwise tell which attempt it is.
// crashCount=1 lets a test assert a successful auto-restart after exactly
// one crash; a very large crashCount (e.g. 999) makes every invocation
// crash, for exercising the backoff-cap giveup path. The -t config-test
// invocation always exits 0 immediately, matching writeFakeMihomo's
// contract.
func writeCrashingFakeMihomo(t *testing.T, crashCount int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mihomo")
	script := fmt.Sprintf(`#!/bin/sh
instance_dir=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-d" ]; then
    instance_dir="$arg"
  fi
  if [ "$arg" = "-t" ]; then
    exit 0
  fi
  prev="$arg"
done
trap 'exit 0' TERM
count_file="$instance_dir/.start-count"
n=0
if [ -f "$count_file" ]; then n=$(cat "$count_file"); fi
n=$((n+1))
echo "$n" > "$count_file"
if [ -n "$instance_dir" ]; then
  : > "$instance_dir/.fake-mihomo-ready"
fi
if [ "$n" -le %d ]; then
  sleep 0.05
  exit 1
fi
while true; do
  sleep 1
done
`, crashCount)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// withWatchdogTiming overrides the package-level watchdog backoff/cap/
// healthy-threshold vars (manager.go) for the duration of t's run, mirroring
// store_test.go's withPortFree pattern: tests exercise the real backoff/cap/
// healthy-reset logic without a real test run waiting out up to 30s of
// backoff or a 60s healthy-run window. Like withPortFree, this is safe
// without an extra mutex only because this package's tests do not use
// t.Parallel (see withPortFree's own doc comment).
func withWatchdogTiming(t *testing.T, base, max time.Duration, maxRestarts int, healthyAfter time.Duration) {
	t.Helper()
	origBase, origMax, origCap, origHealthy := watchdogBaseBackoff, watchdogMaxBackoff, watchdogMaxRestarts, watchdogHealthyAfter
	watchdogBaseBackoff = base
	watchdogMaxBackoff = max
	watchdogMaxRestarts = maxRestarts
	watchdogHealthyAfter = healthyAfter
	t.Cleanup(func() {
		watchdogBaseBackoff = origBase
		watchdogMaxBackoff = origMax
		watchdogMaxRestarts = origCap
		watchdogHealthyAfter = origHealthy
	})
}

func boolPtr(b bool) *bool { return &b }

func newManagerTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createManagerTestInstance(t *testing.T, store *Store, name string, mixedPort, controllerPort int) *Instance {
	t.Helper()
	item, err := store.Create(name, "", defaultUserConfig, mixedPort, controllerPort)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestManagerStartAndStopUsesDoneChannel(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "Graceful", 28101, 29101)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ps := manager.state(item.ID)
	if ps == nil {
		t.Fatal("expected state(id) != nil after Start")
	}
	if ps.cmd.Process == nil || ps.cmd.Process.Pid <= 0 {
		t.Fatalf("expected a positive PID, got %#v", ps.cmd.Process)
	}
	if !manager.Busy(item.ID) {
		t.Fatal("expected Busy(id) to be true while running")
	}
	waitForFakeMihomoReady(t, item)

	started := time.Now()
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	elapsed := time.Since(started)
	// N5 (docs/review-2026-07-11-fix-verification-round4.md): this bound
	// guards against a regression to the old 100ms-polling-plus-3s-SIGTERM-
	// window Stop() path, not against reasonable scheduling jitter -- a 1s
	// bound flaked under parallel test load (observed 1.006s) even though
	// the done-channel path was taken. 3s is still far below what the old
	// polling path would take (its own worst case is exercised separately by
	// TestManagerStopForceKillsStubbornProcess, asserting >= 3s), so this
	// still proves the done-channel path fired instead of falling through to
	// polling.
	if elapsed >= 3*time.Second {
		t.Fatalf("Stop() took %s, want well under 3s via the done channel (no 100ms polling)", elapsed)
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected procs to be cleared after Stop")
	}
	if manager.Busy(item.ID) {
		t.Fatal("expected Busy(id) to be false after Stop")
	}
}

func TestManagerStopForceKillsStubbornProcess(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, false, 0))
	item := createManagerTestInstance(t, store, "Stubborn", 28102, 29102)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	started := time.Now()
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() error = %v, want the force-kill path to still complete cleanly", err)
	}
	elapsed := time.Since(started)
	if elapsed < 3*time.Second {
		t.Fatalf("Stop() took %s, want >= 3s (SIGTERM grace before force kill)", elapsed)
	}
	if elapsed > 4500*time.Millisecond {
		t.Fatalf("Stop() took %s, want well under the 3s+1s worst case plus overhead", elapsed)
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected procs to be cleared after force kill")
	}
}

func TestManagerStopWhileStartingCancelsStart(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	// A one second delay in the "-t" config-test path gives the test a wide,
	// deterministic window to call Stop before StartContext ever reaches
	// cmd.Start().
	manager := NewManager(store, writeFakeMihomo(t, true, 1))
	item := createManagerTestInstance(t, store, "Cancelled", 28103, 29103)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	startErr := make(chan error, 1)
	go func() { startErr <- manager.Start(item.ID) }()

	deadline := time.Now().Add(2 * time.Second)
	busySeen := false
	for time.Now().Before(deadline) {
		if manager.Busy(item.ID) {
			busySeen = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !busySeen {
		t.Fatal("expected Busy(id) to become true while StartContext is preparing")
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected no registered process yet during the config-test delay window")
	}

	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() while starting error = %v, want nil (nothing was ever registered to stop)", err)
	}

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("expected the in-flight Start() to fail once cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() goroutine did not return after Stop cancelled it")
	}

	if manager.state(item.ID) != nil {
		t.Fatal("expected no process to be left running after Stop cancelled the start")
	}
	if manager.Busy(item.ID) {
		t.Fatal("expected Busy(id) to be false once the cancelled start settles")
	}
	for _, line := range manager.Logs(item.ID) {
		if strings.Contains(line, "started mihomo pid=") {
			t.Fatalf("expected cmd.Start() to never run once cancelled, but found log line: %q", line)
		}
	}
}

func TestManagerConcurrentStartSharedPortOnlyOneSucceeds(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))

	first := createManagerTestInstance(t, store, "First", 28104, 29104)
	second := createManagerTestInstance(t, store, "Second", 28105, 29105)
	t.Cleanup(func() {
		_ = manager.Stop(first.ID)
		_ = manager.Stop(second.ID)
	})

	// Store enforces global port uniqueness on every create/update path, so
	// forcing a collision to exercise Manager.reservedPorts' defense-in-depth
	// requires reaching past the API into the store's internal record.
	store.mu.Lock()
	store.items[second.ID].MixedPort = store.items[first.ID].MixedPort
	store.mu.Unlock()

	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make(map[string]error, 2)
	for _, id := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := manager.Start(id)
			mu.Lock()
			errs[id] = err
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	successes, failures := 0, 0
	var failureErr error
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			failures++
			failureErr = err
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("errs = %#v, want exactly one success and one failure", errs)
	}
	if failureErr == nil || !strings.Contains(failureErr.Error(), "in use") {
		t.Fatalf("failure error = %v, want a message mentioning port in use", failureErr)
	}

	manager.mu.Lock()
	leftoverReserved := len(manager.reservedPorts)
	leftoverStarting := len(manager.starting)
	leftoverStarts := len(manager.starts)
	manager.mu.Unlock()
	if leftoverReserved != 0 {
		t.Fatalf("reservedPorts leaked entries: %#v", manager.reservedPorts)
	}
	if leftoverStarting != 0 {
		t.Fatalf("starting leaked entries: %#v", manager.starting)
	}
	if leftoverStarts != 0 {
		t.Fatalf("starts leaked entries: %#v", manager.starts)
	}
}

func TestManagerBusyTrueWhileStarting(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 1))
	item := createManagerTestInstance(t, store, "BusyWindow", 28106, 29106)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if manager.Busy(item.ID) {
		t.Fatal("expected instance to be idle before Start")
	}

	startErr := make(chan error, 1)
	go func() { startErr <- manager.Start(item.ID) }()

	deadline := time.Now().Add(2 * time.Second)
	busySeen := false
	for time.Now().Before(deadline) {
		if manager.Busy(item.ID) {
			busySeen = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !busySeen {
		t.Fatal("expected Busy(id) to become true during the starting window")
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected Busy(id) to be observed before a process is registered (still in the -t delay)")
	}

	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return in time")
	}
	if !manager.Busy(item.ID) {
		t.Fatal("expected Busy(id) to remain true (running) after Start completes")
	}
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if manager.Busy(item.ID) {
		t.Fatal("expected Busy(id) to be false after Stop")
	}
}

// TestManagerBeginDeleteBlocksStartUntilEndDelete covers N4: a DELETE
// handler brackets Stop+store.Delete with BeginDelete/EndDelete so a
// concurrent POST .../start (e.g. from another client) cannot win the race
// and launch a process that immediately becomes orphaned once the instance
// record is removed. StartContext must refuse while the marker is set and
// behave normally again once it is cleared.
func TestManagerBeginDeleteBlocksStartUntilEndDelete(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "DeleteGuard", 28107, 29107)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	manager.BeginDelete(item.ID)
	if err := manager.Start(item.ID); err == nil {
		t.Fatal("expected Start() to fail while BeginDelete is in effect")
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected no process to be registered while deletion is in progress")
	}

	manager.EndDelete(item.ID)
	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v, want nil once EndDelete clears the marker", err)
	}
	if manager.state(item.ID) == nil {
		t.Fatal("expected a registered process after Start() succeeds post-EndDelete")
	}
}

// TestManagerStartLogsDNSListenWarning covers arch M3
// (docs/review-2026-07-11-go-architecture.md): dns.listen is deliberately
// not stripped from the runtime config (it may be an intentional
// single-instance choice), but starting an instance whose profile sets it
// should log a warning about the cross-instance bind-conflict risk instead
// of staying silent about it.
func TestManagerStartLogsDNSListenWarning(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "DNSListen", 28110, 29110)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	profile, ok := store.GetProfile(item.ProfileID)
	if !ok {
		t.Fatal("expected the auto-created profile to exist")
	}
	config := defaultUserConfig + "dns:\n  enable: true\n  listen: 0.0.0.0:1053\n"
	if err := os.WriteFile(profile.ConfigPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	found := false
	for _, line := range manager.Logs(item.ID) {
		if strings.Contains(line, "dns.listen") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a dns.listen warning in the instance log, got: %v", manager.Logs(item.ID))
	}
}

// TestManagerStartDoesNotLogDNSListenWarningWithoutIt is the negative
// counterpart of TestManagerStartLogsDNSListenWarning: a profile that never
// sets dns.listen should never produce the warning.
func TestManagerStartDoesNotLogDNSListenWarningWithoutIt(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "NoDNSListen", 28111, 29111)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	for _, line := range manager.Logs(item.ID) {
		if strings.Contains(line, "dns.listen") {
			t.Fatalf("unexpected dns.listen warning with no dns.listen in the profile config: %q", line)
		}
	}
}

// TestManagerViewReportsPendingRestartAfterRunningUpdate covers arch M5
// (docs/review-2026-07-11-go-architecture.md): editing a running instance's
// stored fields (here, Mode) must surface as InstanceView.PendingRestart
// until the instance is actually restarted, instead of silently implying
// the change already took effect. The Mode change here must be an actual
// change (rule -> global-chain, not rule -> rule) now that PendingRestart is
// derived from ConfigUpdatedAt (N2, docs/review-2026-07-11-fix-verification-
// round4.md), which UpdateWithOptions only bumps when a config-affecting
// field's *value* actually changes.
func TestManagerViewReportsPendingRestartAfterRunningUpdate(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "PendingRestart", 28112, 29112)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the running instance")
	}
	if view.PendingRestart {
		t.Fatal("expected PendingRestart to be false immediately after start")
	}

	// Ensure the update's ConfigUpdatedAt strictly postdates ps.started.
	time.Sleep(5 * time.Millisecond)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Mode: InstanceModeGlobalChain}); err != nil {
		t.Fatalf("UpdateWithOptions() error = %v", err)
	}

	view, ok = manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the running instance")
	}
	if !view.PendingRestart {
		t.Fatal("expected PendingRestart to be true after updating the running instance's stored fields")
	}

	views := manager.Views()
	found := false
	for _, v := range views {
		if v.ID == item.ID {
			found = true
			if !v.PendingRestart {
				t.Fatal("expected Views() to also report PendingRestart for the updated running instance")
			}
		}
	}
	if !found {
		t.Fatalf("expected Views() to include %q", item.ID)
	}
}

func TestManagerViewsReportPendingRestartForAllSharedProfileInstances(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	first := createManagerTestInstance(t, store, "Shared A", 28113, 29113)
	second, err := store.Create("Shared B", first.ProfileID, "", 28114, 29114)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := createManagerTestInstance(t, store, "Unrelated", 28115, 29115)
	for _, item := range []*Instance{first, second, unrelated} {
		item := item
		t.Cleanup(func() { _ = manager.Stop(item.ID) })
		if err := manager.Start(item.ID); err != nil {
			t.Fatalf("Start(%s) error = %v", item.ID, err)
		}
		waitForFakeMihomoReady(t, item)
	}

	time.Sleep(5 * time.Millisecond)
	nextConfig := defaultUserConfig + "\n# shared update\n"
	if _, err := store.PatchProfile(first.ProfileID, ProfilePatch{Config: &nextConfig}); err != nil {
		t.Fatalf("PatchProfile() error = %v", err)
	}

	wantPending := map[string]bool{
		first.ID:     true,
		second.ID:    true,
		unrelated.ID: false,
	}
	for _, view := range manager.Views() {
		want, ok := wantPending[view.ID]
		if !ok {
			continue
		}
		if view.PendingRestart != want {
			t.Fatalf("PendingRestart for %s = %v, want %v", view.ID, view.PendingRestart, want)
		}
		delete(wantPending, view.ID)
	}
	if len(wantPending) != 0 {
		t.Fatalf("missing views for instances: %v", wantPending)
	}
}

// TestManagerViewPendingRestartUnaffectedBySetSelection covers N2's main
// fix (docs/review-2026-07-11-fix-verification-round4.md): decorateStatus
// used to compare item.UpdatedAt, which Store.SetSelection also bumps on
// every call -- so selecting a node on a running instance (already applied
// live via putMihomoProxy, controller.go, before SetSelection ever runs)
// incorrectly and permanently flipped PendingRestart true, contradicting the
// "already applied" message the UI shows for that same action.
func TestManagerViewPendingRestartUnaffectedBySetSelection(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "SelectionNoRestart", 28120, 29120)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	time.Sleep(5 * time.Millisecond)
	if _, err := store.SetSelection(item.ID, "Proxy", "DIRECT"); err != nil {
		t.Fatalf("SetSelection() error = %v", err)
	}

	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the running instance")
	}
	if view.PendingRestart {
		t.Fatal("expected PendingRestart to stay false after SetSelection on a running instance")
	}
}

// TestManagerViewPendingRestartUnaffectedByNameOnlyUpdate is N2's other
// false-positive case: renaming a running instance does not change anything
// the generated runtime config depends on.
func TestManagerViewPendingRestartUnaffectedByNameOnlyUpdate(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "RenameNoRestart", 28121, 29121)
	t.Cleanup(func() { _ = manager.Stop(item.ID) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	time.Sleep(5 * time.Millisecond)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{Name: "Renamed"}); err != nil {
		t.Fatalf("UpdateWithOptions() error = %v", err)
	}

	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the running instance")
	}
	if view.PendingRestart {
		t.Fatal("expected PendingRestart to stay false after a name-only update on a running instance")
	}
}

// TestManagerRestoreSelectionExitsWhenProcessDies covers conc L-3
// (docs/review-2026-07-11-go-concurrency-performance.md): once ps.done
// closes, restoreSelection must give up promptly instead of continuing its
// up-to-5s retry loop against a controller port nothing is listening on
// anymore. item.ControllerPort here points nowhere real, so every
// putMihomoProxy attempt fails immediately (connection refused); the old
// implementation's unconditional time.Sleep(200ms) loop had no way to
// notice ps.done at all and would have run for the entire 5s window.
func TestManagerRestoreSelectionExitsWhenProcessDies(t *testing.T) {
	store := newManagerTestStore(t)
	manager := NewManager(store, "")
	item := createManagerTestInstance(t, store, "RestoreExit", 28113, 29113)
	if _, err := store.SetSelection(item.ID, "Proxy", "US-01"); err != nil {
		t.Fatal(err)
	}
	fresh, ok := store.Get(item.ID)
	if !ok {
		t.Fatal("expected the instance to still exist")
	}

	ps := &processState{done: make(chan struct{})}
	buf := newLogBuffer(100)

	returned := make(chan struct{})
	go func() {
		manager.restoreSelection(context.Background(), fresh, ps, buf)
		close(returned)
	}()
	close(ps.done)

	select {
	case <-returned:
	case <-time.After(1 * time.Second):
		t.Fatal("restoreSelection did not exit promptly after ps.done closed (want well under the 5s retry window)")
	}
}

// --- Crash watchdog / auto-restart (#2) ---

// waitForWatchdogCondition polls cond (reading Manager's internal maps under
// m.mu, mirroring how the rest of this file already inspects
// reservedPorts/starting/starts directly) until it returns true or timeout
// elapses, failing the test otherwise.
func waitForWatchdogCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestManagerAutoRestartRelaunchesOnCrash covers the core #2 contract: an
// unexpected exit (not a user Stop/Restart/Delete) on an AutoRestart
// instance is relaunched via the same StartContext path a normal start
// uses, and the relaunch is recorded as runtime evidence (RestartCount,
// LastExitReason) on the view.
func TestManagerAutoRestartRelaunchesOnCrash(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 10*time.Millisecond, 200*time.Millisecond, 5, 500*time.Millisecond)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 1))
	item := createManagerTestInstance(t, store, "CrashOnce", 28130, 29130)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	// The first invocation crashes on its own after ~0.05s; the watchdog
	// should observe that and relaunch (crashCount=1, so the second
	// invocation is the long-running one). Waiting on restartCount==1
	// specifically (not just state(id) != nil) matters here: state(id) is
	// already non-nil the instant Start() above returns, well before the
	// first invocation has even had a chance to crash.
	waitForWatchdogCondition(t, 3*time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		wd := manager.watchdogs[item.ID]
		return wd != nil && wd.restartCount == 1
	})
	waitForWatchdogCondition(t, 3*time.Second, func() bool {
		return manager.state(item.ID) != nil
	})

	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if wd == nil || wd.restartCount != 1 {
		t.Fatalf("watchdog state = %+v, want restartCount 1", wd)
	}

	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the instance")
	}
	if view.RestartCount != 1 {
		t.Fatalf("view.RestartCount = %d, want 1", view.RestartCount)
	}
	if view.LastExitReason == "" {
		t.Fatal("expected LastExitReason to be recorded")
	}
	if view.Status != "running" {
		t.Fatalf("view.Status = %q, want running after a successful auto-restart", view.Status)
	}
}

// TestManagerAutoRestartDisabledDoesNotRelaunch is the negative counterpart:
// with AutoRestart off, a crash must behave exactly as it does today --
// the instance just goes dead, no relaunch.
func TestManagerAutoRestartDisabledDoesNotRelaunch(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 10*time.Millisecond, 200*time.Millisecond, 5, 500*time.Millisecond)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item := createManagerTestInstance(t, store, "CrashNoAutoRestart", 28131, 29131)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	// Wait for the crash to be observed, then give the watchdog ample extra
	// time to have (wrongly) relaunched the instance if AutoRestart's gate
	// were broken.
	waitForWatchdogCondition(t, 2*time.Second, func() bool {
		return manager.state(item.ID) == nil
	})
	time.Sleep(300 * time.Millisecond)

	if manager.state(item.ID) != nil {
		t.Fatal("expected the instance to stay stopped after a crash with AutoRestart off")
	}
	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if wd != nil && wd.restartCount != 0 {
		t.Fatalf("restartCount = %d, want 0 with AutoRestart off", wd.restartCount)
	}
	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the instance")
	}
	if view.Status != "error" {
		t.Fatalf("view.Status = %q, want error (today's unmodified crash behavior)", view.Status)
	}
}

// TestManagerUserStopDoesNotTriggerAutoRestart proves a direct user Stop on
// an AutoRestart instance never schedules a relaunch, even though the
// process's own wait goroutine observes the exact same "cmd.Wait()
// returned" event a crash would.
func TestManagerUserStopDoesNotTriggerAutoRestart(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 10*time.Millisecond, 200*time.Millisecond, 5, 500*time.Millisecond)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "StopNoRestart", 28132, 29132)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Give the watchdog ample time to have (wrongly) relaunched the instance
	// if the user-stop gating (processState.stopRequested) were broken.
	time.Sleep(200 * time.Millisecond)
	if manager.state(item.ID) != nil {
		t.Fatal("expected the instance to stay stopped after a user Stop, not be auto-restarted")
	}
	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if wd != nil && wd.restartCount != 0 {
		t.Fatalf("restartCount = %d, want 0 after a user Stop", wd.restartCount)
	}
}

// TestManagerRestartIsNotCountedAsCrash covers Restart (Stop then Start):
// the Stop half must mark stopRequested on the exiting process exactly like
// a direct Stop does, so a manual Restart never itself triggers the
// watchdog and never runs away accumulating restartCount.
func TestManagerRestartIsNotCountedAsCrash(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 10*time.Millisecond, 200*time.Millisecond, 5, 500*time.Millisecond)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "RestartNoCrash", 28133, 29133)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	if err := manager.Restart(item.ID); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if manager.state(item.ID) == nil {
		t.Fatal("expected Restart() to leave the instance running")
	}

	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if wd != nil && wd.restartCount != 0 {
		t.Fatalf("restartCount = %d, want 0 -- a manual Restart must never count as an auto-restart", wd.restartCount)
	}
}

// TestManagerDeleteMidRunPreventsAutoRestart mirrors the DELETE handler's
// own BeginDelete/Stop/dropWatchdog/EndDelete sequence (controller.go)
// against an instance that crashed and has a backoff pending: the delete
// must win the race, leave nothing running, and dropWatchdog must remove
// the instance's bookkeeping so nothing leaks.
func TestManagerDeleteMidRunPreventsAutoRestart(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 300*time.Millisecond, 2*time.Second, 5, 2*time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item := createManagerTestInstance(t, store, "DeleteMidRun", 28134, 29134)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	// Wait for the crash to be observed and a backoff to be scheduled
	// (watchdogs[id].cancelPending set) before deleting.
	waitForWatchdogCondition(t, 2*time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		wd := manager.watchdogs[item.ID]
		return wd != nil && wd.cancelPending != nil
	})

	// Mirror the DELETE handler's own sequence (controller.go's
	// handleInstanceRoot MethodDelete case): BeginDelete, Stop, (store.Delete
	// would run here), dropLogs+dropWatchdog, EndDelete.
	manager.BeginDelete(item.ID)
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() during delete error = %v", err)
	}
	manager.dropWatchdog(item.ID)
	manager.EndDelete(item.ID)

	manager.mu.Lock()
	_, stillTracked := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if stillTracked {
		t.Fatal("expected dropWatchdog to remove the instance's watchdog bookkeeping")
	}

	// Give any leaked goroutine ample time to misbehave (the backoff this
	// test configured is 300ms-2s; this comfortably outlasts a cancellation
	// but not a real relaunch).
	time.Sleep(400 * time.Millisecond)
	if manager.state(item.ID) != nil {
		t.Fatal("expected no relaunch after delete mid-run")
	}
}

// TestManagerAutoRestartGivesUpAfterConsecutiveCap covers the
// watchdogMaxRestarts giveup path: an instance that keeps crashing
// immediately after every relaunch stops being relaunched once the
// consecutive-restart cap is exceeded, and lands in a clearly-explained
// error state instead of retrying forever.
func TestManagerAutoRestartGivesUpAfterConsecutiveCap(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 5*time.Millisecond, 20*time.Millisecond, 3, 10*time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item := createManagerTestInstance(t, store, "GiveUp", 28135, 29135)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var lastErr string
	waitForWatchdogCondition(t, 3*time.Second, func() bool {
		fresh, ok := store.Get(item.ID)
		if ok && strings.Contains(fresh.LastError, "giving up") {
			lastErr = fresh.LastError
			return true
		}
		return false
	})
	if lastErr == "" {
		t.Fatal("expected the watchdog to eventually give up and record a clear reason")
	}

	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	consecutive := 0
	if wd != nil {
		consecutive = wd.consecutive
	}
	manager.mu.Unlock()
	if consecutive <= watchdogMaxRestarts {
		t.Fatalf("consecutive = %d, want > watchdogMaxRestarts (%d) once the watchdog gives up", consecutive, watchdogMaxRestarts)
	}

	view, ok := manager.View(item.ID)
	if !ok {
		t.Fatal("expected a view for the instance")
	}
	if view.Status != "error" {
		t.Fatalf("view.Status = %q, want error once the watchdog gives up", view.Status)
	}
	if manager.state(item.ID) != nil {
		t.Fatal("expected no process running once the watchdog has given up")
	}
}

// TestManagerStopDuringBackoffCancelsPendingRestart proves a Stop arriving
// while the watchdog is sleeping out a backoff delay cancels that pending
// relaunch instead of letting it fire after the user already asked to stop.
// The configured backoff (2s) is deliberately far longer than anything this
// test waits on, so a passing assertion can only be explained by actual
// cancellation, not by the test simply not waiting long enough to see a
// relaunch that would have happened anyway.
func TestManagerStopDuringBackoffCancelsPendingRestart(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 2*time.Second, 5*time.Second, 5, 10*time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item := createManagerTestInstance(t, store, "StopDuringBackoff", 28136, 29136)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFakeMihomoReady(t, item)

	waitForWatchdogCondition(t, 2*time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		wd := manager.watchdogs[item.ID]
		return wd != nil && wd.cancelPending != nil
	})

	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() during backoff error = %v", err)
	}

	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	stillPending := wd != nil && wd.cancelPending != nil
	manager.mu.Unlock()
	if stillPending {
		t.Fatal("expected Stop() to cancel the pending auto-restart backoff")
	}

	// Stop() now also stamps userStopped (fix #3, crash-watchdog concurrency
	// review), so the woken goroutine takes the more specific "stopped"
	// branch rather than the generic "cancelled" one -- see
	// runScheduledRestart's post-sleep section.
	waitForWatchdogCondition(t, 500*time.Millisecond, func() bool {
		for _, line := range manager.Logs(item.ID) {
			if strings.Contains(line, "auto-restart aborted: instance was stopped") {
				return true
			}
		}
		return false
	})
	if manager.state(item.ID) != nil {
		t.Fatal("expected no relaunch after Stop cancelled the pending backoff")
	}
}

// TestManagerAutoRestartResetsConsecutiveAfterHealthyRun covers
// watchHealthyRun: once a relaunched process has stayed up longer than
// watchdogHealthyAfter, the consecutive-restart counter resets to 0 so a
// later, unrelated crash gets a full fresh backoff sequence instead of
// picking up where an old, long-resolved crash loop left off.
func TestManagerAutoRestartResetsConsecutiveAfterHealthyRun(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 5*time.Millisecond, 20*time.Millisecond, 5, 50*time.Millisecond)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 1))
	item := createManagerTestInstance(t, store, "HealthyReset", 28137, 29137)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForWatchdogCondition(t, 2*time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		wd := manager.watchdogs[item.ID]
		return wd != nil && wd.restartCount == 1
	})

	waitForWatchdogCondition(t, 2*time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		wd := manager.watchdogs[item.ID]
		return wd != nil && wd.consecutive == 0
	})
}

// --- Crash-watchdog concurrency review fixes (mutex-clean races -race
// cannot see) ---

// TestManagerAutoRestartAbortsWhenStopArrivesAtBackoffWakeInstant covers fix
// #1: select{} inside sleepWithContext can pick its timer branch even though
// ctx.Done() became ready at the very same instant a concurrent Stop
// cancelled the backoff context, so runScheduledRestart must not trust
// err == nil alone -- it rechecks ctx.Err() once more, under m.mu, right
// before deciding to relaunch.
//
// backoffSleep is swapped for a fake that reproduces exactly that instant:
// it directly cancels the pending backoff's own context (bypassing
// StopContext/markUserStopped entirely, so this test does not also depend
// on fix #3's userStopped stamp) and then returns nil anyway, exactly as if
// select{} had picked the timer case despite ctx.Done() also being ready.
func TestManagerAutoRestartAbortsWhenStopArrivesAtBackoffWakeInstant(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 50*time.Millisecond, time.Second, 5, time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeCrashingFakeMihomo(t, 999))
	item := createManagerTestInstance(t, store, "WakeInstantStop", 28142, 29142)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	originalSleep := backoffSleep
	t.Cleanup(func() { backoffSleep = originalSleep })
	backoffSleep = func(ctx context.Context, d time.Duration) error {
		manager.mu.Lock()
		wd := manager.watchdogs[item.ID]
		var cancel context.CancelFunc
		if wd != nil {
			cancel = wd.cancelPending
		}
		manager.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		// Pretend the timer branch of the real select{} won anyway, even
		// though ctx (the very context cancel() above just cancelled) is
		// now done.
		return nil
	}

	if err := manager.Start(item.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForWatchdogCondition(t, 3*time.Second, func() bool {
		for _, line := range manager.Logs(item.ID) {
			if strings.Contains(line, "auto-restart cancelled during backoff") {
				return true
			}
		}
		return false
	})
	if manager.state(item.ID) != nil {
		t.Fatal("expected no relaunch: the post-sleep ctx.Err() recheck should have caught the cancellation the fake sleep missed")
	}
}

// TestManagerStopDuringSuccessorsBackoffStillCancelsIt covers fix #2: a
// descheduled predecessor goroutine (A) that finally reaches its post-sleep
// section after a *newer* attempt (B) has already been armed must not
// clobber B's wd.cancelPending -- otherwise a Stop delivered during B's own
// backoff would find cancelPending == nil, cancel nothing, and B would go on
// to relaunch a stopped instance regardless.
//
// This directly manipulates watchdogState (white-box, same package) to set
// up the exact interleaving the concurrency review described, rather than
// trying to force real goroutine scheduling to reproduce it:
//  1. Arm generation 2 (B) as "current" with its own real cancel func.
//  2. Invoke runScheduledRestart directly as if it were A, generation 1 --
//     stale relative to the armed generation 2.
//  3. Assert A's stale call left B's cancelPending (and B's context) alone.
//  4. Cancel via the normal path (cancelPendingRestart-equivalent) and
//     confirm B's context actually observes the cancellation, proving it was
//     never silently clobbered.
func TestManagerStopDuringSuccessorsBackoffStillCancelsIt(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "SuccessorSurvives", 28143, 29143)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	manager.mu.Lock()
	wd := manager.watchdogFor(item.ID)
	successorCtx, successorCancel := context.WithCancel(manager.ctx)
	wd.generation = 2
	wd.cancelPending = successorCancel
	manager.mu.Unlock()

	// A (generation 1, stale) finally wakes and reaches its post-sleep
	// section. Its own ctx is unrelated/never cancelled -- the point is that
	// it must recognize it has been superseded and return without touching
	// wd.cancelPending, regardless of its own ctx's state.
	staleCtx, staleCancel := context.WithCancel(manager.ctx)
	defer staleCancel()
	manager.runScheduledRestart(item.ID, wd, staleCtx, 0, 1, 1, time.Second)

	manager.mu.Lock()
	stillSuccessors := wd.cancelPending != nil
	manager.mu.Unlock()
	if !stillSuccessors {
		t.Fatal("expected the stale (generation 1) goroutine to leave the successor's (generation 2) cancelPending untouched")
	}
	if successorCtx.Err() != nil {
		t.Fatal("expected the successor's context to still be live after the stale goroutine's call")
	}

	// Now cancel for real (mirrors what StopContext/cancelPendingRestart
	// does) and confirm the successor's own context -- not clobbered above
	// -- actually observes it.
	manager.mu.Lock()
	manager.cancelPendingRestartLocked(item.ID)
	manager.mu.Unlock()
	if successorCtx.Err() == nil {
		t.Fatal("expected cancelling the pending backoff to actually cancel the successor's context")
	}
	manager.mu.Lock()
	clearedAfterCancel := wd.cancelPending == nil
	manager.mu.Unlock()
	if !clearedAfterCancel {
		t.Fatal("expected cancelPendingRestartLocked to clear cancelPending for the still-current generation")
	}
}

// TestManagerStopRacingCrashExitGoroutineSuppressesRelaunch covers fix #3: a
// Stop that races the crash exit goroutine -- arriving after the dead
// process has already been removed from m.procs, but before scheduleRestart
// has armed anything to cancel -- must still suppress the relaunch.
// Previously StopContext found nothing to cancel (state(id) == nil,
// cancelPending == nil) in that exact window and silently lost the race.
func TestManagerStopRacingCrashExitGoroutineSuppressesRelaunch(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 50*time.Millisecond, 200*time.Millisecond, 5, time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "StopRacesCrash", 28144, 29144)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	// Simulate: the process has already crashed, its exit goroutine has
	// already observed stopRequested == false and removed it from m.procs
	// (mirroring maybeAutoRestart's precondition), but a Stop() races in
	// *before* scheduleRestart -- the same call maybeAutoRestart would have
	// made next -- ever arms a backoff to cancel. There is nothing running
	// (state(id) == nil) and nothing pending (cancelPending == nil), exactly
	// what made this race possible before fix #3.
	if err := manager.Stop(item.ID); err != nil {
		t.Fatalf("Stop() on an idle instance error = %v, want nil", err)
	}
	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	manager.mu.Unlock()
	if wd == nil || !wd.userStopped {
		t.Fatal("expected Stop() to stamp userStopped even with nothing running or pending to cancel")
	}

	manager.scheduleRestart(item.ID, "exit status 1")

	manager.mu.Lock()
	wd = manager.watchdogs[item.ID]
	pending := wd.cancelPending != nil
	consecutive := wd.consecutive
	manager.mu.Unlock()
	if pending {
		t.Fatal("expected no backoff to be armed once userStopped is set")
	}
	if consecutive != 0 {
		t.Fatalf("consecutive = %d, want 0 -- a suppressed crash must not consume a backoff attempt", consecutive)
	}

	time.Sleep(150 * time.Millisecond)
	if manager.state(item.ID) != nil {
		t.Fatal("expected no relaunch after a Stop that raced the crash exit goroutine")
	}
}

// TestManagerFailedRelaunchConsumesAttemptAndReachesGiveUp covers fix #4: a
// relaunch attempt that itself fails (a transient port conflict, a config
// regeneration failure, ...) must feed back through scheduleRestart so it
// consumes a consecutive-restart attempt, instead of runScheduledRestart
// just logging and going quiet with backoff budget left.
func TestManagerFailedRelaunchConsumesAttemptAndReachesGiveUp(t *testing.T) {
	withPortFree(t, func(int) bool { return true })
	withWatchdogTiming(t, 5*time.Millisecond, 20*time.Millisecond, 2, 10*time.Second)
	store := newManagerTestStore(t)
	manager := NewManager(store, writeFakeMihomo(t, true, 0))
	item := createManagerTestInstance(t, store, "FailedRelaunch", 28145, 29145)
	if _, err := store.UpdateWithOptions(item.ID, updateInstanceOptions{AutoRestart: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	// From here on every port-availability check fails, simulating a
	// persistent conflict that makes every relaunch attempt startContext
	// makes fail -- the initial instance creation above already succeeded,
	// so this does not interfere with test setup.
	isPortFree = func(int) bool { return false }

	// Simulate the crash directly: maybeAutoRestart's preconditions (not
	// stopped, not deleting, AutoRestart on) already hold, so this is
	// exactly what the exit goroutine would have called.
	manager.scheduleRestart(item.ID, "exit status 1")

	var lastErr string
	waitForWatchdogCondition(t, 3*time.Second, func() bool {
		fresh, ok := store.Get(item.ID)
		if ok && strings.Contains(fresh.LastError, "giving up") {
			lastErr = fresh.LastError
			return true
		}
		return false
	})
	if lastErr == "" {
		t.Fatal("expected repeated failed relaunches to eventually give up")
	}
	if !strings.Contains(lastErr, "restart failed") {
		t.Fatalf("LastError = %q, want it to mention the relaunch failures that consumed each attempt", lastErr)
	}

	manager.mu.Lock()
	wd := manager.watchdogs[item.ID]
	consecutive := 0
	if wd != nil {
		consecutive = wd.consecutive
	}
	manager.mu.Unlock()
	if consecutive <= watchdogMaxRestarts {
		t.Fatalf("consecutive = %d, want > watchdogMaxRestarts (%d) once repeated relaunch failures give up", consecutive, watchdogMaxRestarts)
	}
}
