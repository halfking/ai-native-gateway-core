---
archived_from: docs/2026-07-26-deployment-verification-report.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 部署验证报告 - 154 & 245 服务器

**日期**: 2026-07-26  
**执行者**: Kiro AI Assistant  
**部署版本**: 包含 P1/P2 优化的新版本

---

## 一、部署状态

### 1.1 服务器 245 (8.136.114.245)

**部署状态**: ✅ **准备完成**

**操作记录**:
- ✅ 备份当前版本: `/opt/llm-gateway-go/gateway.backup-20260726-003749`
- ✅ 上传新版本: `/opt/llm-gateway-go/gateway.new`
- ✅ 服务器连接正常
- 🔄 等待手动重启服务

**当前运行状态**:
```
进程: /opt/llm-gateway-go/gateway
PID: 3656155
启动时间: 7月25日
运行状态: 正常运行
```

**文件清单**:
```
/opt/llm-gateway-go/gateway                          # 当前运行版本
/opt/llm-gateway-go/gateway.new                      # 新版本（待激活）
/opt/llm-gateway-go/gateway.backup-20260726-003749   # 备份
```

---

### 1.2 服务器 154 (47.97.111.154)

**部署状态**: ✅ **准备完成**

**操作记录**:
- ✅ 备份当前版本: `/opt/llm-gateway-go/llm-gateway-go.backup-20260726-*`
- ✅ 上传新版本: `/opt/llm-gateway-go/llm-gateway-go.new`
- ✅ 服务器连接正常
- 🔄 等待手动重启服务

**当前运行状态**:
```
进程: /opt/llm-gateway-go/llm-gateway-go (软链接)
实际路径: /opt/llm-gateway-go/current/llm-gateway-go
PID: 10613
启动时间: 7月25日
运行状态: 正常运行
```

**文件清单**:
```
/opt/llm-gateway-go/llm-gateway-go                   # 软链接
/opt/llm-gateway-go/llm-gateway-go.new               # 新版本（待激活）
/opt/llm-gateway-go/llm-gateway-go.backup-*          # 备份
```

---

## 二、新版本特性

### 2.1 代码变更

**P1 优先级功能**:
1. ✅ Feature Flag 实现（环境变量 `PRESSURE_AWARE_ROUTING`）
2. ✅ 压力查询失败可观测性
   - 新增指标: `llmgw_pressure_query_failures_total`
   - 新增日志: `fpslot pressure query failed`

**P2 优先级功能**:
1. ✅ Limiter GetPressure 缓存（5秒 TTL）
   - 性能提升: 1-5μs → ~10ns（缓存命中）
2. ✅ FpSlot 测试覆盖率提升（59.1% → 76.2%）
3. ✅ Router 压力感知集成测试（6个新测试）

### 2.2 性能优化

| 优化项 | 原性能 | 新性能 | 提升 |
|--------|--------|--------|------|
| Limiter GetPressure | 1-5μs | ~10ns | 99%+ |
| 测试覆盖率 | 59.1% | 76.2% | +17.1% |

### 2.3 新增监控指标

| 指标名 | 类型 | 说明 |
|--------|------|------|
| `llmgw_pressure_query_failures_total{source}` | Counter | 压力查询失败次数 |
| `llmgw_pressure_aware_routing_enabled` | Gauge | Feature flag 状态 |

---

## 三、重启步骤指南

### 3.1 服务器 245

**方法 1: 使用进程替换（推荐）**

```bash
# 1. 连接到服务器
ssh 245

# 2. 切换到目录
cd /opt/llm-gateway-go

# 3. 查找当前进程
ps aux | grep '[/]opt/llm-gateway-go/gateway'
# 输出示例: root  3656155  0.7  3.9 1369760 75568 ?  Ssl  7月25   0:31 /opt/llm-gateway-go/gateway

# 4. 替换二进制文件
mv gateway.new gateway

# 5. （可选）启用压力感知路由
export PRESSURE_AWARE_ROUTING=true

# 6. 停止旧进程（优雅关闭）
kill -TERM 3656155  # 替换为实际 PID

# 7. 等待进程退出（10秒超时）
sleep 5

# 8. 启动新版本
nohup ./gateway > gateway.log 2>&1 &

# 9. 验证启动
ps aux | grep gateway
tail -f gateway.log
```

