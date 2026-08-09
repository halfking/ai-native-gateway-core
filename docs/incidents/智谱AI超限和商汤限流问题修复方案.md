# 智谱AI超限和商汤限流问题 - 待处理任务

**创建日期**: 2026-07-19
**优先级**: P0 - 高优先级
**预计工作量**: 2-4小时
**任务类型**: 问题诊断 + 代码修复
**依赖**: 需要生产数据库访问权限

---

## 📋 任务背景

### 问题描述
1. **智谱AI**: 用量已超限，但系统仍在持续发送请求
2. **商汤**: 总是显示限流，但仍在派发请求
3. **火山引擎**: 官网配置了 GLM-5.2，但网关未拉取到该模型数据

### 已完成的准备工作
- ✅ 完整的错误处理流程梳理（`docs/analysis/error-handling-flow-2026-07-19.md`）
- ✅ 火山引擎 GLM-5.2 配置脚本（`sql/migrations/manual/20260719_add_volcano_glm52.sql`）
- ✅ 验证脚本（`sql/migrations/manual/20260719_verify_config.sql`）
- ✅ 自动化诊断工具（`scripts/verify_and_diagnose.sh`）
- ✅ 手动SQL查询（`scripts/manual_verification_sql.sh`）
- ✅ 执行指南（`docs/analysis/execution-guide-2026-07-19.md`）

---

## 🎯 待执行任务清单

### 阶段1: 数据收集与问题确认（30分钟）

#### 任务1.1: 设置数据库连接
**目标**: 配置数据库访问权限

**步骤**:
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 方式1: 使用现有环境配置
source .env
# 或
export DATABASE_URL='postgresql://user:password@host:port/llm_gateway'

# 验证连接
psql $DATABASE_URL -c "SELECT current_database(), current_user, version();"
```

**验证标准**: 能成功连接并查询数据库

---

#### 任务1.2: 执行火山引擎配置（如未执行）
**目标**: 添加 GLM-5.2 模型配置

**步骤**:
```bash
psql $DATABASE_URL -f sql/migrations/manual/20260719_add_volcano_glm52.sql
```

**预期结果**:
```
INSERT 0 2  -- 插入2个模型（glm-5.2 和 GLM-5.2）
INSERT 0 X  -- 为凭证绑定模型
```

**验证SQL**:
```sql
SELECT provider_code, raw_model_name, COUNT(*) as binding_count
FROM v_routable_credential_models
WHERE provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND (raw_model_name = 'glm-5.2' OR raw_model_name = 'GLM-5.2')
GROUP BY provider_code, raw_model_name;
```

---

#### 任务1.3: 执行关键诊断查询
**目标**: 收集问题证据

**方式A: 使用自动化脚本（推荐）**
```bash
./scripts/verify_and_diagnose.sh
# 选择选项4（快速诊断）
```

**方式B: 手动执行SQL**

执行以下2个关键查询并记录结果：

**查询1: 智谱AI错误类型**
```sql
SELECT
    error_kind,
    upstream_status_code,
    COUNT(*) as error_count,
    MAX(created_at) as last_seen,
    STRING_AGG(DISTINCT SUBSTRING(upstream_response_preview, 1, 100), ' | ') as sample_responses
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
GROUP BY error_kind, upstream_status_code
ORDER BY error_count DESC
LIMIT 10;
```

**查询2: 降级模式触发次数**
```sql
WITH unavailable_creds AS (
    SELECT
        credential_id,
        raw_model_name,
        unavailable_at,
        unavailable_reason
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.available = FALSE
      AND cmb.unavailable_at > now() - interval '1 hour'
)
SELECT
    uc.credential_id,
    uc.raw_model_name,
    uc.unavailable_reason,
    COUNT(rl.id) as requests_after_unavailable,
    MAX(rl.created_at) as last_request_at
FROM unavailable_creds uc
LEFT JOIN request_logs rl
    ON rl.credential_id = uc.credential_id
    AND rl.created_at > uc.unavailable_at
GROUP BY uc.credential_id, uc.raw_model_name, uc.unavailable_reason
ORDER BY requests_after_unavailable DESC
LIMIT 10;
```

**记录结果**: 将查询结果保存到文件
```bash
# 保存结果
psql $DATABASE_URL -f sql/migrations/manual/20260719_verify_config.sql > /tmp/diagnosis_result_$(date +%Y%m%d).txt

