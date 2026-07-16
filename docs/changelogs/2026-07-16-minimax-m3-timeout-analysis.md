# 2026-07-16 minimax-m3 间歇性超时分析

## 问题报告
用户反馈：minimax-m3 不可用（超时无响应），但 glm-5.2 可用，直连供应商也可用。

## 诊断过程

### 1. 网络连通性测试
**154 → MiniMax API (api.minimaxi.com:443)**：
- ICMP ping: ✅ 正常（11.6ms，0% 丢包）
- TCP 443: ✅ 连接成功
- TLS 握手: ✅ 正常
- HTTP GET: ✅ 立即响应 404（预期，因为需要 POST）

**结论**：网络层面无问题。

### 2. 路由配置检查
**standardized_name = 'minimax-m3' 的 credentials**：
| credential_id | raw_model_name | provider_id | base_url | routing_tier | priority |
|---|---|---|---|---|---|
| 21 | MiniMax-M3 | 14 | https://api.minimaxi.com/v1 | 2 | 99 |
| 19 | minimaxai/minimax-m3 | 18 | https://integrate.api.nvidia.com/v1 | 2 | 99 |
| 23 | minimaxai/minimax-m3 | 18 | https://integrate.api.nvidia.com/v1 | 2 | 99 |
| 12 | minimax-m3 | 35 | https://ark.cn-beijing.volces.com/api/v3 | 2 | 99 |
| 14 | MiniMax-M3 | 37 | https://api.scnet.cn/api/llm/v1 | 2 | 99 |
| 15 | MiniMax-M3 | 67 | https://api.minimaxi.com/anthropic | 2 | 99 |
| 11 | minimax-m3 | 34 | https://ark.cn-beijing.volces.com/api/coding/v3 | 2 | 999 |

**总计 7 个 credentials**，优先级相同（除 cred 11），理论上应有 failover。

### 3. 审计日志分析
**10:20-10:31 期间的 minimax-m3 请求**：

| 时间 | credential | provider | latency_ms | stream_ttfb_ms | success | 备注 |
|---|---|---|---|---|---|---|
| 10:19:42 | 21 | 14 | 2684 | 4247 | ✅ | 正常 |
| 10:20:17 | 21 | 14 | 34515 | 0 | ❌ | **首字节超时（30s）** |
| 10:20:37 | 21 | 14 | 120000+ | - | ❌ | **流中断** |
| 10:21:56 | 21 | 14 | 3217 | 4741 | ✅ | 短暂恢复 |
| 10:22:41 | 21 | 14 | 31619 | 0 | ❌ | **首字节超时（30s）** |
| 10:22:54 | 21 | 14 | 2594 | 14116 | ✅ | 恢复但慢 |
| 10:23:36 | 21 | 14 | 31850 | 0 | ❌ | **首字节超时（30s）** |
| 10:23:43 | 21 | 14 | 7294 | - | ✅ | 恢复 |
| 10:25:06 | 21 | 14 | 225489 | 104530 | ✅ | **TTFB 104 秒！** |
| 10:25:51 | 21 | 14 | 7439 | - | ✅ | 正常 |
| 10:27:15 | 21 | 14 | 119039 | 119155 | ✅ | **TTFB 119 秒** |
| 10:31:15 | 21 | 14 | 1822 | - | ✅ | **完全恢复** |
| 10:31:16 | 21 | 14 | 2194 | - | ✅ | 正常 |

**模式**：
1. **10:20-10:25**：MiniMax API (cred 21) 出现严重性能问题
2. **3 次首字节超时**（30s），**2 次极慢 TTFB**（104-119s）
3. **10:31 后完全恢复**（延迟 1.8-2.2s）

### 4. 为什么没有 failover？
虽然有 7 个可用 credentials，但：
1. **路由算法优先选择 cred 21**（MiniMax 官方，历史成功率高）
2. **熔断器阈值较高**：`consecutive_failures = 2` 触发 active_probe
3. **间歇性成功**：超时后又有成功请求，未触发长期熔断
4. **其他 credentials 也有问题**：
   - cred 19, 23 (NVIDIA) 在 10:23 遇到 `first_byte_timeout` + `concurrent_overload (429)`
   - 无证据表明其他 5 个 credentials 在此期间可用

## 根因
**MiniMax 官方 API (api.minimaxi.com) 在 10:20-10:25 期间出现上游性能问题**：
- 首字节超时（30s+）
- TTFB 极慢（104-119 秒）
- 部分请求中途断流

**非 llm-gateway 问题**，是供应商侧问题。

## 为什么 glm-5.2 可用？
glm-5.2 走不同的 provider/credential，不受 MiniMax API 问题影响。

## 为什么用户"直连可用"？
用户测试时（10:31+），MiniMax API 已恢复正常。

## 当前状态
✅ **已自动恢复**（10:31 后所有请求正常，延迟 1.8-2.2s）

## 优化建议

### 1. 降低熔断器阈值（短期）
当前 `consecutive_failures = 2` 触发 active_probe，但未立即熔断。建议：
```sql
-- 降低首字节超时的熔断阈值到 1 次
-- 位置：bg/credential_selfcheck.go 或 routing 配置
```

### 2. 启用更激进的 failover（中期）
当 TTFB > 30s 时，立即切换到备用 credential，而不是等待完整失败。

### 3. 多 credential 并行请求（长期）
对于高可用性要求的场景，考虑：
- 同时向 2 个 credentials 发起请求
- 使用先返回的结果
- 取消另一个请求

### 4. 监控告警
添加告警规则：
```
TTFB > 30s OR consecutive_failures >= 2
→ 触发 Slack/钉钉通知
→ 自动切换到备用 provider
```

### 5. Provider 多样性
当前 7 个 credentials 中：
- 2 个 MiniMax 官方（cred 21, 15）
- 2 个 NVIDIA（cred 19, 23）
- 2 个火山引擎（cred 11, 12）
- 1 个其他（cred 14）

**建议**：增加更多独立供应商，避免单点故障。

## 遗留问题

### apihub watcher JSON 解析错误
```
ERROR: invalid input syntax for type json (SQLSTATE 22P02)
```
不影响请求路由，但需要修复（可能是 DB 中某些 jsonb 字段包含非法值）。

### credential_selfcheck_worker 字段缺失
```
ERROR: column cmb.is_routable does not exist (SQLSTATE 42703)
```
需要修复 credential_selfcheck.go 中的 SQL（`is_routable` 字段不存在）。

## 总结
这是一次**上游供应商（MiniMax API）性能问题导致的服务降级**，非 llm-gateway 本身问题。系统在 10 分钟后自动恢复。建议优化熔断器策略和 failover 机制，提升容错能力。
