package app

import (
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
	if elapsed >= time.Second {
		t.Fatalf("Stop() took %s, want well under 1s via the done channel (no 100ms polling)", elapsed)
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
