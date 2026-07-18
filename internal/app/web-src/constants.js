export const defaultConfig = `mixed-port: 7890
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

export const newProfileValue = "__new__";
export const legacyDefaultLatencyUrl = "https://www.gstatic.com/generate_204";
export const legacyDefaultLatencyTimeout = "5000";
export const defaultLatencyUrl = "http://cp.cloudflare.com/generate_204";
export const defaultLatencyTimeout = 10000;
export const latencyBatchConcurrency = 4;
export const latencyKeySeparator = "\u001f";
export const logStickThreshold = 24;
export const defaultProxyBind = "127.0.0.1";
export const API_SECRET_STORAGE_KEY = "fleetApiSecret";
export const slowPollIntervalMs = 4000;
export const fastPollIntervalMs = 1800;

export const latencyKinds = {
  url: "url",
  real: "real",
};

export const instanceModes = {
  rule: "rule",
  globalChain: "global-chain",
};

export const statusLabels = {
  stopped: "已停止",
  starting: "启动中",
  running: "运行中",
  error: "异常",
};

export const proxyCopyDefs = [
  { id: "addr", label: "地址", title: "复制主机和端口" },
  { id: "http", label: "HTTP", title: "复制 HTTP 代理地址" },
  { id: "socks", label: "SOCKS", title: "复制 SOCKS5 代理地址" },
  { id: "env", label: "ENV", title: "复制 bash/zsh export 环境变量" },
];

export const errorLabels = {
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
  "proxy is required": "请选择要测速的节点。",
  "proxy is required for real latency": "真延迟需要指定单个节点。",
  "latency kind must be url or real": "测速类型无效。",
  "latency test URL must start with http:// or https://": "测试 URL 必须以 http:// 或 https:// 开头。",
  "global-chain mode requires proxies, proxy-providers, or local proxies": "全局链式模式需要订阅节点、provider 或本地节点。",
  "instance is starting; retry once it finishes starting": "实例正在启动，请稍后重试。",
  "missing or invalid API token": "API 令牌缺失或无效。",
  "profile is in use by existing instances": "配置档仍被实例使用，无法删除。",
  "profile changed while configuration was being edited": "配置档已被改绑，未保存的 YAML 没有写入。请重新加载后再编辑。",
  "subscriptionUrl requires a new profile": "订阅链接只能用于创建新配置档。",
  "home URL must start with http:// or https://": "主页链接必须以 http:// 或 https:// 开头。",
};

export const errorPatterns = [
  [/^profile "(.+)" not found$/, (match) => `配置档 ${match[1]} 不存在。`],
  [/^profile "(.+)" is not a subscription profile$/, (match) => `配置档 ${match[1]} 不是订阅配置档。`],
  [/^profile "(.+)" subscription update is already running$/, (match) => `配置档 ${match[1]} 正在更新订阅。`],
  [/^instance "(.+)" not found$/, (match) => `实例 ${match[1]} 不存在。`],
  [/^instance "(.+)" is being deleted$/, (match) => `实例 ${match[1]} 正在删除中，请稍后重试。`],
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
  [/^subscription host resolves to blocked address (.+)$/, () => "订阅链接解析到本机、内网或保留地址，已阻止。"],
  [/^remote profile data is invalid yaml: (.+)$/, (match) => `订阅内容不是有效 YAML：${match[1]}`],
  [/^remote profile must contain proxies or proxy-providers$/, () => "订阅内容缺少 proxies 或 proxy-providers。"],
  [/^instance mode "(.+)" is invalid$/, (match) => `实例模式 ${match[1]} 无效。`],
  [/^parse local proxies: (.+)$/, (match) => `解析本地节点失败：${match[1]}`],
  [/^local proxy (.+) is missing name$/, (match) => `本地节点 ${match[1]} 缺少 name。`],
  [/^local proxy name "(.+)" is duplicated$/, (match) => `本地节点 ${match[1]} 重名。`],
  [/^local proxy name "(.+)" conflicts with generated global-chain group$/, (match) => `本地节点 ${match[1]} 与内置链路组重名。`],
  [/^local proxy name "(.+)" conflicts with profile proxy$/, (match) => `本地节点 ${match[1]} 与配置档节点重名。`],
  [/^chain references unknown proxy or group "(.+)"$/, (match) => `链路顺序引用了不存在的节点或组：${match[1]}。`],
  [/^chain cannot reference generated relay group "(.+)"$/, (match) => `链路顺序不能引用 ${match[1]} 自身。`],
  [/^chain contains duplicate member "(.+)"$/, (match) => `链路顺序重复引用了 ${match[1]}。`],
  [/^global-chain mode has no selectable proxy after chain members$/, () => "链路节点移除后没有可选择的节点。请补充订阅/出口节点，或调整链路顺序。"],
  [/^proxy bind address "(.+)" must not include a port; use the mixed port field instead$/, (match) => `代理绑定地址 ${match[1]} 不要写端口，请使用混合端口字段。`],
  [/^proxy bind address "(.+)" must be an IP address, localhost, all, or \*$/, (match) => `代理绑定地址 ${match[1]} 无效，请填写 IP、localhost、all 或 *。`],
  [/^proxy bind address "(.+)" has invalid IPv6 brackets$/, (match) => `代理绑定地址 ${match[1]} 的 IPv6 方括号不完整。`],
];