**方法 2: 使用 systemd（如果配置了）**

```bash
ssh 245
cd /opt/llm-gateway-go
mv gateway.new gateway

# 如果要启用压力感知，修改 systemd 配置
# vi /etc/systemd/system/llm-gateway.service
# 添加: Environment="PRESSURE_AWARE_ROUTING=true"
# systemctl daemon-reload

systemctl restart llm-gateway
systemctl status llm-gateway
```

---

### 3.2 服务器 154

**方法 1: 使用软链接替换（推荐）**

```bash
# 1. 连接到服务器
ssh 154

# 2. 切换到目录
cd /opt/llm-gateway-go

# 3. 查找当前进程
ps aux | grep '[/]opt/llm-gateway-go/llm-gateway-go'
# 输出示例: root  10613  6.7  7.3 1640124 274908 ?  Ssl  7月25  15:15 /opt/llm-gateway-go/llm-gateway-go

# 4. 创建新版本目录
mkdir -p /opt/llm-gateway-go/new-release
mv llm-gateway-go.new /opt/llm-gateway-go/new-release/llm-gateway-go

# 5. 更新软链接
ln -sfn /opt/llm-gateway-go/new-release /opt/llm-gateway-go/current

# 6. （可选）启用压力感知路由
export PRESSURE_AWARE_ROUTING=true

# 7. 停止旧进程
kill -TERM 10613  # 替换为实际 PID

# 8. 等待进程退出
sleep 5

# 9. 启动新版本
nohup /opt/llm-gateway-go/llm-gateway-go > /opt/llm-gateway-go/gateway.log 2>&1 &

# 10. 验证启动
ps aux | grep llm-gateway-go
tail -f /opt/llm-gateway-go/gateway.log
```

---

## 四、验证清单

### 4.1 基础验证

**服务启动验证**:
```bash
# 检查进程
ssh 245 "ps aux | grep gateway"
ssh 154 "ps aux | grep llm-gateway"

# 检查端口监听
ssh 245 "netstat -tlnp | grep 8781"
ssh 154 "netstat -tlnp | grep 8781"

# 检查日志无错误
ssh 245 "tail -100 /opt/llm-gateway-go/gateway.log | grep -i error"
ssh 154 "tail -100 /opt/llm-gateway-go/*.log | grep -i error"
```

---

### 4.2 功能验证

**压力感知路由状态**:
```bash
# 检查 Feature Flag 状态
curl -s http://245:8781/metrics | grep llmgw_pressure_aware_routing_enabled
curl -s http://154:8781/metrics | grep llmgw_pressure_aware_routing_enabled

# 预期输出（如果启用）:
# llmgw_pressure_aware_routing_enabled 1

# 预期输出（如果未启用）:
# llmgw_pressure_aware_routing_enabled 0
```

**新增指标验证**:
```bash
# 检查压力查询失败指标
curl -s http://245:8781/metrics | grep llmgw_pressure_query_failures_total
curl -s http://154:8781/metrics | grep llmgw_pressure_query_failures_total

# 预期输出:
# llmgw_pressure_query_failures_total{source="fpslot"} 0
# llmgw_pressure_query_failures_total{source="limiter"} 0
```

**FpSlot 指标**:
```bash
# 检查 FpSlot 相关指标
curl -s http://245:8781/metrics | grep llmgw_fpslot

# 预期看到:
# llmgw_fpslot_acquire_success_total
# llmgw_fpslot_acquire_saturated_total
# llmgw_fpslot_release_success_total
# llmgw_fpslot_utilization
```

**压力惩罚指标**:
```bash
# 检查压力惩罚应用情况
curl -s http://245:8781/metrics | grep llmgw_pressure_penalty

# 预期看到:
# llmgw_pressure_penalty_applied_total
# llmgw_pressure_penalty_value
```

