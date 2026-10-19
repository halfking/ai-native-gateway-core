# Admin 五项库存失败复核与证据门修复

日期：2026-10-19。基线：干净的 `origin/main a70cb460cd613bad99c4ae5e7de499b3078e47f8`。
代码提交：`0ebe9bf8e19f090d6ccbeade2362b073869aa35e`；主干合并：`ff7b8e3821a8650dbe1182988c164ef05b585088`，已推送。

## 五项原始失败的裁决

原始输出保存在 `/tmp/output-controls-admin-baseline.log`。开始本轮时，上游提交 `f13c2289d3aa3bf8101228b540fcc22c9af38865` 已修正登记；本轮逐项核对实际源码并独立复跑，五项全部通过。以下库存修正归属于上游，不重复认领。

| 判据 | 原因与核实结果 |
| --- | --- |
| `TestRequestLogsControlPlaneKnownEntriesAreReal` | candidate monitor 已改为 session hot 与去重 v1 hot 联合供数；旧 canonical 文本不再是该处冷却查询的证据。staleness 的 v1 视图另经常量拼接，不授权冷却。 |
| `TestRequestLogsReadInventoryIsComplete` | lifecycle 引用由 16 处归并为 7 处；providers 的 error_rate_24h 改读 request_stats_minute 分钟滚存，已无请求日志族读点（active/healthy 等计数另由凭据状态供给）；tenants 的 6 处源引用抽到 tenant_stats_logs_table（原文件剩 2）；candidate monitor 字面读点由 2 到 1。实际为 99 文件 / 225 引用，与登记一致。 |
| `TestReaderPopulationGroundTruth` | providers 的旧登记造成未归类；抽出的租户源及 usage_enhanced 新增的 v1 parent 读取须显式登记。当前 v1 底表读方 50、仅视图 49、未归类 0；未判定 40，预算仍为 46。未把登记为未判定当成已完成退役评估。 |
| `TestV1ReaderTieringEveryFileProvenToReadV1` | providers 已无 v1 证据，移出该总体；抽出的租户源保留 Tier II，不遗漏仍在读取的文件。 |
| `TestV1ArmLiteralBlindSpotIsPinnedAndTiered` | providers 不再读取；candidate monitor 的拼接视图已被间接读方工具识别，转入间接总体。间接 27→28、交集 17→18，并集仍为 109。双向成员校验保留。 |

这些失败属于源码演进后的库存漂移，不能据此判定对应业务查询有缺陷。未扩大扫描豁免或提高未知读方预算。

## 本轮新增修复

1. 两处 Evidence 门原来只做 `strings.Contains`，Go 注释中的过期 SQL 也能当作证据。新增共享 `inventoryEvidenceInCode`：解析 Go，拒绝空证据和非法源码，只接受与 Go 注释范围不相交的原文片段。保留字符串中的 URL、SQL 注释及合法拼接，不通过删注释拼出原本不存在的片段。
2. Go AST 的 `Comment.End()` 基于已移除 CR 的注释文本，不能直接当原文终点。审查反例先证实行/块注释各含 40 个 CR 会假绿，随后改用 AST 起点与原始字节中的换行/闭合符计算范围；CR、CRLF 正反例均通过。
3. 加强后的真实门揭露 waterfall 的 `_request_logs_hot` 旧锚点只剩在迁移说明注释中。控制面及停写登记均改为实际的 `FROM request_logs_with_current_month`，并补专门判据。

证据门只证明片段位于合法 Go 源码的非 Go 注释区域，不替代 SQL 语义分析或实际调用链证明。

waterfall **保留 `errors_out` 风险档**：migration 824 的列投影不能证明所有请求都会镜像。`sessionv2mirror` 排除无会话探针及已有会话的内部自动请求，只有 v1 记录的这些请求在 S4 停写后、内存环外仍可能 404。镜像覆盖的业务请求可由 canonical 会话臂提供；缺失 request_id / t0 的 404 是其他原因。本轮未改业务查询、停写策略或生产配置。

## 验证与变异

所有命令均退出 0；完整包使用 `-count=1`，未使用缓存结果代替执行。

| 验证 | 结果 |
| --- | --- |
| 基线 `go test ./admin -count=1` | 通过，171.633s；原五项另行点名复跑通过（26.089s） |
| 最终代码 `go test ./admin -count=1` | 通过，158.690s |
| 主干合并后 `go test ./admin -count=1` | 通过，167.273s |
| `go build ./...`、`go vet ./admin` | 修复分支及合并主干均通过；Standards 审查另跑 `go vet ./...` 通过 |
| `go test -tags integration ./admin -run '^TestInventoryEvidenceRequiresCode$' -count=1` | 通过；这是标签配置下编译与单元判据，不是数据库集成测试 |
| `go test ./cmd/tools/sql_source_indirection_audit -count=1` | 通过，10.181s |
| `go test ./cmd/gateway -run '^TestS3Wave2WaterfallSources$' -count=1` | 通过，1.142s |
| sessionv2mirror 四类人口对照 | 无会话探针、有会话内部回环跳过；有会话探针保留原会话，无会话内部回环合成系统会话，二者仍镜像；四项通过（0.594s） |

使用 Go overlay 独立施加 14 个变异，不修改工作区；每项必须编译成功且指定 Test 在预期断言处失败，编译错误不计有效变异。

| 变异组 | 数量 | 指定失败判据 |
| --- | --- | --- |
| 恢复原始 substring、恒允许、恒拒绝、恢复 AST 归一化终点 | 4 | `TestInventoryEvidenceRequiresCode` |
| 控制面改回旧注释锚点 | 1 | `TestRequestLogsControlPlaneKnownEntriesAreReal` |
| lifecycle 计数错误、删掉抽出的租户文件 | 2 | `TestRequestLogsReadInventoryIsComplete` |
| 删 usage 成员、把预算改成 0 | 2 | `TestReaderPopulationGroundTruth` |
| 把 providers 放回 v1 分档 | 1 | `TestV1ReaderTieringEveryFileProvenToReadV1` |
| 盲区漏 diagnostics_credential、多加 candidate monitor | 2 | `TestV1ArmLiteralBlindSpotIsPinnedAndTiered` |
| 停写登记改回旧注释锚点、把 waterfall 风险改成 unaffected | 2 | 停写 Evidence 门、`TestWaterfallStopWriteAssessmentUsesCanonicalSource` |

14/14 均在指定断言处被拒绝。原始红/绿输出、overlay 脚本、每项 JSON 测试事件及 `mutation-summary.json` 位于本机私有证据目录 `/tmp/admin-inventory-20261019/`；该临时目录不是持久归档保证。

双轴审查均批准精确代码 SHA `0ebe9bf8e19f090d6ccbeade2362b073869aa35e`，并在合并 SHA `ff7b8e3821a8650dbe1182988c164ef05b585088` 复核本任务文件 blob 一致。并发 URSM 改动独立保留，不包含在本任务审查范围内。无 UI 改动；未部署或修改生产数据库。
