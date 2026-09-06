# 2026-08-19 — P0-1 host_restart_service 安全 drain 设计

> 范围：`scripts/deploy-lib/host.sh` 的 race condition 修复
> 方式：最小侵入，仅改 `host_restart_service` 函数（≤ 30 行）+ 加 1 个新函数
> 授权：follow-up §3.5 候选 C 已验证 100%，本次为代码层根治

---

## 0. 上下文（接手上文）

`docs/session-logs/2026/08/2026-08-19-245-restart-loop-followup.md` §3.5 已诊断：
- 2026-08-18 23:29:48 `bind: address already in use` 9 次 → systemd StartLimitBurst=5 静默
- 01:00:00 二次 deploy 时撞同样 race，停 02:18（共 95 分钟）
- follow-up §3.5 P0-1 行动项要求：deploy-seamless.sh 加 "wait for old process port release"

本文档完成 code-context → LP1 spec → drift check，确认后再动 `host.sh`。

---

## 1. Code-Context（rule 42 §2.1 强制）

### 1.1 Entry Point

| 层级 | file:line | 函数 / 上下文 |
|---|---|---|
| 入口 | `scripts/deploy-seamless.sh:474` | `host_atomic_switch "$SSH_CMD" "$TARGET" "$version"`（在 step 8/9 atomic switch 阶段）|
| 切入 race | `scripts/deploy-lib/host.sh:278-315` | `host_atomic_switch()` 函数本体 |
| race 核心 | `scripts/deploy-lib/host.sh:314` | 调 `host_restart_service "$ssh_cmd" "$target"`（**BEFORE fix 异步 fire-and-forget**）|
| 待改函数 | `scripts/deploy-lib/host.sh:206-212` | `host_restart_service()`（11 行，仅 systemctl restart）|
| 已存在但未用 | `scripts/deploy-lib/host.sh:328-351` | `host_drain_and_stop()`（模板可用，本 LP 复用）|

### 1.2 当前调用链（待修复）

```
deploy-seamless.sh:474   host_atomic_switch
  └─ host.sh:295-308      ln -sfn batched  ✅ 原子链接切换
  └─ host.sh:314          host_restart_service  ❌ 本 LP 修复点
       └─ host.sh:206-212    "$ssh_cmd" "systemctl restart '$service_name'"
                          └─ systemd: stop (drain wait) → start
                          └─ 命令发出，命令返回 ≠ 旧 binary 完全释放 :8781
deploy-seamless.sh:484   host_wait_healthy (90s loop)
```

**race 关键时序**：
1. `systemctl restart` 发 SIGTERM 给旧 PID
2. 旧 Go `srv.Shutdown` 最多阻塞 30s 才让出端口
3. 部署脚本继续往下走 `host_wait_healthy`
4. systemd `TimeoutStopSec=25s` 期内，或新 binary 先被 start 命令拉起
5. 新 binary bind :8781 **失败**（旧 PID 还在）
6. systemd RestartSec=5 → loop → StartLimitBurst=5 → 静默离线

### 1.3 已存在可复用的 `host_drain_and_stop`（host.sh:328-351）

```bash
host_drain_and_stop() {
  local ssh_cmd=$1 target=$2 drain_s=${3:-30}
  local service_name health_url deadline
  service_name=$(target_field "$target" service_name)
  health_url=$(target_field "$target" health_url)

  "$ssh_cmd" "systemctl stop '$service_name'" || true
  deadline=$(( $(date +%s) + drain_s ))
  while (( $(date +%s) < deadline )); do
    if ! "$ssh_cmd" "curl -fsS --max-time 1 '$health_url' >/dev/null 2>&1"; then
      return 0   # port released
    fi
    sleep 1
  done
  return 1  # timeout
}
```

**完全满足 LP1 验收但存在细节差异**：
- 本 LP 不需要单位置式 stop（deploy 流程保留 unit 期望 restart）
- 本 LP 需要 **drain + release port + 同步启新 binary**，因此走 stop+drain+start 三步而非仅 stop

