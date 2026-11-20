export const defaultConfig: string = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`;

export const legacyDefaultLatencyUrl: string = "https://www.gstatic.com/generate_204";
export const legacyDefaultLatencyTimeout: string = "5000";
export const defaultLatencyUrl: string = "http://cp.cloudflare.com/generate_204";
export const defaultLatencyTimeout: number = 10000;
export const latencyBatchConcurrency: number = 4;
export const latencyKeySeparator: string = "";
export const logStickThreshold: number = 24;
export const defaultProxyBind: string = "127.0.0.1";
// 出口 IP 检测：返回纯文本 IP 的公共服务，任选其一或自填。
export const defaultIpCheckUrl: string = "https://api.ip.sb/ip";
export const ipCheckPresets: string[] = [
  defaultIpCheckUrl,
  "https://api.ipify.org",
  "https://icanhazip.com",
  "https://ifconfig.me/ip",
  "https://ifconfig.co/ip",
  "https://ipinfo.io/ip",
  "https://ident.me",
  "https://ip.me",
  "https://ipecho.net/plain",
  "https://checkip.amazonaws.com",
  "https://whatismyip.akamai.com",
  "https://api.seeip.org",
  "https://wtfismyip.com/text",
];
export const API_SECRET_STORAGE_KEY: string = "fleetApiSecret";
export const slowPollIntervalMs: number = 4000;
export const fastPollIntervalMs: number = 1800;
// services/polling.ts's traffic-sampling cadence while the dashboard is not
// the active view: the rolling 60s window still needs pre-warming (see
// sampleFleetTraffic()'s comment there), just not at the full rate when
// nobody is reading the per-tick connection rows.
export const fastPollBackgroundIntervalMs: number = 5400;

// `as const` gives each property its literal string type, so
// `(typeof latencyKinds)[keyof typeof latencyKinds]` below is the exact
// "url" | "real" union instead of a widened `string`.
const latencyKinds = {
  url: "url",
  real: "real",
} as const;
export type LatencyKind = (typeof latencyKinds)[keyof typeof latencyKinds];

export const instanceModes = {
  rule: "rule",
  globalChain: "global-chain",
} as const;

// The two proxy-group names internal/app/config.go generates for global-chain
// mode (globalChainSelectGroupName / globalChainRelayGroupName). The chain may
// reference the select group and must never reference the relay group, so the
// chain picker needs both names -- and they were previously spelled inline in
// format.ts and a CreatePanel placeholder, where a backend rename would have
// left them silently wrong.
export const chainSelectGroupName: string = "节点选择";
export const chainRelayGroupName: string = "代理链";
export type InstanceMode = (typeof instanceModes)[keyof typeof instanceModes];

// Mirrors the Status values the Go controller ever assigns to an
// InstanceView (internal/app/manager.go: decorateStatus/viewFor emit only
// "stopped" | "starting" | "running" | "error").
export const statusLabels = {
  stopped: "已停止",
  starting: "启动中",
  running: "运行中",
  error: "异常",
} as const;
export type InstanceStatus = keyof typeof statusLabels;

export type ProxyCopyActionId = "addr" | "http" | "socks" | "env";

export interface ProxyCopyActionDef {
  readonly id: ProxyCopyActionId;
  readonly label: string;
  readonly title: string;
}

export const proxyCopyDefs: readonly ProxyCopyActionDef[] = [
  { id: "addr", label: "地址", title: "复制主机和端口" },
  { id: "http", label: "HTTP", title: "复制 HTTP 代理地址" },
  { id: "socks", label: "SOCKS", title: "复制 SOCKS5 代理地址" },
  { id: "env", label: "ENV", title: "复制 bash/zsh export 环境变量" },
];

// Keyed by the raw (English) backend error text, which is not a fixed
// literal union (new backend error strings can appear at any time) -- an
// index signature is the correct shape, not `as const`.
export const errorLabels: Record<string, string> = {
  "mihomo binary not found. Install mihomo or start with -mihomo /path/to/mihomo": "未找到 mihomo 可执行文件。请安装 mihomo，或使用 -mihomo /path/to/mihomo 指定路径。",
  "stop the instance before changing ports": "修改端口前请先停止该实例。",
  "mixed and controller ports must differ": "混合端口与控制器端口不能相同。",
  "stop the instance before changing proxy bind": "修改代理绑定地址前请先停止该实例。",
  "stop the instance before changing profile": "修改配置档前请先停止该实例。",
  "profileId and config cannot be changed in the same request": "不能在同一次请求中同时修改配置档和配置内容。",
  "subscriptionUrl and config cannot both be set": "订阅链接和配置内容不能同时设置。",
  "subscription URL must start with http:// or https://": "订阅链接必须以 http:// 或 https:// 开头。",
  "subscription profile config is refreshed from its URL": "订阅配置档的内容由链接更新，请使用手写配置档编辑 YAML。",
  "group and proxy are required": "必须选择节点组和节点。",
  "method not allowed": "请求方法不允许。",
  "invalid host header": "Host 请求头无效。",
  "missing X-Mihomo-Fleet header": "缺少 X-Mihomo-Fleet 请求头。",
  "Content-Type must be application/json": "Content-Type 必须是 application/json。",
  "unable to allocate local ports": "无法自动分配本地端口。",
  "instance must be running to test latency": "请先启动实例再测速。",
  "instance must be running to check ip": "请先启动实例再测试 IP。",
  "ip check URL must start with http:// or https://": "测试 IP 的网址必须以 http:// 或 https:// 开头。",
  "proxy is required": "请选择要测速的节点。",
  "proxy is required for real latency": "真延迟需要指定单个节点。",
  "latency kind must be url or real": "测速类型无效。",
  "latency test URL must start with http:// or https://": "测试 URL 必须以 http:// 或 https:// 开头。",
  "global-chain mode requires proxies, proxy-providers, or local proxies": "全局链式模式需要订阅节点、provider 或本地节点。",
  "instance is starting; retry once it finishes starting": "实例正在启动，请稍后重试。",
  "missing or invalid API token": "API 令牌缺失或无效。",
  "profile is in use by existing instances": "配置档仍被实例使用，无法删除。",
  "profile changed while configuration was being edited": "配置档已被改绑，未保存的 YAML 没有写入。请重新加载后再编辑。",
  "profile config changed since it was loaded": "这个配置档的 YAML 已在别处更新，本次修改没有写入。请先复制你的修改，放弃后重新加载再改。",
  "subscription URL changed while the new one was being fetched": "订阅链接在拉取期间已被另一次修改替换，本次修改没有写入。请刷新后确认。",
  "subscriptionUrl requires a new profile": "订阅链接只能用于创建新配置档。",
  "home URL must start with http:// or https://": "主页链接必须以 http:// 或 https:// 开头。",
  "unknown proxy group or node": "节点组或节点不存在，请刷新后重试。",
  "subscription host did not resolve": "无法解析订阅链接对应的主机，请检查链接或网络连接。",
  "subscription redirect limit exceeded": "订阅链接重定向次数过多，请检查链接是否正确。",
  "remote profile is empty": "订阅返回的配置内容为空，请检查订阅链接。",
  "fetched subscription is required": "缺少已拉取的订阅内容，请重新更新订阅。",
  "instance state conflict": "实例状态发生冲突，请刷新后重试。",
  "reload cannot apply a port or proxy bind change; restart the instance instead": "本次修改涉及端口或代理绑定地址变更，热重载无法应用，请改为重启该实例。",
};

export type ErrorPatternRenderer = (match: RegExpMatchArray) => string;
export type ErrorPatternEntry = readonly [pattern: RegExp, render: ErrorPatternRenderer];

export const errorPatterns: readonly ErrorPatternEntry[] = [
  [/^profile "(.+)" not found$/, (match) => `配置档 ${match[1]} 不存在。`],
  [/^profile "(.+)" is not a subscription profile$/, (match) => `配置档 ${match[1]} 不是订阅配置档。`],
  [/^profile "(.+)" subscription update is already running$/, (match) => `配置档 ${match[1]} 正在更新订阅。`],
  [/^profile "(.+)" subscription URL changed during update$/, (match) => `配置档 ${match[1]} 的订阅链接在更新过程中被修改，请重新触发更新。`],
  [/^config override cannot change "(.+)"; edit the profile or local proxies instead$/, (match) => `配置覆盖不能修改 ${match[1]}，请改配置档或本地节点 YAML。`],
  [/^config override: (.+)$/, (match) => `配置覆盖 YAML 无效：${match[1]}`],
  [/^ip check request failed: (.+)$/, (match) => `测试 IP 请求失败：${match[1]}`],
  [/^ip check returned HTTP (\d+)$/, (match) => `测试 IP 网址返回 HTTP ${match[1]}。`],
  [/^ip check response is not an IP: (.+)$/, (match) => `测试 IP 网址没有返回 IP：${match[1]}`],
  [/^instance "(.+)" not found$/, (match) => `实例 ${match[1]} 不存在。`],
  [/^instance "(.+)" is being deleted$/, (match) => `实例 ${match[1]} 正在删除中，请稍后重试。`],
  // Hit routinely: the proxies tab polls an instance during the 1-4s window
  // right after it stops, while it is still transitioning through the
  // backend's not-running state.
  [/^instance "(.+)" is not running$/, (match) => `实例 ${match[1]} 未在运行，请等待其停止完成或重新启动。`],
  [/^instance "(.+)" is not running \(status: (.+)\)$/, (match) => `实例 ${match[1]} 未在运行（当前状态：${match[2]}），无法用于代理下载。`],
  [/^instance "(.+)" did not stop starting in time$/, (match) => `实例 ${match[1]} 未能在限定时间内停止启动，请稍后重试。`],
  [/^stop instance before delete: (.+)$/, (match) => `删除前请先停止实例：${match[1]}`],
  [/^mixed proxy port (\d+) is unavailable$/, (match) => `混合端口 ${match[1]} 不可用。`],
  [/^controller port (\d+) is unavailable$/, (match) => `控制端口 ${match[1]} 不可用。`],
  [/^mixed proxy port (\d+) is already in use$/, (match) => `混合端口 ${match[1]} 已被占用。`],
  [/^controller port (\d+) is already in use$/, (match) => `控制端口 ${match[1]} 已被占用。`],
  [/^process "(.+)" did not exit after force kill$/, (match) => `进程 ${match[1]} 强制结束后仍未退出。`],
  [/^mihomo config test failed: (.+)$/, (match) => `mihomo 配置测试失败：${match[1]}`],
  [/^mihomo controller unreachable: (.+)$/, (match) => `无法连接 mihomo 控制器：${match[1]}`],
  [/^mihomo returned (.+)$/, (match) => `mihomo 返回错误：${match[1]}`],
  [/^parse user config: (.+)$/, (match) => `解析用户配置失败：${match[1]}`],
  [/^subscription server returned (.+)$/, (match) => `订阅服务器返回错误：${match[1]}`],
  [/^subscription is larger than (\d+) bytes$/, (match) => `订阅内容超过 ${match[1]} 字节限制，请更换更小的订阅。`],
  [/^subscription host resolves to blocked address (.+)$/, () => "订阅链接解析到本机、内网或保留地址，已阻止。"],
  [/^remote profile data is invalid yaml: (.+)$/, (match) => `订阅内容不是有效 YAML：${match[1]}`],
  [/^remote profile must contain proxies or proxy-providers$/, () => "订阅内容缺少 proxies 或 proxy-providers。"],
  [/^instance mode "(.+)" is invalid$/, (match) => `实例模式 ${match[1]} 无效。`],
  [/^parse local proxies: (.+)$/, (match) => `解析本地节点失败：${match[1]}`],
  [/^local proxy (.+) is missing name$/, (match) => `本地节点 ${match[1]} 缺少 name。`],
  [/^local proxy name "(.+)" is duplicated$/, (match) => `本地节点 ${match[1]} 重名。`],
  [/^local proxy name "(.+)" conflicts with generated global-chain group$/, (match) => `本地节点 ${match[1]} 与内置链路组重名。`],
  [/^proxy name "(.+)" conflicts with generated global-chain group$/, (match) => `节点 ${match[1]} 与内置链路组重名。`],
  [/^local proxy name "(.+)" conflicts with profile proxy$/, (match) => `本地节点 ${match[1]} 与配置档节点重名。`],
  [/^chain references unknown proxy or group "(.+)"$/, (match) => `链路顺序引用了不存在的节点或组：${match[1]}。`],
  [/^chain cannot reference generated relay group "(.+)"$/, (match) => `链路顺序不能引用 ${match[1]} 自身。`],
  [/^chain contains duplicate member "(.+)"$/, (match) => `链路顺序重复引用了 ${match[1]}。`],
  [/^global-chain mode has no selectable proxy after chain members$/, () => "链路节点移除后没有可选择的节点。请补充订阅/出口节点，或调整链路顺序。"],
  [/^proxy bind address "(.+)" must not include a port; use the mixed port field instead$/, (match) => `代理绑定地址 ${match[1]} 不要写端口，请使用混合端口字段。`],
  [/^proxy bind address "(.+)" must be an IP address, localhost, all, or \*$/, (match) => `代理绑定地址 ${match[1]} 无效，请填写 IP、localhost、all 或 *。`],
  [/^proxy bind address "(.+)" has invalid IPv6 brackets$/, (match) => `代理绑定地址 ${match[1]} 的 IPv6 方括号不完整。`],
  // Start-time check (checkProxyBindAvailable). Usually means the instance came
  // from a backup made on another machine, or this machine's address changed.
  [
    /^proxy bind address "(.+)" is not available on this host$/,
    (match) => `代理绑定地址 ${match[1]} 不属于本机，请在基础信息里改选一个本机地址。`,
  ],
  // Fleet backup / export-import (feature #7, docs/feature-roadmap-post-1.3.md #7).
  [/^malformed import bundle: (.+)$/, (match) => `导入文件不是合法的备份文件：${match[1]}`],
  [/^unsupported bundle version (\d+) \(expected (\d+) to (\d+)\)$/, (match) => `备份文件版本 ${match[1]} 不受支持（当前程序支持版本 ${match[2]}–${match[3]}）。`],
  [/^import bundle is larger than (\d+) bytes$/, (match) => `导入文件超过 ${match[1]} 字节限制。`],
  [/^instance "(.+)" references unknown profile "(.+)"$/, (match) => `实例 ${match[1]} 引用了备份文件中不存在的配置档 ${match[2]}。`],
  [/^create profile "(.+)": (.+)$/, (match) => `创建配置档 ${match[1]} 失败：${match[2]}`],
  [/^create instance "(.+)": (.+)$/, (match) => `创建实例 ${match[1]} 失败：${match[2]}`],
  // Port mappings (internal/app/port_mappings.go). Bind/OS errors stay raw at
  // the end of a message: they are the actual reason and have no fixed text.
  [/^port mapping #(\d+): port (-?\d+) is out of range \(1-65535\)$/, (match) => `第 ${match[1]} 条端口映射的端口 ${match[2]} 超出范围（1–65535）。`],
  [/^port mapping #(\d+) \(port (\d+)\): proxy name is empty$/, (match) => `第 ${match[1]} 条端口映射（端口 ${match[2]}）未填写目标节点。`],
  [/^port mapping port (\d+) is duplicated \(#(\d+) and #(\d+)\)$/, (match) => `端口 ${match[1]} 在第 ${match[2]} 条和第 ${match[3]} 条映射里重复。`],
  [/^port mapping port (\d+) equals this instance's mixed proxy port$/, (match) => `端口映射端口 ${match[1]} 与本实例混合端口相同。`],
  [/^port mapping port (\d+) equals this instance's controller port$/, (match) => `端口映射端口 ${match[1]} 与本实例控制端口相同。`],
  [/^too many port mappings \((\d+), max (\d+)\)$/, (match) => `端口映射过多（${match[1]} 条，最多 ${match[2]} 条）。`],
  [
    /^port mapping (\d+) -> "(.+)" references unknown proxy or group; available: (.+?)(; proxy-provider nodes cannot be mapped directly, map a proxy group instead)?$/,
    (match) => `端口映射 ${match[1]} 的目标 ${match[2]} 不存在。可选：${match[3]}${match[4] ? "。订阅 provider 里的节点不能直接映射，请改选包含它的代理组" : ""}`,
  ],
  [/^port mapping port (\d+) \(-> "(.+)"\) is unavailable: (.+)$/, (match) => `端口映射端口 ${match[1]}（→ ${match[2]}）不可用：${match[3]}`],
  [/^port mapping port (\d+) \(-> "(.+)"\) is already in use on (.+?): (.+)$/, (match) => `端口映射端口 ${match[1]}（→ ${match[2]}）在 ${match[3]} 上已被占用：${match[4]}`],
  [/^port mapping port (\d+) \(-> "(.+)"\) is already in use by starting instance "(.+)"$/, (match) => `端口映射端口 ${match[1]}（→ ${match[2]}）正被启动中的实例 ${match[3]} 占用。`],
  [/^unable to allocate a port for port mapping (\d+) -> "(.+)" \((.+)\)$/, (match) => `无法为端口映射 ${match[1]}（→ ${match[2]}）分配新端口：${match[3]}`],
  [/^port mappings not listening after (.+?): (.+)$/, (match) => `端口映射在 ${match[1]} 内没有开始监听，已停止本次启动：${match[2]}`],
  [/^stop the instance before changing port mappings \(running: (.+); requested: (.+)\)$/, (match) => `修改端口映射前请先停止该实例。运行中：${match[1]}；请求：${match[2]}`],
  [/^port mappings cannot listen on proxy bind "(.+)": the wildcard (.+) would also open every address of the other family than (.+); use only wildcards or only specific addresses$/, (match) => `代理绑定地址 ${match[1]} 无法用于端口映射：通配地址 ${match[2]} 会同时打开另一地址族的所有地址，而不只是 ${match[3]}。请只用通配地址，或只用具体地址。`],
  [/^instance "(.+)" has port mappings managed by the fleet; change its config through the fleet instead of PUT \/configs$/, (match) => `实例 ${match[1]} 的端口映射由 Fleet 管理，不能通过 PUT /configs 整体替换配置，请在 Fleet 里修改后重载或重启。`],
  [/^instance "(.+)" ports changed while starting \((.+)\)$/, (match) => `实例 ${match[1]} 的端口在启动过程中被修改，本次启动已取消（${match[2]}）。`],
  [/^port mappings changed since they were loaded \(now: (.+); loaded: (.+)\)$/, (match) => `端口映射已在别处修改，本次没有保存。现在：${match[1]}；你打开时：${match[2]}。请刷新后重新修改。`],
  [/^stop the instance before changing port mapping udp \(running udp=(\w+), requested udp=(\w+)\)$/, (match) => `这次修改会让端口映射的 UDP 从 ${match[1]} 变为 ${match[2]}，请先停止该实例。`],
  [/^mihomo could not bind a port mapping: (.+)$/, (match) => `mihomo 没能监听端口映射，已停止本次启动：${match[1]}`],
  [/^mihomo exited right after its port mappings came up \((.+)\)$/, (match) => `mihomo 在端口映射就绪后立即退出，本次启动失败（${match[1]}）。`],
  [/^mihomo exited before port mappings were ready: (.+)$/, (match) => `mihomo 在端口映射就绪前退出：${match[1]}`],
  [/^port mapping check interrupted: (.+?); not yet listening: (.+)$/, (match) => `端口映射检查被中断（${match[1]}），尚未监听：${match[2]}`],
  [
    /^reload cannot apply a port mapping change \(running: (.+); saved: (.+)\); restart the instance instead$/,
    (match) => `端口映射有变更，热重载无法应用，请重启实例。运行中：${match[1]}；已保存：${match[2]}`,
  ],
  [
    /^reload cannot apply a port mapping udp change \(running udp=(\w+), saved udp=(\w+)\); restart the instance instead$/,
    (match) => `端口映射的 UDP 开关随模式变化（运行中 ${match[1]}，已保存 ${match[2]}），热重载无法应用，请重启实例。`,
  ],
];