# 查看结果
cat /tmp/diagnosis_result_$(date +%Y%m%d).txt
```

---

### 阶段2: 问题根因分析（30分钟）

根据查询1和查询2的结果，判断问题类型：

#### 场景A: 降级模式问题（最可能）

**判断条件**:
- 查询1: `error_kind = 'rate_limit'` 且 `upstream_status_code = 429`
- 查询2: `requests_after_unavailable > 0`

**根因**:
```
domains/streaming/executors/router.go 中的 isTransientUnavailableReason 函数
将 'availability:rate_limited' 和 'state:rate_limit' 视为"瞬态"错误，
导致单候选者场景下，降级模式强制使用限流的凭证。
```

**影响范围**:
- 智谱AI超限后仍在发送请求
- 商汤限流后仍在派发请求
- 其他provider的限流状态也会被降级模式绕过

**修复方案**: 跳转到 → **阶段3.1**

---

#### 场景B: 错误分类问题

**判断条件**:
- 查询1: `error_kind = 'model_not_found'` 且 `upstream_status_code = 429`
- 查询2: 可能有数据

**根因**:
```
errorsx/classify.go 中的 ClassifyError 函数
在检查HTTP状态码之前先匹配了body中的"model not found"字样，
导致429响应被错误分类为 model_not_found 而非 rate_limit。
```

**修复方案**: 跳转到 → **阶段3.2**

---

#### 场景C: 自动恢复问题

**判断条件**:
- 查询1: `error_kind = 'rate_limit'` 且 `upstream_status_code = 429`
- 查询2: 无数据或很少

**根因**:
```
credentialhealth/checker.go 中的 RecoverExpired 函数
在冷却期结束后自动恢复凭证，但没有探测验证上游是否真的恢复，
导致恢复 → 立即失败 → 再次冷却 → 循环往复。
```

**修复方案**: 跳转到 → **阶段3.3**

---

#### 场景D: 模型配置问题

**判断条件**:
- 查询1: `error_kind = 'model_not_found'` 且 `upstream_status_code = 400/404`
- 查询2: 无数据

**根因**:
```
智谱AI的 provider_models 表中配置的模型名与实际API不匹配。
例如：配置了 glm-4，但应该使用 glm-5.2。
```

**修复方案**: 跳转到 → **阶段3.4**

---

### 阶段3: 代码修复（1-2小时）

#### 3.1 修复降级模式问题（场景A）

**文件**: `domains/streaming/executors/router.go`

**修改位置**: 第861-877行的 `isTransientUnavailableReason` 函数

**当前代码**:
```go
func isTransientUnavailableReason(reason string) bool {
    switch reason {
    case "availability:cooling",
         "availability:rate_limited",      // ← 需要移除
         "availability:suspended":
        return true
    case "state:" + string(errorsx.KindTimeout),
         "state:" + string(errorsx.KindStreamTimeout),
         "state:" + string(errorsx.KindRateLimit),  // ← 需要移除
         "state:" + string(errorsx.KindUpstreamDown),
         "state:" + string(errorsx.KindEmptyResponse),
         "state:probe_direct_timeout":
        return true
    default:
        return false
    }
}
```

**修改后代码**:
```go
func isTransientUnavailableReason(reason string) bool {
    switch reason {
    case "availability:cooling",
         // "availability:rate_limited",   // REMOVED: 限流不是瞬态，不应降级使用
         "availability:suspended":
        return true
    case "state:" + string(errorsx.KindTimeout),
         "state:" + string(errorsx.KindStreamTimeout),
         // "state:" + string(errorsx.KindRateLimit),  // REMOVED: 限流不是瞬态
         "state:" + string(errorsx.KindUpstreamDown),
         "state:" + string(errorsx.KindEmptyResponse),
         "state:probe_direct_timeout":
        return true
    default:
        return false
    }
}
```

**执行步骤**:
```bash
# 1. 创建分支
git checkout -b fix/rate-limit-degraded-mode

# 2. 修改文件
# 编辑 domains/streaming/executors/router.go

# 3. 运行测试
go test ./domains/streaming/executors/... -v

# 4. 提交
git add domains/streaming/executors/router.go
git commit -m "fix(router): 移除限流状态的降级模式

问题：智谱AI超限和商汤限流后仍在派发请求
根因：isTransientUnavailableReason将rate_limited视为瞬态错误
修复：移除rate_limited的降级逻辑，限流必须等待恢复

Refs: #智谱AI超限问题"

