# S3 波 4：bodies 退役评估（25 文件逐点登记）+ 四处读方迁移 + 两个活缺陷修复

> 轮次：2026-10-13 下午，S4 停写（07:01:04 翻转）后的直接后续。
> 目标（用户原话方向）：request_logs 及相关表按规划将被删除，只使用
> session_* 表；对涉及 request_logs 的查询与 API 尽可能切换，无法切换的
> 修正内部实现，确保数据在更改前后一致。
> 前置：S3 波 2（16 读点）、S3 波 3（最后写点 + 三读点）。

## 一、复审计：删表前的数据一致性四象限（本地真库实测）

| 口径 | 计数 | 判定 |
|---|---|---|
| request_logs / request_logs_bodies（冻结存量） | 35,506 / 35,506 | 翻转后零新行（hot 双表 0） |
| session_turns / session_bodies | 12,011 / 12,011 | 持续承接新流量（hot 4,799） |
| bodies 只在 v1（v1_only） | 23,795 | **= 探针观测 22,951（无会话头，R51 设计不镜像）+ 回环/非终态 844 + 真损失 0** |
| 带会话头而未镜像（drift 校验器口径） | 844 全为 internal_loopback(835)/non_terminal(9) | **genuine_loss = 0** |

方法注记：`MirrorDriftClassSQL` 的字面转录会把无会话头的探针行判成
genuine_loss；drift 校验器（cmd/gateway/dual_read_validator.go:314 起）的
范围 SQL 在分类**之前**就有 `gw_session_id IS NOT NULL AND <> ''`——探针行
出范围。两者不矛盾，「genuine_loss=0」在其口径内正确。删 v1 家族**不丢任何
用户可见正文**；损失面只有两类已登记可接受项：探针观测正文（事实主存
node_probe_runs 族）与 title/summary 回环正文（生成器中间产物）。

**跨冻结会话实证（gw_5f34a63f…，24 turns，05:17→07:29 横跨翻转点）**：
v1 bodies 源只见 13 条、会话族源 24/24——这就是本轮修掉的活缺陷的量化。

## 二、四处读方迁移（本轮代码变更）

### 2.1 admin/session_bodies_batch.go：主批量取数器固定读会话族【活缺陷修复】

消费方 session_compare 的 turns 腿是会话族（SessionFamilyTurnsForSessionSQL），
bodies 腿却读 v1 视图——停写后**新会话正文对对比/摘要端点静默为空**。
改法：`sessionBodiesByRequestIDSQL` FROM 换 `db.SessionFamilyBodiesSourceSQL()`
（hot∪父表；不走运行时开关——其 v1 臂对会话族 turns 是错源）。配套：
- 新增 `v1BodiesByRequestIDSQL` + `queryV1BodiesByRequestID`：session_summary_v2
  的 **v1 回退路径**专用（其 turns 腿是 v1 视图的遗留会话，bodies 腿必须
  同源），随 v1 DROP 退役；
- `sessionBodiesByRequestIDAndTSSQL`（无生产调用方，§5.9 守形钉子）原样保留，
  登记「DROP 时删除」。

### 2.2 admin/session_compare.go：解析器补 delta 数组臂【配套 2.1 的形状合同】

会话族 `request_delta`/`response_delta` 是消息数组 `[{role,content},…]`，
v1 `request_body` 是带 `messages` 键的对象。`parseMessagesFromBody` /
`parseResponseMessages` 各补一个数组臂（放对象臂之前；两种形状互为硬失败，
不误吞）。不补这个就换源 = 门注释警告的「看起来成功的空数据」。

### 2.3 admin/compression_sessions.go：整只端点会话族化

turns 腿×3（countSQL/主查询/LATERAL）换 `SessionFamilyTurnsSourceSQL()`，
bodies 腿×3 换 `SessionFamilyBodiesSourceSQL()`；LATERAL 的原始消息数从
`request_body->'messages'` 改 `jsonb_array_length(request_body)`（会话族
request_body 即消息数组），`rb2.ts = rl2.ts` ts 等值配对按 §5.9 实测口径删除。
压缩列（compression_strategy/outbound_msg_count/outbound_token_est）在
canonical 投影内（真库验证会话侧有数据：strategy 14 行、msg_count 3,334 行）。
对拍：48h 窗新旧 count distinct session 均 16,346。

