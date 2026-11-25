# 证据档：request-logs 超时优化的第一轮尝试（已撤回）——原始产物 ＋ 订正层

**归档时间**: 2026-11-25（L113 审计轮）
**性质**: 点时刻记录。本目录内 8 个文件（4 份 md ＋ 1 份 html ＋ 3 件 maestro 产物）为 2026-11-25 04:21–05:08 第一轮尝试的**原文**，一字未改。**本 README 是唯一新增的订正层**；原文中的结论已被后续事实推翻，阅读时以本订正层为准。

---

## 一、这批产物是什么

2026-11-25 凌晨针对 request-logs 页「查特定日期超时（60s query timeout）」的第一轮修复尝试，实施为：

- `af2bb4970`（admin/logs.go：探测流量 120s 动态超时 ＋ **探测流量 >12h 自动截窗** ＋ X-Query-* 元数据）
- `69d03d5b4`（RequestLogsView.vue：全屏加载覆盖层 ＋ 64px spinner ＋ 动态消息）

本目录文件即该轮的总结/报告/验证模板与 Maestro 测试产物。

## 二、后续事实（以何为准）

1. **截窗方案被撤回**：本车道后续提交 `36c278858`（历史探测车道回归 ＋ 截窗如实披露）已由 `ddc6aed19` 撤回；更早的截窗本体（`override_window_limit` ／ `window_clamped` ／「查询窗口已自动限制至12小时」提示）也已由上游 `71af786f6` **撤销**。当前 `admin/logs.go` 只保留动态超时（业务 60s / 探测 120s），**没有任何截窗逻辑**。
2. **真根因（211 生产 EXPLAIN 逐字坐实，见上游 `c0dad181d`／`44df1219b`）**：超时不是「查询复杂度／超时设置」问题，而是 `session_turns_2026_11` 等月分区**统计信息陈旧 45h** ⇒ 冷分区基数估 3 行（实际 4,850）⇒ details 腿被规划成 Nested Loop ＋ Join Filter（单语句约 1 亿次比较，26–38s）。修法＝details 腿改 `LEFT JOIN LATERAL … LIMIT 1` 按键点查 ＋ PartitionManager 补 autovacuum analyze 设置巡检。**且 12h 自动截窗以「未来时刻 end」为锚推 start ⇒ 窗口整个落在未来 ⇒ 「切时间范围后必然 0 行」——截窗补丁自己制造了第二个缺陷。**
3. **权威验证量具是上游的**：`maestro/run-request-logs-time-ranges.sh` ＋ `maestro/flows/19-request-logs-time-ranges.yaml` ＋ `maestro/tools/assert-request-logs-time-ranges.mjs`（本目录 `maestro/` 下三件已不对应任何现役行为）。
4. **线上前后对拍（211，含探测）**：昨天窗口 115.6s → 1.0s；近 14 天 0 → 272,748 行；近 30 天页查询 ~150ms（top-K 归并，`71af786f6`）。

## 三、原文中已被推翻的具体表述（阅读原文时须知）

| 原文表述（出现于多份文件） | 事实 |
| --- | --- |
| 「动态超时＋自动窗口限制机制**彻底解决**」「问题彻底解决」 | 截窗已撤销（`71af786f6`）；「彻底解决」由上游根修（点查＋统计巡检＋top-K）达成，与本批产物描述的方案无关 |
| 预期收益表「业务24h ~45s／探测6h ~55s／探测12h ~95s」 | 修后实测为秒级/亚秒级（115.6s→1.0s；页查询 ~150ms），且路径完全不同 |
| 「测试5 … `waitForAnimationToEnd: timeout: 150000`」等 Maestro 场景 | 断言「自动限制至12小时」「覆盖窗口限制」等已不存在的行为；且该 flow 五次执行均未成功（Chrome 连接问题），从未产生过一次绿读数 |
| 《手动测试验证报告》各场景「实际结果」 | 全部为空、复选框全部未勾——它是一份**未执行的模板**，不是验证记录 |
| `run-request-logs-test.sh` 「或环境变量 ADMIN_USERNAME/ADMIN_PASSWORD 已设置」 | 不成立：实际委托 `run-local.sh`，其口令**只从仓根 `.env.local` 读**（env var 不生效） |
| `tests/maestro/request-logs-timeout-test.yaml` 的 `appId: com.example.llmgateway` | 与 web 端在用的 `com.mobile.maestro.web` 不一致，launchApp 无法命中 |
| 「回滚计划：`git revert 69d03d5b4 af2bb4970`」 | 已不可作为操作指南：两笔之后各自叠加了多笔后端/前端改动，直接 revert 会连带回退无关修复 |

## 四、处置

- 原文 8 件移入本目录，一字未改（点时刻记录）。
- 已入库的正篇 `docs/audit/2026-11-25-request-logs-超时优化总结.md`（`af2bb4970` 提交、从未修订）由 L113 轮在文首**追加订正头**（原文正文不动）。
- 现役行为口径以 `admin/logs.go`（无截窗、动态超时）与 `web/src/views/RequestLogsView.vue`（序号守卫＋加载态分档）为准；验证以 `run-request-logs-time-ranges.sh` 为准。
