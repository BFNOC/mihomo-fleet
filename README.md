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

WebUI 源码位于 `internal/app/web-src`（Vue 3 单文件组件 + Vite）。构建产物写到
`internal/app/web/`，由 Go 的 `go:embed` 嵌入二进制。**这些产物不在 git 里**——
只有 `internal/app/web/README.md` 被跟踪，因为 `go:embed web/*` 对空目录会编译失败。
`scripts/build.sh` 会先构建前端再编译 Go，并检查产物确实生成了：缺少产物时 Go
仍然能编译通过（README 满足了 `go:embed`），只是跑出来的程序没有界面。

首次构建前先装依赖：

```bash
pnpm install --frozen-lockfile
```

修改前端后的校验：

```bash
pnpm test:web                               # 纯逻辑单测
./node_modules/.bin/vue-tsc --noEmit        # 类型检查，须 0 错误
```

最终程序运行时仍不需要 Node.js 或 pnpm。

### 与已运行的正式版并存调试

`scripts/dev.sh` 用独立端口和独立数据目录起一个开发版，不影响正在运行的正式版：

```bash
./scripts/dev.sh                    # http://127.0.0.1:47891 + <repo>/.mihomo-fleet-dev
./scripts/dev.sh --no-web           # 只改了 Go，跳过前端构建
./scripts/dev.sh --port 47892 --data ~/fleet-dev
```

改前端想即时看到效果时，把前后端分开跑：一个终端起后端，另一个终端起 Vite 开发服务器，
它带热更新，并把 `/api` 转发到后端：

```bash
./scripts/dev.sh --no-web           # 终端 1：后端，http://127.0.0.1:47891
pnpm dev:web                        # 终端 2：前端，打开 http://localhost:5173
```

后端改了 `--port` 时，给 Vite 设同样的 `FLEET_DEV_PORT`。改 Go 代码仍要重跑 `dev.sh`。

版本号会刻成 `dev-<commit>`，WebUI 顶部能直接区分开发版和正式版。数据目录必须分开：
`instances.json` 没有进程锁，两个 fleet 指向同一目录会互相覆盖写，并抢同一组实例端口
（脚本会直接拒绝 `--data` 指向正式版目录）。新建实例的端口从 28000/29000 起分配，
分配前会探测端口是否空闲，所以正式版实例运行时不会被抢；但正式版实例停着的时候
探测是通的，建议给开发版实例手动填 28100/29100 段。

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

### 开机自启（systemd）

Mihomo Fleet 自己不安装服务、不写开机项，也没有 `-install-service` 之类的参数。
需要长期后台运行时，交给系统的服务管理器；下面是 Linux 上的 systemd 示例。

```ini
# /etc/systemd/system/mihomo-fleet.service
[Unit]
Description=Mihomo Fleet
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mihomo
WorkingDirectory=/opt/mihomo-fleet
ExecStart=/opt/mihomo-fleet/mihomo-fleet -data /var/lib/mihomo-fleet -mihomo /opt/mihomo-fleet/mihomo
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now mihomo-fleet
sudo journalctl -u mihomo-fleet -f
```

几点注意：

- `VERSION` 文件要和二进制放在同一个目录（Release 压缩包本身就是这个结构）。
  Mihomo Fleet 从工作目录或二进制所在目录读取它；找不到时版本号显示为 `dev`。
- `-data` 指向的目录会保存实例配置、Profile、控制器密钥和地理数据，需要对
  `User=` 指定的账号可写，且不应放在 `/tmp`。
- 服务默认只监听 `127.0.0.1:47890`。要从别的机器访问 WebUI，先读下一节，
  `-bind` 与 `-api-secret` 必须一起用。

macOS 用 `launchd`、Windows 用任务计划程序或 `sc.exe`，思路相同：由系统拉起
二进制，不要依赖 Mihomo Fleet 自己做守护。

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

