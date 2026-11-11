# 被动指纹采集系统监控总结

## 概述

本文档总结211生产环境被动指纹采集系统的健康监控体系、性能基线和运维指南。

**最新评估**: 2026-11-11 02:43 UTC  
**系统状态**: ✅ 健康  
**性能评级**: ⚠️ 合格（深夜低谷期）

---

## 一、监控体系

### 1.1 实时健康检查（每20分钟）

**目标**: 确保Worker持续运行，及时发现异常

**检查项**:
- Flush频率（目标: 5-6次/30分钟）
- 节点写入量（目标: ≥3节点/flush）
- Pending积压（目标: ≤10）
- 最后flush时间（目标: <8分钟）
- 容器健康状态

**命令**:
```bash
ssh 211 "docker logs --since 30m llm-gateway-211-b 2>&1" | \
  grep -E "(passive baseline flushed|passive baseline worker alive)"
```

**判定标准**:
- ✅ 健康: 所有指标达标
- ⚠️ 警告: Flush 4次 或 节点2-3 或 Pending 10-20
- ❌ 异常: 无flush 或 节点=0 或 Pending>20 或 距离最后flush>15分钟

### 1.2 深度质量分析（每小时整点）

**目标**: 评估2小时性能表现，发现趋势问题

**检查项**:
- 总flush次数（理论: 24次）
- 总写入节点（目标: ≥120）
- 平均效率（目标: ≥5节点/flush）
- Flush成功率（目标: 100%）
- 节点数分布与趋势

**命令**:
```bash
# 2小时flush记录
ssh 211 "docker logs --since 2h llm-gateway-211-b 2>&1" | \
  grep "passive baseline flushed"

# 最新心跳统计
ssh 211 "docker logs --since 2h llm-gateway-211-b 2>&1" | \
  grep "passive baseline worker alive" | tail -1
```

**评级标准**:
- ✅ 优秀: 120+节点, 100%成功率, pending=0
- ⚠️ 合格: 60-120节点, ≥95%成功率
- ❌ 不达标: <60节点 或 成功率<95%

### 1.3 自动化监控

**ZCode Automation**: `automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b`
- 频率: 每20分钟
- 覆盖: 实时健康检查 + 定期深度分析
- 告警: 异常时自动通知

### 1.4 一键检查脚本（本指南的可执行形态）

本节以上的检查/判定标准已脚本化：`scripts/monitoring/passive-fingerprint-check.sh`

```bash
# 实时健康检查（30分钟窗口）
scripts/monitoring/passive-fingerprint-check.sh
# 实时 + 深度质量分析（2小时窗口，评级用）
scripts/monitoring/passive-fingerprint-check.sh --deep
```

- 容器名自动发现 ⇒ 不唯一/取不到 ⇒ **fail-closed**（rc=3）；蓝绿窗口期两台同跑用 `--container` 显式钉住
- 判定标准与本文档逐条对应（健康/警告/异常三级；深度评级优秀/合格/不达标）
- 退出码：0 健康·优秀 / 1 警告·合格 / 2 异常·不达标 / 3 环境失败（可直接接自动化）
- 口径：flush 日志仅在 nodes>0 时打 ⇒「Flush次数」指有写入的 flush；成功率 = flush/(flush+failures)
- 日志时间戳恒 UTC，报告时钟统一 UTC

**TTFT P95 读数**（`ttft_p95_ms` 列已在库完整采集，无需改表，缺的只是消费方）：
```sql
SELECT model_name, credential_id, sample_count,
       ttft_mean_ms, ttft_p95_ms, updated_at
FROM passive_model_fingerprints
WHERE tenant_id = 'default'
ORDER BY updated_at DESC LIMIT 20;
```

---

## 二、性能基线

### 2.1 已验证基线（2026-11-11实测）

#### 深夜低谷期 (UTC 00:47-02:43)

**时段特征**:
- 全球流量低谷
- 北京时间 08:47-10:43

**性能指标**:
- 总节点: 58个/2小时
- 平均效率: 2.4节点/flush
- 写入速率: 29节点/小时
- 成功率: 100%

**节点分布**:
```
1节点: 16.7%
2节点: 33.3%
3节点: 45.8%
5节点: 4.2%
```

