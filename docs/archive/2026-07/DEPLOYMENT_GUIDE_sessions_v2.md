# Sessions V2 完整部署指南

## 🎉 项目概览

Sessions V2 是一个全新的会话详情查看系统，使用 `gateway.session_*` 表替代传统的 `request_logs`，提供增量存储、更高效的查询和更丰富的会话分析功能。

**核心特性**:
- ✅ 增量存储（仅保存每轮新增内容，节省60-80%空间）
- ✅ 轮次级详情展示（原始/压缩/安全检查三视图）
- ✅ LLM驱动的会话总结
- ✅ 实时指标（tokens、成本、延迟、压缩统计）
- ✅ URL参数定位（直接跳转到特定轮次）
- ✅ 灰度发布（1% → 100%逐步开启）

---

## 📦 代码仓库

**后端**: `llm-gateway-go` (main分支)
- 提交: `2918c28b` - feat(sessions-v2): wire session detail/summary APIs
- 文件:
  - `admin/session_detail_v2.go` - 会话详情API
  - `admin/session_summary_v2.go` - 会话总结API
  - `cmd/gateway/main.go` - API注册
  - `scripts/enable_sessions_v2*.sql` - Feature flags配置脚本

**前端**: `distribution-b-c` (feat/distribution-b-c分支)
- 提交: `ded2aefe` - feat(sessions-v2): implement complete frontend
- 文件:
  - `web/src/api/sessions-v2.ts` - API客户端
  - `web/src/views/SessionDetailView.vue` - 主页面
  - `web/src/router.ts` - 路由配置

---

## 🚀 部署步骤

### Step 1: 开启Feature Flags（后端数据写入）

#### 本地环境
```bash
cd /path/to/llm-gateway-go
psql -U postgres -d llmgateway -f scripts/enable_sessions_v2.sql
```

**预期输出**:
```
                    key                     | value | type
-------------------------------------------+-------+------
 sessions_v2.compression_enabled           | true  | bool
 sessions_v2.enabled                       | true  | bool
 sessions_v2.read_timeout_ms               | 300   | int
 sessions_v2.rollout_percent               | 100   | int  ← 本地100%
 sessions_v2.shadow_write                  | true  | bool
 sessions_v2.turn_logs_retention_hours     | 24    | int
 sessions_v2.write_timeout_ms              | 500   | int
```

#### 生产环境（154/245）
```bash
# 154
psql -h 154.xxx.xxx.xxx -U postgres -d llmgateway -f scripts/enable_sessions_v2_prod.sql

# 245
psql -h 245.xxx.xxx.xxx -U postgres -d llmgateway -f scripts/enable_sessions_v2_prod.sql
```

**注意**: 生产环境从 `rollout_percent=1`（1%灰度）开始！

---

### Step 2: 验证数据写入

等待5-10分钟后，检查 `session_turns` 表：

```sql
-- 检查最近1小时的数据
SELECT COUNT(*) as total_turns, 
       COUNT(DISTINCT session_id) as unique_sessions
FROM gateway.session_turns 
WHERE ts > NOW() - INTERVAL '1 hour';

-- 预期结果（本地100%灰度）:
--  total_turns | unique_sessions
-- -------------+-----------------
--          150 |              45
```

如果 `total_turns = 0`，检查：
1. Gateway是否已重启（feature flags需要重启生效）
2. 是否有请求流量
3. 日志是否有错误：`grep session_persist /var/log/llm-gateway/gateway.log`

---

### Step 3: 找一个真实的Session ID测试

```sql
SELECT session_id, COUNT(*) as turns 
FROM gateway.session_turns 
WHERE ts > NOW() - INTERVAL '1 hour' 
GROUP BY session_id 
ORDER BY turns DESC 
LIMIT 5;

-- 示例输出:
--           session_id           | turns
-- -------------------------------+-------
--  sess_abc123def456             |    12
--  sess_xyz789uvw012             |     8
```

---

### Step 4: 测试后端API

使用测试脚本：

```bash
cd /path/to/llm-gateway-go/scripts
chmod +x test_sessions_v2_api.sh

# 用上一步查到的session_id测试
./test_sessions_v2_api.sh http://localhost:8080 sess_abc123def456
```

**预期输出**（部分）:
```json
{
  "session": {
    "session_id": "sess_abc123def456",
    "tenant_id": "default",
    "total_turns": 12,
    "total_tokens": 5678,
    "total_cost_usd": 0.0234,
    ...
  },
  "turns": [
    {
      "turn_no": 12,
      "model": "gpt-4",
      "prompt_tokens": 450,
      "completion_tokens": 320,
      "request_delta": {...},
      "response_delta": {...},
      ...
    },
    ...
  ],
  "total_turns": 12
}
```

