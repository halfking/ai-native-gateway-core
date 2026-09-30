# verify.sh 收口轮 handoff —— 三项既存缺陷修复 + 三道新发现红门登记

日期：2026-09-30
基线：`d0f333687` → 收口于 `a48ef1f08`（origin/main 已同步，0/0）
承接：`20260930-audit-v3-round41-closeout.md`（四十一轮）

## 0. 先说结论

本轮把 verify.sh 从"卡在 pre-commit"推进到"后端全绿、前端剩 3 道既存红门"。
修复三项、新发现三道红门。**verify.sh --web 整体仍未 PASS**，红门均为既存问题，
需要超出「最小改动」的决策，本轮不修。

## 1. 交接基线本身的两个错误（重要，别再踩）

上一轮 handoff 写的是 `HEAD=8d5ddf9df`，实际接手时已是 `d0f333687`；
且 origin/main 在交接之后又推进了 12 个 commit（含"合并后回归红 2 例正面修复"）。
**照单执行 handoff 清单会漏掉已修的项。** 下轮接手请先 `git fetch` + `git log --stat`。

更关键的一条：**上一轮 handoff 给的 web 修法是错的。** 它建议把 mock 标注成
`(key: string, named?: Record<string, unknown>) => string`，那是 vitest 2/3 的
单泛型形态；本仓实测 `vitest 1.6.1`，`Mock`/`vi.fn` 是旧式双泛型
`<TArgs extends any[], R>` 元组形态（`node_modules/@vitest/spy/dist/index.d.ts:233`）。
照抄只会让错误更多。**任何 handoff 给出的具体修法，先核对依赖实际版本再动手。**

## 2. 本轮已修（三项，均已变异验证）

### 2.1 web vue-tsc 类型门 21 错清零 —— `182fecb3f`

交接清单第一项。三处，纯类型，运行时行为未改（这三个文件 vitest 本就 24/24 全过）。

| 文件 | 改法 |
|------|------|
| `web/src/composables/useLiveStreamUrl.test.ts` | `Mock<[key, named?], string>` / `Mock<[], void>` |
| `web/src/composables/useSessionSummaryJump.test.ts` | `vi.fn<RouterPushArgs, R>` |
| `web/src/components/ui/KxDateRangePicker.test.ts` | `beforeEach`/`afterEach` 改块体 |

第三处的根因值得记：`beforeEach(() => vi.setSystemTime(FIXED))` 的表达式体会把
`VitestUtils` 返回值当成 hook 的清理回调返回，撞上 vitest 1.x 的
`Awaitable<HookCleanupCallback>` 签名 → TS2322。**凡是用 `() => vi.xxx()` 写
beforeEach/afterEach 都会踩这条。**

复验：`pnpm run typecheck` exit=0；`pnpm run test` 145 文件 / 1060 测试全绿；
`npm run build` exit=0。

### 2.2 750/752/753 台账 SHA 补正 —— `277547c30`

根因是审计轮改写了迁移文件却没更新台账行（753 台账行记的 `9c8493f9` 正是旧版）。
按 754 既有做法补正，并写明变更来源 commit 与新旧值。

| 迁移 | 变更 commit | 新 SHA | 旧值对应版本 |
|------|------------|--------|--------------|
| 750 | `0ba35dc2b`（R69 P2） | `e5756633` | `1dfe88c08` |
| 752 | `5955fcdb0`（R73） | `d0a9bc0b` | `5193d88f2` |
| 753 | `7230ea1b3`（R72 P2） | `60ef1ccf` | `14d34867f` |

750 的 `applied+verified` 状态早于该次改写，已在行内注明新内容随下次部署走通道。

复验：0 mismatch。变异：台账 753 SHA 改全 0 → 门点名 753 → 还原 → 绿。

### 2.3 800/801 补登升级序列通道 —— `a48ef1f08`（**交接清单外的硬阻塞**）

`ec5edcfd7`（759→801 撞号修正）只把 800/801 登记进 installer `StartupFiles`，
**没进 `apply-db-revision-sequence.sh` 的 `files=()` 升级序列**。结果是 fresh install
拿得到、存量库升级永远不落地——正是该门注释里点名的 693/699/701/703 复发形态。
`verify.sh` 在 pre-commit 阶段即停。

两处必须一起补，缺一不可：

1. `files=()` 追加 800/801，排在序列末尾（必须晚于 733：801 重定义的目标函数
   依赖 733 建的 `session_turn_details` 表与 hot 表）。
2. `intentional_function_chains` 登记
   `'promote_session_turn_details_hot_to_partition|733_session_turn_details.sql|801_session_turn_details_duplicate_drain.sql|'`
   —— 801 替换的是 733 建的同名函数，不登记会被 clobber guard **exit 5 卡死每次部署**
   （与 703 当年同一 pre-flight abort 类别）。

