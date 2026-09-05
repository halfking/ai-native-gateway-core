---
archived_from: (legacy) docs/archive/2026-07/DEPLOYMENT_CHECKLIST.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# 部署前检查清单

**日期**: 2026-07-19  
**目标**: 154 生产服务器  
**修复内容**: Phase 1 (probe-direct) + 泳道跳变 (方案 B + C)

---

## ✅ 部署前验证

### 1. 代码编译 ✅
```bash
go build -o /tmp/llm-gateway-go ./cmd/gateway
# 结果: ✅ 编译成功，无错误
```

### 2. 单元测试 ✅
```bash
# Phase 1 测试
go test ./bg -run "TestProbeResultContainsRequestResponseBodies|TestProbeEmitterBuildsEntryWithBodies"
# 结果: ✅ 2/2 通过

# 方案 C 测试
go test ./admin -run "TestNeedsFullRefresh|TestAbsInt"
# 结果: ✅ 8/8 通过
```

### 3. Git 状态 ✅
```bash
git log --oneline -5
# 结果: ✅ 所有修改已提交
```

---

## 🚀 部署命令

```bash
# 标准部署（推荐）
bash scripts/deploy-154.sh

# 部署选项
# --seq 1144              # 指定构建序号
# --no-frontend           # 仅后端（不推荐）
# --direct                # 直连 154（应急）
```

**SSH 连接**: 默认通过 252 跳板机 → 154

---

## 📊 部署后验证

### 验证 1: Phase 1 数据完整性

**SSH 到 252 服务器**:
```bash
ssh root@115.29.212.252 -p 25022
psql -h localhost -p 5432 -U llm_gateway -d llm_gateway
```

**运行 SQL 查询**:
```sql
-- 查询最近的 probe-direct 请求
SELECT 
    request_id,
    ts,
    length(request_body::text) as req_len,
    length(response_body::text) as resp_len,
    prompt_tokens,
    completion_tokens,
    total_tokens
FROM request_logs_hot
WHERE request_id LIKE 'probe-direct-%'
  AND ts > NOW() - INTERVAL '1 hour'
ORDER BY ts DESC
LIMIT 10;
```

**期望结果**:
- ✅ `req_len > 0` (有请求体)
- ✅ `resp_len > 0` (有响应体)
- ✅ `prompt_tokens > 0` (有 token 统计)
- ✅ `completion_tokens > 0`
- ✅ `total_tokens = prompt_tokens + completion_tokens`

**失败场景**:
- ❌ `req_len = 0` 或 `NULL` → Phase 1 未生效，检查部署
- ❌ `prompt_tokens = 0` → 数据提取失败，查看日志

---

### 验证 2: 泳道跳变修复

**打开前端页面**: https://llm.kxpms.cn/admin/live-stream

**打开浏览器开发者工具 (F12)**，粘贴以下代码：

```javascript
// 监控全量快照推送
const es = new EventSource('/api/admin/live-stream');
let lastRefresh = Date.now();
let refreshCount = 0;

es.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'snapshot_refresh') {
        const now = Date.now();
        const interval = (now - lastRefresh) / 60000;  // 分钟
        refreshCount++;
        console.log(`🔄 第 ${refreshCount} 次全量快照`, 
                    `距上次 ${interval.toFixed(1)} 分钟`, 
                    new Date().toLocaleTimeString());
        lastRefresh = now;
    }
};

console.log('✅ 监控已启动，请保持此页面打开 2-4 小时');
```

**观察 2-4 小时**:

**期望结果**:
- ✅ 第一次 `🔄` 立即出现（首次推送）
- ✅ 第二次 `🔄` 出现时，间隔约为 **120 分钟**（方案 B 生效）
- ✅ 如果流量稳定，之后**长时间不出现** `🔄`（方案 C 生效）
- ✅ 只在流量突变时才出现 `🔄`

