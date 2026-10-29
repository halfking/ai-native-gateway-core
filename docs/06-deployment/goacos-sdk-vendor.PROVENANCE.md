# goacos Go SDK（vendor 副本）溯源记录

**副本位置**：`vendor/github.com/halfking/goacos/`
**上游**：<https://github.com/halfking/goacos>（模块路径 `github.com/halfking/goacos`）
**钉住的版本**：`v1.2.0`（tag commit `3bf242746202af0660cfc6f1dc29348e20ac46e0`）
**记录日期**：2026-10-29
**方案**：54 / CO-20（llm-gateway 接入 goacos 自动注册）

---

## 为什么会有这个仓内副本

本仓有 `vendor/` 目录 ⇒ Go 默认以 **vendor 模式**构建 ⇒ 新增的依赖必须出现在
`vendor/` 里，否则 `go build` 报 "inconsistent vendoring"。

离线环境下 `go mod vendor` 不能用（模块缓存里缺传递依赖
`github.com/Azure/go-ansiterm/winterm`，`GOPROXY=off` 直接失败）⇒ 这份副本是
**手工放入**的，并在这里留下可机器核对的记录。

⚠ **手工放入的副本天然会腐化**：没人改它，也没人知道它被谁改过。所以下面
每个文件都记 sha256，跨仓判据
`myvpn/scripts/tests/test_goacos_registration_contract.py` 的 **T12** 会逐个核对
（核对对象是**本文件里记的 sha256**，不是上游工作区 —— 理由见下）。

## 逐文件 sha256

| 副本文件（相对本仓） | sha256 |
|---|---|
| `vendor/github.com/halfking/goacos/sdk/registry/client.go` | `d5160d0cb1ccaba40e729293aae95369e4d2eebe976250e686e82538e0b0f9b9` |
| `vendor/github.com/halfking/goacos/sdk/registry/discover.go` | `7dc8136cf03f9cecd891a387d47bedd941d9de873b7616eb6ed722317719f2af` |
| `vendor/github.com/halfking/goacos/sdk/registry/doc.go` | `33340790883d19aaa9a6080869b95fd2572f44617412b3e8a2f22217ffc83aa2` |
| `vendor/github.com/halfking/goacos/LICENSE` | `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30` |

## 为什么**不**拿上游工作区当对照

2026-10-29 实测：上游仓工作区的 `sdk/registry/client.go` 与 `doc.go` 已经
**领先于** `v1.2.0`（上游工作区还有 `service.go` / `app.go` 等 v1.2.0 里没有的
文件）。逐字节对比的读数（as-of 2026-10-29）：

| 文件 | 上游工作区 | `v1.2.0` 标签 | 模块缓存 v1.2.0 | 本仓 vendor |
|---|---|---|---|---|
| `client.go` | `741d657f…` | `d5160d0c…` | `d5160d0c…` | `d5160d0c…` |
| `discover.go` | `7dc8136c…` | `7dc8136c…` | `7dc8136c…` | `7dc8136c…` |
| `doc.go` | `ad3dff0b…` | `33340790…` | `33340790…` | `33340790…` |

⇒ 对照基准必须是**版本标签**（或模块缓存），不是上游工作区。
**本仓这份副本 = `v1.2.0` 的发布态**，这是有意的：`go.mod` 钉的就是 `v1.2.0`。

## 怎么重算

```bash
cd llm-gateway/llm-gateway-go
for f in sdk/registry/client.go sdk/registry/discover.go sdk/registry/doc.go LICENSE; do
  printf '%-28s %s\n' "$f" \
    "$(git -C ../../goacos show v1.2.0:$f | shasum -a 256 | cut -d' ' -f1)"
done
```

若上游发了新版本并升级 `go.mod`：先 `go mod vendor`（联网）刷新 `vendor/`，
再重算上表并更新本文件；**两步都要做**，只做一步 ⇒ T12 立刻报红。

## 已知取舍

- **Go 服务不需要像 Python / Node 那样额外维护一份"未发布"的副本**：`goacos`
  这个模块已经发布到代理仓，`go.mod` 直接 require 即可（RedClaw 的 8 个服务
  就是这么用的）。本仓的 `vendor/` 副本是**构建模式**的产物，不是"因为没发布
  才自己拷一份"。
- Python 绑定（`sdk/registry-python/`）与 Node 绑定（`sdk/registry-node/`）在
  PyPI / npm 上**尚未发布** ⇒ 那两边才需要仓内副本 + 各自的 PROVENANCE。
