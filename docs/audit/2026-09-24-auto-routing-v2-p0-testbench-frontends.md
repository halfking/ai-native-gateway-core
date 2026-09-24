# 2026-09-24 AUTO 路由 v2 闭环 P0（测试地基 + 前端最后一公里）落地与验证轮

- 依据：`docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` §六 P0 行（五项：① auto-testbench 统一 harness + GRRQ 定稿 ② 套件 60→≥200 例 ③ TaskProfile/AutoTuning 双页 + 菜单 + i18n ④ vue-tsc 进 build 门禁 ⑤ 标注采样策略 v2）。
- 性质：纯加法轮，零热路径改动（`domains/`、`autoroute/` 决策代码零修改；`autoroute/` 仅测试文件追加 v3 套件注册）。
- 结论：P0 五项全部落地，回归门禁 GATE: PASS（240 例，GRRQ=100.00），前端 typecheck/build/vitest 全绿（除 2 处 HEAD 既有失败，见 §四）。

## 一、交付物清单

| # | 交付物 | 位置 |
|---|--------|------|
| ① | auto-testbench 统一 harness（回归 + 门禁 + 报告 + 候选生成四模式） | `cmd/auto-testbench/{main,suite,metrics,generate}.go` + `main_test.go`（230 行单测） |
| ① | 一键脚本（离线回归+门禁；`AUTO_E2E=1` 合并 E2E 层；`--refresh-baseline`） | `scripts/auto-testbench.sh` |
| ① | GRRQ 定稿：`accuracy×100 − overprovision×30`（简单类=chat；function_call 因 tools 硬升级不算简单类） | `cmd/auto-testbench/metrics.go` |
| ① | 随仓回归基线（accuracy/macro_f1/grrq 三阈值门禁，低于即 exit 1） | `cmd/auto-testbench/testdata/baseline.json` |
| ② | 套件 v3：140 例（en_business/mixed_signal/trap_v2 桶），总量 60+40+140=**240 ≥ 200** | `autoroute/testdata/auto_matching_suite_v3.jsonl`（属主仍为 autoroute/testdata） |
| ② | "修正即测试"候选通路：corrections（human_task_type 金标签）/ selections（分层弱标签）→ generated 候选 JSONL，prompt 占位符（隐私红线），人工脱敏复核后转正 | `cmd/auto-testbench/generate.go` |
| ② | 套件测试注册 v3（minSize=135 钉桩） | `autoroute/auto_matching_suite_test.go` |
| ③ | 任务档案页（档案表+修正统计+分层建议+apply+overlay reload） | `web/src/views/TaskProfileView.vue` + `web/src/router.ts` `/routing-v2/task-profile` |
| ③ | 路由调参页（提案列表/批准/驳回 + 分类质量窗口，superAdmin 门控） | `web/src/views/AutoTuningView.vue` + `/routing-v2/auto-tuning`（meta.requiresSuper） |
| ③ | 菜单注册（模型与路由组）+ menu-config 导出 | `web/src/config/appNav.ts`、`web/public/menu-config.json` |
| ③ | i18n **8 语言**（超出验收口径"四语言"）：taskProfile/autoTuning 模块 ×8 + nav 键 ×8 + index 注册 ×8 | `web/src/locales/{zh-CN,zh-TW,en-US,ja-JP,de-DE,fr-FR,es-ES,ar-SA}/` |
| ④ | vue-tsc 进 build 门禁：`build = export-menu-config && vue-tsc --noEmit && vite build` | `web/package.json` |
| ⑤ | 采样策略 v2：`strategy=recent(默认,行为不变)|disagreement(LLM 兜底行优先)|stratified(task_type×置信度四档桶配额, per_strata≤20)` | `admin/annotation_handler.go`（响应新增 `strategy` 回显字段） |
| ⑤ | 采样纯函数单测（参数解析/占位符连续性/三策略 SQL 形状钉桩） | `admin/annotation_handler_sampling_test.go` |

## 二、验证证据（本机 Windows，go1.27.1）

### 2.1 Go 侧

```
go test ./cmd/auto-testbench/                      → ok 0.741s（含 TestDefaultSuiteFilesLoadable ≥200 例门禁）
go test -run 'TestAutoMatchingSuite…' ./autoroute/ → ok 8.272s（240 例离线启发式回归）
go test -overlay=… -run 'TestParseSamplingParams|TestBuildSamples…' ./admin/ → ok（纯函数单测）
go test ./taskprofile/ ./routingopt/               → ok（插件边界 wiring_guard 无回归）
go vet ./cmd/auto-testbench/ ./autoroute/ ./admin/（-overlay）→ clean
gofmt -l（改动文件）                                → clean
```