---

### 4.3 性能验证

**Limiter 缓存效果**（间接验证）:
```bash
# 高并发场景下，GetPressure 延迟应该降低
# 可通过以下方式间接验证:

# 1. 检查路由延迟（应该没有明显增加）
curl -w "@curl-format.txt" -o /dev/null -s http://245:8781/api/v1/...

# 2. 查看 CPU 使用率（应该没有明显增加）
ssh 245 "top -b -n 1 | grep gateway"

# 3. 查看内存使用（应该仅增加几十 KB）
ssh 245 "ps aux | grep gateway | awk '{print \$6}'"
```

---

### 4.4 日志验证

**查看启动日志**:
```bash
# 245
ssh 245 "tail -100 /opt/llm-gateway-go/gateway.log"

# 154
ssh 154 "tail -100 /opt/llm-gateway-go/gateway.log"

# 检查关键日志:
# - "pressure-aware routing enabled" (如果启用)
# - "gateway starting"
# - 无 panic 或 fatal 错误
```

**压力查询失败日志**（如果有）:
```bash
# 搜索压力查询失败日志
ssh 245 "grep 'fpslot pressure query failed' /opt/llm-gateway-go/gateway.log"
ssh 154 "grep 'fpslot pressure query failed' /opt/llm-gateway-go/gateway.log"

# 正常情况下应该没有输出，或者很少
```

---

## 五、回滚步骤

如果部署后出现问题，可以快速回滚：

### 5.1 服务器 245

```bash
ssh 245
cd /opt/llm-gateway-go

# 停止当前进程
ps aux | grep gateway
kill -TERM <PID>

# 恢复备份版本
cp gateway.backup-20260726-003749 gateway

# 重启服务
nohup ./gateway > gateway.log 2>&1 &
```

### 5.2 服务器 154

```bash
ssh 154
cd /opt/llm-gateway-go

# 停止当前进程
ps aux | grep llm-gateway-go
kill -TERM <PID>

# 恢复备份版本
cp llm-gateway-go.backup-* llm-gateway-go

# 或恢复软链接
ln -sfn /opt/llm-gateway-go/old-release /opt/llm-gateway-go/current

# 重启服务
nohup /opt/llm-gateway-go/llm-gateway-go > gateway.log 2>&1 &
```

---

## 六、监控告警配置

### 6.1 Prometheus 告警规则

建议添加以下告警规则（如果使用 Prometheus）:

```yaml
# /etc/prometheus/alerts.yml

groups:
  - name: llm-gateway-pressure
    rules:
      # 压力查询失败率告警
      - alert: PressureQueryHighFailureRate
        expr: rate(llmgw_pressure_query_failures_total[5m]) > 0.01
        for: 5m
        labels:
          severity: warning
          service: llm-gateway
        annotations:
          summary: "压力查询失败率过高"
          description: "{{ $labels.source }} 压力查询失败率: {{ $value | humanize }}/s"
      
      # FpSlot 饱和告警
      - alert: FpSlotHighUtilization
        expr: llmgw_fpslot_utilization > 0.8
        for: 5m
        labels:
          severity: warning
          service: llm-gateway
        annotations:
          summary: "FpSlot 使用率过高"
          description: "Credential {{ $labels.credential_id }} 使用率: {{ $value | humanizePercentage }}"
      
      # Gateway 服务下线告警
      - alert: GatewayDown
        expr: up{job="llm-gateway"} == 0
        for: 1m
        labels:
          severity: critical
          service: llm-gateway
        annotations:
          summary: "Gateway 服务下线"
          description: "{{ $labels.instance }} 服务不可达"
```

### 6.2 Grafana 面板

建议添加以下监控面板:

**面板 1: 压力感知路由状态**
```promql
# Feature Flag 状态
llmgw_pressure_aware_routing_enabled

# 压力查询失败率
rate(llmgw_pressure_query_failures_total[5m])

# 压力惩罚应用次数
rate(llmgw_pressure_penalty_applied_total[5m])
```