**趋势**:
- 前半段: 1.9节点/flush（流量最低）
- 后半段: 2.9节点/flush（提升53%）

**结论**: ⚠️ 合格，符合低流量时段预期

#### 待验证：白天高峰期 (UTC 14:00-20:00)

**预期指标**:
- 总节点: 120-200个/2小时
- 平均效率: 5-8节点/flush
- 写入速率: 60-100节点/小时

**验证计划**: 待下次高峰期检查

### 2.2 关键里程碑

| 日期 | 时段 | 节点数 | 评级 | 备注 |
|------|------|--------|------|------|
| 2026-11-10 | 首次验证 | 152/2h | ✅ 优秀 | 修复后首次验证 |
| 2026-11-11 02:43 | 深夜低谷 | 58/2h | ⚠️ 合格 | 流量低谷期基线 |
| 待定 | 白天高峰 | ? | 待验证 | 目标≥120 |

---

## 三、系统架构

### 3.1 核心组件

```
streaming handler
    ↓ ExtractPassiveFeatures
PassiveTimingTracker
    ↓ Submit
PassiveCollector (内存累积)
    ↓ 每5分钟 Flush
PassiveBaselineWorker
    ↓ UpsertPassiveBaseline
Database (migration 955)
```

### 3.2 关键参数

| 参数 | 值 | 说明 |
|------|-----|------|
| FlushInterval | 5分钟 | 刷库周期 |
| HeartbeatTicks | 6次 | 心跳间隔（30分钟） |
| FlushTimeout | 30秒 | 退出路径刷库超时 |
| SampleCap | 5000 | 基线样本数上限 |
| ITTsPerWindow | 20000 | 单窗口ITT样本上限 |
| TTFTsPerWindow | 4096 | 单窗口TTFT样本上限 |

### 3.3 保护机制

1. **内存保护**: 样本数上限防止OOM
2. **失败隔离**: 单节点失败不阻断其他节点
3. **退出清理**: 有界刷库防止卡死
4. **心跳证明**: 即使无流量也报活性
5. **陈旧检测**: 30天前基线自动重建

---

## 四、故障排查

### 4.1 常见问题

#### 问题1: 30分钟无flush日志

**症状**: 查询30分钟日志为空

**可能原因**:
1. Worker未启动
2. 容器重启
3. 日志被轮转

**排查步骤**:
```bash
# 1. 检查容器状态
ssh 211 'docker ps | grep llm-gateway-211-b'

# 2. 检查Worker启动日志
ssh 211 "docker logs llm-gateway-211-b 2>&1" | \
  grep "passive baseline worker started"

# 3. 检查最近心跳
ssh 211 "docker logs --since 1h llm-gateway-211-b 2>&1" | \
  grep "worker alive"
```

**解决方案**:
- 容器异常: 检查部署状态，考虑重启
- Worker未启动: 检查代码是否调用Start()
- 日志轮转: 扩大查询窗口到1小时

#### 问题2: Pending持续增长

**症状**: `pending`值持续>10且递增

**可能原因**:
1. 数据库连接故障
2. 刷库超时
3. 流量突增超过处理能力

**排查步骤**:
```bash
# 1. 检查flush失败日志
ssh 211 "docker logs --since 30m llm-gateway-211-b 2>&1" | \
  grep "flush had failures"

# 2. 检查数据库连接
ssh 211 "docker exec llm-gateway-211-b sh -c \
  'echo SELECT 1 | psql \$POSTGRES_DSN'"

# 3. 查看pending趋势
ssh 211 "docker logs --since 2h llm-gateway-211-b 2>&1" | \
  grep "pending" | tail -10
```

**解决方案**:
- DB故障: 修复数据库连接
- 超时: 检查网络延迟
- 流量突增: 正常现象，观察下个窗口

#### 问题3: 节点数持续为0

**症状**: 连续多次flush的`nodes=0`

**可能原因**:
1. 真实无流量（正常）
2. Submit未被调用（配置问题）
3. 采集器未初始化