**失败场景**:
- ❌ 间隔仍然是 30 分钟 → 方案 B 未生效，检查部署
- ❌ 流量稳定时仍频繁出现 `🔄` → 方案 C 未生效

---

### 验证 3: 服务器日志监控

**SSH 到 154 服务器**:
```bash
ssh -J root@115.29.212.252:25022 root@47.97.111.154
```

**实时监控日志**:
```bash
# 监控智能推送日志
tail -f /var/log/llm-gateway-go/*.log | grep "snapshot refresh"

# 或者查看最近的日志
journalctl -u llm-gateway-go -f | grep "snapshot"
```

**期望看到**:
```
[DEBUG] snapshot refresh skipped: no significant changes old_total=1234 new_total=1245
[DEBUG] snapshot refresh skipped: no significant changes old_total=1245 new_total=1250
... (大部分时候)

[DEBUG] snapshot refresh needed: total count changed old=1000 new=1300 diff_pct=30.0
[DEBUG] snapshot refresh needed: lane count changed dimension=vendor old_count=3 new_count=4
... (流量突变时)
```

**失败场景**:
- ❌ 没有看到任何 "snapshot refresh" 日志 → 日志级别太高，调整为 DEBUG
- ❌ 总是看到 "needed" → 判断逻辑有问题

---

## 🔄 回滚方案

如果部署后发现问题：

### 快速回滚
```bash
# 方法 1: 切换回上一个版本
ssh root@154
cd /opt/llm-gateway-go
ln -snf releases/<previous-seq> current
systemctl restart llm-gateway-go
```

### Git 回滚
```bash
# 方法 2: 回滚代码并重新部署
git revert HEAD~2..HEAD  # 回滚最近 2 个 commits
bash scripts/deploy-154.sh
```

### 临时禁用智能推送
如果只想禁用方案 C，保留方案 B：
```bash
# SSH 到 154
ssh root@154

# 编辑代码（临时修复）
cd /opt/llm-gateway-go/current
# 注释掉 needsFullRefresh 调用
# 重启服务
systemctl restart llm-gateway-go
```

---

## 📞 问题排查

### 问题 1: 部署失败

**症状**: `deploy-154.sh` 报错

**排查**:
```bash
# 检查 SSH 连接
ssh root@115.29.212.252 -p 25022 echo "OK"

# 检查磁盘空间
ssh root@154 df -h

# 检查服务状态
ssh root@154 systemctl status llm-gateway-go
```

---

### 问题 2: Phase 1 数据仍然缺失

**症状**: SQL 查询显示 `req_len = 0`

**排查**:
```bash
# 检查服务日志
ssh root@154
journalctl -u llm-gateway-go --since "10 minutes ago" | grep -i "probe-direct"

# 检查代码版本
ssh root@154
cd /opt/llm-gateway-go/current
git log --oneline -3
# 应该看到 Phase 1 的 commit
```

---

### 问题 3: 泳道仍然跳变

**症状**: 前端每 30 分钟仍有跳变

**排查**:
```bash
# 检查配置
ssh root@154
journalctl -u llm-gateway-go --since "1 hour ago" | grep -i "SnapshotRefreshInterval"

# 应该看到: SnapshotRefreshInterval=2h0m0s
# 如果是 30m0s，说明方案 B 未生效
```

---

## 📋 验收标准

部署成功的标志：

- [ ] Phase 1: probe-direct 请求有完整的 request_body / response_body / tokens
- [ ] 方案 B: 全量快照推送间隔变为 120 分钟
- [ ] 方案 C: 流量稳定时不再频繁推送全量快照
- [ ] 服务正常运行，无错误日志
- [ ] 前端泳道图不再频繁跳变

**全部通过后，视为部署成功 ✅**

---

**准备时间**: 2026-07-19  
**负责人**: AI Agent  
**预计部署时长**: 15 分钟  
**预计验证时长**: 2-4 小时

