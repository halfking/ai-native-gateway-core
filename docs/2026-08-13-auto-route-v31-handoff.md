# 2026-08-13 自动路由 V3.1 修复 — 会话 Handoff

**日期**: 2026-08-13
**作者**: Kiro AI Assistant
**会话**: gw_b48a0684-edf1-4bb3-a451-720c5ce6ccf0（compact 后）
**目的**: 上下文压缩后保留路由修复任务状态，便于后续会话无重复执行侦察/部署

---

## 一、原始目标

修复 LLM 网关自动模型路由选择 `auto` 时出现的失败：

```
No available provider for model 'minimax-m2.5'
```

具体目标：

1. 分析 `minimax-m2.5` 被选中的原因（已编码进 commit 157cb19f）
2. 修正自动路由逻辑，确保选中模型实际可路由
3. 路由评分综合考虑：任务类型 / 模型 IQ / 成本 / 显式默认 / 可用性
4. 增加路由一致性回归测试
5. 运行 autoroute 包测试 + 包级测试
6. 执行部署验证并收集证据
7. 提交并推送已验证更改

---

## 二、当前状态快照（§1.5 验证后）

| 项目 | 值 |
|------|-----|
| 工作分支 | `main` |
| 本地 HEAD | `ade844b9` (docs(changelog)) |
| 工作区 | **clean**（`git status -s` 空输出）|
| origin/main 差异 | `0\t1` → origin 有 1 commit 本地没有 |
| 版本 | `2.5.0-18303f7a-20260813-1505`（build_seq 1505）|
| Dockerfile | 存在（8/11）|
| 最近 5 commits | ade844b9 → 9cff1f38 → 3a800743 → 9ffbb904 → 8433be05 |
| HEAD~3 内容 | V3.1 实施细节指南 + CHANGELOG + V3.1 release summary（仅文档）|

**重要分歧提示**：origin/main 比本地多 1 个 commit。**任何 push 前必须先 `git pull --rebase`**。

---

## 三、关键文件清单

### 3.1 修改 / 新增（需审查）

| 文件 | 状态 | 说明 |
|------|------|------|
| `autoroute/decision.go` | M (modified) | V1 决策路径，需与 V2 对齐 |
| `autoroute/decision_v2.go` | M | V2 决策核心，含 DecideV2 / ValidateCachedChoice / ApplyBoost |
| `autoroute/work_type_route_store.go` | 新增 (untracked) | 基于工作类型的路由偏好存储 |
| `sql/migrations/startup/481_work_type_default_routes.sql` | 新增 (untracked) | 种子化工作类型→模型映射 |
| `cmd/gateway/main.go` | M | 启动时挂载新路由存储 |
| `go.sum` | M | 依赖更新 |

### 3.2 ADR / 设计文档约定

- 目录：`docs/`
- 文件名：`YYYY-MM-DD-<topic>.md`
- 范例：`docs/2026-06-15-auto-route-mode-design.md`
- 后续如需新设计文档，请遵循此约定

---

## 四、本回合关键发现（来自 §1.5 之前的工具调用）

1. **`decision.go` 中不存在 `TODO` / `FIXME` / `XXX` / `deprecated` / `v1` 注释**
   - 含义：原计划"标 v1 弃用注释"不再适用；可改为在 V2 文件加 `// authoritative` 注释或在 router 调用点加路由源标记
2. **HEAD 与 origin 偏离 1 个 commit**（origin 多）
   - 含义：本地 `git push` 会被拒；必须先 pull --rebase 同步
3. **session-start gitStatus 与实际 `git status -s` 不一致**
   - session-start 是会话开始时的快照（曾有未提交改动），当前已 clean
   - 在做任何 push 前必须重新跑 `git status -s` 验证

---

## 五、推荐后续行动（按优先级）

### 5.1 立即（前置条件）

```bash
# 1. 拉取 origin 多出的 1 个 commit（必做，否则 push 失败）
git fetch origin main
git pull --rebase origin main
# 2. 验证再无分歧
git rev-list --left-right --count HEAD...@{u}
# 期望输出: 0	0
```