**2605 部署端点实测抓到并修复的形状缺陷（M5）**：request_delta 有四种形态
——array / SQL NULL / **JSON 字面量 null / 其它标量**。JSON null 的 SQL 值
非 NULL，`COALESCE` 救不了，裸 `jsonb_array_length` 直接 22023
「cannot get array length of a scalar」（本地真库 outbound 非空行里 JSON null
1,433+ 行）。修复：`CASE WHEN jsonb_typeof(rb2.request_body)='array' THEN
jsonb_array_length(…) ELSE 0 END`，2606 上线后端点 200。判据补「禁裸
COALESCE 形 + 必须在场 jsonb_typeof 守卫」双钉，变异 M5 红。
（Go 侧解析器无此问题：`json.Unmarshal("null", &slice)` 返回 nil slice
不报错，len=0。）

### 2.4 cmd/scenario_driver/main.go：落库断言改数会话族热表【活缺陷修复】

停写后数 v1 表 delta 恒 0，s22-3/s23-3 永久 FAIL。bodies 计数改
session_bodies_hot、失败计数改 session_turns_hot.success=false（同为 8h 热窗带 ts）。

## 三、bodies 退役门：25 文件逐点登记完毕，门转绿

`admin/request_logs_bodies_retirement_gate_test.go` 的 `v1BodiesReaders`
登记表首次填齐（评估前 27 文件/39 站点，四处迁移后 25 文件/34 站点全部
登记）。按门自身定义：**全绿 = 登记表覆盖全部 bodies 读方 = 那时才谈得上
request_logs_bodies 可删**。登记分四组：

1. **切换层消费点 13 个**（session_title/memora/logs_summary/no_topic/
   auto_title/session_sanitize/compression_stats/session_export/session_compare/
   summarizer/system_prompt_prefix/sessionforensics·export/passive_probe_listener）：
   退役后 = 切换层 v1 臂删除、会话臂转唯一；覆盖性由 §一 的真损失 0 背书。
2. **字面 v1 腿 + 会话兜底并存的点查族 4 个**（logs/body_resolver/
   unified_detail/history_store）：DROP 时删 v1 腿，兜底腿转唯一。
3. **v1 专属读方 7 个**：session_bodies_batch（v1 变体=summary 回退同源腿，
   随 v1 退役；TS 元组无调用方直接删）、quality_correlations（业务 auto 行
   改读**原生 outbound_body**——机械换切换层会静默塌桶，request_delta 无
   tools 键；探针维度归 node_probe_runs，主拍板项）、data_lifecycle（v1
   存储治理语义随 v1 退役）、data_lifecycle_blobs（**DROP 前必须先做会话臂
   扩展**，否则停写后新正文无容量治理——S3 波 2 已登记的债）、
   compression-bench（改 session_bodies 原生，ts 在）、backfill 工具与
   validate_sessions_v2 loader（随 v1 退役，工具使命即 v1 真值）、
   db/view_schema（切换层本体：删 v1 臂）。

## 四、审计网络同步（7 张登记表 + manifest 旧账清偿）

| 登记 | 变更 |
|---|---|
| 停写分类表 | compression_sessions/scenario_driver 出列（各自预言的失效形态本轮修掉） |
| read inventory / tiering 域 | 两文件除名；字面量总体 105→103、并集 113→111 |
| v1ArmLiteralBlindSpot | session_bodies_batch 迁出（拼接形态工具已可见）；盲区 44 |
| population 基线 | 49→48（scenario_driver 出列） |
| 控制面表 | scenario_driver 出列 |
| v1_bodies_read_path / shapes 表 | 两文件出列（含 shapes 表里「换源前先定口径」警告的兑现记录） |
| 审计文档静默档 | 59→58（S4 波 4 订正批注，TestAuditDocSilentClaimMatchesRegistry 钉住） |
| **cmd/tools/sql_source_indirection_audit manifest** | **波 2/3 遗留的 8 条未登记旧账清偿**（body_resolver/routing/unified_detail/main_v3_wiring/request_summary/history_store/model_alternatives/summarystore 全部判定 nonv1-by-inspection）+ 快照 73→92/canonical 12/18→22/36 更新——该工具测试自波 2 起一直是红的，本轮复绿 |

## 五、判据与变异验证

新增 `admin/s3_wave4_bodies_sources_test.go`（五件套）：

- 批量取数器三分格局（会话族双腿 count==2 + v1 变体钉 v1 视图）；
- 解析器双形状合同（数组臂 + **对象形状既有行为不回退**，判据纪律 #3 前提在场）；
- compression_sessions 双源 count==3×2 + 三个旧形态禁入（剥注释判活代码）；
- scenario_driver 双热表在场 + v1 两表禁入。

