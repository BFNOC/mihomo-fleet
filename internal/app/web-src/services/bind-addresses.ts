import { reactive } from "vue";

import { api } from "../api.ts";
import { localizedMessage } from "../messages.ts";
import type { BindAddressOption } from "../proxy-bind.ts";

/*
 * The host's own listen addresses, for the 代理绑定地址 picker.
 *
 * Module-scope state rather than a useX() factory because the answer is a
 * property of the machine, not of the form asking: the create panel and the
 * instance edit form want the identical list, and fetching it twice would just
 * be two syscall round trips for the same bytes.
 *
 * Not polled. Interface addresses only change when the network does (VPN up, Wi-Fi
 * switch), which no timer can predict usefully, so the picker offers an explicit
 * 刷新 instead of guessing an interval.
 */
interface BindAddressState {
  addresses: BindAddressOption[];
  loading: boolean;
  error: string;
}

export const bindAddressState: BindAddressState = reactive<BindAddressState>({
  addresses: [],
  loading: false,
  error: "",
});

let inFlight: Promise<void> | null = null;
let loaded = false;

/**
 * Fetches once and caches. Concurrent callers share the in-flight promise, so two
 * pickers opening in the same frame make one request. `force` re-fetches after a
 * network change.
 */
export function loadBindAddresses(force = false): Promise<void> {
  if (inFlight) return inFlight;
  if (loaded && !force) return Promise.resolve();
  bindAddressState.loading = true;
  bindAddressState.error = "";
  inFlight = api<{ addresses?: BindAddressOption[] }>("/api/system/bind-addresses")
    .then((body) => {
      bindAddressState.addresses = Array.isArray(body?.addresses) ? body.addresses : [];
      loaded = true;
    })
    .catch((err: unknown) => {
      // A failed lookup must not block the field: the text input stays usable and
      // the backend still validates on save, so this surfaces as a note next to
      // the list rather than an error banner over the form.
      bindAddressState.error = localizedMessage(err instanceof Error ? err.message : err);
    })
    .finally(() => {
      bindAddressState.loading = false;
      inFlight = null;
    });
  return inFlight;
}
