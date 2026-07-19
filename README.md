# Mihomo Fleet

Mihomo Fleet 是一个只在本机运行的 `mihomo` 多实例控制器。

它以一个原生 Go 二进制文件运行，并内置 WebUI。运行时不需要 Node.js 或
Python 服务。

## 项目目录名

本项目文件夹建议命名为 `mihomo-fleet`。这个名称与 Go module、命令目录、
构建出的可执行文件以及产品名保持一致，克隆、解压或本地重命名后都更容易识别。

## 构建

```bash
./scripts/build.sh
```

版本号来自根目录 `VERSION`，脚本会把版本注入到根目录 `./mihomo-fleet`，避免误用
`go build cmd/mihomo-fleet/main.go` 生成 `./main`。
如果没有通过 `-ldflags` 注入版本，程序会在启动时从同目录/当前目录的 `VERSION` 文件
兜底读取版本号；没有 VERSION 文件时再使用 Go build info。所以本地 `go run`、普通
`go build` 和 WebUI 顶部也会显示同一版本。

查看当前二进制版本：

```bash
./mihomo-fleet -version
```

WebUI 源码位于 `internal/app/web-src`（模块化 ES modules）。构建产物提交到
`internal/app/web/app.js` 与 `internal/app/web/vendor`，并由 Go 二进制嵌入。
修改前端后使用 pnpm 重新构建和校验：

```bash
pnpm install --frozen-lockfile
pnpm test:web
pnpm verify:web
```

最终程序运行时仍不需要 Node.js 或 pnpm。

## 版本和发布

当前版本以根目录 `VERSION` 文件为准。

GitHub Actions 只在 `VERSION` 文件变更的 push 上执行三端编译，并创建或更新
`v版本号` 的 GitHub Release。Release assets 只包含对应平台的单个可执行文件：

```text
mihomo-fleet-linux-amd64
mihomo-fleet-linux-arm64
mihomo-fleet-darwin-amd64
mihomo-fleet-darwin-arm64
mihomo-fleet-windows-amd64.exe
mihomo-fleet-windows-arm64.exe
```

构建 artifact 保留 1 天；每周清理 14 天前的 artifact 和 30 天前已完成的 workflow run，
不会删除 Release 及其 assets。

## 运行

```bash
./mihomo-fleet
```

打开：

```text
http://127.0.0.1:47890
```

Fleet WebUI 默认绑定到 `127.0.0.1`。可复用的 Profile 用来保存订阅或配置 YAML。
每个受管 mihomo 实例会引用一个 Profile，同时拥有自己生成的运行时配置、
代理端口、external-controller 端口、随机控制器密钥，以及已保存的节点选择。

配置档与实例在 WebUI 中分别管理：顶部的“配置档管理”用于新建、重命名、修改和
删除 Profile；新建实例时只选择已有 Profile，不再在实例表单里临时创建配置档。
同一个 Profile 可以被多个实例引用。手写 YAML 或订阅缓存内容发生变化时，会影响
所有引用实例，运行中的实例会提示重启后生效；仍被实例引用的 Profile 不能删除，
需先将这些实例改绑到其他 Profile。

Profile 可以有两种来源：

- 手写配置：直接编辑 YAML，作为最基础、最可控的使用方式。
- 订阅链接：填写 HTTP(S) 订阅地址后，Mihomo Fleet 会下载并缓存 Clash/Mihomo
  YAML。订阅 Profile 支持手动“立即更新”，也可以设置自动更新间隔。
  手动或自动更新得到新内容后，所有引用实例都会标记为需要重启。
  为避免订阅链接通过 DNS 重绑定访问本机或内网，订阅下载会直连并拒绝本机、
  内网、链路本地和保留地址，不读取系统代理环境变量。
  订阅 Profile 的 YAML 内容来自远端缓存；需要手动编辑 YAML 时，请使用手写配置。

多个实例可以引用同一个订阅 Profile，并分别保存自己的节点选择。例如一个实例的
混合端口固定选择美国节点，另一个实例的混合端口固定选择日本节点。实例运行时会
通过 mihomo external-controller 立即应用选择；实例停止时也可以先从缓存配置里选择，
下次启动后会自动恢复。

## 安全模型与 `-api-secret`

Fleet WebUI 默认绑定 `127.0.0.1`：控制面（读取实例/Profile 配置、启停实例、改代理
选择等）在这个默认值下**没有额外认证**，信任模型是“同一台机器上能访问回环地址的
进程/用户是可信的”，与 `mihomo` 自身的默认行为一致。

