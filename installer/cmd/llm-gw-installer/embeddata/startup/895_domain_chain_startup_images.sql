-- ===========================================================================
-- File:          sql/migrations/startup/895_domain_chain_startup_images.sql
-- Migration:     895
-- Database:      llm_gateway
-- Purpose:       给 **11 张 domain 链的表**补 **startup 通道镜像**。
--
--                生产 252（共享库）/ 211 上这 11 张表**全部不存在**，
--                而它们**都有 Go 生产读写点** ⇒ 相关功能在生产上因缺表而失败
--                （42P01 relation does not exist）。
--
-- ── 根因 ────────────────────────────────────────────────────────────────
--
--   这 11 张表由 `sql/migrations/domain/` 下的迁移创建。
--   而 **domain 链从来没有在生产上跑过**：
--     · 生产只跑 **startup 通道**（`installer/internal/dbinit/runner.go`
--       的 `Runner.StartupFiles` + `scripts/apply-db-revision-sequence.sh`
--       的 `files=()`）与 installer 的 00-prereqs/01-schema/02-seed；
--     · **domain 通道两者都不含** ⇒ 生产 252 的 `schema_migrations` 里
--       domain 链的号**一个都没有** ⇒ domain 建的那些表**一个都不在**。
--
--   ⇒ 本条把 domain 链里这 11 张表**按原形态**镜像到 startup 通道。
--     这是**补通道**，不是**改语义**：列/类型/默认值/NOT NULL/CHECK/外键/
--     唯一约束/主键/索引/注释/序列（identity 或 SERIAL）**逐条照抄**
--     域链源文件，不简化、不只建列。
--
-- ── 逐张结论（★ 本轮要查清的核心事实）────────────────────────────────
--
--   核对口径：对 11 张表逐张在**基线** `sql/schema/01-schema.sql` 与
--   **全 `sql/` 树**（startup/ manual/ objects/ domain/）里找
--   `CREATE TABLE [IF NOT EXISTS] [public.]<table>`。
--
--   ┌──────────────────────────────┬──────────────────────────────────┬──────────────┐
--   │ 表                            │ 域链源文件                        │ baseline    │
--   ├──────────────────────────────┼──────────────────────────────────┼──────────────┤
--   │ client_profiles               │ domain/132_client_profile.sql     │ **未建**     │
--   │ client_behavior_events        │ domain/132_client_profile.sql     │ **未建**     │
--   │ credential_state_nodes        │ domain/035_credential_state_...sql │ **未建**     │
--   │ feishu_bot_routing_rules      │ domain/220_feishu_bot_routing.sql │ **未建**     │
--   │ feishu_bot_send_log           │ domain/220_feishu_bot_routing.sql │ **未建**     │
--   │ notification_channels         │ domain/135_approval_routing.sql   │ **未建**     │
--   │ provider_incidents            │ domain/133_provider_reputation.sql│ **未建**     │
--   │ provider_reputation_timeseries│ domain/133_provider_reputation.sql│ **未建**     │
--   │ session_credential_rotations  │ domain/331_session_state_and_...sql│ **未建**    │
--   │ session_state_snapshots       │ domain/331_session_state_and_...sql│ **未建**    │
--   │ tool_executions               │ domain/134_tool_execution.sql     │ **未建**     │
--   └──────────────────────────────┴──────────────────────────────────┴──────────────┘
--
--   ★★ **结论：11 张表**全部**要由本条新建**（0 张被 baseline 覆盖）。
--     （基线里的 `client_profiles text[]` / `notification_channels jsonb`
--     是**别的表上的列名**，不是表本身 —— 逐条 grep 确认。）
--
--   ⇒ ★★ **但域链源文件里的「兄弟表」不能一起镜像**，逐张核对如下：
--     · `tool_usage_stats`        → **baseline 已建**（01-schema.sql:17738）
--                                  ⇒ **不镜像**（否则与 baseline 抢同名对象）
--     · `approval_routing_rules`  → **baseline 已建**（01-schema.sql:5550）
--                                  ⇒ **不镜像**
--     · `credential_state_log`    → **baseline 已建**（01-schema.sql:6774）
--                                  ⇒ **不镜像**
--     · `notification_send_log`   → baseline 未建，但**全仓 Go 无任何
--                                  读写点**（本轮逐文件核对）⇒ 不在 11 张
--                                  表的口径内 ⇒ **不镜像**
--     ★ 即：**同一批域链源文件里，只有 11 张表在生产上缺失**，
--       其余三张早已被 baseline 吸收。
--
-- ── 依赖：本条一并镜像的对象 ─────────────────────────────────────────
--
--   ① **6 个触发器函数**（域链源文件里 `CREATE OR REPLACE FUNCTION` 建的）：
--        update_client_profile_updated_at      ← domain/132
--        update_feishu_route_updated_at        ← domain/220
--        update_notification_channels_updated_at ← domain/135
--        update_provider_reputation_updated_at ← domain/133
--        calculate_incident_duration           ← domain/133
--        calculate_tool_execution_duration     ← domain/134
--      ★ 实测（基线 01-schema.sql 逐个 grep）：这 6 个**基线一个都没有**
--        （命中数 0/0/0/0/0/0）⇒ 若不镜像，触发器建不出来 ⇒ 必须一并镜像。
--      ★ `update_tool_usage_stats_updated_at`（domain/134）**不镜像**
--        —— 它服务的 `tool_usage_stats` 已由 baseline 建，基线也没有该函数；
--        本条不碰那个族（见上「兄弟表」一节）。
--
--   ② **7 个触发器**（域链源文件里 `CREATE TRIGGER` 建的）：
--        trg_client_profiles_updated_at      ← domain/132
--        trg_feishu_route_updated_at        ← domain/220
--        trg_notification_channels_updated_at← domain/135
--        trg_provider_rep_ts_updated_at     ← domain/133
--        trg_provider_incidents_updated_at  ← domain/133
--        trg_provider_incidents_duration     ← domain/133
--        trg_tool_executions_duration        ← domain/134
--      ★ 实测（基线逐个 grep）：这 7 个**基线一个都没有**（命中数全 0）。
--      ★ **形态差异（有意）**：PostgreSQL **没有** `CREATE TRIGGER IF NOT EXISTS`
--        ⇒ 域链源文件里 132/133/134 的裸 `CREATE TRIGGER` **不幂等**
--        （第二次跑报 42710 duplicate_trigger）。
--        ⇒ 本条统一改成 **DROP TRIGGER IF EXISTS + CREATE TRIGGER**
--        —— 这是 220/135 **域链自己已经用的形态**（它们就是先 DROP 再 CREATE），
--          语义等价，只为让整条迁移可重放。
--
--   ③ **外键目标** `public.credentials(id)`（`credential_state_nodes` 的
--      `FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE CASCADE`）：
--      ★ `credentials` 由 **baseline** 建（01-schema.sql:6820）⇒ 生产已存在
--        ⇒ 外键可建，无需镜像 `credentials` 本身。
--      ⇒ 域链 035 里那条从 `credential_model_bindings` / `provider_models`
--        **回填** `credential_state_nodes` 的 INSERT（源文件第 63~77 行）
--        是**数据迁移**不是 DDL ⇒ **不镜像**（生产上这 11 张表本来就不存在，
--        没有可回填的历史；且回填会依赖 `credentials.lifecycle_status`
--        与 `provider_models` 的既有数据，超出本条范围）。
--
--   ④ **序列 / identity**：`SERIAL` / `BIGSERIAL` 的隐式序列与
--      `GENERATED ALWAYS AS IDENTITY` 的 identity 序列**由 CREATE TABLE 一并建**，
--      无需单独镜像（也不应单独 CREATE —— 那会造成序列与列脱钩）。
--
--   ★ **不依赖任何本通道其它迁移**：11 张表全部由本条一次性建出，
--     依赖对象（credentials / 基线）**在 baseline 之后就已经存在**
--     ⇒ **本条可以放在 startup 链的任意位置**（本条登记在链尾）。
--
-- ── 幂等性 ─────────────────────────────────────────────────────────────
--
--   · 建表：`CREATE TABLE IF NOT EXISTS`（11/11）
--   · 建索引：`CREATE INDEX IF NOT EXISTS`（**注意**：域链 035 的两条
--     `idx_csn_credential_model` / `idx_csn_active_probe` **原本没有**
--     `IF NOT EXISTS` ⇒ 本条补上，否则第二次跑报 42P07）
--   · 建函数：`CREATE OR REPLACE FUNCTION`（6/6）
--   · 建触发器：`DROP TRIGGER IF EXISTS` + `CREATE TRIGGER`（7/7）
--   · 注释：`COMMENT ON ...` 天然幂等
--   ⇒ **整条迁移可重复执行，无副作用**（第二次跑全部命中「已存在」分支）。
--
-- ── ⚠ 阻塞写的影响（属主已知悉）────────────────────────────────────────
--
--   ★ 本条**不使用 PostgreSQL 的「并发建索引」选项**（那个让索引构建
--     不阻塞写入的修饰词）—— startup 迁移默认在事务里跑，而该选项在
--     **事务块内直接报错**，除非文件声明那条「逐文件事务豁免」标记
--     （标记的字面量见 installer/internal/dbinit/runner.go 的
--      noTransactionMarker 常量 —— ★ **本文件刻意不写它**：
--      那个检测器是**按注释行**扫字面量的 ⇒ 哪怕只是在注释里解释它，
--      也会让 installer 误判本文件「声明了豁免」⇒ applySQL 就不再加
--      `--single-transaction` ⇒ ★★ **本文件的执行语义被静默改掉**）。
--     而那条豁免需要额外的门与理由 —— 本条没有不可事务化的语句，
--     **不需要**它。
--   ⇒ ⇒ 建索引用的是**普通 `CREATE INDEX IF NOT EXISTS`**，
--     它拿 **ShareLock** ⇒ **建索引期间阻塞该表的写入**。
--   ⇒ ⇒ 且 11 张表在生产上是**新建的空表** ⇒ 索引建成时表是空的
--     ⇒ 实际阻塞窗口极短（毫秒~秒级）⇒ 风险可接受。
--   ⇒ ⇒ 建表本身拿 **AccessExclusiveLock**，但对象是**全新命名**的
--     ⇒ 不与任何既有表争锁 ⇒ 不会像 §44 那次一样堵住全库。
--   ★★ 本文件的**全文**（含注释）都不含那个「并发建索引」选项的字面量
--     ⇒ 判据可以做成机械检查，不必区分「注释里提到」与「真的用了」。
--   ★ 与之配套：`SET LOCAL lock_timeout = '60s'` ⇒ 命中即失败（55P03）
--     而不是无限期等锁堵住全库（**宁可让迁移失败，也不要让整库被卡住**）。
--
-- ── 超时预算 ───────────────────────────────────────────────────────────
--
--   本目录既定约定：共享 PG 服务端强制 **30s statement_timeout**
--   （见 `sql/migrations/startup/855_session_analytics_time_window.sql`
--   的文件头：2026-10-13 在 245 实测，超时点就是第一条 CREATE MATERIALIZED
--   VIEW）⇒ **长耗时迁移必须自带 `SET LOCAL statement_timeout`**。
--   本条全部是「建空表 + 建空索引」的 DDL ⇒ 秒级 ⇒ 300s 有三个数量级余量。
--
-- ── 词法自证（部署通道 runtime adapter）────────────────────────────────
--
--   ⚠ 部署通道 `scripts/apply-db-revision-sequence.sh` 的 runtime adapter
--     （`request-logs-physical-v1` 世代探测）会对**整份文件文本**（含注释）
--     做**词法替换** ⇒ 命中即改写执行体。
--   ★★ **本文件不含那个族名的任何连续词法 token** ⇒ 适配器对本文件
--     **零改写** ⇒ 执行体与源逐字相同。
--     （机器可读断言由 `scripts/ursmcheck/section_14360_domain_chain_startup_images_test.go`
--      钉住：本文件做「连续词法 token」扫描，命中即红。）
--   ⇒ 本文件里的 `request_id` **列名**（client_behavior_events / tool_executions
--     各一列）与本族名**无关**，不会被适配器命中。
--
-- ── 回滚 ───────────────────────────────────────────────────────────────
--
--   `895_domain_chain_startup_images.down.sql`（同名回滚）：
--   删 11 张表 + 6 个触发器函数。
--   ⚠ **回滚会销毁这 11 张表里的数据** ⇒ 回滚前请确认这些数据可丢
--     （或先自行导出）。在生产上，这 11 张表从未存在过 ⇒ 若本条尚未在
--     生产应用过，down 是**完全无损**的。
-- ===========================================================================

