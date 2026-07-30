# 已知限制

记录已识别、经过评估后决定暂不修复的问题。每条说明问题本身、影响范围、
不修的理由，以及真要修时的具体方案。

---

## 实例控制器通信的 check-then-connect TOCTOU

**识别时间**：2026-07-30，代码审查

### 问题

mihomo-fleet 通过 localhost TCP 端口与它管理的每个 mihomo 实例通信：

- `mihomo_proxy.go` 的 `handleMihomoProxy` — WebUI 反向代理到实例控制器
- `manager.go` 的 `ReloadContext` → `reloadMihomoConfig` — 热重载推送配置
- `mihomo_api.go` — 延迟测试、代理组查询等

这些路径都是先检查实例存活（`c.manager.state(id) != nil` 或
`m.procs[id] == ps`），再向 `item.ControllerPort` 发起连接。两步之间不是
原子的：

1. 检查通过，确认进程在运行
2. 进程此刻退出（崩溃，或用户 Stop），控制器端口被释放
3. 本机另一个进程 bind 了同一端口
4. 请求连同 `Authorization: Bearer <实例控制器密钥>` 发给了那个进程

加锁无法消除这个窗口。进程可以在任意时刻退出，包括连接成功之后——
没有任何"检查存活 + 建立连接"的序列是原子的。

### 影响

- 需要本机存在恶意或行为异常的进程
- 需要它在毫秒级窗口内抢到刚释放的端口
- 泄露的是单个实例的控制器密钥，不是 mihomo-fleet 自身的 API token

已做的收敛：`handleMihomoProxy` 的判据从 `Busy`（running 或 starting）
收紧为 `state(id) != nil`（确认已运行），关掉了"已知停止的实例仍被转发"
这个远大得多的窗口。剩下的是崩溃与连接之间的固有残留。

### 不修的理由

彻底修复需要把实例通信从 TCP 端口迁移到进程独占的传输，这是架构级改动，
与 1.4.0 分支的三个功能无关，且改动面远超一次审查修复的合理范围：
`ControllerPort` 出现在 130+ 处，涉及 store schema、导出/导入包格式、
前端 UI 展示与端口冲突校验、以及既有实例和备份包的兼容。

### 真要修时的方案

mihomo **原生支持**这两个键（`config.go` 目前把它们从用户配置里剥离）：

- `external-controller-unix` — Unix domain socket
- `external-controller-pipe` — Windows 命名管道

迁移步骤：

1. 每次进程启动生成唯一、不复用的 socket / pipe 地址（含进程代次，
   例如 `<dataDir>/run/<id>-<generation>.sock`），写入运行配置
2. 地址存入该代 `processState`，而不是实例记录——它是进程级而非实例级属性
3. `mihomo_api.go`、`reloadMihomoConfig`、`mihomoProxyFor` 改用自定义
   `DialContext` 连接该地址
4. 反向代理缓存按进程代次而非端口建立（`mihomoProxies` 目前按端口 keyed）
5. `ControllerPort` 可以保留为纯展示/兼容字段，或分阶段废弃

关键性质：旧进程退出后其 socket 路径永不复用，新进程使用新地址，
所以指向旧地址的请求只会得到"连接失败"，不可能落到别的进程上。

Windows 命名管道需要单独的拨号实现（`winio.DialPipe` 一类），这也是
迁移工作量的一部分。
