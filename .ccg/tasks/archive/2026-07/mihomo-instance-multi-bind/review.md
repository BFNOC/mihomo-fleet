# Review

## Scope

- Fleet WebUI remains single-bind and loopback-protected.
- Fleet-managed mihomo instances gained per-instance `proxyBind`.
- Runtime config keeps `external-controller` on `127.0.0.1`.
- Multi-address proxy bind emits mihomo `listeners` with the same mixed port and different `listen` addresses.
- Log auto-refresh no longer forces scroll-to-bottom while the user is reading older lines.

## Findings

- Critical: none.
- Warning: port availability probing is still port-level, not bind-address-level. Mihomo config/start remains the final conflict check.
- Info: exact IPv6 listener format was smoke-tested with local mihomo and accepted.

## Follow-up Fixes Applied

- `localhost` proxy bind now normalizes to `127.0.0.1`, avoiding duplicate loopback listeners.
- IPv6 link-local zones are preserved during canonicalization, so `fe80::1%en0` and `fe80::1%en1` stay distinct.
- Added tests for invalid controller input, single explicit bind address, localhost normalization, and IPv6 zone handling.

## Local Validation

- `rtk node --check internal/app/web/app.js`
- `rtk go test ./...` -> 106 passed in 2 packages
- `rtk go vet ./...`
- `rtk go build -o mihomo-fleet ./cmd/mihomo-fleet`
- `rtk git diff --check`
- Local `./mihomo -t` smoke for exact multi-listener bind and IPv6 listener bind passed.

## Note

Dual external review was run before the user requested no more multi-model review. No further multi-model calls were made after that request.