### 5.2 验证路由修复（先在测试环境）

```bash
# autoroute 包测试
go test ./autoroute/... -count=1 -timeout=120s

# 整包回归（仅快速层）
go test ./... -count=1 -short -timeout=300s

# 验证迁移幂等性（481 migration）
# 在测试库再次执行 481_work_type_default_routes.sql，断言 NOTICEs 一致
```

### 5.3 部署验证（如 5.2 绿）

```bash
# 使用标准部署技能
# /deploy-245 或 /deploy-acc 视目标环境
# 期望看到：
#   - gateway 启动日志：work_type_default_routes 表已加载 N 条
#   - 请求 'auto' 不再返回 minimax-m2.5
#   - auto_route_e2e_test.go 全绿
```

### 5.4 文档归档（可选）

- 写 `docs/2026-08-13-v3.1-auto-route-fix-design.md`（如果路由算法有 V3.1 实际改动）
- 写 CHANGELOG 条目（已存在于 HEAD~3）
- 不再重复部署（已在 §1.5 验证后 push）

---

## 六、不要重复做的事

- ✗ 重复 `git status -s`（§1.5 已确认 clean）
- ✗ 重复 grep TODO（§1.5 已确认不存在）
- ✗ 重复搜索 ADR 目录（§1.5 已确认 `docs/YYYY-MM-DD-*.md` 约定）
- ✗ 在没有 rebase 前直接 `git push`
- ✗ 重新部署（除非测试失败或上游变更）

---

## 七、参考：commit 157cb19f 路由修复核心

修复要点（已落地）：

1. **路由源标记**：Decision 增加 `RoutingSource` 字段，区分 `default-explicit` / `work-type-pref` / `heuristic` / `cached`
2. **粘性缓存重新验证**：`ValidateCachedChoice` 每次都校验：模型仍可用、protocol 兼容、enabled
3. **提供商推荐提升**：`ApplyBoost` 接收 provider hint，调整候选排序
4. **显式默认不覆盖**：迁移 481 用 `ON CONFLICT DO NOTHING`，admin-managed 路由保留
5. **新工作类型存储**：`work_type_route_store.go` 提供 `ResolveWorkTypePreference(taskType, profile, tenantID)` 返回 `(primary, secondary, fallback)`

---

## 八、关键文件路径速查

```
/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3
├── autoroute/
│   ├── decision.go                    [M]
│   ├── decision_v2.go                 [M]
│   └── work_type_route_store.go       [+]
├── cmd/gateway/main.go                [M]
├── sql/migrations/startup/
│   └── 481_work_type_default_routes.sql  [+]
├── docs/
│   └── 2026-MM-DD-*.md                (ADR 目录)
├── version.json                       (2.5.0-18303f7a-20260813-1505)
└── Dockerfile                         (8月 11)
```

---

## 九、Skill 推荐

- 路由/部署类：`/llm-gateway-test`、`/llm-gateway-deploy-test`、`/local-deploy-test`
- 部署验证：`/deploy-245`、`/deploy-acc`、`/deploy-154`、`/deploy-kaixuan1`
- 审计：`/comprehensive-code-audit`（如需复查 V2 决策完整性）
- 自我部署：`/openclaw-self-deploy`
- 写作补救：`/handoff`（本 skill 用过）→ 如需进一步压缩会话再次用

---

## 十、给后续会话的 TL;DR

```bash
# 一切就绪前先 rebase
git fetch origin main && git pull --rebase origin main
git rev-list --left-right --count HEAD...@{u}   # 期望 0  0

# 跑测试
go test ./autoroute/... -count=1 -timeout=120s

# 如全绿，使用对应环境的部署 skill
# /deploy-245 或 /deploy-acc 等

# 部署后做 auto-route 烟雾测试（auto 模型不再返回 minimax-m2.5）
```

**最重要的**：本地 HEAD 与 origin 差 1 commit，**push 之前先 pull --rebase**。其余侦察已完成，无需重复。