### 1.4 现有约束（必须保持兼容）

| 维度 | 现状 | 本 LP 要求 |
|---|---|---|
| contract 不变 | `host_restart_service ssh_cmd target` 2 参数 | **保持 2 参数签名不变**（deploy-seamless.sh:474 不改）|
| 总耗时上限 | restart ~5s + 健康轮询 ~30s | drain 上限 30s + restart 5s；总 ≤ 35s 健康 |
| 失败行为 | systemd 自动循环 | 同步等待 + 仍然失败时才退出（本 LP 不消除 systemd 兜底）|
| 并发 deploy | 通过 `lock_acquire_remote` 串行 | 保留（脚本顶部已锁）|

---

## 2. Logic Points（rule 42 §4）

### 2.1 总览

```
逻辑点清单（本 PR）：
  LP1: host_restart_service 重构为 stop → wait port release → start（≤ 30 行）

依赖：仅 LP1 不需要拆（≤ 300 行）
分支：1 个文件 1 个函数改动，符合 minimal patch 原则（rule 37 + rule 43 §2.3）
```

### 2.2 依赖图（简）

```
deploy-seamless.sh:474 ──calls──▶ host_atomic_switch (host.sh:278)
                                       │
                                       └─calls──▶ host_restart_service (host.sh:206) ← 【本 LP 改】
                                                       │
                                              uses──▶ 健康端点 (targets.sh)
```

---

## 3. LP1 Spec — `host_restart_service` 安全 drain

### 输入（Input）

- `$1 ssh_cmd` —— 已注入的 ssh wrapper function 名字（来自 deploy-seamless.sh remote_ssh）
- `$2 target` —— 目标标识，取值 `{245, 154}`
- 隐式：从 `targets.sh` 读 `service_name` 和 `health_url`（与现 `host_restart_service` 同）

### 产出（Output）

**主路径**：与原函数同 — 即"服务重启成功，最终 healthz 2xx 可达"

但**新增可观察副作用**：
- 部署日志多行：`[host_restart_service] stop -> drain wait -> start`
- 失败时：日志显式说明 "port still occupied after N seconds" 让排查有依据

**行为契约改动**：
| 维度 | 旧行为 | 新行为 |
|---|---|---|
| 命令 | `systemctl restart $service` | `systemctl stop` → `wait port released` → `systemctl start` |
| 阻塞等待 | 命令发完即返 | 同步等 `:8781` 端口可 bind（用 curl healthz 不可达判断）|
| 总耗时 | systemd 内部调度（~5-25s） | drain 上限 30s + start 5s |
| 失败模式 | systemd 循环 / StartLimit 触发 | 同步报错给调用方（与现有 `host_drain_and_stop` 行为一致）|

### 验收标准（Acceptance Criteria）

| # | 检查项 | 期望 |
|---|---|---|
| 1 | `host_restart_service` 签名不变（参数 / 返回值） | `host_restart_service ssh_cmd target` |
| 2 | 函数内部第 1 步是 `systemctl stop` + 等端口释放 | grep 显示 "systemctl stop" 出现在函数 body |
| 3 | 端口释放等待最大 30s（与 `host_drain_and_stop` 默认值一致） | 数字字面量 30 |
| 4 | 端口释放后才 `systemctl start` 新 binary | grep 显示 "systemctl start" 在 drain loop 之后 |
| 5 | 失败 30s 超时返回非 0，部署脚本可 catch | 函数 return 1 时 systemd 已被 stop；上层 `_seamless_auto_rollback` 能兜底 |
| 6 | **不修改 deploy-seamless.sh 任何行** | git diff 仅限 host.sh |
| 7 | 调用方 `host_atomic_switch` 函数（host.sh:278）行为不变 | 仅换了实现细节，调用语义一致 |
| 8 | 现有 `host_restart_service` test（如有）继续 pass | grep `host_restart_service tests/` |
| 9 | `bash -n host.sh` syntax OK | exit 0 |
| 10 | **否定式**：不增加新 deploy 端点 / 不修改 systemd 配置 / 不需要新增 SSH 调用数（仅多 1-2 次用于检测 healthz 不可达） | diff 检查 |

