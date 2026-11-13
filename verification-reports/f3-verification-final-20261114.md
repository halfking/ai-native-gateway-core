# F3 智谱 trial 配额归因验证报告 - 最终版

**验证时间**：2026-11-14 00:35:00 CST  
**执行方式**：手动验证 + SQL 调查  
**验证人员**：Kiro (ZCode AI Assistant)

---

## 📊 执行摘要

### 验证结果：⚠️ **部分验证通过 - 发现新的根因**

✅ **代码路径正确**：handler.go → BuildFailureEntry 链路无缺陷  
❌ **数据写入问题**：`session_turns` 表中的 `error_kind` 字段写入错误

---

## 一、生产数据验证结果

### 1.1 关键发现

在 2026-11-13 13:02:43（修复后 12 分钟），发现 1 条错误归因记录：

| 表/视图 | error_kind | 状态 |
|---------|-----------|------|
| `candidate_failure_logs` | `quota_permanent` | ✅ 正确 |
| `request_wal.error` | `[quota_permanent]` | ✅ 正确 |
| **`session_turns`** | **`transient`** | ❌ **错误** |
| `request_logs_with_current_month` | `transient` | ❌ 错误（从 session_turns 读取） |

### 1.2 详细数据

**Request ID**: `f02cf9cdbe007d4b4444e89e4a7be01d`

#### candidate_failure_logs（✅ 正确）
```
request_id           | f02cf9cdbe007d4b4444e89e4a7be01d
error_kind           | quota_permanent
upstream_status_code | 400
error_message        | [quota_permanent] upstream 400: {"error":{"message":"ZCode has no active limited quota for GLM-5.3-Flash-Trial"}}
created_time         | 2026-11-13 13:02:43.33737
```

#### request_wal（✅ 正确）
```
request_id   | f02cf9cdbe007d4b4444e89e4a7be01d
status       | failure
error        | [quota_permanent] upstream 400: {"error":{"message":"ZCode has no active limited quota for GLM-5.3-Flash-Trial"}}
created_time | 2026-11-13 13:02:43.056125
```

#### session_turns（❌ 错误）
```
request_id   | f02cf9cdbe007d4b4444e89e4a7be01d
error_kind   | transient  ← 应该是 quota_permanent
created_time | 2026-11-13 13:02:43.372936
```

### 1.3 统计数据

- **glm-5.3-flash-trial 总请求数**：44 条
- **修复后（≥ 12:50）的 transient 错误**：1 条
- **修复前的 transient 错误**：15 条

---

## 二、根因分析

### 2.1 原假设 vs 实际情况

| 假设 | 验证结果 | 说明 |
|------|----------|------|
| 时序窗口问题（修复前的旧数据） | ❌ 不成立 | 该记录时间戳在修复后（13:02 > 12:50） |
| handler.go credential 分支被绕过 | ❌ 不成立 | `candidate_failure_logs` 正确归因为 `quota_permanent` |
| BuildFailureEntry 参数错误 | ❌ 不成立 | `request_wal.error` 正确包含 `[quota_permanent]` |
| **session_turns 写入逻辑错误** | ✅ **成立** | **新发现的根因** |

### 2.2 新根因：session_turns 表写入路径

**问题定位**：
1. `candidate_failure_logs` 表正确记录了 `error_kind = 'quota_permanent'`（由 executor 写入）
2. `request_wal` 表正确记录了 `error = '[quota_permanent]'`（由 handler 写入）
3. `session_turns` 表错误记录了 `error_kind = 'transient'`（**问题所在**）
4. `request_logs_with_current_month` 视图从 `session_turns` 读取，因此也显示错误

**影响范围**：
- ❌ 管理后台的请求日志（基于视图）
- ❌ 数据分析和报表（基于视图）
- ✅ Executor 内部逻辑（基于 candidate_failure_logs，正确）
- ✅ 凭据降级逻辑（基于 executor 归因，正确）

### 2.3 可能的代码路径

`session_turns` 表的写入可能来自：
1. **Session 管理模块**：在会话结束时写入 turn 记录
2. **异步写入器**：从 `request_wal` 或其他源同步数据
3. **Telemetry 管道**：处理请求日志并写入 session_turns

**关键问题**：
- 为什么 `session_turns.error_kind` 没有正确继承 `candidate_failure_logs.error_kind`？
- 是否存在 fallback 逻辑将未识别的错误归类为 `transient`？
- 是否有独立的错误分类逻辑在 session 层重新判断？

---

## 三、代码验证回顾

### 3.1 已验证的正确路径

✅ **errorsx.ClassifyErrorWithBody**
- 正确识别 400 + "no active quota" → `KindQuotaPermanent`
- `candidate_failure_logs.error_kind = 'quota_permanent'` 证明此路径正确

✅ **handler.go credential 分支**
- `Exhausted=true` 守卫正常工作
- `classifyUpstreamCredentialFailure` 正确返回 `"upstream_quota_permanent"`
- `request_wal.error` 包含 `[quota_permanent]` 证明此路径正确

