# 245服务器CST时区标准化部署验证报告

**部署时间**: 2026-11-19 23:46-23:49 CST  
**验证时间**: 2026-11-19 23:52 CST  
**目标服务器**: 8.136.114.245:25022  
**部署版本**: v2.6.1-779dc8b1-20261119-3134 (seq=3134)

---

## ✅ 部署结果

### 1. 版本信息
- **版本**: v2.6.1  
- **Git SHA**: 779dc8b1  
- **构建序列**: 3134  
- **构建日期**: 20261119  

### 2. Migration应用情况

所有3个Migration已成功应用：

#### Migration 990: stats_consistency_fixes
- ✅ 添加session_summaries缺失字段（total_credits_charged, TTFT统计）
- ✅ 添加stats_usage_daily成本分解字段
- ✅ 更新update_session_summary()触发器
- ✅ 创建stats_consistency_check视图

#### Migration 991: stats_timezone_standardization  
- ✅ 创建utc_today()函数
- ✅ 创建utc_days_ago(n)函数
- ✅ 创建utc_day_start(ts)函数

#### Migration 992: add_cst_timezone_functions (本轮核心)
- ✅ 创建cst_today()函数 - 返回当前CST日起点（北京时间00:00）
- ✅ 创建cst_days_ago(n)函数 - 返回N天前的CST日起点
- ✅ 创建cst_day_start(ts)函数 - 返回给定时间戳的CST日起点
- ✅ 创建cst_date(ts)函数 - 返回给定时间戳的CST日期

### 3. 系统健康检查

#### 服务状态
```json
{
  "status": "ok",
  "version": "2.6.1-779dc8b1-20261119-3134",
  "git_sha": "779dc8b1",
  "build_seq": 3134,
  "build_date": "20261119",
  "ready": true
}
```

#### 端点验证
- ✅ `/api/system/version` - 返回正确版本信息
- ✅ `/healthz` - 健康检查通过
- ✅ `/readyz` - 就绪检查通过

#### 数据库连接
- ✅ DB就绪（1秒，authenticated background-tasks=200）

#### 凭据验证
- ✅ 凭据解密冒烟测试通过
  - providers: 14, 18, 13092
  - credentials: 17
  - failed: 0

#### Admin密码同步
- ✅ 密码已从env同步到users表
- ✅ 登录验证HTTP 200

### 4. 蓝绿部署

#### 切换详情
- **旧版本**: 3086-7ca52d0b (端口8781)
- **新版本**: 3134-779dc8b1 (**端口8782，已激活**)
- **切换时间**: 24秒
- **Nginx路由**: 已切换到候选端口8782
- **路由验证**: 20次独立连接全部通过

#### 回滚能力
- ✅ 旧实例保留，可随时回滚
- 回滚命令: `bash scripts/deploy-seamless.sh rollback 245`

---

## 📊 时区标准化影响

### 受影响的API端点

以下端点的"今日"和"最近N天"定义已统一为CST时区：

1. **对账页面** (`/admin/reconciliation`)
   - 供应商对账统计
   - 内部对账统计
   - 时间范围查询

2. **看板页面** (`/dashboard`)
   - 今日请求数
   - 最近7天趋势
   - 成功率统计

3. **请求日志页面** (`/request-logs`)
   - 请求列表筛选
   - 状态统计
   - 时间范围查询

### 时区语义变更

**修改前**：
- 不同API使用不同时区处理（CURRENT_DATE/本地时区/UTC/Asia/Shanghai硬编码混用）
- "今日"定义不一致

**修改后**：
- 统一使用CST（Asia/Shanghai UTC+8）时区
- "今日" = CST当日00:00开始（北京时间00:00，等价于UTC前一日16:00）
- "最近7天" = 从CST今日00:00回溯7个CST日

**示例**（2026-11-19 23:52 CST）：
- `cst_today()` → 2026-11-19 00:00:00+08 (UTC 2026-11-18 16:00:00)
- `cst_days_ago(7)` → 2026-11-12 00:00:00+08 (UTC 2026-11-11 16:00:00)

---

## ✅ 验证清单

### 部署层面
- [x] 版本号正确（3134-779dc8b1）
- [x] Migration 990/991/992已应用
- [x] 服务健康检查通过
- [x] 数据库连接正常
- [x] 凭据解密正常
- [x] Admin密码同步成功
- [x] 蓝绿切换成功
- [x] Nginx路由验证通过（20次连接）
- [x] 旧版本保留，回滚就绪

### 代码层面（已在开发环境验证）
- [x] 13个admin/*.go文件已标准化
- [x] 32处CST函数调用
- [x] 0处残留（CURRENT_DATE/utc_xxx/裸Asia/Shanghai）
- [x] 编译测试通过
- [x] 完整文档已生成

---

## 📋 后续验证步骤

### 需要人工验证的项目

由于需要登录凭据和业务数据，以下项目需要通过浏览器人工验证：

1. **访问三个页面，验证数据一致性**：
   - https://llm-gw-245.itestu.cn/admin/reconciliation?view=provider
   - https://llm-gw-245.itestu.cn/dashboard
   - https://llm-gw-245.itestu.cn/request-logs

2. **对比验证项**（相同时间范围和状态条件下）：
   - "今日"总请求数在三个页面应该一致
   - "最近7天"趋势数据应该对齐
   - 按模型分组的统计数据应该一致
   - 成功/失败状态的计数应该匹配

3. **跨日边界测试**（可选，建议在23:50-00:10窗口测试）：
   - 验证CST零点切换时数据归属正确
   - 对比UTC零点（8:00）与CST零点（0:00）的数据差异

---

## 🔄 回滚方案

如果发现数据不一致或其他问题，可立即回滚：

```bash
# SSH到245服务器
ssh -p 25022 root@8.136.114.245

# 执行回滚
cd /opt/llm-gateway-go
bash scripts/deploy-seamless.sh rollback 245
```

回滚后系统将恢复到：
- 版本: 3086-7ca52d0b
- 端口: 8781
- Migration: 990/991/992的效果会保留（数据库不回滚）

---

## 📈 预期效果

### 数据一致性提升
- reconciliation、dashboard、request-logs三个页面的"今日"/"最近N天"统计将完全一致
- 消除因时区混用导致的数据差异

### 用户体验改善
- 所有统计与用户认知的"今天""昨天"对齐（北京时间）
- 跨日边界行为可预测

### 技术债清理
- 消除CURRENT_DATE（本地时区依赖）
- 消除硬编码的Asia/Shanghai
- 统一时区处理口径

---

## 📚 参考文档

- 完整变更文档: `llm-gateway-go/docs/STATS_CST_TIMEZONE_MIGRATION.md`
- 工作总结: `llm-gateway-go/docs/STATS_CST_STANDARDIZATION_SUMMARY.md`
- Migration定义: `llm-gateway-go/sql/migrations/startup/992_add_cst_timezone_functions.sql`
- 部署日志: `llm-gateway-go/docs/db-changelog.md`

---

**验证人**: AI Assistant (Kiro)  
**验证状态**: ✅ 部署成功，服务正常，等待业务层数据一致性人工验证  
**报告版本**: v1.0  
**生成时间**: 2026-11-19 23:52 CST
