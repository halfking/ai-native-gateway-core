# 附录 C — 数据库 Schema

> 关键表结构（与 license / 升级 / 采集相关）。

## 一、License 相关表

### 1.1 licenses（主表，已存在）

来源：`sql/migrations/startup/372_license_modules.sql`

```sql
CREATE TABLE IF NOT EXISTS licenses (
    id               BIGSERIAL PRIMARY KEY,
    license_key      TEXT NOT NULL UNIQUE,
    customer_name    TEXT NOT NULL DEFAULT '',
    customer_email   TEXT NOT NULL DEFAULT '',
    max_devices      INT NOT NULL DEFAULT 2,
    subscription_tier TEXT NOT NULL DEFAULT 'starter',
    features         JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at       TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**v2.x 扩展字段**（待 M3 任务实施）：

```sql
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS
    -- 订阅周期
    subscription_started_at    TIMESTAMPTZ,
    subscription_renews_at     TIMESTAMPTZ,
    subscription_interval      TEXT NOT NULL DEFAULT 'yearly',
    billing_cycle_anchor       DATE,
    -- 包含额度
    included_tokens_per_month  BIGINT NOT NULL DEFAULT 0,
    included_users             INT NOT NULL DEFAULT 2,
    -- Overage
    overage_price_per_million_tokens NUMERIC(10,4),
    overage_price_per_user     NUMERIC(10,4),
    -- 累计用量
    cumulative_tokens_used     BIGINT NOT NULL DEFAULT 0,
    current_period_tokens_used BIGINT NOT NULL DEFAULT 0,
    current_period_start_at    TIMESTAMPTZ,
    current_period_end_at      TIMESTAMPTZ,
    -- 计费状态
    billing_status             TEXT NOT NULL DEFAULT 'inactive',
    -- 支付
    payment_method_id          TEXT,
    last_invoice_id            TEXT,
    last_invoice_amount        NUMERIC(12,2),
    last_invoice_at            TIMESTAMPTZ,
    auto_renew                 BOOLEAN NOT NULL DEFAULT TRUE;
