# Review

## Commits

- `20ca7de feat: 支持实例代理多地址绑定`
- `2d0ce2a feat: 添加程序版本号解析`

## Version

- Bumped `VERSION` from `0.12.2` to `0.13.0`.
- Added annotated tag `v0.13.0`.

## Validation

- `rtk node --check internal/app/web/app.js`
- `rtk go test ./...` -> 110 passed in 2 packages
- `rtk go vet ./...`
- `rtk go run ./cmd/mihomo-fleet -version` -> `mihomo-fleet 0.13.0`
- `rtk go build -o mihomo-fleet ./cmd/mihomo-fleet`
- `rtk ./mihomo-fleet -version` -> `mihomo-fleet 0.13.0`
- `rtk git diff --check`

## Note

- No external or multi-model review was run, per user request.
