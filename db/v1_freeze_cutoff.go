package db

// V1FreezeCutoffSQL — _request_logs 宽表家族「冻结点」标量表达（S3 波 3，
// 2026-10-13）：v1 两表（hot ∪ 分区母表）当前的 max(ts)。
//
// # 它在解决什么
//
// S4 停写（storage.request_logs_write_enabled=false）后，新流量只落
// session_turns 家族；但**双写期两族并存**——同一请求在 _request_logs 与
// session_turns 各有一行。停写前的历史统计要继续由 v1 腿服务、停写后的
// 新流量要由会话腿服务，两腿 UNION 而不双计，需要一条**确定性的分界**。
//
// 冻结点 = v1 的 max(ts)：
//   - 写门开着（生产/回滚）：max(ts)≈now()，会话腿近似空集，读面行为与
//     纯 v1 时代逐字节一致；
//   - 写门关了：max(ts) 永久停在翻转时刻（音频计量等流量写点也已挂同一
//     道门，见 domains/streaming/audio_usage_log.go），会话腿接管全部新流量。
//
// 镜像链是异步的（outbox），且镜像行的 ts 是**落库侧时刻**、不等于 v1 行
// ts（2026-10-13 本地实测差 ~15s）。因此冻结点切分只把双子探测范围收窄到
// 「冻结点以上」的少数行；**消费方仍须在会话腿上带 NOT EXISTS v1 双子**
// （canonical 视图同款双 NOT EXISTS）才能保证逐行恰计一次——翻转边界存在
// 「v1 双子（ts≤冻结点）+ 晚落镜像（ts>冻结点）」的行（实测 1 例）。
//
// # 为什么不用反连接（NOT EXISTS session 族）
//
// R36-A1 实测裁决（2026-09-30，252-dev）：canonical 视图的反连接 + 租户
// 过滤 COUNT 30s 超时。冻结点是一次 initplan 标量求值（两表 max 各走
// ts 索引倒扫），把去重成本从「逐行探测」降到「一次求值」。
//
// # 为什么 max 要同时覆盖 hot 与母表
//
// promote 只搬 8h 前的旧行，刚写入的行只在 hot——只看母表会把冻结点
// 定在数小时前，双写期行会被两腿各计一次。
//
// 两表全空（新装/退役完成）时 GREATEST 回落 -infinity：会话腿收全部行
// （v1 腿本就空），语义正确。
//
// 供 admin/tenants.go（租户统计四聚合）与 bg/auto_index_refresher.go
// （autoroute 指标 rollup）共用——**冻结点只允许这一种拼写**（与
// settings.KeyRequestLogsWriteEnabled 同一教训：分界值散写多处的门，
// 早晚有一处被单独改掉）。
//
// ★ 890（v1 族退役）：冻结点**退化为常数 -infinity**。
//
// 为什么不是「让 SQL 自己报错」：冻结点是**拼进大查询**的标量子句，
// 42P01 会把整条查询（租户统计四聚合 / autoroute 指标 rollup）一起带走，
// 而它的语义后果只是「会话腿多收几行历史」——一个**可精确表达**的常量。
// ⇒ 与 v1 族退役后的真实状态一致：v1 腿恒空 ⇒ 冻结点 = -infinity ⇒
// 会话腿收全部行。
//
// ⚠ 这条常量与 890 迁移是**同一个决定的两半**：迁移删表、代码把冻结点
// 钉成常数。缺任一半 ⇒ 另一半要么 42P01（代码仍读 v1），要么恒 -infinity
// 而**遮住**迁移没跑的事实（v1 还在、还有历史行、冻结点却说"没有历史"）。
// ⇒ 有门钉住两者同时到位（见 db/request_logs_view_schema_test.go 的
// 「冻结点常数 ⇔ v1 族不存在」互钉断言）。
const V1FreezeCutoffSQL = `(SELECT '-infinity'::timestamptz)`