如果你需要从另一台设备访问面板，把 `-bind` 改成 `0.0.0.0` 或某个局域网 IP 会让这个
无认证的控制面暴露到网络上——任何能连到该地址的人都可以读取代理凭据、启停或删除
实例。因此：

- 绑定非回环地址（`-bind` 不是 `127.0.0.1`/`::1`/`localhost`）时，**必须**同时设置
  `-api-secret`，否则程序会拒绝启动并报错退出。
- 设置 `-api-secret` 后，所有 `/api/*` 请求都必须带上
  `Authorization: Bearer <token>`，否则返回 `401`；静态页面资源不需要令牌，
  这样浏览器才能先加载出界面。WebUI 会在首次收到 `401` 时弹窗询问令牌并保存到
  浏览器 `localStorage`（键名 `fleetApiSecret`），之后自动带上。
- 即使配置了 `-api-secret`，非回环绑定仍会在启动日志打印一条明显的 WARNING，
  提醒你控制面已经暴露到网络，请只在可信网络（或配合防火墙/VPN）下这样做，
  并妥善保管这个令牌（它等价于该 Fleet 实例的完整控制权限）。

```bash
# 局域网访问示例：生成一个随机令牌并要求所有 /api/ 请求携带它
./mihomo-fleet -bind 0.0.0.0 -api-secret "$(openssl rand -hex 32)"
```

回环绑定（默认）下 `-api-secret` 是可选的：不设置时行为和之前完全一样（无需认证）；
设置了同样会对 `/api/*` 生效，可用于同主机多用户场景下的额外隔离。

## 按端口给程序分流

Mihomo Fleet 的核心用法不是切换系统代理，而是让每个实例固定占用一个本地混合端口。
你可以把一个实例配置成美国出口，另一个实例配置成英国出口，然后把不同程序指向不同
代理地址：

```text
程序 A -> 127.0.0.1:28000 -> 美国出口
程序 B -> 127.0.0.1:28001 -> 英国出口
```

每个实例的“代理绑定地址”默认是 `127.0.0.1`，也就是只让本机程序访问该实例的代理端口。
如果你需要让虚拟机、局域网设备或指定网卡访问这个实例，可以在实例里填写多个绑定地址：

```text
127.0.0.1,192.168.64.1
```

Fleet 会为这些地址生成 mihomo `listeners`，让同一个混合端口监听在多个网卡地址上。
如果确认当前网络可信，也可以填写：

```text
all
# 或
0.0.0.0
```

这会让该实例的代理端口监听所有 IPv4 网卡。注意这暴露的是 mihomo 代理端口，不是 Fleet
WebUI；不要把它开放到不可信网络。

这样程序 A 和程序 B 可以同时运行在不同出口上，不需要在同一个 Clash Verge Rev 窗口里
反复切换节点。

WebUI 侧边栏的“端口矩阵”会列出每个实例当前可用的代理地址，并提供四种复制入口：

- 地址：复制 `<绑定地址>:<混合端口>`；多绑定地址会按行复制。
- HTTP：复制 `http://<绑定地址>:<混合端口>`；多绑定地址会按行复制。
- SOCKS：复制 `socks5://<绑定地址>:<混合端口>`；多绑定地址会按行复制。
- ENV：复制 bash/zsh 可直接使用的 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`
  以及对应小写变量。多绑定地址时 ENV 使用第一条绑定地址。

例如把某个命令临时指向英国实例：

```bash
export HTTP_PROXY='http://127.0.0.1:28001'
export HTTPS_PROXY='http://127.0.0.1:28001'
export ALL_PROXY='socks5://127.0.0.1:28001'
export http_proxy='http://127.0.0.1:28001'
export https_proxy='http://127.0.0.1:28001'
export all_proxy='socks5://127.0.0.1:28001'
```

如果程序自身支持代理设置，直接填对应实例的 HTTP 或 SOCKS 地址即可。Mihomo Fleet 不会
替你改系统代理，也不会按进程名自动接管流量；分流边界由目标程序是否使用你填入的代理
地址决定。把 ENV 内容导出到当前 shell 时，只影响当前 shell 及其后续启动的子进程。

## 全局链式代理模式

实例默认使用“规则分流”模式：运行配置会沿用 Profile 里的 `proxy-groups` 和 `rules`。
如果只想使用订阅里的节点池，而不想继承订阅分流规则，可以把实例模式切到“全局链式”。

全局链式模式会在启动时生成新的运行配置：

- 保留订阅缓存里的 `proxies` 和 `proxy-providers`。
- 丢弃订阅自带的 `proxy-groups`、`rules`、`rule-providers`。
- 生成 `节点选择` 组，用来选择订阅节点或本地节点。
- 使用 mihomo 的 `dialer-proxy` 串起前置节点，并写入 `MATCH,节点选择`，进入该实例端口的流量全部走选中的出口节点。

本地节点以 YAML 列表保存到实例上，不会写回共享订阅 Profile。例如：

```yaml
- name: local-hop
  type: socks5
  server: 127.0.0.1
  port: 1080
