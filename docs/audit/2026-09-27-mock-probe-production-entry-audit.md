# Mock Probe 生产入口收口审计轮（2026-09-27）

**范围**：对"Mock 2x2 探测通道 + 总开关"需求（docs/design/2026-09-23-mock-probe-channel，2026-09-24 已合入 main 的 v2 实现）做需求完善与全量对账，收口生产入口（cmd/gateway）在制接入，修正审计发现的问题。

**结论**：需求八项全部达成；发现并修复 4 个问题（P0×2、P1×2），其中最重的是 mock_probe_history DDL 无投递通道（745 同款病灶复发）+ 分区函数时区依赖（751 同款病复发）。

---

## 一、需求完善（对账清单）

原始需求（用户口述）细化为可验收的八项：

| # | 需求 | 验收标准 | 状态 |
|---|------|---------|------|
| R1 | 系统参数：是否启动 mock 探测 | `mock_probe_enabled` / `LLM_GATEWAY_MOCK_PROBE_ENABLED`，默认 false | ✅ config/config.go:279 |
| R2 | 部署时 2 个 mock 供应商同步启动，响应测试任务 | mock-fast / mock-slow，4 端点 = 2 供应商 × 2 协议（OpenAI Chat Completions 主探测通道 + Anthropic Messages 联调），stream 由请求体决定 | ✅ 生产入口本轮接入（修复 F-1） |
| R3 | 定时 mock 客户端探测，检查流程完整 | 每 interval 一轮 2x2（供应商 × stream/non-stream），全量校验（状态码 + 响应形状 + [DONE] 收尾） | ✅ internal/mockprobe |
| R4 | 开关关闭时客户端零请求、完全静默 | runner 不启动、零 HTTP 请求、零日志 | ✅ 活体 B 实测 |
| R5 | 开关关闭时 mock 供应商不显示 | admin 列表隐藏 mock-fast/mock-slow（精确集合，历史种子/自建 mock-x 不受影响）；`mock_probe_hide_in_admin` 独立子开关（默认隐藏） | ✅ admin/providers.go + 真库两态实测 |
| R6 | 2x2 交叉测试通道 | 2 供应商 × stream 与否，指标带 scope=mock_probe 标签 | ✅ |
| R7 | 探测开=可见 | /metrics 序列 + mock_probe_history 落行 | ✅ 活体 A 实测 |
| R8 | 探测关=不可见 | /mock/* 404（语义=未部署该子系统）、/metrics 无 mock_probe 序列（CounterVec 未打标签不渲染）、无落行 | ✅ 活体 B 实测 |

设计澄清（与用户口述的映射）："开关关闭时 mock 供应商不显示"以 `MockProbeHideInAdmin`（默认 true）实现而非直接绑定 `MockProbeEnabled`——两个 mock 供应商是进程内 handler 不落 providers 表，列表可见性只对"手工插入同 code 行"等边缘场景有意义，独立子开关保留调试放行能力（`HIDE_IN_ADMIN=false` 显式放行）。

## 二、审计发现与修复

### F-1（P0）生产入口在制修改编译失败：startMockProbeRunner 未定义

cmd/gateway/main.go 与 middleware/auth_mw.go 的在制修改（端点注册 + runner 装配 + 停机序列 + /mock/ 鉴权旁路）调用了 `startMockProbeRunner(probeCtx, cfg, dbConn)`，但函数从未定义，`go build ./...` 失败。

**修复**：cmd/gateway/main_helpers.go 补定义。与 v2 版本的差异刻意保留并注释：复用主 DB 池（不建独立池）、dbConn nil/未启用降级为只打指标、不注册 shutdown.Manager（生产停机序列显式驱动 probeCancel → Stop）。dbConn 类型是 `*db.DB`（非 pgxpool），经 `dbConn.Pool()` 取池，`Enabled()` 判 nil 安全。

### F-2（P0）mock_probe_history DDL 无投递通道（745 同款病灶复发）

migrations/036_mock_probe_history.sql 头注释拍板"ops 维护窗口手工执行、不走 startup revision-sequence"。实际结果：仅 252 生产库被手工跑过；本机库（以及任何新环境）永远缺表——`MockProbeEnabled=true` 时历史落库静默降级，且 writeLoop 每条 INSERT 失败刷 Warn（默认 4 条/30s 的持续日志噪音）。与 745_report_snapshots 曾登记的病灶同构（"死放 migrations/ 顶层无执行器接线"）。

**修复**：DDL 收编正典通道——新增 `sql/migrations/startup/752_mock_probe_history.sql`（+down），四点同步登记：installer/internal/dbinit/runner.go 显式清单、scripts/apply-db-revision-sequence.sh files=()、docs/db-changelog.md 台账（SHA256）、顶层 migrations/036 删除（消除与 db/migrations/036_fp_slot_limit 的撞号异文件混淆）。幂等性保证 252 存量库重放安全。

### F-3（P1）分区函数时区依赖（751/750-R69 同款病复发）+ DEFAULT 当日行 ATTACH 必炸窗口

036 原函数 `mock_probe_history_daily_partition()` 用 `current_date` + `d::timestamptz` 求边界，随会话时区漂移：UTC 会话产出与上海日边界错位 8h 的分区窗口，且与后续上海日历分区相邻日重叠后 ATTACH 必报 overlap、永不自愈（750 R69 修订实证的同款病）。且原函数直接 `CREATE TABLE ... PARTITION OF`——DEFAULT 分区已存当日行时被 DEFAULT 约束校验击杀（036 时代启动 bootstrap 只跑一次不触发；F-4 的 ensure tick 引入后该窗口真实存在）。

**修复**（752 内）：函数级 `SET timezone = 'Asia/Shanghai'`（proconfig，函数入口生效、先于 DECLARE 初始化器；751 的对偶，CREATE OR REPLACE 顺带收敛 252 存量旧版函数）+ move-then-attach 搬移挂接（advisory lock 串行化 + pg_inherits 双检短路 + `CREATE TABLE (LIKE ... INCLUDING DEFAULTS INCLUDING INDEXES)` + DEFAULT 当日行 DELETE-INSERT 搬移 + ATTACH 元数据挂接）。无参签名保留（兼容 036 存量调用点与 runner.go 调用面），内部滚动预建当日 + 次日。

### F-4（P1）长期运行分区缺失：DEFAULT 分区堆积

036 时代只在启动 bootstrap 建一次当日分区；网关数周不重启时跨午夜后新一日行全部落 DEFAULT 分区——数据不丢但 partition pruning 退化，违背按日分区设计意图。

**修复**：HistoryStore 增加 23h 周期的分区 ensure tick（writeLoop 消费路径上，CAS 抢占防并发，失败回退 0 使下一批记录即重试）。停机排空分支刻意不 ensure（停机语义）。

### 附带修复

- 在制注释悬空引用 `docs/audit/2026-09-26-mock-probe-audit.md`（该文档不存在，为上一轮计划产物）→ 改指本文档。
- mockprobe 真库测试不可重入（重复运行累积同 request_id 标记行 → 假失败"expected 2 rows, got 14"）→ 两个真库测试加自清理前导。

## 三、验证记录

**编译/vet**：`go build ./...`、`go vet`（主模块 + installer 模块）全过。

**单测**（全绿）：internal/mockprobe（14 例，含新增 TestHistoryStorePartitionTickThrottle / TestHistoryStorePartitionEnsureTickRealDB，3 连跑确认可重入）、internal/auth、internal/providers/mock、config、middleware、internal/observability、admin（66s 全量）、cmd/gateway、cmd/gateway-v2（含 e2e）。

**752 真库实跑**（本机 llm_gateway 库，R37 纪律"新迁移必须存量真库实跑"）：
- UTC 会话（`SET timezone='UTC'`）双轮执行 752：幂等无错；分区边界 `2026-09-27 00:00:00+08`（上海日历，钉扎生效）；`pg_proc.proconfig = {TimeZone=Asia/Shanghai}`；父表三索引齐（PK + supplier/channel 两查询索引）；UTC 会话冒烟 INSERT 落当日分区。
- move-then-attach 回滚事务实证：DETACH+DROP 0928 分区 → DEFAULT 塞 0928 行 → 重跑函数 → DEFAULT 行数 2→1、新分区收 1 行 → ROLLBACK 还原。

**admin 可见性真库两态**：插 code='mock-fast' 行后，带隐藏谓词查询 0 行（hide=true 默认）、不带谓词 1 行（hide=false 放行），行已清理。谓词语义（精确集合 vs 前缀）由 admin/providers_mock_filter_test.go 10 例钉死。

**生产入口活体 A/B**（本机临时网关 :18799/:18798 连本机 PG，5s 探测周期）：
- A（enabled=true）：日志顺序 endpoints registered → gateway listening → runner started（绑定后启动，首轮不记脏失败）；2x2 探测行落库全 200；/metrics 64 行 mock_probe 序列（counter + histogram 的 sum/count）；端点鉴权矩阵 Bearer=200 / 无凭证=401 / GET=405；优雅停机 "mock probe runner stopped"。
- B（enabled=false）：无 endpoints/runner 日志；/mock/* → 404；/metrics 零 mock_probe 序列；mock_probe_history 行数零增长（27→27）；零探测失败日志。R4/R8 完全静默达成。

## 四、风险与遗留

- **252 生产库重放 752**：表/索引已存在走 IF NOT EXISTS 跳过；函数 OR REPLACE 升级为钉扎版。该库 schema_migrations 无 036 条目（手工通道不记账），752 按 startup 台账正常记账，无重放冲突。部署走既有维护窗口流程，非本轮动作。
- **探测流量与 request_logs 隔离**：探测打自身 mux 的 /mock/v1/*（经鉴权旁路 + MockEndpoint 守卫），不进 streaming pipeline、不写 request_logs（设计原则，R63 结论维持）；prometheus HTTP 层计数（llm_gateway_http_requests_total{path="/mock/v1/..."}）会出现——这是中间件层对任何路径的正常计量，非 mock 数据泄漏。
- **MockEndpoint 守卫凭证是公开系统标记**（mock-probe-client，非机密、不在 api_keys 表）：已由设计 §六风险表拍板；/mock/ 全局鉴权放行的爆炸半径 = 4 个只读 mock handler，无状态副作用。
- 遗留（登记不改）：admin **detail** 端点（按 id 查单供应商）未挂隐藏过滤——需求口径是"列表不显示"，detail 需先知道 id（列表拿不到），信息泄漏面为零；如后续要求 detail 也隐藏，挂 mockProviderHidden 同款判断即可。
