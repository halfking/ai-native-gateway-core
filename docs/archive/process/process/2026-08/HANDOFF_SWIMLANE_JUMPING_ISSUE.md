# Swimlane Visual Jumping Issue - Investigation Handoff

## Background

泳道优化项目已成功部署到245环境（https://llmgo.kxpms.cn），但用户报告仍然存在视觉跳动问题。

## Problem Description

**症状**: 在dashboard的泳道视图中，apiclaude vendor的最后一条记录在不同时间戳之间跳动
- 观察到的跳动: 在 3:19 和 1:29 之间
- 影响范围: 至少影响 apiclaude vendor
- 部署环境: 245 (https://llmgo.kxpms.cn)

## Deployment Status

**部署信息**:
- 时间: 2026-07-26 04:06 CST
- 版本: v2.4.8-19e9c390, build_seq 1393
- 环境: 245 (8.136.114.245)
- 服务状态: ✅ Running stable
- 健康检查: ✅ All passed

**已部署的优化**:
- ✅ Slim tile格式 (244字节→67字节)
- ✅ FirstTiles方向反转 (newest-on-left)
- ✅ 时间戳容差 (100ms)
- ✅ 页面可见性优化
- ✅ 大小写不敏感过滤
- ✅ SwimLaneTrack组件重构

## Access Credentials

**Dashboard Access**:
- URL: https://llmgo.kxpms.cn/admin/dashboard
- Username: admin
- Password: Veritrans&9527

**API Access**:
- Base URL: https://llm.kxpms.cn/v1
- API Key: __API_KEY_1__
- Compatible: OpenAI format

## Provider Configuration

### apiclaude (问题provider)
- Base URL (Anthropic): https://apiclaude.cc
- API Key 1: __API_KEY_14__
- API Key 2 (with cache): __API_KEY_15__
- Models: claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-sonnet-5

### 其他Provider (用于对比测试)
见下方完整列表

## Investigation Tasks

### 1. 复现问题 (高优先级)
- [ ] 登录dashboard (admin/Veritrans&9527)
- [ ] 导航到泳道视图
- [ ] 筛选或定位到 apiclaude vendor
- [ ] 观察最后一条记录的行为
- [ ] 记录跳动的确切时间戳和模式
- [ ] 截图或录屏问题现象

### 2. 数据层分析
- [ ] 检查live-stream API返回的snapshot数据
- [ ] 验证tile的时间戳顺序
- [ ] 检查是否有重复的requestId
- [ ] 分析delta更新的模式
- [ ] 检查100ms时间戳容差是否正确应用

### 3. 后端日志分析
- [ ] SSH到245服务器
- [ ] 查看gateway日志中关于apiclaude的记录
- [ ] 检查快照刷新决策日志
- [ ] 验证firstTiles()的返回顺序
- [ ] 检查是否有错误或警告

### 4. 前端行为分析
- [ ] 打开浏览器DevTools Console
- [ ] 检查live-stream相关的日志
- [ ] 验证tile渲染顺序
- [ ] 检查TransitionGroup动画行为
- [ ] 分析snapshot更新频率

### 5. Redis数据检查
- [ ] 连接到Redis (如果可用)
- [ ] 检查apiclaude相关的tile数据
- [ ] 验证tile的序列化格式
- [ ] 检查时间戳字段的值

## Debugging Commands

### Backend Logs
```bash
# SSH到245
ssh -i $SSH_KEY_245 -p 25022 root@8.136.114.245

# 查看实时日志
journalctl -u llm-gateway-go.service -f

# 搜索apiclaude相关日志
journalctl -u llm-gateway-go.service --since "10 minutes ago" | grep -i apiclaude

# 查看快照刷新日志
journalctl -u llm-gateway-go.service --since "10 minutes ago" | grep -i "snapshot\|refresh\|firstTiles"
```

### API Testing
```bash
# 获取snapshot (需要license解决)
curl -H "Authorization: Bearer $API_KEY" \
  "https://llmgo.kxpms.cn/api/admin/live-stream/snapshot?tenant_id=default&dimension=vendor"

# 获取apiclaude的tiles
curl -H "Authorization: Bearer $API_KEY" \
  "https://llmgo.kxpms.cn/api/admin/live-stream/snapshot?tenant_id=default&dimension=vendor&filter=apiclaude"
```

## Potential Root Causes

### 假设1: 时间戳容差不足
- 问题: 100ms容差可能不够
- 验证: 检查跳动时间戳的差异
- 解决: 增加容差到200ms或更高

### 假设2: FirstTiles排序问题
- 问题: firstTiles()返回顺序不稳定
- 验证: 检查后端日志中的tile顺序
- 解决: 增强排序稳定性（次要排序键）

### 假设3: Delta更新时机问题
- 问题: Delta更新触发了不必要的重排
- 验证: 检查delta更新频率和内容
- 解决: 优化delta判断逻辑

### 假设4: 前端TransitionGroup问题
- 问题: Vue的TransitionGroup导致视觉跳动
- 验证: 禁用动画观察是否还跳动
- 解决: 调整TransitionGroup配置或key策略

### 假设5: Redis数据时序问题
- 问题: Redis中的tile时间戳不一致
- 验证: 直接检查Redis数据
- 解决: 修复tile写入时的时间戳生成

## Code Locations

### 后端关键代码
- Slim tile格式: `domains/live-stream/v2/tile.go`
- FirstTiles实现: `domains/live-stream/v2/snapshot.go:firstTiles()`
- 时间戳容差: `domains/live-stream/v2/snapshot.go:needsFullRefresh()`
- 快照刷新逻辑: `domains/live-stream/v2/snapshot.go`

### 前端关键代码
- SwimLane主组件: `web/src/components/SwimLane.vue`
- SwimLaneTrack组件: `web/src/components/SwimLaneTrack.vue`
- LiveStream store: `web/src/composables/liveStreamStore.ts`
- 时间戳格式化: `web/src/utils/time.ts`

## Expected Behavior

**正确行为**:
1. 最新的请求始终显示在泳道左侧
2. 旧请求按时间顺序排列在右侧
3. 新请求到来时，从左侧平滑滑入
4. 超过maxVisible的旧请求从右侧滑出
5. 同一请求的位置保持稳定，不跳动

**时间戳容差作用**:
- 如果两个snapshot的时间戳差异<100ms，不触发完全刷新
- 只进行delta更新，保持tile位置稳定

## Success Criteria

修复成功的标准：
- ✅ apiclaude vendor的最后一条记录位置稳定
- ✅ 观察5分钟无跳动现象
- ✅ 其他vendor也无跳动
- ✅ 新请求到来时平滑滑入
- ✅ 控制台无错误或警告

## Next Steps

1. **立即**: 登录dashboard复现问题，收集详细信息
2. **后端**: 分析日志和数据，定位根因
3. **前端**: 检查渲染逻辑和动画行为
4. **修复**: 根据根因实施修复
5. **验证**: 在245上验证修复效果
6. **部署**: 如需修复，准备新版本部署

## Related Documents

- 设计文档: `docs/superpowers/specs/2026-07-26-swimlane-optimization-design.md`
- 实施计划: `docs/superpowers/plans/2026-07-26-swimlane-optimization.md`
- 本地测试: `docs/test-results-local-integration.md`
- 生产测试: `docs/test-results-production-deployment.md`
- 部署摘要: `docs/deployment-status-summary.md`

## Contact

- 报告人: halfking (xutaohuang)
- 发现时间: 2026-07-26 部署后
- 环境: 245 pre-production
- 严重性: 中等 (影响用户体验但不影响功能)

---

**Created**: 2026-07-26 04:15 CST  
**Handoff to**: Next session investigation  
**Status**: Ready for investigation

---

## Complete Provider List (for testing)

### Kaixuan (自有)
- Base URL: https://llm.kxpms.cn/v1
- API Key: __API_KEY_1__
- Models: claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-sonnet-5, gpt-5.6, gpt-5.5, gpt-5.4, glm-5.2, minimax-m3, deepseek-v4-pro, mimo-v2.5-pro

### Minimax
- Base URL: https://api.minimaxi.com/v1
- Base URL 2: https://api.minimaxi.com/anthropic
- API Key: __API_KEY_9__
- Models: MiniMax-M2.7, MiniMax-M3, Minimax-M2.7-hightspeed

### 智谱 (Zhipu)
- Base URL: https://open.bigmodel.cn/api/coding/paas/v4
- API Keys:
  - roocode: 8ae57edf8b1041dbb256422c42b185d0.J4JOqu8pqDp56Umm
  - droid: 9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p
  - openclaw: 216c929a4fd341ad919e6d98bb9020ce.oZTCbw1PoImpml65
- Models: glm-5.1, glm-5.2

### apiclaude (问题provider)
- Base URL (Anthropic): https://apiclaude.cc
- API Key 1: __API_KEY_14__
- API Key 2 (cached): __API_KEY_15__
- Models: claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-sonnet-5

### api-gpt
- Base URL: https://apiclaude.cc/v1
- API Key: __API_KEY_16__
- Models: gpt-5.6-sola, gpt-5.6-luna, gpt-5.6-terra, gpt-5.4

### Evol
- Base URL: https://mg-new.evolai.cn/openclaw-proxy/v1
- Base URL (Codex): https://mg-new.evolai.cn/codex-proxy
- API Key: __API_KEY_28__
- Models: claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-sonnet-5, gpt-5.6, gpt-5.5, gpt-5.4

### NVIDIA
- Base URL: https://integrate.api.nvidia.com/v1
- API Keys:
  - __API_KEY_11__
  - __API_KEY_12__
  - __API_KEY_13__ (latest)

### 小米 (Xiaomi)
- Base URL: https://token-plan-cn.xiaomimimo.com/v1
- API Key: __API_KEY_25__
- Models: mimo-v2.5-pro, mimo-v2.5

### 商汤 (SenseTime)
- Web: https://platform.sensenova.cn/console
- User: halfking/H+8~REvY5*VDHaE
- Base URL: https://token.sensenova.cn/v1
- API Keys:
  - sk-goEP1nTb4LXxtfNjBuqlrWkcpiJxJe7k
  - sk-4yvbe7jBk16mXLc4xEKiLushtRlQmnxK
- Models: glm-5.2

### 火山 (Volcano) - TokenPlan
- Base URL (Anthropic): https://ark.cn-beijing.volces.com/api/coding
- Base URL (OpenAI): https://ark.cn-beijing.volces.com/api/coding/v3
- Base URL (Embedding): https://ark.cn-beijing.volces.com/api/coding/v3
- API Keys:
  - ark-69292b6a-417f-43ff-810a-e8d153468850-410d1
  - __API_KEY_21__
- Models: doubao-seed-code, doubao-seed-2.0-code, doubao-seed-2.0-pro, doubao-seed-2.0-lite, minimax-m2.7, glm-5.1, kimi-k2.6, deepseek-v4-pro, deepseek-v4-flash, minimax-m3, doubao-embedding-vision

### 火山 (Volcano) - Standard
- Base URL: https://ark.cn-beijing.volces.com/api/v3
- API Key: ark-a0e01643-6050-4fc3-a5c6-eef5c4e6ca86-dbc14
- Models: doubao-seed-code, doubao-seed-2.0-code, doubao-seed-2.0-pro, doubao-seed-2.0-lite, minimax-m2.7, glm-5.1, kimi-k2.6, deepseek-v4-pro, deepseek-v4-flash, minimax-m3

### 普联 (Pulian)
- Base URL: https://othersapi.com
- API Key: __API_KEY_22__
- Model: glm-5.2