```

链路顺序可以每行写一个名称，例如：

```text
local-hop
节点选择
```

链路顺序的方向是从上到下：上面的节点是前置入口，最后一行是最终出口。上例表示
`节点选择` 里选中的订阅节点会通过 `local-hop` 拨号。也可以反过来写：

```text
节点选择
local-hop
```

这表示 `local-hop` 会通过 `节点选择` 里选中的订阅节点拨号。Fleet 会把链路里已经固定使用的
节点从 `节点选择` 的候选项里移除，避免选到自己形成环。

如果链路顺序留空，Fleet 会默认按“本地节点 YAML 顺序 + `节点选择`”生成链；如果只有本地节点、
没有订阅节点，则退化为只走 `节点选择`。订阅使用 `proxy-providers` 时，provider 节点会通过
`override.dialer-proxy` 接入链路；provider 节点需要实例启动后由 mihomo 展开，停止状态下只能看到
配置里已有的 inline 节点和本地节点。链路成员只接受配置里已有的 inline 节点名、本地节点名或
`节点选择` 组；`DIRECT`、`REJECT` 这类内置结果不能作为链路成员。

## mihomo 二进制文件放置

推荐把 `mihomo` 和 `mihomo-fleet` 放在同一个执行目录中，不需要放进全局 `PATH`：

```text
mihomo-fleet/
  mihomo-fleet
  mihomo        # macOS / Linux
  mihomo.exe    # Windows
```

然后直接启动：

```bash
./mihomo-fleet
```

程序会优先查找 `mihomo-fleet` 可执行文件所在目录里的 `mihomo`；在 Windows 下会先尝试
`mihomo.exe`，再尝试 `mihomo`。

如果你要临时指定另一个版本，可以用 `-mihomo` 覆盖自动查找：

```bash
./mihomo-fleet -mihomo /path/to/mihomo
```

相对路径会按启动 `mihomo-fleet` 时的当前目录解析；如果不确定当前目录，建议传入绝对路径。

如果同目录没有 `mihomo`，程序仍会尝试从 `PATH` 查找，作为兼容兜底。

不建议把 `mihomo` 放进 `.mihomo-fleet/`，因为那里是运行时数据目录，会被程序写入
实例状态和配置文件；混放可执行文件容易在迁移或清理数据时被误删。

如果同执行目录和 `PATH` 都找不到 `mihomo`，WebUI 仍会启动；但当你尝试启动实例时，
界面会显示可操作的启动错误。

## 运行时数据

默认数据会存放在：

```text
.mihomo-fleet/
  instances.json
  geo/
    GeoSite.dat
    GeoIP.dat
  profiles/
    <id>/
      config.yaml
  instances/
    <id>/
      config.runtime.yaml
```

可以用下面的参数改到其他位置：

```bash
./mihomo-fleet -data /path/to/runtime
```

如果订阅规则使用 `GEOSITE` 或 `GEOIP`，需要让 mihomo 能读到 geodata 文件。
推荐把 `GeoSite.dat` 和 `GeoIP.dat` 放进 `.mihomo-fleet/geo/`；程序启动实例前会自动
把它们链接到对应实例目录。为了兼容本地调试，也会从 `mihomo-fleet` 可执行文件所在目录、
启动时当前目录、以及 `mihomo` 二进制文件所在目录查找 `GeoSite.dat` / `geosite.dat` 和
`GeoIP.dat` / `geoip.dat`。查找顺序是：数据目录的 `geo/`、数据目录、Fleet 可执行文件
目录、启动时当前目录、`mihomo` 二进制文件目录。