BEGIN;

-- 本仓 870+ 迁移的通行值：命中即失败（55P03）而不是继续堵库。
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '300s';

-- ===========================================================================
-- ① domain/132_client_profile.sql —— client_profiles / client_behavior_events
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.client_profiles (
    identity_hash VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    virtual_client_id VARCHAR(32) NOT NULL,

    -- 统计数据
    total_sessions BIGINT DEFAULT 0 NOT NULL,
    total_requests BIGINT DEFAULT 0 NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,

    -- 行为特征（JSONB存储复杂结构）
    preferred_models JSONB DEFAULT '[]'::jsonb,           -- [{"model": "gpt-4", "usage_count": 100, "success_rate": 0.95, "avg_latency_ms": 1200}]
    task_distribution JSONB DEFAULT '{}'::jsonb,          -- {"code": 50, "chat": 30, "reasoning": 20}
    avg_session_length FLOAT DEFAULT 0 NOT NULL,          -- 平均会话轮次
    avg_tokens_per_turn FLOAT DEFAULT 0 NOT NULL,         -- 平均每轮Token数

    -- 质量指标
    error_rate FLOAT DEFAULT 0 NOT NULL,                  -- 错误率 (0-1)
    approval_rate FLOAT DEFAULT 0 NOT NULL,               -- 高风险审批占比 (0-1)

    -- 时间模式
    active_hours JSONB DEFAULT '[]'::jsonb,               -- [10, 14, 15, 16] (UTC小时，0-23)
    peak_usage_day INT,                                   -- 0=Sunday, 6=Saturday

    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_client_profiles_tenant ON public.client_profiles(tenant_id);
CREATE INDEX IF NOT EXISTS idx_client_profiles_last_seen ON public.client_profiles(last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_client_profiles_tenant_last_seen ON public.client_profiles(tenant_id, last_seen_at DESC);

-- 客户端行为事件表（时序数据，用于分析）
CREATE TABLE IF NOT EXISTS public.client_behavior_events (
    event_id VARCHAR(64) PRIMARY KEY,
    identity_hash VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    session_id VARCHAR(64),
    request_id VARCHAR(64),

    -- 事件详情
    event_type VARCHAR(32) NOT NULL,                     -- session_start, request_completed, approval_required, error
    model VARCHAR(128),
    task_type VARCHAR(32),                               -- code, chat, reasoning, unknown

    -- 指标
    tokens_used INT,
    latency_ms BIGINT,
    success BOOLEAN DEFAULT true,

    -- 时间戳
    event_time TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引（时序查询优化）
CREATE INDEX IF NOT EXISTS idx_client_behavior_identity_time ON public.client_behavior_events(identity_hash, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_tenant_time ON public.client_behavior_events(tenant_id, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_event_type ON public.client_behavior_events(event_type, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_client_behavior_session ON public.client_behavior_events(session_id);

-- 自动更新 updated_at 触发器
CREATE OR REPLACE FUNCTION public.update_client_profile_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_client_profiles_updated_at ON public.client_profiles;
CREATE TRIGGER trg_client_profiles_updated_at
    BEFORE UPDATE ON public.client_profiles
    FOR EACH ROW
    EXECUTE FUNCTION public.update_client_profile_updated_at();

-- 注释
COMMENT ON TABLE public.client_profiles IS '客户端画像主表，跨会话聚合客户端行为特征';
COMMENT ON TABLE public.client_behavior_events IS '客户端行为事件时序表，用于画像分析和趋势追踪';
COMMENT ON COLUMN public.client_profiles.identity_hash IS '客户端稳定身份哈希（来自identity.ClientIdentity）';
COMMENT ON COLUMN public.client_profiles.preferred_models IS 'JSON数组，按使用频次排序的模型偏好';
COMMENT ON COLUMN public.client_profiles.task_distribution IS 'JSON对象，任务类型分布统计';
COMMENT ON COLUMN public.client_profiles.active_hours IS 'JSON数组，活跃时段（UTC小时0-23）';

-- ===========================================================================
-- ② domain/035_credential_state_management.sql —— credential_state_nodes
--
-- ★ 同文件里的 credential_state_log 已由 baseline 建（01-schema.sql:6774）
--   ⇒ 本条**不镜像**它。
-- ★ 同文件末尾那条从 credential_model_bindings / provider_models 回填
--   credential_state_nodes 的 INSERT 是**数据迁移**⇒ 本条**不镜像**。
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.credential_state_nodes (
    id SERIAL PRIMARY KEY,
    credential_id INT NOT NULL,
    raw_model_name TEXT NOT NULL,

    -- 节点状态
    node_status TEXT NOT NULL DEFAULT 'active', -- active, disabled

    -- 监控配置
    probe_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    probe_interval_seconds INT NOT NULL DEFAULT 3600,
    last_probe_at TIMESTAMPTZ,
    next_probe_at TIMESTAMPTZ,

    -- 创建/更新信息
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by TEXT, -- 'system' or 'admin:<user_id>'
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ,
    disabled_by TEXT,

    -- 约束
    UNIQUE (credential_id, raw_model_name),
    FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE CASCADE
);

-- ★ 域链源文件这两条索引**没有** IF NOT EXISTS ⇒ 本条补上（幂等）。
CREATE INDEX IF NOT EXISTS idx_csn_credential_model ON public.credential_state_nodes(credential_id, raw_model_name);
CREATE INDEX IF NOT EXISTS idx_csn_active_probe ON public.credential_state_nodes(next_probe_at)
    WHERE node_status = 'active' AND probe_enabled = TRUE;

COMMENT ON TABLE public.credential_state_nodes IS '凭据+模型状态监控节点注册表';
COMMENT ON COLUMN public.credential_state_nodes.node_status IS '节点状态：active=活跃监控, disabled=已禁用';
COMMENT ON COLUMN public.credential_state_nodes.created_by IS '创建者：system 或 admin:<user_id>';

-- ===========================================================================
-- ③ domain/220_feishu_bot_routing.sql
--    —— feishu_bot_routing_rules / feishu_bot_send_log
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.feishu_bot_routing_rules (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    open_id VARCHAR(128) NOT NULL,
    display_name VARCHAR(128),
    user_role VARCHAR(16) NOT NULL DEFAULT 'member',  -- admin | member | auditor
    risk_levels JSONB NOT NULL DEFAULT '["low","medium","high","critical"]'::jsonb,
    priority INT NOT NULL DEFAULT 100,
    enabled BOOLEAN NOT NULL DEFAULT true,
    note TEXT,
    created_by VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_feishu_route_openid UNIQUE (tenant_id, open_id),
    CONSTRAINT chk_feishu_route_role CHECK (user_role IN ('admin', 'member', 'auditor'))
);

CREATE INDEX IF NOT EXISTS idx_feishu_route_tenant_enabled
    ON public.feishu_bot_routing_rules(tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_feishu_route_priority
    ON public.feishu_bot_routing_rules(tenant_id, priority) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_feishu_route_updated_at
    ON public.feishu_bot_routing_rules(updated_at DESC);

-- 自动更新 updated_at
CREATE OR REPLACE FUNCTION public.update_feishu_route_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_feishu_route_updated_at ON public.feishu_bot_routing_rules;
CREATE TRIGGER trg_feishu_route_updated_at
    BEFORE UPDATE ON public.feishu_bot_routing_rules
    FOR EACH ROW
    EXECUTE FUNCTION public.update_feishu_route_updated_at();


-- 飞书机器人发送审计表（轻量）
-- 用于追踪每条告警/审批消息的发送状态
CREATE TABLE IF NOT EXISTS public.feishu_bot_send_log (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    event_type VARCHAR(32) NOT NULL,                    -- alert / approval / command
    event_id VARCHAR(64),                              -- approval_id / alert_fingerprint
    recipients_count INT NOT NULL DEFAULT 0,
    success BOOLEAN NOT NULL,
    error_code INT,
    error_message TEXT,
    latency_ms INT,
    deduped BOOLEAN NOT NULL DEFAULT false,            -- 是否被去重拦截
    rate_limited BOOLEAN NOT NULL DEFAULT false,       -- 是否被限流
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_feishu_send_log_tenant_created
    ON public.feishu_bot_send_log(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feishu_send_log_event
    ON public.feishu_bot_send_log(event_id, created_at DESC) WHERE event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_feishu_send_log_failure
    ON public.feishu_bot_send_log(created_at DESC) WHERE success = false;


COMMENT ON TABLE public.feishu_bot_routing_rules IS '飞书机器人路由规则：按 (tenant_id, risk_levels) 映射飞书 OpenID 白名单';
COMMENT ON COLUMN public.feishu_bot_routing_rules.open_id IS '飞书 OpenID，机器人发送消息的目标用户';
COMMENT ON COLUMN public.feishu_bot_routing_rules.user_role IS 'admin: 可执行命令；member: 普通接收人；auditor: 审计人';
COMMENT ON COLUMN public.feishu_bot_routing_rules.risk_levels IS '适用的风险级别列表，空 = 不参与匹配';
COMMENT ON COLUMN public.feishu_bot_routing_rules.priority IS '数字越小优先级越高，同 (tenant, risk) 多个匹配时按升序拼接';

COMMENT ON TABLE public.feishu_bot_send_log IS '飞书机器人发送审计表：记录每次发送的成功/失败/去重/限流状态';
COMMENT ON COLUMN public.feishu_bot_send_log.deduped IS 'true = 被 dedup 拦截，未实际发送';
COMMENT ON COLUMN public.feishu_bot_send_log.rate_limited IS 'true = 被 rate limit 拦截，未实际发送';

-- ===========================================================================
-- ④ domain/135_approval_routing.sql —— notification_channels
--
-- ★ 同文件里的 approval_routing_rules 已由 baseline 建（01-schema.sql:5550）
--   ⇒ 本条**不镜像**它。
-- ★ 同文件里的 notification_send_log：baseline 未建，但全仓 Go 无任何
--   读写点 ⇒ 不在本条口径内 ⇒ **不镜像**。
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.notification_channels (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    channel_type VARCHAR(16) NOT NULL,                    -- lark/dingtalk/wechat
    config JSONB NOT NULL DEFAULT '{}'::jsonb,            -- {"app_id":"xxx","app_secret":"xxx","webhook_url":"xxx",...}
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_notification_channel UNIQUE (tenant_id, channel_type),
    CONSTRAINT chk_notification_channel CHECK (channel_type IN ('lark','dingtalk','wechat'))
);

CREATE INDEX IF NOT EXISTS idx_notification_channels_tenant_enabled
    ON public.notification_channels(tenant_id, enabled);
CREATE INDEX IF NOT EXISTS idx_notification_channels_updated_at
    ON public.notification_channels(updated_at DESC);

CREATE OR REPLACE FUNCTION public.update_notification_channels_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_notification_channels_updated_at ON public.notification_channels;
CREATE TRIGGER trg_notification_channels_updated_at
    BEFORE UPDATE ON public.notification_channels
    FOR EACH ROW
    EXECUTE FUNCTION public.update_notification_channels_updated_at();

COMMENT ON TABLE public.notification_channels IS '通知渠道凭证配置，config JSONB 包含 app_secret 等敏感字段';

-- ===========================================================================
-- ⑤ domain/133_provider_reputation.sql
--    —— provider_reputation_timeseries / provider_incidents
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.provider_reputation_timeseries (
    id BIGSERIAL PRIMARY KEY,
    provider_id VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    date DATE NOT NULL,

    -- 每日指标
    reliability_score FLOAT,                              -- 可靠性评分 (0-1)
    avg_latency_ms FLOAT,                                 -- 平均延迟（毫秒）
    error_rate FLOAT,                                     -- 错误率 (0-1)
    request_count BIGINT DEFAULT 0,                       -- 请求总数
    success_count BIGINT DEFAULT 0,                       -- 成功请求数

    -- 来自 credential bandit scoring
    bandit_alpha FLOAT,                                   -- Thompson Sampling alpha参数
    bandit_beta FLOAT,                                    -- Thompson Sampling beta参数
    success_rate FLOAT,                                   -- 成功率 (0-1)

    -- 错误分类
    rate_limit_errors INT DEFAULT 0,
    quota_errors INT DEFAULT 0,
    auth_errors INT DEFAULT 0,
    timeout_errors INT DEFAULT 0,
    network_errors INT DEFAULT 0,
    other_errors INT DEFAULT 0,

    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(provider_id, model, date)
);

-- 索引（时序查询优化）
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_provider_model_date ON public.provider_reputation_timeseries(provider_id, model, date DESC);
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_date ON public.provider_reputation_timeseries(date DESC);
CREATE INDEX IF NOT EXISTS idx_provider_rep_ts_model ON public.provider_reputation_timeseries(model, date DESC);

-- 供应商事件表（事故追踪）
CREATE TABLE IF NOT EXISTS public.provider_incidents (
    id BIGSERIAL PRIMARY KEY,
    provider_id VARCHAR(64) NOT NULL,
    model VARCHAR(128),                                   -- NULL表示影响所有模型

    -- 事件详情
    incident_type VARCHAR(32) NOT NULL,                  -- rate_limit_spike, outage, auth_failure, degraded_performance
    impact_level VARCHAR(16) NOT NULL,                   -- low, medium, high, critical
    description TEXT,

    -- 时间范围
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    duration_seconds INT,                                 -- 自动计算或手动设置

    -- 影响统计
    affected_requests BIGINT,
    affected_tenants INT,

    -- 状态
    resolved BOOLEAN DEFAULT FALSE,
    resolution_notes TEXT,

    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_provider_incidents_provider ON public.provider_incidents(provider_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_model ON public.provider_incidents(model, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_type ON public.provider_incidents(incident_type, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_incidents_unresolved ON public.provider_incidents(resolved, started_at DESC) WHERE resolved = FALSE;

-- 自动更新 updated_at 触发器
CREATE OR REPLACE FUNCTION public.update_provider_reputation_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_provider_rep_ts_updated_at ON public.provider_reputation_timeseries;
CREATE TRIGGER trg_provider_rep_ts_updated_at
    BEFORE UPDATE ON public.provider_reputation_timeseries
    FOR EACH ROW
    EXECUTE FUNCTION public.update_provider_reputation_updated_at();

DROP TRIGGER IF EXISTS trg_provider_incidents_updated_at ON public.provider_incidents;
CREATE TRIGGER trg_provider_incidents_updated_at
    BEFORE UPDATE ON public.provider_incidents
    FOR EACH ROW
    EXECUTE FUNCTION public.update_provider_reputation_updated_at();

-- 自动计算事件持续时间
CREATE OR REPLACE FUNCTION public.calculate_incident_duration()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.ended_at IS NOT NULL AND NEW.started_at IS NOT NULL THEN
        NEW.duration_seconds = EXTRACT(EPOCH FROM (NEW.ended_at - NEW.started_at))::INT;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_provider_incidents_duration ON public.provider_incidents;
CREATE TRIGGER trg_provider_incidents_duration
    BEFORE INSERT OR UPDATE ON public.provider_incidents
    FOR EACH ROW
    EXECUTE FUNCTION public.calculate_incident_duration();

-- 注释
COMMENT ON TABLE public.provider_reputation_timeseries IS 'LLM供应商信誉时序表，每日指标聚合';
COMMENT ON TABLE public.provider_incidents IS '供应商事件追踪表，记录故障、降级等事件';
COMMENT ON COLUMN public.provider_reputation_timeseries.bandit_alpha IS 'Thompson Sampling Beta分布参数alpha（成功数+1）';
COMMENT ON COLUMN public.provider_reputation_timeseries.bandit_beta IS 'Thompson Sampling Beta分布参数beta（失败数+1）';
COMMENT ON COLUMN public.provider_incidents.incident_type IS '事件类型：rate_limit_spike, outage, auth_failure, degraded_performance';
COMMENT ON COLUMN public.provider_incidents.impact_level IS '影响级别：low, medium, high, critical';

-- ===========================================================================
-- ⑥ domain/331_session_state_and_rotations.sql
--    —— session_state_snapshots / session_credential_rotations
-- ===========================================================================

-- ──── session_state_snapshots: 会话状态快照（停止时写入） ────
CREATE TABLE IF NOT EXISTS public.session_state_snapshots (
    id                      bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id              text         NOT NULL UNIQUE,
    tenant_id               text         NOT NULL,
    api_key_id              bigint       NOT NULL,
    task_id                 text,
    client_ip               text,
    client_fp               text,
    client_profile          text,
    status                  text         NOT NULL DEFAULT 'active',
    created_at              timestamptz  NOT NULL,
    first_request_at        timestamptz,
    last_request_at         timestamptz,
    stopped_at              timestamptz,
    stop_reason             text,
    recovered_at            timestamptz,
    final_credential_id     bigint,
    final_model             text,
    final_provider          text,
    total_turns             integer      NOT NULL DEFAULT 0,
    total_duration_sec      integer      NOT NULL DEFAULT 0,
    total_prompt_tokens     bigint       NOT NULL DEFAULT 0,
    total_completion_tokens bigint       NOT NULL DEFAULT 0,
    total_cost_usd          numeric(14,8) NOT NULL DEFAULT 0,
    title                   text,
    summary                 text,
    annotation              text,
    tags                    text[]       NOT NULL DEFAULT '{}',
    fp_slot_index           integer,
    raw_snapshot            jsonb,
    created_at_db           timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sss_tenant ON public.session_state_snapshots (tenant_id, stopped_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_sss_api_key ON public.session_state_snapshots (api_key_id, stopped_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_sss_status ON public.session_state_snapshots (status) WHERE status IN ('active', 'stopped');
CREATE INDEX IF NOT EXISTS idx_sss_credential ON public.session_state_snapshots (final_credential_id, stopped_at DESC);

COMMENT ON TABLE public.session_state_snapshots IS '会话状态快照，会话停止或结束时写入，用于审计与历史查询';

-- ──── session_credential_rotations: 凭据轮换审计 ────
CREATE TABLE IF NOT EXISTS public.session_credential_rotations (
    id                  bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id          text         NOT NULL,
    tenant_id           text         NOT NULL,
    seq                 integer      NOT NULL,
    credential_id       bigint       NOT NULL,
    credential_label    text,
    model               text,
    provider            text,
    started_at          timestamptz  NOT NULL,
    ended_at            timestamptz,
    turns               integer      NOT NULL DEFAULT 0,
    duration_sec        integer      NOT NULL DEFAULT 0,
    prompt_tokens       bigint       NOT NULL DEFAULT 0,
    completion_tokens   bigint       NOT NULL DEFAULT 0,
    cost_usd            numeric(14,8) NOT NULL DEFAULT 0,
    switch_reason       text         NOT NULL DEFAULT 'initial',
    fp_slot_index       integer,
    created_at          timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT session_credential_rotations_session_seq UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_scr_session ON public.session_credential_rotations (session_id, seq);
CREATE INDEX IF NOT EXISTS idx_scr_credential ON public.session_credential_rotations (credential_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_scr_tenant ON public.session_credential_rotations (tenant_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_scr_model ON public.session_credential_rotations (model, started_at DESC);

COMMENT ON TABLE public.session_credential_rotations IS '凭据轮换审计，记录会话内每次凭据切换的持续时间、轮次、token、费用';

-- ===========================================================================
-- ⑦ domain/134_tool_execution.sql —— tool_executions
--
-- ★ 同文件里的 tool_usage_stats 已由 baseline 建（01-schema.sql:17738）
--   ⇒ 本条**不镜像**它（也不镜像它的 updated_at 触发器函数）。
-- ===========================================================================

CREATE TABLE IF NOT EXISTS public.tool_executions (
    execution_id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,

    -- 工具信息
    tool_name VARCHAR(128) NOT NULL,
    tool_call_id VARCHAR(128),                           -- OpenAI: tool_call_id, Anthropic: tool_use.id
    arguments JSONB,                                     -- 工具调用参数
    result JSONB,                                        -- 工具执行结果

    -- 执行状态
    status VARCHAR(16) NOT NULL,                         -- pending, success, error, timeout
    error_message TEXT,
    error_type VARCHAR(32),                              -- network_error, timeout, invalid_args, execution_failed

    -- 时间统计
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    duration_ms BIGINT,

    -- 关联信息
    identity_hash VARCHAR(64),                           -- 客户端标识
    model VARCHAR(128),                                  -- 使用的模型

    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引（多维度查询优化）
CREATE INDEX IF NOT EXISTS idx_tool_exec_session ON public.tool_executions(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_request ON public.tool_executions(request_id);
CREATE INDEX IF NOT EXISTS idx_tool_exec_tenant ON public.tool_executions(tenant_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_identity ON public.tool_executions(identity_hash, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_tool_name ON public.tool_executions(tool_name, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_status ON public.tool_executions(status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_exec_started_at ON public.tool_executions(started_at DESC);

-- 自动计算执行时长
CREATE OR REPLACE FUNCTION public.calculate_tool_execution_duration()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.completed_at IS NOT NULL AND NEW.started_at IS NOT NULL THEN
        NEW.duration_ms = EXTRACT(EPOCH FROM (NEW.completed_at - NEW.started_at))::BIGINT * 1000;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_tool_executions_duration ON public.tool_executions;
CREATE TRIGGER trg_tool_executions_duration
    BEFORE INSERT OR UPDATE ON public.tool_executions
    FOR EACH ROW
    EXECUTE FUNCTION public.calculate_tool_execution_duration();

-- 注释
COMMENT ON TABLE public.tool_executions IS '工具执行记录表，跟踪所有LLM工具调用的详细信息';
COMMENT ON COLUMN public.tool_executions.tool_call_id IS 'LLM返回的工具调用ID（OpenAI: tool_call_id, Anthropic: tool_use.id）';
COMMENT ON COLUMN public.tool_executions.status IS '执行状态：pending, success, error, timeout';
COMMENT ON COLUMN public.tool_executions.duration_ms IS '执行时长（毫秒），自动从started_at和completed_at计算';

COMMIT;
