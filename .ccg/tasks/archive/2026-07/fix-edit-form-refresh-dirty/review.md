# Review: fix-edit-form-refresh-dirty

## Local Verification

- `rtk node --check internal/app/web/app.js`: passed
- `rtk go test ./...`: passed, 86 tests in 2 packages
- `rtk go vet ./...`: passed
- `rtk go build -o mihomo-fleet ./cmd/mihomo-fleet`: passed
- `rtk git diff --check`: passed

## Dual Model Review

### Antigravity

- Verdict: APPROVE
- Findings: No Critical, Warning, or Info findings after the save-in-flight version guard.

### Claude

- Verdict: APPROVE
- Findings: No Critical findings. Confirmed `editVersion` preserves user edits if input changes while save is in flight. Notes were informational only.

## Outcome

The dirty guard prevents the 4s background refresh from overwriting unsaved instance basic-form edits, including switching the mode from `规则分流` to `全局链式`. The version guard also prevents a save response from clearing dirty state if the user continues editing before the refresh completes.
