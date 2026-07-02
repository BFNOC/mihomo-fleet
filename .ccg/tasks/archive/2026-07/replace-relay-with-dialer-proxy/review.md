# Review: replace-relay-with-dialer-proxy

## Local Verification

- `rtk node --check internal/app/web/app.js`: passed
- `rtk go test ./...`: passed, 90 tests in 2 packages
- `rtk go vet ./...`: passed
- `rtk go build -o mihomo-fleet ./cmd/mihomo-fleet`: passed
- `rtk git diff --check`: passed
- `rtk ./mihomo -t -d .mihomo-fleet/instances/new-instance -f .mihomo-fleet/instances/new-instance/config.runtime.yaml`: passed

## External Reference

- Checked mihomo docs for `dialer-proxy`: `relay` proxy groups are deprecated, proxy groups do not directly support `dialer-proxy`, provider nodes can use `override.dialer-proxy`.

## Dual Model Review

### Analysis

- Antigravity and Claude agreed the old relay chain must be represented by assigning `dialer-proxy` from each later chain member to its previous chain member.
- Both warned that proxy groups cannot carry `dialer-proxy`; when `节点选择` is the dialer or exit selector, the concrete nodes and provider overrides must carry the field.

### Review

- Antigravity: APPROVE, no findings after cleanup.
- Claude: APPROVE, no Critical or Warning findings after cleanup. Remaining notes were cosmetic naming only.

## Outcome

The runtime config no longer generates `type: relay` or `MATCH,代理链`. The chain order is preserved from top to bottom; the final chain member becomes the rule target, concrete proxies get `dialer-proxy` pointing to the previous member, and `节点选择` applies its previous member to selectable inline/local proxies and provider overrides. Fixed chain members are removed from `节点选择` candidates to avoid self-dial loops.
