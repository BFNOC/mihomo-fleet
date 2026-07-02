# Fleet 管理实例的 mihomo 多绑定地址

## 需求纠正

- 绑定开关应该作用在 Fleet 管理的每个 mihomo 实例代理端口上，不是 Fleet WebUI 自身。
- Fleet WebUI 仍保持原有单监听行为。
- 每个实例的代理绑定地址默认是 `127.0.0.1`。
- 支持在实例上填写逗号分隔地址，例如 `127.0.0.1,192.168.64.1`。
- 支持 `all` / `*` / `0.0.0.0` 表示所有 IPv4 网卡。
- 精确多地址由 Fleet 在运行时配置里生成 mihomo `listeners`，每个 listener 使用同一个 mixed port 和不同 listen 地址。
- 保留日志滚动修复：自动刷新时不打断用户查看历史日志。

## 非目标

- 不把 Fleet WebUI 变成多地址监听服务。
- 不开放 mihomo external-controller，它仍固定在 `127.0.0.1`。
