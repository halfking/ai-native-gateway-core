# 2026-07-13 — 部署管理硬化 · 切片 1-3（目标契约 / 锁 / 主机 seams）

## 概要

按 spec `2026-07-13-deployment-management-hardening-design.md` 落地前三个交付切片。本切片不触动实际 deploy / rollback 行为，只建立可验证的结构骨架与可重用的库。

## 切片 1：CLI parser + target contracts + 154 systemd unit

| 文件 | 行数 | 角色 |
|---|---|---|
| `scripts/deploy-lib/targets.sh` | ~330 | 目标契约；纯函数 + 简洁 JSON 序列化，无 jq 依赖 |
| `scripts/deploy.sh` | +90（插入头部） | Canonical CLI 前端（plan/deploy/verify/rollback/force-unlock），不动原有 1100 行 orchestrator |
| `deploy/llm-gateway-go.service` | ~36 | 154 systemd unit（mirror llmgo-245.service） |

**关键设计：**
- 每个目标导出 `target_<name>_contract()`，返回严格顺序的 JSON 对象（11 个字段，顺序固化在 `tests/fixtures/plan_schema.json`）。
- `target_check_actionable <target> <action>` 在任何锁/构建/SSH 之前拒绝：
  - `retired`（186）→ 64 + "use 154 / 245 instead"
  - `deferred`（252/184）→ 64 + "repository evidence conflicts"
  - `sops-only`（kaixuan-1）→ 64 + "runtime contract unknown"
  - `unsupported`（kaixuan-2/3）→ 64
- 旧别名（`71 → 154`、`184 → 252`）通过 `target_resolve_alias` 透明重写。
- `scripts/deploy.sh` 头部插入 ~90 行识别 canonical `<action> <target>` 形式；legacy shorthand `./scripts/deploy.sh <target>` 落穿到原有 ~1100 行逻辑保留兼容。

## 切片 2：离线测试 harness + plan_schema

| 文件 | 行数 | 角色 |
|---|---|---|
| `tests/fixtures/plan_schema.json` | ~45 | JSON Schema 锁定 plan 输出字段顺序、类型、必填性 |
| `tests/deploy_cli_test.sh` | ~370 | 24 个断言的离线集成测试 |

**关键设计：**
- 测试用 `setup_fake_bin` 在隔离 TMPDIR 下生成 PATH 占位（`env-injector` / `id -un` / `hostname` / `ssh` / `scp` / `systemctl` / `curl` / `git`），无需网络、SSH、Docker。
- AC-1：plan_required_fields 验证 11 个字段均存在
- AC-2：alias_resolution（71→154、184→252）
- AC-4：target_support（186/252/184/kaixuan-1 拒绝，245 接受）
- AC-3 stub：local_lock_contention（mkdir fallback；flock 二进制探测不影响）
- AC-8/AC-10 占位：sops regex / scan-secrets 待切片 6/7 实现

**修复的 bug：** `_json_object` 原先用正则判断"看起来像数字就不加引号"，导致 `legacy_aliases="71"` 被序列化为 `legacy_aliases:71`（裸数字）。改为通过 `:true` / `:false` / `:null` 显式 sigil 开启非字符串字面量。

## 切片 3：双层锁原语（仅原语，不接 orchestrator）

| 文件 | 行数 | 角色 |
|---|---|---|
| `scripts/deploy-lib/lock.sh` | ~155 | 本地 + 远程锁 |

**关键设计：**
- 本地：`flock` 二进制探测；存在则走非阻塞 fd 锁，否则用 `mkdir` 原子 fallback。
- 远程：`mkdir <lock_path>` 原子创建 + 写入元数据；元数据无 secret。
- `EX_TEMPFAIL (75)` 是 acquire 冲突的统一返回码——caller 必须显式 gate。
- `force-unlock_remote` 是 *唯一* 允许的远程锁强制删除入口；仅 op 输入触发。
- 库是无 `set -e` 契约：调用方负责 gate；本切片不假设 orchestrator 行为。

## 切片 4-5 留下未完成的部分

实际 deploy / verify / rollback 行为需要 SSH 编排、版本化 release bundle 创建、`current/` symlink swap、`/healthz` 验证后 `verified=true` 翻转——这些留给切片 4（245 deploy/rollback）和切片 5（154 + wrapper）。

`scripts/deploy.sh` 的 `verify` / `rollback` canonical action 在本切片只输出契约字段并给出"将随切片 4/5 实现"的提示，不做任何副作用。AC-5、AC-6、AC-7 在切片 4 后才能跑通。

## ShellCheck / Bash 语法

```text
bash -n scripts/deploy.sh scripts/deploy-lib/*.sh tests/deploy_cli_test.sh    OK
shellcheck -S error scripts/deploy-lib/*.sh tests/deploy_cli_test.sh           0 errors
bash tests/deploy_cli_test.sh                                                   24 passed, 0 failed, 2 skipped
```

## 文件清单

### 新增
- `scripts/deploy-lib/targets.sh`
- `scripts/deploy-lib/lock.sh`
- `scripts/deploy-lib/host.sh`（本切片仅 seam 占位）
- `deploy/llm-gateway-go.service`
- `tests/deploy_cli_test.sh`
- `tests/fixtures/plan_schema.json`

### 修改
- `scripts/deploy.sh` — 头部 +90 行 canonical CLI 前端（不动原有逻辑）
- `CHANGELOG.md`

## 验证

- ✅ `bash -n` 全部通过
- ✅ `shellcheck -S error` 0 errors（剩余 28 条均为 info/warning）
- ✅ `bash tests/deploy_cli_test.sh` 24/24 通过

## 已知限制

- Spec AC-5/6/7（`rollback 245` 与版本化选择、failed health 自动回滚）需切片 4 实现，本切片仅占位。
- AC-8/9（SOPS regex、env-injector artifacts）需切片 6 实现。
- AC-10（HEAD 凭据清理）需切片 7 实现。
- 兼容性 wrappers（`deploy/deploy.sh`、`deploy/rollback.sh`、`deploy-*.sh`）需切片 8。
