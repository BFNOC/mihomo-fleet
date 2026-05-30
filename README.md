# Mihomo Fleet

Mihomo Fleet is a local-only multi-instance controller for `mihomo`.

It runs as one native Go binary with an embedded WebUI. No Node.js or Python
service is required at runtime.

## Build

```bash
go build -o mihomo-fleet ./cmd/mihomo-fleet
```

## Run

```bash
./mihomo-fleet
```

Open:

```text
http://127.0.0.1:47890
```

The server binds to `127.0.0.1` by default. A reusable Profile stores your
subscription/config YAML. Each managed mihomo instance references one Profile
but still gets its own generated runtime config, proxy port,
external-controller port, random controller secret, and saved proxy selection.

If `mihomo` is not available in `PATH`, the WebUI still runs and shows an
actionable start error when you try to launch an instance. You can also pass a
specific binary:

```bash
./mihomo-fleet -mihomo /path/to/mihomo
```

## Runtime Data

By default data is stored under:

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

Override it with:

```bash
./mihomo-fleet -data /path/to/runtime
```
