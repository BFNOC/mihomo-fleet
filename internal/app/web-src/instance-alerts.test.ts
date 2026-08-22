// The alert state machine is the kind of logic that regresses silently -- a
// wrong gate shows nothing, or shows the same crash five times -- so it is
// tested here rather than left to the throwaway harness that found the first
// version's duplicate-notification bug.
//
// Stubs go in before the dynamic imports: the service pulls in vue (which calls
// document.createElement at module load) and reads localStorage at module
// scope. Same pattern as notifications.test.ts.
import test from "node:test";
import assert from "node:assert/strict";

const storage = new Map<string, string>();
const desktop: string[] = [];

(globalThis as unknown as { document: unknown }).document = {
  createElement: () => ({ style: {} }),
};
(globalThis as unknown as { localStorage: unknown }).localStorage = {
  getItem: (key: string) => (storage.has(key) ? storage.get(key)! : null),
  setItem: (key: string, value: string) => storage.set(key, String(value)),
  removeItem: (key: string) => storage.delete(key),
};

class FakeNotification {
  static permission = "granted";
  static async requestPermission(): Promise<string> {
    return "granted";
  }
  constructor(title: string, options: { body: string }) {
    desktop.push(`${title}: ${options.body}`);
  }
}
(globalThis as unknown as { Notification: unknown }).Notification = FakeNotification;
// Enabled before the import: the service reads the preference at module scope.
storage.set("fleetInstanceAlerts", "1");

const { nextTick } = await import("vue");
const { store } = await import("./store.ts");
const { notices } = await import("./notifications.ts");
const { startInstanceAlerts } = await import("./services/instance-alerts.ts");

interface TestInstance {
  id: string;
  name: string;
  status: string;
  lastError: string;
}

function instance(id: string, status: string, lastError = ""): TestInstance {
  return { id, name: id.toUpperCase(), status, lastError };
}

async function setFleet(...items: TestInstance[]): Promise<void> {
  // fleet-refresh.ts reassigns the whole array on every poll, which is what the
  // service watches; mutating in place would not reproduce that.
  (store as unknown as { instances: TestInstance[] }).instances = items;
  await nextTick();
}

function texts(): string[] {
  return notices.map((notice) => notice.text);
}

// The module-scope state is shared across this file, so the tests run as one
// ordered sequence rather than pretending to be independent.
startInstanceAlerts();

test("an instance already failing at load is not announced, on this tick or the next", async () => {
  await setFleet(instance("a", "error", "boom"));
  assert.deepEqual(texts(), [], "the first snapshot is a baseline, not news");
  await setFleet(instance("a", "error", "boom"));
  assert.deepEqual(texts(), [], "and the episode stays known on later ticks");
});

test("a running instance turning to error raises one alert with the localized reason", async () => {
  await setFleet(instance("a", "running"));
  await setFleet(instance("a", "error", "mixed proxy port 28001 is already in use"));
  assert.deepEqual(texts(), ["实例「A」运行出错：混合端口 28001 已被占用。"]);
  assert.equal(desktop.length, 1);
});

test("a crash loop does not re-alert on every lap", async () => {
  for (const status of ["stopped", "starting", "error", "stopped", "error"]) {
    await setFleet(instance("a", status, "mixed proxy port 28001 is already in use"));
  }
  assert.equal(notices.length, 1, "one card for one failure episode");
  assert.equal(desktop.length, 1, "and one desktop notification, not one per lap");
});

test("returning to running clears the alert and re-arms it", async () => {
  await setFleet(instance("a", "running"));
  assert.deepEqual(texts(), []);
  await setFleet(instance("a", "error", "boom"));
  assert.equal(notices.length, 1, "a later failure is a new episode");
  assert.equal(desktop.length, 2);
});

test("an alert without a reason still names the instance", async () => {
  await setFleet(instance("a", "running"));
  await setFleet(instance("a", "error", ""));
  assert.deepEqual(texts(), ["实例「A」运行出错。"]);
});

test("each instance owns its own alert", async () => {
  await setFleet(instance("a", "error", ""), instance("b", "error", "boom"));
  assert.deepEqual(texts(), ["实例「A」运行出错。", "实例「B」运行出错：boom"]);
  // Recovering one must not take the other's card down with it.
  await setFleet(instance("a", "error", ""), instance("b", "running"));
  assert.deepEqual(texts(), ["实例「A」运行出错。"]);
});

test("deleting a failing instance takes its alert away", async () => {
  await setFleet(instance("b", "running"));
  assert.deepEqual(texts(), []);
});

// Two instances may legitimately share a display name -- the store only makes
// ids unique. Same name plus same error used to collapse into one card, and the
// first recovery then took that card away while the other instance was still
// failing, with its openAlerts entry blocking any replacement.
test("same-named instances failing on the same error stay separately addressable", async () => {
  await setFleet(instance("x", "running"), instance("y", "running"));
  const twins = [
    { id: "x", name: "TWIN", status: "error", lastError: "boom" },
    { id: "y", name: "TWIN", status: "error", lastError: "boom" },
  ];
  await setFleet(...twins);
  assert.equal(notices.length, 2, "one card per failing instance");
  assert.deepEqual(texts(), [
    "实例「TWIN（x）」运行出错：boom",
    "实例「TWIN（y）」运行出错：boom",
  ]);

  // One recovers; the other's card must survive.
  await setFleet({ ...twins[0]!, status: "running", lastError: "" }, twins[1]!);
  assert.deepEqual(texts(), ["实例「TWIN（y）」运行出错：boom"]);
  await setFleet({ ...twins[0]!, status: "running", lastError: "" }, { ...twins[1]!, status: "running", lastError: "" });
  assert.deepEqual(texts(), []);
});

test("a unique name is not cluttered with the instance id", async () => {
  await setFleet(instance("solo", "running"));
  await setFleet(instance("solo", "error", "boom"));
  assert.deepEqual(texts(), ["实例「SOLO」运行出错：boom"]);
  await setFleet(instance("solo", "running"));
});

test("desktop notifications are tagged per instance", async () => {
  const tags: string[] = [];
  const original = (globalThis as unknown as { Notification: unknown }).Notification;
  class TaggingNotification {
    static permission = "granted";
    static async requestPermission(): Promise<string> {
      return "granted";
    }
    constructor(_title: string, options: { tag?: string }) {
      tags.push(options.tag || "");
    }
  }
  (globalThis as unknown as { Notification: unknown }).Notification = TaggingNotification;
  const { setDesktopAlerts } = await import("./services/instance-alerts.ts");
  await setDesktopAlerts(true);
  try {
    await setFleet(instance("m", "running"), instance("n", "running"));
    await setFleet(instance("m", "error", "boom"), instance("n", "error", "boom"));
    // A shared tag makes the browser replace the first banner with the second,
    // so only the last failure would ever be seen.
    assert.equal(new Set(tags).size, tags.length, `tags must be distinct, got ${JSON.stringify(tags)}`);
    assert.equal(tags.length, 2);
  } finally {
    (globalThis as unknown as { Notification: unknown }).Notification = original;
    await setDesktopAlerts(false);
    await setFleet(instance("m", "running"), instance("n", "running"));
  }
});

test("turning desktop alerts off leaves the in-page card alone", async () => {
  const { setDesktopAlerts } = await import("./services/instance-alerts.ts");
  await setDesktopAlerts(false);
  const before = desktop.length;
  await setFleet(instance("b", "error", "boom"));
  assert.equal(notices.length, 1, "the card is not what the toggle controls");
  assert.equal(desktop.length, before, "no desktop notification once opted out");
});
