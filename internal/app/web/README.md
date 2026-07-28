# 前端构建产物目录

这个目录的内容由 `pnpm build:web` 从 `internal/app/web-src/` 生成，**不提交到 git**。

正常构建后这里会有：

```
app.js                 Vue 入口，首屏唯一阻塞的脚本
chunk-app.js           动态加载：轮询与配置档网络层（app.ts）
chunk-yaml-editor.js   动态加载：CodeMirror 6，只有配置档视图会取
styles.css
index.html
vendor/THIRD_PARTY_NOTICES.txt
```

两个 chunk 都必须是**动态** import。`app.js` 里出现 `from"./chunk-*.js"`
（静态）就是回归，构建后 grep 产物确认，别看构建日志。

## 这个文件本身为什么要提交

`internal/app/static.go` 用 `//go:embed web/*` 把整个目录编译进二进制。而 `go:embed`
在找不到任何匹配文件时是**编译期错误**，不是警告：

```
pattern web/*: no matching files found
```

产物既然不进 git，新克隆下来这个目录就是空的，`go build ./...` 会直接编译失败。留一个
文件在这里，glob 就总有匹配，编译能过。

注意不能用 `.gitkeep` 之类的点文件占位——`go:embed` 默认跳过 `.` 和 `_` 开头的文件，
放了也等于没放。

## 本地开发

拿到源码后先构建前端，再编译 Go：

```
pnpm install
pnpm build:web
go build ./cmd/mihomo-fleet
```

跳过第二步的话二进制能编译出来，但访问首页是 404——因为嵌进去的只有这个说明文件。