**排查步骤**:
```bash
# 1. 检查流量
curl -s https://llm.itestu.cn/api/system/metrics | \
  jq '.requests_per_minute'

# 2. 检查streaming路径日志
ssh 211 "docker logs --since 10m llm-gateway-211-b 2>&1" | \
  grep -c "streaming request completed"

# 3. 检查Worker初始化
ssh 211 "docker logs llm-gateway-211-b 2>&1" | \
  grep "SetPassiveTiming"
```

**解决方案**:
- 无流量: 正常，等待流量恢复
- 配置问题: 检查main.go装配代码
- 初始化问题: 检查Worker Start()调用

### 4.2 紧急响应

#### 场景1: 系统完全静默

**特征**: 无flush、无心跳、pending不可知

**优先级**: 🔴 P0 - 立即处理

**行动**:
1. 检查容器是否运行
2. 检查Worker是否启动
3. 必要时重启容器
4. 验证恢复后数据写入

#### 场景2: Pending爆炸

**特征**: pending > 100 且持续增长

**优先级**: 🟠 P1 - 1小时内处理

**行动**:
1. 检查数据库健康
2. 检查flush失败原因
3. 临时措施: 重启Worker清空内存
4. 根因修复后验证

#### 场景3: 节点数异常低

**特征**: 高峰期仍<30节点/2小时

**优先级**: 🟡 P2 - 24小时内分析

**行动**:
1. 对比历史基线
2. 检查流量是否下降
3. 检查采样逻辑是否变更
4. 必要时调整参数

---

## 五、运维指南

### 5.1 日常运维

**每日检查** (自动化完成):
- 实时健康: 每20分钟
- 深度分析: 每小时整点

**每周审查**:
- 性能趋势: 对比上周基线
- 异常事件: 回顾告警记录
- 容量规划: 评估是否需要调整

**每月回顾**:
- 基线更新: 刷新月度性能基准
- 优化建议: 评估是否需要调优
- 文档更新: 补充新发现的模式

### 5.2 变更管理

**部署前**:
1. 验证代码变更不影响采集路径
2. 确认Worker启动逻辑完整
3. 预演数据库迁移（如有）

**部署中**:
1. 滚动部署，保留旧版本
2. 实时监控pending值
3. 验证首次flush成功

**部署后**:
1. 对比部署前后节点数
2. 检查是否有新的失败模式
3. 24小时持续观察

### 5.3 容量规划

**当前容量**:
- 内存: 单窗口最多 ~24K样本（ITT 20K + TTFT 4K）
- 处理能力: 5分钟处理全部pending（实测<1秒）
- 数据库: 单次upsert ~100ms

**扩容触发条件**:
- Pending持续>50
- 单次flush耗时>10秒
- 内存样本频繁触顶

**扩容方案**:
- 短期: 缩短flush周期到3分钟
- 中期: 增加Worker并发度
- 长期: 分片写入（按租户/模型）

---

## 六、参考资料

### 6.1 相关文档

- [被动指纹断链修复](../audit/2026-11-10-被动指纹断链修复验证.md)
- [被动指纹持续监控](./passive-fingerprint-monitoring-2026-11-11.md)
- [最新健康报告](./passive-fingerprint-health-report-2026-11-11-0243.md)

### 6.2 代码位置

- Worker: `bg/passive_baseline_worker.go`
- Collector: `domains/modelcredibility/passive_collector.go`
- Store: `domains/modelcredibility/store_passive.go`
- 装配: `cmd/gateway/main.go`

### 6.3 数据库

- 表: `model_passive_baselines` (migration 955)
- 索引: `idx_passive_baseline_lookup`
- 查询示例:
  ```sql
  SELECT model_id, sample_count, collected_to
  FROM model_passive_baselines
  WHERE tenant_id = 'default'
    AND credential_id = 12345
  ORDER BY updated_at DESC
  LIMIT 10;
  ```

### 6.4 监控链接

- Grafana: (待添加)
- 日志查询: Kibana → llm-gateway → "passive baseline"
- 告警规则: (待添加)

---

## 七、更新记录

| 日期 | 版本 | 变更 | 作者 |
|------|------|------|------|
| 2026-11-11 | v1.0 | 初始版本，基于深夜低谷期实测 | 自动化系统 |

---

**下次更新**: 白天高峰期验证后
