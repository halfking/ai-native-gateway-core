-- Migration: 999a_data_maintenance_playbook
-- Purpose: 运维管理/数据维护手册条目 —— 数据异常与缺陷的处置路径 + 探测器口径
-- Phase: 数据维护手册轮（2026-11-24）
-- Author: System
-- Date: 2026-11-24
--
-- ★ 背景（owner 需求，逐句对应）：
--
--   「根据这些问题，给出运维需要进行维护的工作，可以建一个系统中的问题，
--     并要求人工进行操作的功能，可以放在运维管理的或数据维护的工作中，
--     需要统计所有的可能的数据异常或缺陷，或者将这些给 pi 智能体来进行分析
--     与自动化的优化，并记录这些问题，放在我们的 pi 错误处理中。」
--
--   2026-11-24 模型归属审计（211 生产实测）暴露了一个结构性缺口：数据面
--   的异常（模型归因缺失、计费口径不一致、账本分叉、物化视图漂移）**没有
--   任何一条进得了错误处理手册** —— worker 的摄取源（SQLSource）只读
--   candidate_failure_logs（上游失败），数据面异常既不在手册里、也没有
--   探测器。运维只能靠人肉跑 SQL 发现，发现了也没有标准处置路径。
--
--   本迁移补两件事里的第一件：**手册条目**（人工操作面）。第二件
--   （探测器：DataQualitySource 接进 worker 摄取链）落在 Go 侧，随本迁移
--   同批提交 —— 两者缺一不可：只有条目没有探测器 ⇒ 永远不会命中；只有
--   探测器没有条目 ⇒ 全部走「未知错误分析」，每次烧一次 LLM 分析费。
--
-- ★ 条目设计原则（与 995 同纪律，全部 fail-closed）：
--   · 全部 plan_kind='prompt'、auto_execute=FALSE —— 数据维护条目的方案
--     是「标准处置路径」的分析指引（给 PI 智能体与运维同一份话术），不是
--     可自动落地的变更。数据修复类动作（UPDATE/回填/补账）触碰生产数据，
--     必须人工确认后才升级成 script/api。
--   · error_type 闭集前缀 'data_maintenance:' —— 与既有 errorsx kind
--     （auth/rate_limit/...）和 PI 队列类型命名域隔离，PickEntry 的类型级
--     匹配不会误伤；探测器只发这个前缀的事件。
--   · ON CONFLICT 面向 (error_type, COALESCE(match_pattern,'')) 表达式
--     唯一索引（995 Part 1 建）⇒ 重放幂等。
--
-- ★ 实测底稿（2026-11-24 211 生产只读盘点，全部数字可复跑）：
--   ① session_turns.model IN ('<unknown>','unknown')  ⇒ 1,332 行（全是失败行；
--      修后残留 + 未部署窗口的增量）；model IS NULL/'' ⇒ 170 行。
--   ② usage_ledger_hot.raw_model_name IN ('<unknown>','unknown') ⇒ 6 行。
--   ③ session_summaries：273,865 行里 133,099 行 models_used 为空、
--      7,066 行有 models_used 却缺 primary_model ⇒ 会话分析的人员/模型维度
--      出现「未知模型」桶。
--   ④ cost_usd>0 且 credits_charged=0 ⇒ 113,444 行（90,462 行
--      usage_source='llm'、22,918 行 'estimated'、64 行空）——需要运维判定
--      是「租户未启用计费（豁免）」还是「漏计费」。
--   ⑤ 983 断层：schema_migrations 有 983 行而部署通道账
--      gateway_db_revision_sequences 无其 marker（另有 983b/983c 同族）。
--   ⑥ 两处跨面视图列序漂移（392 candidate_failure_logs 族 / 344 usage_ledger
--      族）——列序门已产报告，处置仍是人工轴。
--
-- ★ 锁面：纯 INSERT（ON CONFLICT DO NOTHING），不建表不建索引不碰热表
--   DDL ⇒ 锁面为零。声明 5s lock_timeout / 10min statement_timeout
--   （与 995 同口径）。
--
-- ★ 幂等：种子 ON CONFLICT DO NOTHING + 台账 ON CONFLICT DO UPDATE
--   ⇒ 内容指纹重放零冲突。

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ============================================================================
-- Part 1: 数据维护手册条目（六条，全部 prompt 型 + 不自动执行）
-- ============================================================================

INSERT INTO public.error_playbook_entries
    (error_type, match_pattern, title, description, root_cause, plan_kind, plan_payload, confidence, source, enabled, auto_execute, created_by)