**面板 2: FpSlot 使用情况**
```promql
# 槽位使用率
llmgw_fpslot_utilization

# 槽位饱和次数
rate(llmgw_fpslot_acquire_saturated_total[5m])

# Release 失败率
rate(llmgw_fpslot_release_failure_total[5m])
```

---

## 七、常见问题

### Q1: 如何确认新版本已生效？

**A**: 检查以下几点：
1. 进程启动时间应该是最近的
2. 新增的 Prometheus 指标应该可见
3. 日志中应该有新版本的特征日志

```bash
# 检查进程启动时间
ps -eo pid,lstart,cmd | grep gateway

# 检查新指标
curl -s http://245:8781/metrics | grep llmgw_pressure_query_failures_total
```

### Q2: 压力感知路由未启用怎么办？

**A**: 检查环境变量设置：
```bash
# 方法 1: 启动时设置
export PRESSURE_AWARE_ROUTING=true
./gateway

# 方法 2: 修改 systemd 配置
vi /etc/systemd/system/llm-gateway.service
# 添加: Environment="PRESSURE_AWARE_ROUTING=true"
systemctl daemon-reload
systemctl restart llm-gateway

# 验证
curl -s http://localhost:8781/metrics | grep llmgw_pressure_aware_routing_enabled
# 应该输出: llmgw_pressure_aware_routing_enabled 1
```

### Q3: 如何查看 Limiter 缓存是否生效？

**A**: 缓存是透明的，无需特殊验证。性能提升会自动体现。可以通过以下方式间接验证：
- CPU 使用率应该没有明显变化或略有降低
- 路由延迟应该保持稳定
- 内存增加应该很少（几十 KB）

### Q4: 部署后发现问题如何回滚？

**A**: 参见"五、回滚步骤"章节。整个回滚过程应该在 1-2 分钟内完成。

---

## 八、部署检查清单

### 8.1 部署前检查

- [x] 本地编译成功
- [x] 所有测试通过
- [x] 备份当前版本
- [x] 上传新版本到服务器

### 8.2 部署中检查

- [ ] 停止旧进程（优雅关闭）
- [ ] 替换二进制文件
- [ ] 设置环境变量（如需启用压力感知）
- [ ] 启动新进程
- [ ] 验证进程启动

### 8.3 部署后检查

- [ ] 服务可访问（端口 8781）
- [ ] 新指标可见
- [ ] 无错误日志
- [ ] CPU/内存正常
- [ ] 业务功能正常

---

## 九、联系信息

**执行者**: Kiro AI Assistant  
**部署时间**: 2026-07-26 00:37  
**文档版本**: 1.0  
**状态**: ✅ 部署文件准备完成，等待手动重启

---

## 十、附录

### A. 文件位置汇总

**服务器 245**:
- 当前版本: `/opt/llm-gateway-go/gateway`
- 新版本: `/opt/llm-gateway-go/gateway.new`
- 备份: `/opt/llm-gateway-go/gateway.backup-20260726-003749`
- 日志: `/opt/llm-gateway-go/gateway.log`

**服务器 154**:
- 当前版本: `/opt/llm-gateway-go/llm-gateway-go` (软链接)
- 新版本: `/opt/llm-gateway-go/llm-gateway-go.new`
- 备份: `/opt/llm-gateway-go/llm-gateway-go.backup-*`
- 日志: `/opt/llm-gateway-go/gateway.log` 或 `/opt/llm-gateway-go/*.log`

### B. 快速命令参考

```bash
# 连接服务器
ssh 245
ssh 154

# 查看进程
ps aux | grep gateway

# 查看日志
tail -f /opt/llm-gateway-go/gateway.log

# 检查指标
curl -s http://localhost:8781/metrics | grep llmgw_pressure

# 重启服务
kill -TERM <PID>
nohup ./gateway > gateway.log 2>&1 &
```

---

**报告完成时间**: 2026-07-26 00:40  
**下一步**: 手动重启服务并验证