# 5. 推送（可选）
git push origin fix/rate-limit-degraded-mode
```

**验证方法**:
```bash
# 部署后，重新执行查询2
# 应该看到 requests_after_unavailable = 0
```

---

#### 3.2 修复错误分类问题（场景B）

**文件**: `errorsx/classify.go`

**修改位置**: 第286-320行的 `ClassifyError` 函数

**修改内容**: 确保HTTP状态码优先级高于body匹配

**当前逻辑**:
```go
func ClassifyError(err error, body []byte) ErrorKind {
    // ... 省略前面的代码 ...

    // 问题：先检查body中的pattern
    if modelNotFoundRe.MatchString(msg) {
        return KindModelNotFound  // ← 429也可能匹配这里
    }

    // 后检查状态码
    if resp.StatusCode == 429 {
        return KindRateLimit
    }
}
```

**修改后逻辑**:
```go
func ClassifyError(err error, body []byte) ErrorKind {
    // ... 省略前面的代码 ...

    // 优先检查HTTP状态码（永远准确）
    if resp != nil {
        switch resp.StatusCode {
        case 429:
            return KindRateLimit  // ← HTTP 429永远是限流
        case 401, 403:
            return KindAuth
        // ... 其他状态码
        }
    }

    // 然后才检查body中的pattern
    if modelNotFoundRe.MatchString(msg) {
        return KindModelNotFound
    }
}
```

**执行步骤**:
```bash
# 1. 创建分支
git checkout -b fix/error-classification-priority

# 2. 修改文件
# 编辑 errorsx/classify.go

# 3. 运行测试
go test ./errorsx/... -v

# 4. 提交并推送
git add errorsx/classify.go
git commit -m "fix(errorsx): HTTP状态码优先于body匹配

问题：429响应被误判为model_not_found
根因：body匹配优先级高于状态码
修复：状态码优先，确保429永远返回KindRateLimit"
```

---

#### 3.3 修复自动恢复问题（场景C）

**文件**: `credentialhealth/checker.go`

**修改位置**: 第307行的 `RecoverExpired` 函数

**修改内容**: 恢复前先探测验证

**修改后代码**:
```go
func RecoverExpired(ctx context.Context, db DBQuerier, prober CredentialProber) (int, error) {
    // 1. 查询待恢复的凭证
    rows, err := db.Query(ctx, `
        SELECT cmb.credential_id, pm.canonical_raw_name
        FROM credential_model_bindings cmb
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        WHERE cmb.available = FALSE
          AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
          AND cmb.unavailable_reason <> 'model_probe_broken'
          AND COALESCE(cmb.unavailable_recover_at,
                       cmb.unavailable_at + INTERVAL '30 seconds') < now()
    `)
    if err != nil {
        return 0, err
    }
    defer rows.Close()

    recovered := 0
    for rows.Next() {
        var credID int
        var model string
        if err := rows.Scan(&credID, &model); err != nil {
            continue
        }

        // 2. 恢复前先探测
        if prober != nil {
            probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
            result := prober.ProbeCredential(probeCtx, credID, model)
            cancel()

            if !result.Success {
                slog.Warn("credentialhealth: recover skipped due to failed probe",
                    "credential_id", credID,
                    "model", model,
                    "probe_error", result.Detail)

                // 探测失败，延长冷却期
                _, _ = db.Exec(ctx, `
                    UPDATE credential_model_bindings cmb
                    SET unavailable_recover_at = now() + INTERVAL '5 minutes'
                    FROM provider_models pm
                    WHERE pm.id = cmb.provider_model_id
                      AND cmb.credential_id = $1
                      AND pm.canonical_raw_name = $2
                `, credID, model)
                continue
            }
        }

        // 3. 探测成功才恢复
        tag, err := db.Exec(ctx, `
            UPDATE credential_model_bindings cmb
            SET available = TRUE,
                unavailable_reason = NULL,
                unavailable_at = NULL,
                unavailable_recover_at = NULL,
                updated_at = now()
            FROM provider_models pm
            WHERE pm.id = cmb.provider_model_id
              AND cmb.credential_id = $1
              AND pm.canonical_raw_name = $2
        `, credID, model)

        if err == nil && tag.RowsAffected() > 0 {
            recovered++
        }
    }

    return recovered, nil
}
```

**执行步骤**:
```bash
# 1. 创建分支
git checkout -b fix/auto-recovery-probe

# 2. 修改文件
# 编辑 credentialhealth/checker.go

# 3. 修改调用方（增加prober参数）
# 查找所有调用 RecoverExpired 的地方并传入prober

# 4. 运行测试
go test ./credentialhealth/... -v

# 5. 提交
git commit -am "fix(credentialhealth): 自动恢复前增加探测验证

问题：冷却期结束后自动恢复，但上游可能仍在限流
根因：RecoverExpired直接恢复，未验证上游状态
修复：恢复前先探测，失败则延长冷却期"
```

---

#### 3.4 修复模型配置问题（场景D）

**SQL修复**:
```sql
-- 检查智谱AI当前配置
SELECT raw_model_name, canonical_name, outbound_model_name
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.provider_code = 'zhipuai';