启动实例前，Fleet 会逐个试绑这些地址。如果某个地址不属于当前机器（常见于备份导入到
另一台设备，或本机网卡地址已变化），实例不会启动，并提示`代理绑定地址 X 不属于本机`；
改选一个本机地址后再启动即可。

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

## 给局域网提供服务

代理端口和 Fleet WebUI 是两个独立的暴露面，分别控制：代理端口由每个实例的
“代理绑定地址”决定，WebUI 由启动参数 `-bind` 决定。只开放需要的那一个，
并且都只在可信网络中开放，或配合防火墙限制来源。

让局域网设备使用某个实例的代理端口：

1. 在 WebUI 中编辑该实例，把“代理绑定地址”改为 `127.0.0.1,<本机局域网 IP>`；
   确认当前网络可信时也可以填 `all`（见“按端口给程序分流”）。
2. 重启该实例。绑定地址属于网络配置，无法热更新到运行中的实例，重启后才生效。
3. 在侧边栏“端口矩阵”复制局域网可用的代理地址，填到局域网设备的代理设置里。

代理端口没有认证，任何能连到该地址的设备都可以使用这个实例的出口。

让局域网设备访问 WebUI，启动时绑定非回环地址并设置令牌：

```bash
./mihomo-fleet -bind 0.0.0.0 -api-secret "$(openssl rand -hex 32)"
```

局域网浏览器打开 `http://<本机局域网 IP>:47890`。页面首次请求 API 收到 `401` 时，
WebUI 会弹窗询问令牌；填入 `-api-secret` 的值后浏览器会记住它，之后自动携带。
风险边界和令牌保管要求见“安全模型与 `-api-secret`”。

## 测试出口 IP

实例详情的标签行右侧有“测试 IP”按钮：Fleet 经这个实例的混合端口请求一个回显 IP 的网址，
把结果写到“概览 → 出口 IP”。默认网址是 `https://api.ip.sb/ip`，“概览”里的“测试 IP 网址”
可以换成下拉里的其他服务（ipify、icanhazip、ifconfig.me、ipinfo）或自填；网址需返回纯文本 IP
或带 `ip` 字段的 JSON，选择记在浏览器本地。实例必须处于运行中。

## 实例级配置覆盖

多个实例共用一份订阅配置档时，可以在“概览 → 编辑基础信息 → 配置覆盖 YAML”里给单个实例
叠加一段 YAML，只影响这个实例生成的运行配置，不改动配置档本身，订阅更新后依然生效。
合并方式沿用 clash-verge-rev 的 Merge 配置：

- `prepend-<键>` / `append-<键>`：把列表拼到配置档同名列表的前面 / 后面，常用于 `rules`、
  `proxies`、`proxy-groups`
- 两边都是映射的键（如 `dns`）递归合并，只改你写的字段
- 其余键直接替换配置档里的值

例如订阅规则里有 `NETWORK,udp,REJECT`，但这个实例需要放行 UDP：

```yaml
prepend-rules:
  - NETWORK,udp,节点选择
```

规则整条写成一个列表项；用行内写法时要加引号 `['NETWORK,udp,节点选择']`，否则 YAML 会按逗号拆成三项。

覆盖不能改 `proxies`、`proxy-groups`、`proxy-providers`（含 `prepend-` / `append-` 形式），保存时会拒绝：
节点列表、订阅刷新时的选择校正、链路校验都按配置档本身解析，覆盖改了节点集合会和运行配置脱节。
要加节点请改配置档，全局链式模式下用“本地节点 YAML”。端口、`listeners`、`tun` 等由 Fleet 接管的键
即使写进覆盖也会被剥离。“全局链式”模式自己生成 `rules`（先 `NETWORK,UDP,REJECT` 再 `MATCH,<链路目标>`，
默认拒绝 UDP 以免链路承载不了 UDP 时回落直连），覆盖里的 `rules:` 在该模式下不生效，
但 `prepend-rules` / `append-rules` 仍会拼到生成规则的前后，所以上面的放行 UDP 写法在两种模式下都可用。

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
