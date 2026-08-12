# `_to-be-deprecated/` 删除核查报告

**日期**: 2026-08-12
**性质**: 删除前核查 + 可执行清单（**未执行删除**，待明确授权）
**关联**: `_to-be-deprecated/README.md`（R1.13 待删除代码）、`docs/architecture/legacy-replacement-audit-20260626.md`

---

## 一、结论（TL;DR）

| 目录 | 结论 | 依据 |
|------|------|------|
| `_to-be-deprecated/`（14 老包，~220 文件） | ✅ **可安全删除** | 全部有活跃替代品、0 活跃 import、已被构建排除、阻塞原因已消除 |
| `_to_be_deleted/`（maintain 迁移回滚快照） | ⛔ **不要删** | manifest 明确 "do not delete until B1 staging gate + observation window"，是回滚安全网 |

**本报告未执行任何删除**——`_to-be-deprecated/README.md` 写明删除"等待用户授权 R1.13 后执行"。授权后按§四的命令一次完成。

---

## 二、核查方法

1. **构建排除确认**：两个目录均以 `_` 开头，Go tool 不编译（`go build ./...` 不含它们）。
2. **活跃 import 扫描**：`grep -rn "to-be-deprecated|to_be_deleted" --include="*.go"` 排除自引用后 → **0**。
3. **活跃替代品确认**：逐包定位 live 目录（见§三表）。
4. **非 Go 引用扫描**：Dockerfile / 脚本 / yml / sql → 5 处，全部良性（见§五）。

---

## 三、14 个老包 → 活跃替代品映射

| 老包（`_to-be-deprecated/`） | 活跃替代品 | 替代品已确认 |
|------------------------------|-----------|--------------|
| `audit/` | `domains/hooks/audit/` | ✅ |
| `auth/` | `domains/authentication/`（`internal/auth/` 亦活跃） | ✅ |
| `circuit/` | `domains/credential/breaker.go` | ✅ |
| `identity/` | `domains/identity/` | ✅ |
| `identitypool/` | `admin/identity_pool.go`（被 `executor.go` + `admin/handler.go` 使用） | ✅ |
| `limiter/` | `domains/credential/limiter.go` | ✅ |
| `relay/` | `domains/streaming/` + `domains/transformation/anthropic/` | ✅ |
| `sessions/` | `domains/session/` | ✅ |
| `transform/` | `domains/transformation/transform*.go` | ✅ |
| `ursm/` | `domains/ursm/v2/` | ✅ |
| `observability/siem/` | 0 外部 import、从未集成（manifest §1.1） | ✅ |
| `orphan-tests/model-routing-test.go` | 孤立 test runner，顶层 package main | ✅ |
| `compliance-候选废弃-20260701/` | 候选废弃，无活跃引用 | ✅ |
| `flowcontrol-候选废弃-20260701/`、`llmclient-候选废弃-20260701/`、`notification-候选废弃-20260701/`、`taskmanagement-候选废弃-20260701/`、`hooks-handoff-20260706/` | 候选废弃/已迁入 migration，无活跃引用 | ✅ |

**原阻塞原因**（README §1.2）："旧 `relay/routing/transport` 内部仍引用"——而 `routing/`、`transport/`、`credentialstate/`、`memora/`、`telemetry/` 已于 **R1.13 (2026-06-29) 删除**。故阻塞已解除。

---

## 四、授权后的执行清单（一条命令）

```bash
# 1. 删除前安全网：确认全量构建不依赖这些目录
go build ./...

# 2. 删除整个待废弃目录
git rm -r _to-be-deprecated/

# 3. 清理变 stale 的非 Go 引用（见 §五，均为 exclude/注释）
#    - .golangci.yml: 删除第 15 行的 '- _to-be-deprecated' exclude
#                     第 62 行 relay/ 废弃规则可保留（仍描述历史）
#    - scripts/verify_partition_architecture.sh:308,322 的 --exclude-dir 变 no-op，可删可留
#    - scripts/scan-secrets.sh:88 的 "_to-be-deprecated") 分支变 dead，可删可留
#    - db/migrations/356,352 的 SQL 注释为历史记录，无需改

# 4. 再次构建 + 测试
go build ./... && go test ./...

# 5. 提交
git commit -m "chore: 删除 _to-be-deprecated/ 老包（R1.13 后清理）

依据 docs/2026-08-12-legacy-deprecated-deletion-audit.md 核查:
14 个老包全部有活跃替代品, 0 import, 构建已排除, 阻塞原因(旧
relay/routing/transport)已于 R1.13 删除。"
```

**范围建议**：默认全删（`_to-be-deprecated/` 本就是一个逻辑单元，README 把它定义为"最终将被删除的代码"）。若只想先删审计相关子集：`ursm/ identity/ identitypool/ limiter/ circuit/ relay/ orphan-tests/`。

---

## 五、非 Go 引用明细（全部良性）

| 文件 | 行 | 内容 | 删除后影响 |
|------|----|----|-----------|
| `.golangci.yml` | 15 | `- _to-be-deprecated`（lint exclude） | 变 no-op，建议清理 |
| `.golangci.yml` | 62 | relay/ 废弃规则描述 | 仍描述历史，可留 |
| `scripts/verify_partition_architecture.sh` | 308,322 | `--exclude-dir="_to-be-deprecated"` | 变 no-op |
| `scripts/scan-secrets.sh` | 88 | `"_to-be-deprecated")` 分支 | 变 dead 分支 |
| `db/migrations/356_handoff_enhanced.sql` | 4 | 注释 "previously parked at _to-be-deprecated/..." | 历史记录，无需改 |
| `db/migrations/352_goal_auto_control.sql` | 25 | 注释同上 | 历史记录，无需改 |

**无任何功能性依赖**。

---

## 六、`_to_be_deleted/` 保留说明

- `_to_be_deleted/distribution-v1/`、`anthropic_bridge.go`、`legacy_transport.go` 是 **maintain 迁移的回滚快照**。
- 其 manifest 明确：**do not delete until B1 has passed its staging gate and one observation window has elapsed with no rollback**。
- 回滚步骤已写在该 manifest 中（`git mv _to_be_deleted/distribution-v1 distribution` + 恢复 main.go Phase 8）。
- **本报告不动该目录。**

---

## 七、关于 `_to-be-deprecated/ursm/state.go` 与 node_state.go 注释

`credentialfpslot/node_state.go:1-8` 注释称 "domains/ursm/state.go 从未存在"，指的是**活跃路径** `domains/ursm/state.go` 确实不存在。`_to-be-deprecated/ursm/state.go` 是**老 ursm**（已被 `domains/ursm/v2/` 取代，本身也在本删除清单内）。两者不矛盾，注释准确。

---

**报告状态**: 核查完成，删除待授权。授权后按§四执行。