-- 如果缺少 glm-5.2，添加
INSERT INTO provider_models (provider_id, raw_model_name, canonical_name, context_window)
SELECT p.id, 'glm-5.2', 'glm-5.2', 512000
FROM providers p
WHERE p.provider_code = 'zhipuai'
  AND NOT EXISTS (
      SELECT 1 FROM provider_models pm2
      WHERE pm2.provider_id = p.id AND pm2.raw_model_name = 'glm-5.2'
  );

-- 为凭证绑定
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available)
SELECT c.id, pm.id, TRUE
FROM credentials c
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.provider_id = p.id
WHERE p.provider_code = 'zhipuai'
  AND pm.raw_model_name = 'glm-5.2'
  AND NOT EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = c.id AND cmb.provider_model_id = pm.id
  )
ON CONFLICT DO NOTHING;
```

---

### 阶段4: 测试验证（30分钟）

#### 4.1 单元测试
```bash
# 运行相关测试
go test ./domains/streaming/executors/... -v -run TestDegradedMode
go test ./errorsx/... -v -run TestClassifyError
go test ./credentialhealth/... -v -run TestRecoverExpired
```

#### 4.2 集成测试
```bash
# 重新执行诊断查询
psql $DATABASE_URL -c "
WITH unavailable_creds AS (
    SELECT credential_id, unavailable_at
    FROM credential_model_bindings cmb
    WHERE cmb.available = FALSE
      AND cmb.unavailable_at > now() - interval '1 hour'
)
SELECT COUNT(*) as degraded_usage_after_fix
FROM unavailable_creds uc
JOIN request_logs rl ON rl.credential_id = uc.credential_id
WHERE rl.created_at > uc.unavailable_at
  AND rl.created_at > now() - interval '10 minutes';  -- 部署后的10分钟
"
```

**预期结果**: `degraded_usage_after_fix = 0`

#### 4.3 监控验证
```bash
# 监控智谱AI的错误率
watch -n 5 "psql $DATABASE_URL -c \"
SELECT
    COUNT(*) FILTER (WHERE request_status = 'failure') as failures,
    COUNT(*) as total,
    ROUND(100.0 * COUNT(*) FILTER (WHERE request_status = 'failure') / COUNT(*), 2) as failure_rate
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND created_at > now() - interval '5 minutes';
\""
```

---

### 阶段5: 部署上线（30分钟）

#### 5.1 代码审查
- [ ] 代码变更已通过测试
- [ ] 代码已提交到功能分支
- [ ] 创建PR并请求审查

#### 5.2 灰度发布（推荐）
```bash
# 1. 部署到1台机器
# 2. 观察10分钟
# 3. 检查降级模式触发次数
# 4. 如果正常，全量发布
```

#### 5.3 回滚方案
```bash
# 如果出现问题，立即回滚到上一版本
git revert <commit-hash>
# 重新部署
```

---

## 📊 成功标准

### 主要指标
- [ ] 查询2（降级模式触发）返回0条记录或 `requests_after_unavailable = 0`
- [ ] 智谱AI超限后不再发送新请求
- [ ] 商汤限流后不再派发请求
- [ ] 火山引擎 GLM-5.2 模型可被正常路由

### 次要指标
- [ ] 错误日志中不再出现"降级模式激活"的警告
- [ ] `credential_model_bindings.unavailable_recover_at` 到期前不再有请求
- [ ] 自动恢复后的第一个请求成功率 > 90%

---

## 📚 参考文档

- **错误处理流程**: `docs/analysis/error-handling-flow-2026-07-19.md`
- **执行指南**: `docs/analysis/execution-guide-2026-07-19.md`
- **配置脚本**: `sql/migrations/manual/20260719_add_volcano_glm52.sql`
- **验证脚本**: `sql/migrations/manual/20260719_verify_config.sql`
- **自动化工具**: `scripts/verify_and_diagnose.sh`

---

## ⚠️ 注意事项

1. **数据库备份**: 执行SQL前先备份相关表
2. **生产环境**: 建议先在测试环境验证
3. **监控**: 部署后持续监控错误率和降级模式触发次数
4. **回滚**: 准备好回滚方案，如果问题加剧立即回滚

---

## 📞 联系方式

- **问题咨询**: 参考 `docs/analysis/error-handling-flow-2026-07-19.md` 第5节（待验证的关键问题）
- **技术支持**: 查看自动化脚本输出的诊断结果

---

**最后更新**: 2026-07-19
**创建者**: AI Assistant
**状态**: 待执行