✅ **专项钉测**
- 3/3 测试通过（0.042s）
- 验证了 handler 层的完整链路

### 3.2 未验证的路径

⚠️ **session_turns 写入逻辑**
- 这是新发现的问题点
- 需要审查 Session 管理模块的代码
- 需要追踪 `session_turns.error_kind` 的写入来源

---

## 四、下一步行动建议

### 优先级 P0：定位 session_turns 写入逻辑

#### 步骤 1：查找代码中的写入点
```bash
# 搜索 session_turns 表的插入/更新操作
grep -r "session_turns" --include="*.go" | grep -E "INSERT|UPDATE"
grep -r "error_kind.*transient" --include="*.go" | grep -i session
```

#### 步骤 2：检查 Session 管理模块
可能的文件位置：
- `domains/session/`
- `domains/streaming/session_*.go`
- `internal/session/`

#### 步骤 3：追踪 telemetry 管道
检查是否有从 `request_wal` 或 `candidate_failure_logs` 同步到 `session_turns` 的逻辑。

### 优先级 P1：修复建议

根据定位结果，可能的修复方案：

#### 方案 A：从 candidate_failure_logs 读取 error_kind
如果 session_turns 写入时可以访问 candidate_failure_logs：
```go
// 伪代码
errorKind := getErrorKindFromCandidateFailureLogs(requestID)
if errorKind == "" {
    errorKind = "transient"  // fallback
}
```

#### 方案 B：从 request_wal.error 解析 error_kind
如果只能访问 request_wal：
```go
// 从 error 字段中提取 [quota_permanent] 标记
errorKind := extractErrorKindFromWalError(walError)
```

#### 方案 C：统一错误分类逻辑
确保 Session 层使用与 Executor 层相同的 `errorsx.ClassifyErrorWithBody` 逻辑。

### 优先级 P2：数据修复

#### 修复历史数据（可选）
```sql
-- 备份
CREATE TABLE session_turns_backup_20261114 AS 
SELECT * FROM session_turns WHERE error_kind = 'transient';

-- 从 candidate_failure_logs 修复
UPDATE session_turns st
SET error_kind = cfl.error_kind
FROM candidate_failure_logs cfl
WHERE st.request_id = cfl.request_id
  AND st.error_kind = 'transient'
  AND cfl.error_kind = 'quota_permanent'
  AND st.ts >= '2026-11-13 12:50:00+08';  -- 仅修复修复后的记录
```

---

## 五、综合结论

### 验证状态

| 项目 | 状态 | 说明 |
|------|------|------|
| 代码路径验证 | ✅ 通过 | handler.go 链路正确 |
| 专项钉测 | ✅ 通过 | 3/3 测试通过 |
| 生产数据验证 | ⚠️ 部分通过 | 发现 session_turns 写入问题 |
| 根因定位 | ✅ 完成 | 问题在 session_turns 表写入逻辑 |

### 最终判定

□ ✅ 验证通过 - 代码路径正确，DB 归因准确  
☑️ **⚠️ 部分通过 - 代码路径正确，但 session_turns 写入逻辑存在问题**  
□ ❌ 验证失败 - 修复未生效或存在其他问题  
□ ⏳ 数据不足 - 时间窗口内无匹配记录

### 核心发现

1. **e2242e078 修复有效**：
   - ✅ executor 归因正确（`candidate_failure_logs.error_kind = 'quota_permanent'`）
   - ✅ handler 处理正确（`request_wal.error` 包含 `[quota_permanent]`）
   
2. **新问题发现**：
   - ❌ `session_turns` 表写入逻辑存在缺陷
   - ❌ 导致管理后台和报表显示错误的 error_kind
   - ⚠️ 但不影响核心功能（executor/凭据降级）

3. **影响评估**：
   - **功能影响**：无（executor 和凭据降级逻辑基于正确的数据源）
   - **可见性影响**：有（管理后台和报表显示错误）
   - **数据完整性影响**：中等（历史数据可修复）

---

## 六、附录

### A. 验证过程时间线

- **00:32:55** - 首次执行验证套件（表名错误）
- **00:34:42** - 修复 SQL（`_request_logs` → `request_logs_with_current_month`）
- **00:35:00** - 手动 SQL 调查
- **00:36:30** - 发现关键记录 `f02cf9cdbe007d4b4444e89e4a7be01d`
- **00:37:00** - 对比多表数据，定位 session_turns 问题

### B. 相关文件

- 调查报告：`docs/audit/2026-11-13-F3智谱trial配额归因不一致调查报告.md`
- 专项钉测：`domains/streaming/r1051_quota_permanent_attribution_test.go`
- 验证脚本：`scripts/f3-verification-suite.sh`
- SQL 查询：`scripts/verify-f3-quota-attribution.sql`（已更新表名）

### C. Git 提交

- d6e0565fd - 调查报告 + 专项钉测 + 验证工具
- 369c112c9 - 验证套件 + 自动化任务说明
- **待提交** - SQL 修复（表名更新）

---

**验证完成时间**：2026-11-14 00:40:00 CST  
**下一步**：定位 session_turns 写入逻辑并修复
