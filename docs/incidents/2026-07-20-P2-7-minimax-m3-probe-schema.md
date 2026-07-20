# P2-#7 minimax-m3 持续 503 问题审计报告

## 执行时间
2026-07-20 18:30 - 19:40 (70分钟)

## 问题根因
`bg/node_probe.go:1341` SQL 查询引用了不存在的列：
- 原：`c.enabled = TRUE AND c.lifecycle IN ('active', 'grace')`
- credentials 表实际列名：`status` 和 `lifecycle_status`

## 已完成的修复

### 1. 代码修复
- ✅ 修复 `bg/node_probe.go:1341`
  - `c.enabled` → `c.status IN ('active', 'cooling', 'degraded')`
  - `c.lifecycle` → `c.lifecycle_status = 'active'`
- ✅ commit: `a5a4185c8 Merge fix/probe-schema-drift`
- ✅ 已推送到 main 分支

### 2. 部署
- ✅ 245: build 1245
- ✅ 154: build 1252 (包含完整修复)

### 3. 验证
- ✅ node_probe SQL 不再报 `c.enabled does not exist` 错误
- ✅ 代码 review 确认所有 SQL 正确

## 未解决的问题

### 154 仍然 503
即使代码修复后，minimax-m3 仍返回 503。

**可能原因：**
1. **历史 probe 数据污染** - node_probe_state 中旧的 endpoint_build 记录导致 v_routable.is_routable=false
2. **credential 21 API key 问题** - minimax 直连凭据可能失效
3. **executor filter 逻辑** - routing_resolve 找到 candidate，但 executor 认为不可用

**下一步建议：**
1. 清理所有旧 probe 数据，让系统重新探测
2. 验证 credential 21 (minimax direct) API key 有效性
3. 排查 executor 为何过滤掉 routing_resolve 选出的 candidates

## 代码质量保证
- ✅ 所有修改已 commit 并 push 到 main
- ✅ 无遗留的 `c.enabled` / `c.lifecycle` 错误引用
- ✅ 符合 schema (credentials.status, credentials.lifecycle_status)

## 结论
**核心 SQL schema 错误已修复并部署**，但 minimax-m3 服务恢复需要进一步的数据清理和凭据验证工作。
