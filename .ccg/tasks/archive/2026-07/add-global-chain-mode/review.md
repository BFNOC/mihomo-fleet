# Review

## 双模型审查

- antigravity: APPROVE；无 Critical，无 Warning。Info 提到全局链式新建时隐藏手写配置框、生成组名为中文。
- Claude: 无 Critical；轻量 Request Changes。主要意见：明确 relay 链不应接受 `DIRECT/REJECT` 等内置结果、保守处理 geodata、补错误分支测试、relay 只读展示不应带 `aria-pressed`。

## 已处理

- 禁止 `DIRECT`、`REJECT`、`GLOBAL` 等内置结果作为 relay 链路成员。
- 恢复 geodata 准备路径，避免 Profile 其他段落潜在引用 geodata 时缺文件。
- relay 组只读按钮不再设置 `aria-pressed`。
- README 补充链路顺序边界。
- 补充未知链路成员、内置结果、节点重名冲突测试。

## 验证

- `node --check internal/app/web/app.js`
- `go test ./...`
- `go vet ./...`
- `go build -o mihomo-fleet ./cmd/mihomo-fleet`
- `git diff --check`