VALUES

    -- ① 模型归因缺失（本仓 2026-11-24 审计的主缺陷）
    ('data_maintenance:model_attribution_missing', NULL,
     '数据维护｜模型归因缺失：失败请求记成 <unknown>/unknown，模型榜出现幻影模型',
     'session_turns.model（= 客户端请求模型）为空或写成了占位符字面量。修后（写侧 large_ingress 不再硬编码、读侧统一归一链）新流量应归真模型；存量行不会自愈。后果：模型用量榜/人员模型维度出现名为 <unknown> 与 unknown 的两个"模型"，且这些行全部 success=false，拉低分母。',
     '写侧丢模型（已修：入口拒绝与早退出口不摘 model）＋ 存量行未回填。每条占位符行都对应一次"网关丢了客户端明确给过的信息"。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（模型归因缺失）：1) 分桶统计：SELECT model, error_kind, COUNT(*) FROM session_turns WHERE model IN (''<unknown>'',''unknown'') OR model IS NULL OR model='''' GROUP BY 1,2 —— 区分 <unknown> 字面量、unknown、空串三态与错误类型分布；2) 判增量还是存量：按 ts 看 MAX(ts) 是否晚于修复部署时刻 —— 晚于 ⇒ 写侧修法未全覆盖，带样本报缺陷给 PI 智能体分析新丢失点；只早于 ⇒ 存量，走第 3 步；3) 存量回填（人工确认后执行）：可归真的行（request_body/search_text 里带真模型名、或同 request_id 在 details 层有 client_model）按 details.client_model 回填；不可归真的行保持原样（不要编造模型名）；4) 回填后复核模型榜：SELECT model, SUM(request_count) FROM model_usage_aggregates GROUP BY 1 确认 <unknown>/unknown 两桶不再增长；5) 把本次分桶数字记进运维日志（下次对比是否复发）。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a'),

    -- ② 计费口径缺口（113k 行的判定轴在 owner 手里）
    ('data_maintenance:billing_credit_gap', NULL,
     '数据维护｜计费口径缺口：cost_usd>0 但 credits_charged=0，需判定豁免还是漏计',
     '有上游成本（cost_usd）却零积分扣减（credits_charged）的行。2026-11-24 实测 113,444 行（usage_source=llm 90,462 / estimated 22,918 / 空 64）。两种成因处置完全不同：租户未启用计费（豁免，正常）vs 计费链漏写（真实欠账）。不做判定 ⇒ 对账页面与 MaaS 用量对不上。',
     '租户计费开关未启用（豁免面）或计费写入链在特定路径上漏写 credits_charged（欠账面）。两条成因的分界线是租户的 maas/计费配置，不是行本身。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（计费口径缺口）：1) 按租户分桶：SELECT tenant_id, COUNT(*), SUM(cost_usd) FROM session_turns WHERE cost_usd>0 AND COALESCE(credits_charged,0)=0 GROUP BY 1 —— 单租户集中 ⇒ 大概率该租户未启用计费（豁免，去管理面核对配置即可闭案）；2) 跨租户散布 ⇒ 疑似漏计费：按 usage_source 分桶（llm=真实用量路径漏写最严重）与时间窗（是否集中在某次部署后 ⇒ 回归）；3) 漏计费确认后不要手工 UPDATE 积分（会破坏 credits 流水可追溯性）——把样本（request_id 清单、金额合计）交给 owner 决定是补账（走 credit_ledger 调整分录）还是接受豁免；4) 处置后在运维日志登记本次判定的分桶数字与结论，下次探测器再报时对比是否收敛。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a'),

    -- ③ 会话摘要维度缺失
    ('data_maintenance:session_summary_dim_missing', NULL,
     '数据维护｜会话摘要维度缺失：models_used 为空或缺 primary_model，人员/模型维度出现未知桶',
     'session_summaries 里 models_used 为空（2026-11-24 实测 133,099/273,865）或有 models_used 却缺 primary_model（7,066 行）。会话分析的人员维度与模型维度把这些会话归进 unknown 桶；跨窗会话（E 类）的归属判断也依赖这组维度。',
     '首值优先（first_request_at/primary_model）的 upsert 语义遇上「首轮请求发生在维度采集上线前」或「镜像桥接未写维度列」的历史会话。属于存量缺口，不是增量回归。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（会话摘要维度缺失）：1) 分形态统计：models_used 全空 vs 有 models_used 缺 primary_model（后者优先处置——数据在手只是没落主模型位）；2) 有 models_used 的行可安全回填：primary_model = models_used[1]（首值优先语义与 upsert 一致，人工确认后执行）；3) models_used 全空的行先抽样核对 session_turns 里同 session_id 的 model 列——turns 有值 ⇒ 回填链断了（镜像桥接缺陷，报给 PI 智能体分析）；turns 也无值 ⇒ 真无模型（旧数据），登记豁免；4) 回填后复核 agent_usage_aggregates / model_usage_aggregates 的 unknown 桶占比是否下降；5) 顺带核对 total_credits_charged 与 turns 的 credits_charged 聚合是否一致（会话统计与看板的桥接闭合）。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a'),

    -- ④ 迁移双账本分叉（L33-L35 系列的运维化）
    ('data_maintenance:migration_ledger_fork', NULL,
     '数据维护｜迁移双账本分叉：schema_migrations 有记录而部署通道账无 marker（或反之）',
     '安装器账（schema_migrations）与部署通道账（gateway_db_revision_sequences）对「哪些迁移已应用」口径不一致。已知实例：983/983b/983c 在 schema_migrations 有行而通道账无 marker ⇒ 通道下次跑会重放它们（重放安全是未证假设——280/392 条迁移不自记账）。列序漂移门已把「会在第几条炸」做成可执行报告。',
     '两条通道对同一批文件各有各的账、写入方不同（安装器 F1 补记 vs 通道按内容指纹），历史演进没有同步机制。分叉本身不立即损坏数据，但让「重放安全」变成赌注。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（迁移双账本分叉）：1) 跑只读覆盖门取最新读数（scripts/checks/deploy-ledger-coverage-gate.sh，只 SELECT 零写）——把缺口分成断层（版本在 schema_migrations ⇒ 会被重放）与待部署（不在 ⇒ 首次应用，正常）；2) 对每个断层核对重放安全性：迁移是否幂等（IF NOT EXISTS / ON CONFLICT）⇒ 幂等则记录待下次部署自愈（正常部署会写 marker）；不幂等 ⇒ owner 决定补 marker（生产写，需授权）还是跳过；3) 对照列序对拍门（legacy-select-star-column-order-gate.sh）确认断层迁移里有没有会在重放时炸的（392 类列序漂移）⇒ 有的话先修列序再允许部署；4) 全程只读——任何补账/补 marker 都是生产写，必须 owner 授权；5) 把断层清单记进运维日志，下次跑门对比是否收敛。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a'),

    -- ⑤ 跨面视图列序漂移
    ('data_maintenance:select_star_column_drift', NULL,
     '数据维护｜跨面视图列序漂移：SELECT * UNION ALL 两侧列序错位，重放必炸',
     '用 SELECT * 做集合运算的两侧（hot 表与父表）被后续 ALTER TABLE ADD COLUMN 各自追加、追加顺序不同 ⇒ 列序错位。已知实例：candidate_failure_logs 族（392，第 17 位 bigint↔text）与 usage_ledger 族（344，第 20 位 integer↔double precision），211 与 252 都漂、错位位置不同。视图本体仍可用（列清单创建时冻结），但相关迁移一旦被重放（内容加固）必炸 UNION 类型不匹配。',
     '静态门只禁新建跨面 SELECT *（hot_surface_select_star_contract_test），管不了已有对象今天漂没漂。运行时对拍由列序门（legacy-select-star-column-order-gate.sh）承担。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（跨面视图列序漂移）：1) 跑列序对拍门取当前漂移清单（只读）；2) 对每个漂移对判断触发条件：该迁移在部署通道投递面里吗？本次部署会重放它吗（指纹一致⇒跳过⇒潜伏；不一致⇒会炸）⇒ 用覆盖门回答；3) 潜伏态 ⇒ 记录待收口（下次内容加固前必须处理）；会炸态 ⇒ 在部署前先对齐列序（两侧按同序 ALTER 或改迁移为逐列枚举——后者改已 applied 迁移字节，需 owner 授权与 checksum 同步）；4) 不需要现在动生产（视图本体可用）——处置目标是「下次部署不在第 N 条炸」，不是「立刻修表」；5) 处置后复跑门确认漂移清单收敛，记进运维日志。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a'),

    -- ⑥ 大请求归档残片（可自动回收但默认人工确认）
    ('data_maintenance:archive_staging_fragments', NULL,
     '数据维护｜归档暂存残片：.raw-* 临时文件堆积，占用归档盘且含未完成请求体',
     'largebody 归档的中断上传留下的 .raw- 前缀临时文件（2026-11-23 实测 660 个 / 29.8 MiB；2026-11-24 复测已收敛）。残片含客户端原始请求体（可能含敏感内容），长期堆积既占盘又是数据保留面的隐患。',
     '上传中断（客户端断连/处理超时）后 Capture 的 defer 清理没跑到（进程退出/panic 路径），或清理只覆盖部分失败分支。',
     'prompt',
     '{"kind":"prompt","expect":"analysis","prompt":"标准处置（归档暂存残片）：1) 统计残片：find <归档根>/raw -name ''.raw-*'' | wc -l 与 du -sh —— 对照运维日志里的上次数字判断是增长还是收敛；2) 增长 ⇒ 先查最近是否有进程异常退出（容器重启/OOM）⇒ 残片是重启窗口的正常副产品，量级稳定则可接受；持续增长 ⇒ Capture 清理路径有真缺陷，带样本（文件名时间分布）报给 PI 智能体分析；3) 清理（人工确认后执行）：只删 mtime 超过保留期（默认 7 天，与 anomalies retention 对齐）的 .raw-* 文件——正在写入的文件不能碰；4) 清理后复核归档盘 free 空间回到阈值以上；5) 记录本次清理的文件数与字节数进运维日志。"}'::jsonb,
     1.00, 'seed', TRUE, FALSE, 'seed:999a')

ON CONFLICT (error_type, COALESCE(match_pattern, '')) DO NOTHING;

-- ============================================================================
-- Part 2: 台账自登记（≤952 纪律：迁移必须自己把行写进 public.schema_migrations）
-- ============================================================================

INSERT INTO public.schema_migrations (version, description)
VALUES ('999a', 'data_maintenance playbook: 运维/数据维护手册条目六条（模型归因/计费缺口/会话维度/账本分叉/列序漂移/归档残片），全 prompt 型 fail-closed')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