```

### 1.2 license_devices（设备绑定，已存在）

来源：`sql/migrations/startup/374_license_devices.sql`

```sql
CREATE TABLE IF NOT EXISTS license_devices (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    instance_id         TEXT NOT NULL,
    hardware_hash       TEXT NOT NULL,
    device_name         TEXT NOT NULL,
    activated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat      TIMESTAMPTZ,
    status              TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'deactivated')),
    deactivated_at      TIMESTAMPTZ,
    deactivate_reason   TEXT,
    UNIQUE (license_id, hardware_hash)
);
```

### 1.3 offline_activation_requests（离线激活请求，已存在）

```sql
CREATE TABLE IF NOT EXISTS offline_activation_requests (
    id                  BIGSERIAL PRIMARY KEY,
    license_key         TEXT NOT NULL,
    hardware_hash       TEXT NOT NULL,
    instance_id         TEXT NOT NULL,
    device_name         TEXT NOT NULL,
    request_id          TEXT NOT NULL UNIQUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at         TIMESTAMPTZ,
    signed_license      JSONB
);
```

### 1.4 license_modules（模块授权）

```sql
CREATE TABLE IF NOT EXISTS license_modules (
    id              BIGSERIAL PRIMARY KEY,
    license_id      BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    module_key      TEXT NOT NULL REFERENCES product_modules(key),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    config          JSONB,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (license_id, module_key)
);
```

### 1.5 license_module_audit（审计日志）

```sql
CREATE TABLE IF NOT EXISTS license_module_audit (
    id              BIGSERIAL PRIMARY KEY,
    license_key     TEXT NOT NULL,
    module_key      TEXT NOT NULL,
    action          TEXT NOT NULL,
    old_value       JSONB,
    new_value       JSONB,
    actor           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## 二、升级相关表（已在 autoupdate 包内）

### 2.1 releases

```sql
CREATE TABLE IF NOT EXISTS releases (
    id          BIGSERIAL PRIMARY KEY,
    version     TEXT NOT NULL,
    build_seq   INT NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'stable',
    title       TEXT NOT NULL,
    description TEXT,
    changelog   TEXT,
    image_tag   TEXT NOT NULL,
    image_digest TEXT,
    min_version TEXT,
    mandatory   BOOLEAN NOT NULL DEFAULT FALSE,
    is_published BOOLEAN NOT NULL DEFAULT FALSE,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
```

### 2.2 gray_release_rules

```sql
CREATE TABLE IF NOT EXISTS gray_release_rules (
    id          BIGSERIAL PRIMARY KEY,
    release_id  BIGINT NOT NULL REFERENCES releases(id),
    phase       TEXT NOT NULL,  -- canary/batch_1/batch_2/batch_3/full
    percent     INT NOT NULL,
    selectors   JSONB,
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 2.3 upgrade_history（每实例升级历史）

```sql
CREATE TABLE IF NOT EXISTS upgrade_history (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    release_id      BIGINT REFERENCES releases(id),
    from_version    TEXT,
    to_version      TEXT,
    status          TEXT NOT NULL,
    -- pending/downloading/installing/success/failed/rolled_back
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error_message   TEXT,
    retry_count     INT NOT NULL DEFAULT 0
);
```

## 三、采集相关表（待 M3 实施）

### 3.1 runtime_metrics（运行状态时序）

```sql
CREATE TABLE IF NOT EXISTS runtime_metrics (
    id                  BIGSERIAL PRIMARY KEY,
    instance_id         TEXT NOT NULL,
    license_id          BIGINT REFERENCES licenses(id),
    
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    -- 系统资源
    cpu_usage_pct       REAL,
    mem_used_mb         BIGINT,
    mem_total_mb        BIGINT,
    disk_used_gb        BIGINT,
    disk_total_gb       BIGINT,
    db_size_mb          BIGINT,
    uptime_secs         BIGINT,
    
    -- 流量
    current_concurrency INT,
    last_5min_tps       REAL,
    last_5min_p50_ms    REAL,
    last_5min_p99_ms    REAL,
    last_5min_success_pct REAL,
    
    -- 业务（聚合）
    model_usage         JSONB,  -- {"gpt-4": 1234, ...}
    tenant_count        INT
);

-- 时序查询优化
CREATE INDEX idx_rt_instance_time ON runtime_metrics (instance_id, timestamp DESC);
CREATE INDEX idx_rt_time ON runtime_metrics (timestamp DESC);
```

### 3.2 instance_info（实例元数据）

```sql
CREATE TABLE IF NOT EXISTS instance_info (
    instance_id         TEXT PRIMARY KEY,
    license_id          BIGINT REFERENCES licenses(id),
    hostname            TEXT,
    ip_address          TEXT,
    region              TEXT,
    version             TEXT,
    build_seq           INT,
    
    status              TEXT NOT NULL DEFAULT 'online',
    -- online / offline / degraded
    
    started_at          TIMESTAMPTZ,
    last_heartbeat      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_collect_at     TIMESTAMPTZ
);

CREATE INDEX idx_instance_status ON instance_info (status, last_heartbeat DESC);
```

## 四、计费相关表（v2.x 预留）

### 4.1 usage_records（逐租户聚合用量）

```sql
CREATE TABLE IF NOT EXISTS usage_records (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT NOT NULL REFERENCES licenses(id),
    tenant_id           TEXT NOT NULL,
    period_start        TIMESTAMPTZ NOT NULL,
    period_end          TIMESTAMPTZ NOT NULL,
    
    prompt_tokens       BIGINT NOT NULL DEFAULT 0,
    completion_tokens   BIGINT NOT NULL DEFAULT 0,
    total_tokens        BIGINT NOT NULL DEFAULT 0,
    request_count       INT NOT NULL DEFAULT 0,
    
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (license_id, tenant_id, period_start)
);

CREATE INDEX idx_usage_license_period ON usage_records (license_id, period_start DESC);
```

### 4.2 invoices（发票）

```sql
CREATE TABLE IF NOT EXISTS invoices (
    id                  TEXT PRIMARY KEY,  -- UUID
    license_id          BIGINT NOT NULL REFERENCES licenses(id),
    period_start        TIMESTAMPTZ NOT NULL,
    period_end          TIMESTAMPTZ NOT NULL,
    
    base_amount         NUMERIC(12,2) NOT NULL DEFAULT 0,
    user_amount         NUMERIC(12,2) NOT NULL DEFAULT 0,
    token_overage       BIGINT NOT NULL DEFAULT 0,
    token_overage_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    total_amount        NUMERIC(12,2) NOT NULL,
    
    status              TEXT NOT NULL DEFAULT 'pending',
    -- pending / paid / overdue / void
    
    due_at              TIMESTAMPTZ,
    paid_at             TIMESTAMPTZ,
    payment_method      TEXT,
    
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invoice_license ON invoices (license_id, created_at DESC);
CREATE INDEX idx_invoice_status ON invoices (status, due_at);
```

## 五、遥测偏好表（待 M3 实施）

### 5.1 telemetry_prefs（用户授权偏好）

```sql
CREATE TABLE IF NOT EXISTS telemetry_prefs (
    id                  BIGSERIAL PRIMARY KEY,
    enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    interval_sec        INT NOT NULL DEFAULT 300,
    
    -- 元数据
    agree_at            TIMESTAMPTZ,           -- 用户首次同意时间
    last_change_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_by          TEXT                   -- 用户标识
);

-- 单租户只有一行（id=1），但允许多行方便多租户场景
```

## 六、其他相关表

### 6.1 product_modules（产品模块定义）

```sql
CREATE TABLE IF NOT EXISTS product_modules (
    key             TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT,
    default_enabled BOOLEAN NOT NULL DEFAULT FALSE
);
```

### 6.2 telemetry_events（事件流）

来源：`domains/hooks/observability/telemetry/`

（具体的 telemetry_events 表 schema 由 telemetry 包管理，不在 license 范围内）

## 七、迁移规则

1. **新增字段必须 NOT NULL + DEFAULT** （向后兼容）
2. **删除字段需分两阶段**（先 rename → 再下一个版本删除）
3. **改字段类型需谨慎**（应先加新字段，迁移数据，再切换）
4. **所有 schema 变更需通过 migration 文件记录**

## 八、待建表清单

| 表名 | 任务 | 估计表大小 |
|------|------|----------|
| runtime_metrics | M3-T06 | 中（每天 12 × 24 = 288 行/实例） |
| instance_info | M3 增强 | 小（每实例一行） |
| telemetry_prefs | M3-T04 | 小（每租户一行） |
| usage_records | v2.x | 中（每小时聚合） |
| invoices | v2.x | 小（每月 N 条） |