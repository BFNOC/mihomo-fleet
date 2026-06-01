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

查看当前二进制版本：

```bash
./mihomo-fleet -version
```

## 运行

```bash
./mihomo-fleet
```

打开：

```text
http://127.0.0.1:47890
```

服务默认绑定到 `127.0.0.1`。可复用的 Profile 用来保存订阅或配置 YAML。
每个受管 mihomo 实例会引用一个 Profile，同时拥有自己生成的运行时配置、
代理端口、external-controller 端口、随机控制器密钥，以及已保存的节点选择。

Profile 可以有两种来源：

- 手写配置：直接编辑 YAML，作为最基础、最可控的使用方式。
- 订阅链接：填写 HTTP(S) 订阅地址后，Mihomo Fleet 会下载并缓存 Clash/Mihomo
  YAML。订阅 Profile 支持手动“立即更新”，也可以设置自动更新间隔。
  为避免订阅链接通过 DNS 重绑定访问本机或内网，订阅下载会直连并拒绝本机、
  内网、链路本地和保留地址，不读取系统代理环境变量。
  订阅 Profile 的 YAML 内容来自远端缓存；需要手动编辑 YAML 时，请使用手写配置。

多个实例可以引用同一个订阅 Profile，并分别保存自己的节点选择。例如一个实例的
混合端口固定选择美国节点，另一个实例的混合端口固定选择日本节点。实例运行时会
通过 mihomo external-controller 立即应用选择；实例停止时也可以先从缓存配置里选择，
下次启动后会自动恢复。

## 按端口给程序分流

Mihomo Fleet 的核心用法不是切换系统代理，而是让每个实例固定占用一个本地混合端口。
你可以把一个实例配置成美国出口，另一个实例配置成英国出口，然后把不同程序指向不同
代理地址：

```text
程序 A -> 127.0.0.1:28000 -> 美国出口
程序 B -> 127.0.0.1:28001 -> 英国出口
```

这样程序 A 和程序 B 可以同时运行在不同出口上，不需要在同一个 Clash Verge Rev 窗口里
反复切换节点。

WebUI 侧边栏的“端口矩阵”会列出每个实例当前可用的代理地址，并提供四种复制入口：

- 地址：复制 `127.0.0.1:<混合端口>`。
- HTTP：复制 `http://127.0.0.1:<混合端口>`。
- SOCKS：复制 `socks5://127.0.0.1:<混合端口>`。
- ENV：复制 bash/zsh 可直接使用的 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`
  以及对应小写变量。

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
