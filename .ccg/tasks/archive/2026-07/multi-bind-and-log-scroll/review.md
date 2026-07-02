# Review

## Analysis

- Antigravity 和 Claude 已完成并行分析。
- 结论一致：默认继续绑定 `127.0.0.1`；多地址监听需要同步 Host 白名单；日志滚动问题来自自动刷新时无条件置底。

## Review Round 1

- Antigravity: APPROVE，指出日志请求返回前用户滚动的异步时序问题。
- Claude: REQUEST_CHANGES，指出 `0.0.0.0,::` 双通配监听可能冲突、无效裸 IPv6 输入应提前拒绝、畸形 Host bracket 应拒绝、日志错误恢复与 CSS 小视口可再收紧。

## Fixes After Round 1

- 监听按地址族选择 `tcp4` / `tcp6`，避免 IPv4 与 IPv6 通配同端口冲突。
- `listenAndServe` 改为返回 error，监听失败时先关闭已创建 listener，由 `main` 在退出前显式关闭 controller。
- 多个 HTTP server 并发 shutdown，共享同一个 5 秒上下文。
- 绑定解析拒绝 `127.0.0.1:47890`、`[::1]:47890`、`::1:47890` 这类带端口或无效 IPv6 输入。
- Host 解析拒绝 `[::1`、`[::1].attacker.test` 等畸形 Host。
- 日志滚动状态改为 API 返回后再判断，避免慢请求期间用户上滚后被拉回底部。
- README 补充 `all` / `0.0.0.0` 的启动时网卡快照和自定义域名默认被拦截。

## Review Round 2

- Antigravity: APPROVE，100/100，无 Critical/Warning。
- Claude: APPROVE，无 Critical/Warning。

## Verification

- `node --check internal/app/web/app.js`
- `go test ./...`
- `go vet ./...`
- `go build -o mihomo-fleet ./cmd/mihomo-fleet`
- `git diff --check`
- 本地 smoke:
  - `127.0.0.1` 单监听可访问，`attacker.test` Host 返回 403。
  - `127.0.0.1,::1` 双监听均可访问，`attacker.test` Host 返回 403。
  - `0.0.0.0,::` 双通配监听可启动，loopback IPv4/IPv6 均可访问，`attacker.test` Host 返回 403。