离线回归 + 门禁实测（`go run ./cmd/auto-testbench -mode regression -gate-default`）：

```
cases: 240   accuracy: 1.0000   macro_f1: 1.0000
tier distribution: tier-a=85 tier-b=105 tier-c=50
over-provision: 0/30 simple cases → tier-a (rate=0.0000)
GRRQ: 100.00
gate vs cmd/auto-testbench/testdata/baseline.json → GATE: PASS
```

### 2.2 前端侧

```
npm run typecheck       → 0 错误
npx vitest run          → 970/971 通过（唯一失败为 HEAD 既有，见 §四.2）
npm run build           → 27.11s 成功（含新增 vue-tsc 门禁与两个新视图 chunk）
node scripts/i18n-audit.mjs → exit 0；missing keys=3（全部 HEAD 既有占位噪声，无新增）
```

## 三、设计要点备案

1. **known_failure 与 generated 候选不豁免口径**：known_failure 行计入指标（本次 240 例中为 0）；generated=true 候选行跳过分类评测、仅进报告 inventory（`metrics.go add()`）。
2. **分歧采样的可判定口径**：启发式自身结论未落库，`auto_route_selections.classifier <> 'heuristic'`（llm/v3 兜底接管）即"两分类器分歧或低置信"的等价证据（`annotation_handler.go` 注释）。
3. **stratified 分页语义**：一次确定性配额抽样，无 OFFSET；total=选中行数；桶界 0.85/0.70/0.50 对齐 LLM 兜底阈值与强置信带。
4. **recent 策略零依赖新列**：不触碰 `classifier` 列，既有响应 shape 不变（测试钉桩 `recent must not depend on classifier column`）。
5. **隐私红线**：generate 模式仅 SELECT 658 结构化特征列；产出 prompt 为占位符；候选文件不注册进 `autoMatchingSuiteFiles`，转正需人工脱敏。

## 四、遗留与既有问题（非本轮引入）

1. **admin 全包 3 个 Windows 固有失败**（Linux CI 不受影响）：`TestQueryFilesystem`/`TestQueryDirectory` 断言 POSIX 绝对路径（`got "C:\workspace\..."`）；`TestLiveStreamSSEHub_ComputeScopeDelta…` 依赖单调时钟在相邻两次读取间推进（Windows 读数相同）。三个测试均属 data_lifecycle/SSE 子系统，与本轮改动零交集。
2. **`keys_referenced.test.ts` 1 例既有失败**：引用扫描把 locale 文件**注释**里的 `t('common.xxx')`/`t('dashboard.stat.X')` 计为引用（`zh-CN/common.ts:123`、`en-US/common.ts:123`、`en-US/dashboard.ts:5`，三文件本轮零改动）——扫描器应排除注释/或排除 locale 文件自身，另行处理。
3. **`i18n-cjk-count.mjs` Windows 不兼容**：自定义 `dirname` 按 `/` 切分，Windows 反斜杠路径导致 ROOT 解析到仓库根（CLI 场景）；CI 口径（vitest `hardcoded_cjk.baseline.test.ts`）不受影响，vitest 实测通过。
4. **验收项"标注采样 SQL 有执行计划"顺延**：需真实 PostgreSQL（EXPLAIN），按规划惯例随部署验证走 252 只读库执行；stratified 策略为 window-function 配额（`row_number() OVER (PARTITION BY task_type, conf_bucket) … WHERE sample_rn <= $n`），形状已由单测钉桩。
5. **验收项"E2E 层"**：`AUTO_E2E=1` 路径已编排（e2e-audit 三套件拼接 → testbench 合并报告），需活网关 + API key，随部署窗口执行。

## 五、对照规划验收门禁

| 规划验收门禁 | 状态 |
|---|---|
| 套件回归绿 + 基线 JSON 落库 | ✅ 240 例 GATE: PASS；`cmd/auto-testbench/testdata/baseline.json` 随仓 |
| 四语言页面可用 | ✅ 8 语言补齐（超出口径），typecheck/build/vitest parity 通过 |
| `npm run typecheck` 进 CI | ✅ `verify.sh --web` 既有显式步骤 + 本轮将 `vue-tsc --noEmit` 内嵌进 `npm run build`（构建门禁双保险） |
| 标注采样 SQL 有执行计划 | ⏳ 顺延至 252 只读部署验证（§四.4） |