### 边界

| 场景 | 期望 |
|---|---|
| `health_url` 不在 targets.sh | `host_drain_and_stop` 已有的 `target_field` 返回空时 bail out，本 LP 保持一致 |
| `systemctl stop` 失败（unit 不存在等情况）| drain 部分已用 `\|\| true` 容错，本 LP 同样容错（仍可能误判；但比旧行为好）|
| `:8781` 已被另一个进程占用 | drain 30s 超时 → 函数返回 1，部署回滚由上层 `_seamless_auto_rollback` 兜底（行为已存在）|
| systemd 出现更新（Service=notify）| `systemctl start` 后 systemd 自动通知，本 LP 不需要改 |

---

## 4. Drift-Check（rule 42 §6）

### 4.1 行数预估

| 改动 | 文件 | +/- 行数 |
|---|---|---|
| `host_restart_service` 函数重写 | `scripts/deploy-lib/host.sh` | +15 行 (新逻辑) / -3 行 (旧实现) = +12 行净增 |
| 注释头说明 | 同上 | +5 行 |
| **合计** | **1 文件** | **~17 行净增** |

✅ ≤ 300 行（rule 42 §2.2 强制）
✅ 最小补丁路径（rule 43 §2.3 + rule 37 原则 3 精准修改）
✅ 不引入新依赖 / 新端点 / 新文件（rule 42 §5.5 反模式检查）

### 4.2 引用一致性

- ✅ `host.sh:328-351` `host_drain_and_stop` 函数调用 `targets.sh` 的 `service_name` + `health_url`，本 LP 复用同样 contract
- ✅ `host.sh:206-212` 当前调用方 `host_atomic_switch:314` 仍期望 2 参数返回，本 LP 不改

### 4.3 测试覆盖

| 测试位置 | 是否需要新增 |
|---|---|
| 现有 `tests/` 目录 | grep `tests/.*host` 确认（待查）|
| 247-258 测试支撑 | 仅手动验证 `bash -n host.sh` syntax 通过；不在本 LP 加新测试（deploy 库没看到 unit test infra）|
| staging 验证 | 等 implement 完成人工跑一次 245 `status` 子命令（dry-run，等同 syntax check）|

### 4.4 回滚方案

```
1. git revert 这次 commit（恢复到旧 host_restart_service 行为）
2. 重新 deploy 时会回到原 race 行为，但已加固的 systemd override (StartLimitBurst=10) 仍兜底
```

### 4.5 关键文件路径

| 路径 | 用途 |
|---|---|
| `scripts/deploy-lib/host.sh` | **唯一改动文件**（LP1 落地） |
| `scripts/deploy-lib/targets.sh` | 读取 service_name + health_url（不改） |
| `scripts/deploy-seamless.sh` | 调用方（不改任何行） |
| `docs/session-logs/2026/08/2026-08-19-245-restart-loop-followup.md` | 上游根因分析（LP1 是 §4 行动项 1） |

---

## 5. 关联规则

| Rule | 用途 |
|---|---|
| rule 09 §2.1 | Factuality（race path 实测，非猜） |
| rule 11 §1 | plan-first（先把 design 落地到文档） |
| rule 11 §5 | 诚实汇报（不掩盖 race 真实成因） |
| rule 37 原则 3 | 精准修改（只改 host_restart_service 函数体） |
| rule 42 §2 | code-context 完整 + 300 行限制 |
| rule 43 §2.3 | 最小补丁（≤ 30 行单函数改动） |
| rule 44 | 本 LP 不涉及服务器编译/部署流水线（仅 update deploy 库，本地就是 server 245） |

---

最后更新：2026-08-19 13:30 +0800
作者：本会话
状态：**待老板审核（human_only - rule 10 §2.3 / rule 04 §6 多环境风险）**