双向变异确认两处都真被守住：撤 chain → exit 5 点名 801；撤 files 条目 → exit 1
复现原报错；还原 → passed。

## 3. 三道红门（既存，本轮不修，需要决策）

均已实证与本轮改动无关：本轮只动 3 个 `.test.ts` + 1 个 `.md` + 1 个 `.sh`，
**未触碰任何 `.vue`、未触碰 `go.mod`/`go.sum`**。

### 3.1 govulncheck exit=3（依赖漏洞）

```
GO-2026-6348  grpc  HTTP/2 DATA Frame 分片导致堆耗尽   1.81.1 -> 1.83.1
GO-2026-6061  grpc  xDS RBAC / HTTP2 传输实现          1.81.1 -> 1.82.1
GO-2026-5970  x/text 非法输入死循环                     0.38.0 -> 0.39.0
```

`grpc` 当前是 `// indirect`。**注意 govulncheck 在 verify.sh 里位于 web 段之前**，
`set -e` 会在此中止，导致 `responsive:check` / `element:check` / `color:check`
在 `--web` 模式下根本跑不到——必须单独跑或 `--skip-govulncheck` 才能测到前端门。
这是 verify.sh 的一个结构缺陷，下轮可考虑把 govulncheck 挪到最后或改为警告级。

### 3.2 `responsive:check --strict` exit=1

2 处白名单外断点，**今日 `123b2540e`（统计 UI 优化三页落地）引入**：

- `web/src/components/UserDetailDrawer.vue:900px@L334`
- `web/src/views/TenantDetailView.vue:1080px@L1105`

白名单是 `[480,640,768,1024,1440]`。脚本自带 `--allow-legacy` 可放行存量。
**待定**：改用白名单断点，还是给 verify.sh 放行存量。

### 3.3 `color:check` exit=1（rule 12 P0）

23 个违规文件 / 78 处硬编码颜色，其中 **37 处基线外新增**（基线 `124522ea2`，
2026-09-14 入库 44 条）。集中在：

```
19  views/AutoTuningView.vue
18  views/TaskProfileView.vue
 8  components/lifecycle/UpdateActivateVersionsCard.vue
 5  views/CredentialHeatmapView.vue
 3  components/credential-monitor/CredentialDetailDrawer.vue
 3  views/ProvidersView.vue
 3  views/RequestLogsView.vue
```

这是设计令牌化整改工程，不是本轮该顺手做的。脚本提示两条路：
令牌化后提交，或**评审确认保留后**跑 `npm run color:baseline:update`。
后者是评审动作，不该由 agent 自行决定。

## 4. 顺带记录：构建副作用

`web/public/menu-config.json` 被 tracked，而 `npm run build` 的第一步
`node scripts/export-menu-config.mjs` 会重写它的 `exported_at` 时间戳。
**每次 build 都会产生一处无意义 diff。** 本轮已 `git checkout --` 回退。
下轮别把它当改动提交。若要根治，考虑 build 后自动还原或把时间戳移出版本控制。

## 5. 复验命令

```bash
cd __DEV_HOME__/workspace/ai-native-tools/syncfield/llm-gateway-go-4
./scripts/verify-migration-checksums.sh --quiet        # 期望 OK / 0 mismatch
bash scripts/apply-db-revision-sequence_test.sh        # 期望 passed
go test ./internal/sqlreadguard/ -count=1              # 期望 ok
cd web && pnpm run typecheck && pnpm run test         # 期望 0 / 1060 全绿
```

## 6. 风险

- **origin/main 由并行会话高频推送**（本轮接手期间就推进了 13 个 commit，
  其中一个是净零内容增量的 merge）。提交前必须重新 `git fetch` 核对。
- 本轮给 800/801 补了升级序列通道，**下次部署到 154/245 时这两个迁移会经
  sequence 通道落地**。两者均幂等（800 走 `CREATE TABLE IF NOT EXISTS` +
  `ON CONFLICT DO NOTHING`；801 只 `CREATE OR REPLACE`，不跑批量 DELETE、不改存量
  分区），但这是本轮唯一一处**改变部署语义**的改动，如与预期不符请回退 `a48ef1f08`。
- 上一轮遗留的 `TestDiscoverGateways` /
  `TestStrictProxyTransportBlocksOverseasWithoutProxy` 环境依赖判定，本轮**未取到
  新证据**：本轮 `go test ./...` 全绿，这两个测试在当前环境已通过，但仍未取得
  跨环境证据，不排除是间歇性网络抖动。
