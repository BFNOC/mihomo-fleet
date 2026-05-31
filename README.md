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