如果API返回404或500，检查：
1. Gateway是否启用了admin API（`/api/admin/sessions/detail`）
2. 是否有权限（super admin）
3. 后端日志：`tail -f /var/log/llm-gateway/gateway.log`

---

### Step 5: 构建并部署前端

#### 合并前端分支到main
```bash
cd /path/to/distribution-b-c
git checkout main
git pull origin main
git merge feat/distribution-b-c
git push origin main
```

#### 构建前端
```bash
cd web
npm install
npm run build
```

#### 部署到154/245
```bash
# 根据您的部署流程，通常是：
rsync -avz dist/ user@154.xxx.xxx.xxx:/var/www/llm-gateway/
rsync -avz dist/ user@245.xxx.xxx.xxx:/var/www/llm-gateway/

# 或使用您的CI/CD流程
```

---

### Step 6: 访问新页面

在浏览器中打开：
```
https://llmgo.kxpms.cn/sessions/sess_abc123def456
```

**功能验证**:
- [ ] 页面加载成功，显示会话头部（ID、总轮次、tokens、成本）
- [ ] 轮次列表倒序显示（最新的在最上面）
- [ ] 点击任意轮次卡片，右侧抽屉打开
- [ ] 抽屉内4个Tab可切换（原始/压缩/安全/元数据）
- [ ] "原始会话"Tab显示request_delta和response_delta
- [ ] "压缩会话"Tab显示outbound_body（完整上下文）
- [ ] "安全检查"Tab显示injection_verdict和output_verdict
- [ ] "元数据"Tab显示所有tokens、成本、延迟等信息
- [ ] 点击"生成总结"按钮，显示AI总结

#### 测试URL参数定位
```
https://llmgo.kxpms.cn/sessions/sess_abc123def456?turn_no=5
```
- [ ] 页面自动滚动到Turn #5
- [ ] 抽屉自动打开显示Turn #5详情

---

### Step 7: 生产环境灰度发布（154/245）

#### Day 1: 1% 灰度
```sql
-- 已通过 enable_sessions_v2_prod.sql 设置为1%
SELECT value FROM platform_settings WHERE key = 'sessions_v2.rollout_percent';
-- 预期: 1
```

**监控指标**（观察24小时）:
```sql
-- 每小时写入量
SELECT DATE_TRUNC('hour', ts) as hour, COUNT(*) as turns
FROM gateway.session_turns
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;

-- 错误日志
SELECT * FROM logs 
WHERE message LIKE '%session_persist%' 
  AND level = 'ERROR'
  AND ts > NOW() - INTERVAL '24 hours';
```

#### Day 2: 增加到10%
如果Day 1无异常：
```sql
UPDATE platform_settings 
SET value = '10', updated_at = NOW() 
WHERE key = 'sessions_v2.rollout_percent';

-- 重启Gateway使其生效
-- systemctl restart llm-gateway
```

#### Day 3: 全量100%
如果Day 2无异常：
```sql
UPDATE platform_settings 
SET value = '100', updated_at = NOW() 
WHERE key = 'sessions_v2.rollout_percent';

-- 重启Gateway
```

---

## 🔍 故障排查

### 问题1: V2表无数据写入

**症状**: `SELECT COUNT(*) FROM gateway.session_turns` 返回0

**排查步骤**:
1. 检查feature flags:
   ```sql
   SELECT key, value FROM platform_settings WHERE key LIKE 'sessions_v2.%';
   ```
   确认 `enabled=true`, `shadow_write=true`, `rollout_percent>0`

2. 检查Gateway是否重启:
   ```bash
   ps aux | grep gateway
   systemctl status llm-gateway
   ```

3. 检查日志:
   ```bash
   grep -i "sessions_v2\|session_persist" /var/log/llm-gateway/gateway.log | tail -50
   ```
   
   **预期日志**:
   ```
   INFO v2 pipeline: session persist hook wired (dual-write ready) pool_healthy=true
   ```
   
   **错误日志**:
   ```
   WARN session_persist_hook: V2 write failed (best-effort) error="..."
   ```

4. 检查数据库连接:
   ```sql
   SELECT COUNT(*) FROM pg_stat_activity WHERE application_name LIKE '%gateway%';
   ```

---

### 问题2: 前端页面404

**症状**: 访问 `/sessions/xxx` 返回404

**排查步骤**:
1. 检查router.ts是否已更新:
   ```bash
   grep -n "SessionDetailView" web/src/router.ts
   # 应该有两行：import 和 route 配置
   ```

2. 检查前端构建是否包含新文件:
   ```bash
   ls -la web/src/views/SessionDetailView.vue
   ls -la web/src/api/sessions-v2.ts
   ```

3. 重新构建前端:
   ```bash
   cd web
   npm run build
   ```

