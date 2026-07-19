# 2026-07-20 — 动态 L1 任务类型（WorkType 体系下的彻底动态化）

## 背景

老板在审计阶段指出"我们的任务类型是动态的，在数据库中设置的，
因此，在处理工作类型的地方，需要从数据库中获取数据，不是用写死的模式"。

实际检查发现：前端 4 个视图/组件各自有一份写死的 8 项 L1 任务类型常量：

- `web/src/api-work-types.ts`：`L1_TASK_TYPES` (canonical seed)
- `web/src/api-autoroute.ts`：`TASK_TYPES` (重复定义，labels 还比上面长)
- `TaskTypeRail.vue` v-for 迭代 `L1_TASK_TYPES`
- `WorkTypesView.vue` v-for 迭代 `L1_TASK_TYPES`（2 处：detail 编辑 + create modal）+ `l1Label()` 静态查表
- `RoutingDefaultsView.vue` `availableTaskTypes` fallback 链 `TASK_TYPES || L1_TASK_TYPES`
- `RoutingDashboardView.vue` task pills v-for + `taskLabel()` 静态查表

违反"work types 是 DB 配置"的设计原则，operator 在 work_type_config 里加新 L1
后，前端看不见。

## 变更

### 后端 `admin/work_types.go`

- 新增 `L1TaskTypeMeta` struct 与 `canonicalL1TaskTypes`（canonical 8）常量
- 新增 handler `listL1TaskTypes`，挂到 `GET /api/admin/work-types/l1-task-types`
  （在 `handleSub` 里加了 reserved path 分支，避免与 `getWorkType(key="l1-task-types")` 冲突）
- 返回值 = canonical 8 ∪ `SELECT DISTINCT l1_task_type FROM work_type_config`，
  每项带 `count`（work_type_config 中使用该 L1 的行数）
- canonical 8 永远存在（即使 DB 空，保证"创建第一个 work type"流程有选项可选）
- 操作员新增的 L1 类型（DB 里出现但不在 canonical 列表）会用 key 作 label + ◆ 图标展示

### 前端

- `web/src/api-work-types.ts`：
  - 保留 `L1_TASK_TYPES` 常量（canonical seed，empty-DB / 后端宕机时第一帧不空白）
  - 类型 `Omit<L1TaskTypeMeta, 'count'>` 与动态响应 `L1TaskTypeMeta` 兼容
  - 新增 `listL1TaskTypes()` API 函数 + `L1TaskTypesResponse` 接口
- `web/src/composables/useL1TaskTypes.ts`（**新文件**）：
  - 模块级 `l1TaskTypes` ref（共享给所有 caller，避免多次 round-trip）
  - `loading` / `error` 响应式状态
  - `refreshL1TaskTypes()`：inflight 去重，fetch 失败时保留 seed
  - `l1Label(key)` / `l1Icon(key)` 辅助函数
- 4 个使用方替换：
  - `TaskTypeRail.vue`：`L1_TASK_TYPES` → `useL1TaskTypes().l1TaskTypes`
  - `WorkTypesView.vue`：2 处 `<option v-for>` + `l1Label()` → composable
  - `RoutingDefaultsView.vue` `availableTaskTypes` fallback 链：`TASK_TYPES || L1_TASK_TYPES` → `l1TaskTypes`
  - `RoutingDashboardView.vue` task pills + `taskLabel()` → composable
- 5 个文件 `onMounted` 加 `void refreshL1TaskTypes()`（composable 会去重 inflight，
  实际只有第一次 mount 触发网络请求）

## 设计取舍

| 决策 | 理由 |
|------|------|
| 保留 `L1_TASK_TYPES` 作为 frontend seed | empty-DB / 后端宕机时第一帧不能空白；operator 现场可读 |
| 模块级共享 ref 而非每个 component 单独 fetch | Vue mount 生命周期里 4 个组件同 tick 触发，只想打 1 次 API |
| inflight 去重（同 Promise await） | 同上 |
| fetch 失败时保留 seed 而非置空 | L1 dropdown 空了会让用户无法新建 work type，比"短暂显示 stale label"更糟 |
| 后端 `canonicalL1TaskTypes` + 前端 `L1_TASK_TYPES` 双份 | 单源真相太激进——后端是 DB-backed，前端是 UI seed，职责不同。两份需要保持字段一致（key / label / icon），注释里已写明同步规则 |
| `L1TaskTypeMeta` 用 `Omit<..., 'count'>` 区分静态 / 动态 | 静态 seed 不可能知道 live count；动态响应带 count（运营面板可显示"已被 N 个 work type 使用"） |

## 验证

- `pnpm build` 通过（9.23s）
- `go build ./admin/...` 通过
- `go vet ./admin/...` 通过
- `bash scripts/pre-commit-check.sh` PASS=4 FAIL=0
- `pnpm run i18n:check` 仅剩 6 个 pre-existing missing key（与本改动无关）

## 风险

| 风险 | 说明 |
|------|------|
| **并发 agent 冲突** | 本会话期间有另一位 agent 在同 workspace 操作，`api-work-types.ts` 被合并两次（出现重复 `listL1TaskTypes` / `L1TaskTypeMeta` 声明）。最终手动合并到单份声明。后续如再有并发改动，需 `git diff HEAD~1 --stat` 自检重复声明。 |
| **operator 加新 L1 后 classifier 不识别** | 前端展示没问题，但 LLM classifier 实际不产出这个 L1 类型 → 用户在 work_type 创建时选了它，但永远没有流量匹配。建议后续把 classifier taxonomy 与 `canonicalL1TaskTypes` SSOT 化（classifier 配置也读 DB）。 |
| **canonical 8 labels 与 i18n** | `L1_TASK_TYPES` 的 label 是中文硬编码，en-US / ja-JP 等 locale 下 `l1Label()` 返回中文——这部分原本就是硬编码常量，现在依然是；想要 i18n 化需要后端返回 i18n key 或前端 locale 文件夹。本 PR 不动。 |
| **`task_type` 字段的 L1 vs work type 语义混淆** | `RoutingDefaultsView.vue:88-95` 的 `availableTaskTypes` 用 `workTypes.value.map(...)` 把 work type key 当作 routing_default.task_type 推给后端——这是 **pre-existing bug**，本 PR 仅把 fallback 链改成 `l1TaskTypes`，没修这个 bug。老板若要修，需单独 PR + 与后端 `create_routing_default` schema 对齐。 |

## 迁移注意

- operator 把 `l1_task_type` 设到 canonical 8 之外的 key（如 `multimodal`）时：
  - 前端展示：用 key 作 label + ◆ 图标（不优雅但不阻断）
  - 后端 `canonicalL1TaskTypes` 不需要改（union 自动包含）
  - 若希望前端也有自定义 label + icon，需另开 PR 在前端加 L1 metadata 路由（比如
    `GET /api/admin/work-types/l1-task-types/:key/icon`）或扩展响应字段
- 删 canonical 8 中的某项：前端 seed 同步删 + 后端 `canonicalL1TaskTypes` 同步删 + classifier 同步学。**两端必须同时改**，单边会回归到 stale label。