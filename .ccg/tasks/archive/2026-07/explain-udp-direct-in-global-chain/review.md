# Review

## Result

通过。

## Notes

- 全局链式运行态规则现在先写入 `NETWORK,UDP,REJECT`，再写入 `MATCH,<链路目标>`，避免 HTTP/链式代理无法承载 UDP 时回落到 DIRECT。
- 运行中节点选择界面在全局链式模式下隐藏 mihomo 内置 `GLOBAL` 组，避免把内置 DIRECT 状态误认为 Fleet 的链路目标。
- 按用户要求未调用外部多模型审查。

## Verification

- `node --check internal/app/web/app.js`
- `go test ./...`
- `go vet ./...`
- `go build -o mihomo-fleet ./cmd/mihomo-fleet`
- `git diff --check`
- `./mihomo -t -f <process-substitution smoke config with NETWORK,UDP,REJECT>`