4. 检查nginx配置（如果使用nginx）:
   ```nginx
   location / {
     try_files $uri $uri/ /index.html;  # 确保SPA路由工作
   }
   ```

---

### 问题3: API返回403 Forbidden

**症状**: 前端调用 `/api/admin/sessions/detail` 返回403

**原因**: 需要super admin权限

**解决**:
1. 确认当前用户是super admin:
   ```sql
   SELECT * FROM users WHERE id = <your_user_id>;
   -- role 应该是 'super_admin'
   ```

2. 如果不是，升级权限:
   ```sql
   UPDATE users SET role = 'super_admin' WHERE id = <your_user_id>;
   ```

---

### 问题4: 会话总结生成失败

**症状**: 点击"生成总结"按钮后显示错误

**排查步骤**:
1. 检查后端日志:
   ```bash
   grep -i "session.*summary\|LLM" /var/log/llm-gateway/gateway.log | tail -20
   ```

2. 检查LLM配置（session_summary_v2.go 中的LLM endpoint）:
   - 默认调用: `http://localhost:8080/v1/chat/completions`
   - 可能需要配置API key或更换endpoint

3. 临时解决：代码会fallback到简单总结（不调用LLM）

---

## 📊 监控和指标

### 关键指标

1. **写入成功率**:
   ```sql
   -- 预期 > 99%
   SELECT 
     COUNT(*) FILTER (WHERE quality = 'verified') * 100.0 / COUNT(*) as success_rate
   FROM gateway.session_turns
   WHERE ts > NOW() - INTERVAL '1 hour';
   ```

2. **写入延迟**:
   ```sql
   -- 从日志中提取（P99应 < 500ms）
   grep "session_persist" /var/log/llm-gateway/gateway.log | 
     grep "duration_ms" | 
     awk '{print $NF}' | 
     sort -n | 
     tail -20
   ```

3. **数据完整性**:
   ```sql
   -- V1和V2的session count差异应 < 1%
   SELECT 
     (SELECT COUNT(DISTINCT gw_session_id) FROM request_logs WHERE ts > NOW() - INTERVAL '1 hour') as v1_count,
     (SELECT COUNT(DISTINCT session_id) FROM gateway.session_turns WHERE ts > NOW() - INTERVAL '1 hour') as v2_count;
   ```

---

## 🎯 下一步优化

完成基础部署后，可以考虑：

1. **L3缓存读取** (当前仅写入，未读取):
   ```sql
   UPDATE platform_settings SET value = 'true' WHERE key = 'sessions_v2.l3_read';
   ```

2. **双读对账** (验证V1和V2数据一致性):
   ```sql
   UPDATE platform_settings SET value = 'true' WHERE key = 'sessions_v2.dual_read';
   ```

3. **主读切换** (V2作为主数据源):
   ```sql
   UPDATE platform_settings SET value = 'true' WHERE key = 'sessions_v2.primary_read';
   ```

4. **添加到导航菜单**:
   在前端导航栏添加"会话详情"链接

5. **集成到RequestLogsView**:
   在request-logs页面添加"查看会话"按钮，跳转到SessionDetailView

---

## 📚 相关文档

- [Migration 430: Sessions V2 Schema](../llm-gateway-go/sql/migrations/startup/430_sessions_v2_schema.sql)
- [SessionPersistHook Implementation](../llm-gateway-go/domains/session/v2/pipeline_hook.go)
- [Feature Flags README](../llm-gateway-go/scripts/README_sessions_v2.md)
- [API Test Script](../llm-gateway-go/scripts/test_sessions_v2_api.sh)

---

## ✅ 部署检查清单

### 后端
- [ ] 已拉取最新main分支（commit 2918c28b+）
- [ ] 已执行SQL脚本开启feature flags
- [ ] 已重启Gateway服务
- [ ] `session_turns`表有数据写入
- [ ] API `/api/admin/sessions/detail` 可访问
- [ ] API `/api/admin/sessions/summary` 可访问

### 前端
- [ ] 已合并feat/distribution-b-c分支到main
- [ ] 已构建前端（npm run build）
- [ ] 已部署dist到web服务器
- [ ] 页面 `/sessions/:sessionId` 可访问
- [ ] 轮次列表正常显示
- [ ] 抽屉可以打开和切换Tab
- [ ] URL参数定位功能正常
- [ ] 生成总结功能正常

### 生产环境
- [ ] 154环境已部署（1%灰度）
- [ ] 245环境已部署（1%灰度）
- [ ] 监控指标正常（24h观察期）
- [ ] 无错误日志
- [ ] 准备增加到10%灰度

---

**部署完成！** 🎉

如有问题，请查看故障排查章节或联系开发团队。
