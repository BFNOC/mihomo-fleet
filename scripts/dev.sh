#!/usr/bin/env bash
# 与已在运行的正式版并存的开发版启动脚本。
#
# 关键约束：端口和数据目录都必须与正式版分开。instances.json 没有进程锁，
# 两个 fleet 指向同一个数据目录会互相覆盖写，并且会用同一组 mixedPort /
# controllerPort 去拉 mihomo 子进程。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PORT="${FLEET_DEV_PORT:-47891}"
DATA="${FLEET_DEV_DATA:-$ROOT/.mihomo-fleet-dev}"
OUT="${FLEET_DEV_BIN:-$ROOT/.dev/mihomo-fleet-dev}"
MIHOMO="${FLEET_DEV_MIHOMO:-}"
BUILD_WEB=1

usage() {
  cat <<'EOF'
用法: scripts/dev.sh [选项] [-- 透传给 mihomo-fleet 的参数]

  --port <n>       WebUI 端口，默认 47891（正式版默认 47890）
  --data <dir>     数据目录，默认 <repo>/.mihomo-fleet-dev
  --mihomo <path>  mihomo 二进制路径，默认 <repo>/mihomo，再退回 PATH
  --no-web         跳过 pnpm build:web，只重新编译 Go（前端没改时用）
  --build-only     构建完就退出，不启动
  -h, --help       显示本帮助

环境变量同名可用: FLEET_DEV_PORT / FLEET_DEV_DATA / FLEET_DEV_MIHOMO /
FLEET_DEV_BIN。
EOF
}

BUILD_ONLY=0
PASSTHROUGH=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    --data) DATA="$2"; shift 2 ;;
    --mihomo) MIHOMO="$2"; shift 2 ;;
    --no-web) BUILD_WEB=0; shift ;;
    --build-only) BUILD_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; PASSTHROUGH=("$@"); break ;;
    *) printf '未知参数: %s\n\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

# 数据目录撞上正式版是最容易造成静默数据损坏的一种用法，直接拒绝。
if [[ "$(cd "$(dirname "$DATA")" 2>/dev/null && pwd)/$(basename "$DATA")" == "$ROOT/.mihomo-fleet" ]]; then
  printf '拒绝启动: --data 指向了正式版的 %s。\n' "$ROOT/.mihomo-fleet" >&2
  printf '两个 fleet 共用数据目录会互相覆盖 instances.json，并抢同一组实例端口。\n' >&2
  exit 1
fi

if [[ -z "$MIHOMO" ]]; then
  if [[ -x "$ROOT/mihomo" ]]; then
    MIHOMO="$ROOT/mihomo"
  else
    MIHOMO="$(command -v mihomo || true)"
  fi
fi
if [[ -z "$MIHOMO" ]]; then
  printf '找不到 mihomo 二进制，用 --mihomo 指定路径。\n' >&2
  exit 1
fi

if [[ $BUILD_WEB -eq 1 ]]; then
  printf '[1/2] build:web\n'
  # Direct, not via `pnpm run`: pnpm's deps-status check can decide to purge and
  # reinstall node_modules, and with no TTY it aborts mid-removal.
  (cd "$ROOT" && node scripts/build-web.mjs)
else
  printf '[1/2] 跳过前端构建 (--no-web)\n'
fi

# 版本号刻成 dev-<commit>，这样 WebUI 上能一眼分清开发版和正式版。
# 不刻的话 main.go 会回退去读仓库根的 VERSION，显示成正式版本号。
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || printf 'unknown')"
if ! git -C "$ROOT" diff --quiet --ignore-submodules HEAD -- 2>/dev/null; then
  COMMIT="${COMMIT}-dirty"
fi

printf '[2/2] go build -> %s\n' "$OUT"
mkdir -p "$(dirname "$OUT")"
(cd "$ROOT" && go build -ldflags "-X main.version=dev-$COMMIT -X main.commit=$COMMIT" -o "$OUT" ./cmd/mihomo-fleet)

if [[ $BUILD_ONLY -eq 1 ]]; then
  exit 0
fi

if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then
  exec 3>&- 3<&-
  printf '端口 %s 已被占用，换一个: scripts/dev.sh --port %s\n' "$PORT" "$((PORT + 1))" >&2
  exit 1
fi

if [[ ! -d "$DATA" ]]; then
  printf '\n数据目录 %s 是新建的，实例列表为空。\n' "$DATA"
  printf '新建实例时端口从 28000/29000 起分配，分配前会探测端口是否空闲，\n'
  printf '所以正式版实例正在运行时不会被抢。但正式版实例停着的时候探测是通的，\n'
  printf '开发版会占掉 28000 导致正式版之后起不来 —— 建议手动填 28100/29100 段。\n'
fi

printf '\n开发版: http://127.0.0.1:%s  (数据目录 %s)\n\n' "$PORT" "$DATA"

# 必须在仓库根启动: geo 文件查找顺序是 dataDir/geo -> dataDir -> 可执行文件目录
# -> CWD -> mihomo 所在目录，靠 CWD 兜底命中根目录的 country.mmdb / geoip.dat /
# geosite.dat，新数据目录才不用复制这几个大文件。
cd "$ROOT"
exec "$OUT" -port "$PORT" -data "$DATA" -mihomo "$MIHOMO" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