**变异验证 4/4 红**：M1b 主取数器改指运行时开关（「假迁移」形态）→ count==2 红；
M2 删 request 数组臂 → 数组判据红；M3 主查询退回 v1 视图 → 禁入红；
M4 一处计数退回 request_logs_bodies_hot → 禁入红。（M1 的字面量回退版本
因未用 import 编译红，不采信为变异证据，改用 M1b。）

守形门 `session_bodies_pairing_test.go` 同步：summary 回退取数器改名钉
`queryV1BodiesByRequestID`；批量取数器守形从「必须含 v1 视图」改为
「必须含会话族双腿 + v1 变体钉 v1 视图」。

## 六、门状态

- `go build ./...`（CGO_ENABLED=0）/ `go vet`（admin、scenario_driver）rc=0；
  改动文件 gofmt 零输出。
- `CGO_ENABLED=0 go test ./admin/ ./bg/ ./db/ ./settings/ ./cmd/tools/...`：
  **admin 全绿（含退役门首次转绿）**、bg/db/settings ok、manifest 工具复绿。
  cmd/gateway 的 lite 系列在 CGO_ENABLED=0 下不可运行（既有环境边界）。
- 真库预验：§一 四象限 + §2.3 对拍 + LATERAL 表达式实测。
- 变异 M1b/M2/M3/M4/**M5/M6/M7** 全红在正确断言。
- **本地部署端点验证（2605→2607，8782）**：
  - `/api/admin/session-compare`（跨冻结会话 gw_5f34a63f…）：70 turns，
    冻结前 13/13、**冻结后 57/57 全部有正文**（修复前冻结后恒 0——旧 v1
    源无这些行），original_msgs 6,406 条解析成功；
  - `/api/admin/compression/sessions`：200，count 16,427，
    LATERAL orig_msg_count 有真值（2605 曾 500=§2.3 的 22023，2606 修复）；
  - `/api/admin/data-lifecycle/blobs/top|cleanup/preview`（2607，会话臂）：
    Top-N 真实大对象行（minimax-m3 726KB 等），preview >50KB 命中
    426 行 / 34.6MB 可清理；
  - `/api/admin/auto-route/quality-correlations`（2607，会话族）：200，
    分桶空 = 新口径如实反映（本地会话族 auto 行 0，业务 auto 流量
    到达后自动有数）。

## 七、删表前置清单（DROP request_logs_bodies 之前的剩余工作）

1. ~~**data_lifecycle_blobs 会话臂扩展**~~ **已落地（2026-10-13 波 4 下半场）**：
   Top-N/preview 改读 session_bodies 族（hot∪父表全列源），大对象口径 =
   outbound_body + request_attachments/response_attachments；execute 的
   UPDATE 目标改 session_bodies_hot，SET 三大对象列置 NULL、**增量正文
   （request_delta/response_delta）刻意保留**（比 v1 语义多保对话内容）；
   VACUUM 改 session_bodies_hot。端点实测（2607）：Top-N 返回真实大对象行
   （maximamax-m3 726KB 等），preview >50KB 命中 426 行 / 34.6MB。
   判据 `TestS3Wave4BlobGovernanceTargetsSessionFamily`（源双腿 count==2 +
   v1 禁词 + SET 清单与增量列保留钉子），变异 M6 红。
2. ~~**quality_correlations 重写**~~ **已落地（同轮）**：turns 腿 canonical、
   bodies 腿经切换层读 **outbound_body**（tools 桶带 jsonb_typeof 守卫）；
   语义边界显式拍板：探针流量出围（R51 不进会话族；本地实测 v1 auto
   23,786 行里探针占 22,951）——业务 auto 流量到达前分桶为空是**新口径的
   如实反映**。探针维度若需保留 → node_probe_runs（仍属主拍板项）。
   判据 `TestS3Wave4QualityCorrelationsTargetsSessionFamily`（同层驱动
   buildBreakdownQuery ×5 + rb.request_body 禁词 + 守卫在场），变异 M7 红。
3. **生产 owner 复核**：生产库跑 §一 同口径四象限（with-header 未镜像 =
   0 才可删；未镜像遗留会话（sessions_v2.enabled=false 时代）人群规模
   需单列——它们的摘要回退路径随 v1 消失）。
4. 生产 settings_kv 确认 `storage.session_bodies_native_read=true` 已开
   （本地已开；这是切换层消费点组的取源前提）。

## 八、遗留与边界（登记不修）

- summary_v2 的 v1 回退路径在 v1 DROP 后失效（遗留会话摘要语料消失）——
  与 7.3 同一 owner 复核项。
- 探针观测正文（22,951 行）无会话族落点——与音频秒数、model_task_index
  同桶（主拍板项），probe 面族读方迁移是独立轮次。
- 工作区并行会话遗留（executor_anthropic.go 等 r1025 相关脏文件）与本轮
  无关，未纳入提交。

## 九、生产删表前置确认（2026-10-13 19:25 经 245 直连 172.16.2.210 实测，只读）

| 检查项 | 结果 | 判定 |
|---|---|---|
| 生产写态 | `storage.request_logs_write_enabled=true`，request_logs_hot / session 侧 max_ts=now | 双写仍在（未翻转，符合预期） |
| 四象限-存量 | request_logs 327,039 / bodies 317,360（gap 9,679 行无 bodies，存量保留策略）；session_turns 925,038 / session_bodies 130,538（含早于 v1 现存量的深历史） | 会话族独立于 v1 有完整历史 |
| 四象限-真损失 | 带会话头未镜像：全量 34,927（回环 32,532 + 非终态 2,243 + **真损失 158**）；168h 窗内真损失 147 | **≠0，翻转未就绪** |
| 真损失成因 | 154 网关日志实证：`sessionv2mirror: V2 shadow write failed`（outbox 入队/turn 插入 `context deadline exceeded`），今日 5-54 次/小时持续滴漏；**245 零失败**；session_mirror_outbox pending=0（reaper 在消化，但入队本身超时的行不进 outbox、不可重放） | 154→共享 PG 写超时（基础设施争用），非代码缺口；丢失行可从 v1 定向回填 |
| 探针量 | 无会话头探针行 156,385（设计不镜像，事实主存 node_probe_runs） | owner 拍板项维持 |
| 读开关 | `storage.session_bodies_native_read` **未设置**（默认 false=切换层 v1 臂） | 翻转 runbook 第 3 步前置设置 |

**生产翻转 runbook（顺序刚性）**：
1. 修 154→共享 PG 写超时（252 PG 负载/网络路径）。可先试**免部署调参**：
   settings_kv 调 `sessions_v2.write_timeout_ms`（默认 2000，运行时生效）——
   直至 168h 真损失=0（s4_ready 门转绿）。245 零失败可作对照基线；
2. 定向回填现存 ~158 丢失行（**只能从 v1 回填**：session_mirror_outbox 的
   147 行全是 09-23 的旧探针死信（request_id='probe-direct'），与今日丢失
   无关；镜像失败当下若重试入队也超时则落进程内 backlog，同样无着落）；
3. settings_kv 置 `storage.session_bodies_native_read=true`（切换层 14 消费点转会话臂）；
4. 置 `storage.request_logs_write_enabled=false`（停写），观察期；
5. owner 最终授权后执行 v1 家族 DROP。

## 十、翻转执行记录（2026-10-13 晚，用户授权执行）

**245/154（共享库）**：先部署 2612/2613（af27bb7b 含 wave4）→ 翻转验证期
抓到并当日修复两个生产规模缺陷（compression LATERAL 10s 超时 → 单趟 CTE，
e01ffed39；quality avg_quality NULL 崩 → COALESCE，d3ce08d86）→ 重部署
2617/2618 → write_timeout_ms=5000（154 镜像滴漏 5-54/h → 0）→
**20:18:27 执行 native_read=true + 停写**。验证：v1 hot 冻结于 20:18:11、
会话族推进、post-flip 会话 compare 有正文、端点清扫两台 4/5 绿
（stats=v1 存量端点超时，登记随 v1 退役）。

**211**：DB/Redis 本就 bind 在 /opt/kaixuan（机器规范）；网关状态目录
（keystore/hotzone/attachments/backups/model-quality/logs）迁出容器层到
/opt/kaixuan/data/llm-gateway + deploy-211-docker.sh 补挂载（562cc1e6d）。
双写为继承关闭态（库=本机副本）+ timeout 对齐。E2E：真实 glm-4.5-flash
请求 → 仅落会话族（turns+bodies 在场、request_logs_hot 恒 0）；管理面
全绿。新登记：容器重建首启 DB 池竞态（重启即愈，部署就绪门需断言 mode）、
redis 库号再漂 10→9。

158 条生产真损失仍在 v1 可回填；DROP 前余下动作不变（见 §九 runbook，
第 1/3 步已完成）。
