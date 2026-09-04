# 事故复盘：本地部署静默复用陈旧二进制（stale gateway.build promotion）

**日期：** 2026-09-05
**环境：** `local`（llm-gateway-local-8781/8782，macOS 宿主 + Docker）
**发现者：** 会话快照特性环境侧复验（2026-09-05 晨）
**修复提交：** `bc6e696b3`（storage/sqlite build-tag 分裂 + build_backend 防线）、`b08ea0607`（与 cbf6de062 合并）

## 1. 事故摘要

2026-09-04 夜间至 09-05 晨的多次本地部署（至少 2.4.7.1939、2.5.0.1934 两次已实证）
虽然流程显示成功、容器 `/version` 显示新版本号，但**实际打包并上线的是同一个数小时前的
旧二进制**（`run/gateway.build`，mtime 04:59，`go version -m` 显示 vcs.revision
`b68806a7…` —— 该对象**不存在于本仓库**，来自另一 clone 的构建）。版本号与代码的绑定
关系（promotion order 中「same commit, same binary」契约）被静默破坏。

## 2. 根因（两层叠加）

### R1：CGO_ENABLED=0 构建断裂（触发条件）

dual-mode storage 架构合入（`035df5f74`/`db88b7573`，2026-09-04）把
`storage/sqlite`（依赖 `github.com/mattn/go-sqlite3`，**必须 CGO**）带入
`cmd/gateway` 依赖图（路径：`cmd/gateway → storage/factory → storage/sqlite`）。
而 `scripts/deploy-local.sh:build_backend` 固定
`CGO_ENABLED=0 GOOS=linux go build ./cmd/gateway`，自此该构建**必然失败**：

```text
storage/sqlite/schema.go:193:23: conn.Exec undefined
    (type *sqlite3.SQLiteConn has no field or method Exec)
```

### R2：bash set -e 赋值语境怪癖（错误被吞，防线缺失）

`scripts/deploy-local.sh` 顶部虽有 `set -euo pipefail`，但调用形态是
`binary=$(build_backend)`。经实证（bash 5.x）：**命令替换处于赋值语境时，函数内
失败命令不触发 `set -e`，函数继续执行后续语句**。于是：

1. `go build` 失败（错误仅打到 stderr，部署日志中可见但易被淹没）；
2. 函数继续走到 `printf '%s\n' "$out"`，以 rc=0 返回**未更新的陈旧产物路径**；
3. `run/gateway.build` 因构建失败未被重写（mtime 仍是旧值）；
4. `migrate_database`/`stage_release` 照常使用陈旧二进制 → 打上**新版本号的
   version.json** → 蓝绿切换照常晋升。

版本号来自 `version.json`（LLM_GATEWAY_VERSION_FILE）而非二进制内嵌信息，
导致 `/version`、`/healthz` 全绿，掩盖了真实运行代码。

## 3. 影响评估

| 发布 | 打包二进制 | 证据 |
|---|---|---|
| 2.5.0.1935（09-05 06:10，修复前） | 陈旧（04:59 构建验证成立时即受影响） | gateway.build mtime 早于部署时间；与 1939 同源 |
| 2.4.7.1939（09-05 06:15，另一会话） | **确认陈旧**（b68806a7，非本仓库对象） | bundle 与 gateway.build 字节数一致（54263970） |
| 2.4.7.1937（09-04 晚，已晋升覆盖） | 高度疑似，bundle 已被清理无法取证 | 构建断裂自 sqlite merge（09-04）起即存在 |

风险面：本地单机环境（合成数据），无生产影响；但「版本号 ≠ 代码」使一切基于
`/version` 的验证结论失效。对外部提交（b68806a7 来源 clone）执行的代码已完成行为级
复验（特性端点全部正常），provenance 不可追溯性本身按事故记录。

## 4. 修复内容（bc6e696b3）

1. **storage/sqlite build-tag 分裂**：
   - `driver_cgo.go`（`//go:build cgo`）：原 `driverFor` 真实现（ConnectHook 驱动注册）；
   - `driver_nocgo.go`（`//go:build !cgo`）：返回哨兵驱动名 `driverNameUnavailable`；
   - `OpenSQLite` 对哨兵显式报错（lite 模式诚实失败，full 模式静态构建恢复可用）。
2. **build_backend 三重防线**：构建前 `rm -f "$out"`（杜绝旧产物残留）→ 显式检查
   `go build` 退出码（失败立即 `die`）→ 产物非空校验。

验证：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/gateway` 成功；
`go test ./storage/...` 全绿；部署四件套（blue_green 35 / host 12 / wrapper /
credential_decrypt）全过；修复后真实部署 2.5.0.1935 的 bundle
`go version -m` 显示 vcs.revision=`b08ea0607` 与构建时 HEAD 一致。

## 5. 遗留与防复发（下一阶段输入）

- [ ] **同源漏洞排查**：`deploy-245.sh` / `deploy-154.sh` / `deploy-seamless.sh` /
  `deploy-local-lib.sh` 是否存在同类「函数内失败 + 命令替换赋值」吞错模式与
  CGO=0 构建断裂（245/154 构建路径在 252 远端，构建方式未审计）。
- [ ] **回归测试固化**：将 `CGO_ENABLED=0 go build ./cmd/gateway` 纳入 deploy
  测试组合（如 `tests/deploy_nocgo_build_test.sh`），防止依赖图再次引入 cgo-only 包。
- [ ] **部署后身份核验步骤**：晋升后以 `go version -m bin/<release>/gateway` 的
  vcs.revision 与 version.json 的 git_sha 比对，作为部署验证合同的一部分
  （可写入 llm-gateway-deploy skill）。
- [ ] 多 clone 并行开发时的构建来源纪律：仅在与 origin/main 同步的 clone 中构建发布。
