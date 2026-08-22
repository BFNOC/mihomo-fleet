// notifications.ts pulls in vue, and @vue/runtime-dom calls
// document.createElement at module load, so the stub has to be in place before
// the import runs -- hence the dynamic import() below rather than a top-level
// one (CLAUDE.md, "Verifying frontend changes").
import test from "node:test";
import assert from "node:assert/strict";

(globalThis as unknown as { document: unknown }).document = {
  createElement: () => ({ style: {} }),
};

const { notices, pushNotice, dismissNotice, dismissAllNotices } = await import("./notifications.ts");

function reset(): void {
  dismissAllNotices();
}

test("pushNotice keeps distinct messages as separate entries", () => {
  reset();
  const first = pushNotice("已请求启动。", "info");
  const second = pushNotice("已请求停止。", "info");
  assert.notEqual(first, second);
  assert.deepEqual(
    notices.map((notice) => notice.text),
    ["已请求启动。", "已请求停止。"],
  );
});

test("an identical message merges into the entry already on screen", () => {
  reset();
  const first = pushNotice("refresh failed", "error");
  const again = pushNotice("refresh failed", "error");
  assert.equal(again, first, "a repeat must reuse the existing id");
  assert.equal(notices.length, 1);
  assert.equal(notices[0]?.count, 2);
});

test("same text at a different tone is a different entry", () => {
  reset();
  pushNotice("已保存。", "info");
  pushNotice("已保存。", "error");
  assert.equal(notices.length, 2);
});

test("the queue caps at five entries, dropping the oldest", () => {
  reset();
  const ids = [];
  for (let index = 0; index < 7; index += 1) ids.push(pushNotice(`错误 ${index}`, "error"));
  assert.equal(notices.length, 5);
  assert.deepEqual(
    notices.map((notice) => notice.text),
    ["错误 2", "错误 3", "错误 4", "错误 5", "错误 6"],
  );
});

test("dismissNotice removes only its own entry and ignores stale ids", () => {
  reset();
  const first = pushNotice("A", "error");
  const second = pushNotice("B", "error");
  dismissNotice(first);
  assert.deepEqual(
    notices.map((notice) => notice.text),
    ["B"],
  );
  // Dismissing twice, or dismissing an id that never existed, must not throw
  // or take out the wrong entry: fleet-refresh.ts calls this with an id the
  // user may already have dismissed by hand.
  dismissNotice(first);
  dismissNotice(9999);
  assert.deepEqual(
    notices.map((notice) => notice.id),
    [second],
  );
});

// The real delay is 6s, so the timer is intercepted rather than waited out.
// notifications.ts looks `setTimeout` up on the global at call time, so
// swapping it here is enough.
// clearTimeout is stubbed alongside setTimeout on purpose: without it a
// cancelled callback would still be callable from `fire`, and the
// restart-the-countdown test below would pass no matter what the module did.
function withCapturedTimers(run: (fire: (index: number) => void, delays: number[]) => void): void {
  const originalSet = globalThis.setTimeout;
  const originalClear = globalThis.clearTimeout;
  const delays: number[] = [];
  const callbacks: Array<(() => void) | null> = [];
  (globalThis as unknown as { setTimeout: unknown }).setTimeout = (fn: () => void, delay: number) => {
    delays.push(delay);
    callbacks.push(fn);
    return callbacks.length - 1;
  };
  (globalThis as unknown as { clearTimeout: unknown }).clearTimeout = (handle: number) => {
    if (typeof handle === "number" && callbacks[handle]) callbacks[handle] = null;
  };
  try {
    run((index) => callbacks[index]?.(), delays);
  } finally {
    globalThis.setTimeout = originalSet;
    globalThis.clearTimeout = originalClear;
  }
}

test("info entries get a 6s dismiss timer and errors get none", () => {
  reset();
  withCapturedTimers((fire, delays) => {
    pushNotice("start failed", "error");
    assert.deepEqual(delays, [], "an error must never be scheduled for dismissal");
    pushNotice("已请求启动。", "info");
    assert.deepEqual(delays, [6000]);
    fire(0);
    assert.deepEqual(
      notices.map((notice) => notice.text),
      ["start failed"],
      "only the info entry expires",
    );
  });
});

test("a repeat info message restarts its countdown instead of stacking", () => {
  reset();
  withCapturedTimers((fire, delays) => {
    pushNotice("已请求启动。", "info");
    pushNotice("已请求启动。", "info");
    assert.deepEqual(delays, [6000, 6000], "the second call reschedules");
    // Firing the first (now cleared) timer must not dismiss the entry the
    // second call is still counting down -- that was the bug the old banner's
    // `seq` counter existed to avoid.
    fire(0);
    assert.equal(notices.length, 1);
    fire(1);
    assert.equal(notices.length, 0);
  });
});

// Merging on text alone made a card a shared object with no owner: two sources
// raising the same string got the same id, and the first to release it took the
// card away from the other.
test("an owner releases only its own claim on a shared card", () => {
  reset();
  const pollId = pushNotice("controller offline", "error", "fleet-refresh");
  const actionId = pushNotice("controller offline", "error", "save-profile");
  assert.equal(actionId, pollId, "still one card -- it is literally the same message");
  dismissNotice(pollId, "fleet-refresh");
  assert.equal(notices.length, 1, "the other owner still needs it");
  dismissNotice(actionId, "save-profile");
  assert.equal(notices.length, 0);
});

test("the user's own dismissal beats every outstanding claim", () => {
  reset();
  const id = pushNotice("controller offline", "error", "fleet-refresh");
  pushNotice("controller offline", "error", "save-profile");
  // The × on the card: whoever is looking at it has decided they are done.
  dismissNotice(id);
  assert.equal(notices.length, 0);
});

test("an anonymous push keeps its card when an owner releases", () => {
  reset();
  const id = pushNotice("boom", "error");
  pushNotice("boom", "error", "someone");
  dismissNotice(id, "someone");
  assert.equal(notices.length, 1, "the anonymous claim is still outstanding");
});

// Errors are contractually sticky, so the cap must not be what retires them.
test("the cap evicts expiring info before any error", () => {
  reset();
  for (let index = 0; index < 3; index += 1) pushNotice(`错误 ${index}`, "error");
  pushNotice("提示 A", "info");
  pushNotice("提示 B", "info");
  pushNotice("错误 3", "error");
  assert.deepEqual(
    notices.map((notice) => notice.text),
    ["错误 0", "错误 1", "错误 2", "提示 B", "错误 3"],
    "the oldest info goes, not the oldest error",
  );
});

test("a queue of nothing but errors still evicts the oldest", () => {
  reset();
  for (let index = 0; index < 6; index += 1) pushNotice(`错误 ${index}`, "error");
  assert.deepEqual(
    notices.map((notice) => notice.text),
    ["错误 1", "错误 2", "错误 3", "错误 4", "错误 5"],
  );
});

test("dismissAllNotices empties the queue", () => {
  reset();
  pushNotice("A", "info");
  pushNotice("B", "error");
  dismissAllNotices();
  assert.equal(notices.length, 0);
});
