# LLM Gateway 性能基线测试计划

**版本**: v1.0  
**日期**: 2026-11-18  
**目的**: 为P0-14性能优化建立数据基线  
**预计时间**: 3天

---

## 1. 测试目标

### 1.1 主要目标
建立LLM Gateway的性能基线，量化当前系统性能状态，为P0-14优化提供数据支持。

### 1.2 P0-14优化目标
| 指标 | 当前值 | 目标值 | 改进幅度 |
|------|--------|--------|----------|
| P95延迟 | 2s | 1s | -50% |
| QPS | 100 | 500 | +400% |
| 缓存命中率 | 35% | 50% | +15pp |

### 1.3 测试范围
- ✅ API延迟（P50, P95, P99）
- ✅ 吞吐量（QPS）
- ✅ 缓存效率（命中率、miss率）
- ✅ 资源使用（CPU、内存、数据库连接）
- ✅ 错误率
- ✅ 故障恢复能力

---

## 2. 测试环境

### 2.1 硬件配置
```
CPU: [待填写]
内存: [待填写]
磁盘: [待填写]
网络: localhost（减少网络延迟影响）
```

### 2.2 软件配置
```
OS: macOS [版本]
Go: [版本]
PostgreSQL: [版本]
Redis: [如果使用]
```

### 2.3 服务配置
```bash
# Gateway配置
GATEWAY_PORT=8080
WORKERS=[待确定]
CONNECTION_POOL=[待确定]

# 数据库配置
DB_HOST=localhost
DB_PORT=5432
DB_NAME=llm_gateway
MAX_CONNECTIONS=[待确定]

# Mock Providers
NUM_PROVIDERS=20
PORT_RANGE=19080-19099
```

---

## 3. 测试场景

### 3.1 场景1：正常负载测试
**目的**: 建立正常负载下的性能基线

**配置**:
```bash
NUM_CLIENTS=150          # 并发客户端数
NUM_ROUNDS_PER_CLIENT=80 # 每客户端请求轮次
TOTAL_REQUESTS=12000     # 总请求数
```

**预期输出**:
- P50/P95/P99延迟分布
- 平均QPS
- 成功率
- 错误分布

**执行命令**:
```bash
cd /path/to/llm-gateway-go
./scripts/comprehensive-loadtest.sh
```

### 3.2 场景2：渐进压力测试
**目的**: 找到系统的性能拐点

**配置**:
```bash
# 逐步增加并发数
CONCURRENT_LEVELS=(50 100 150 200 250)
REQUESTS_PER_LEVEL=1000
```

**预期输出**:
- 各并发级别的延迟变化
- QPS增长曲线
- 系统瓶颈点

**执行命令**:
```bash
./scripts/progressive-load-test.sh
```

### 3.3 场景3：缓存效率测试
**目的**: 评估缓存命中率和效果

**配置**:
```bash
# 模拟重复请求
CACHE_ENABLED=true
REPEAT_RATIO=0.5  # 50%重复请求
CLIENTS=100
ROUNDS=50
```

**预期输出**:
- 缓存命中率
- 缓存命中时延迟 vs 未命中时延迟
- 缓存对QPS的影响

**执行命令**:
```bash
./scripts/cache-efficiency-test.sh
```

### 3.4 场景4：故障场景测试
**目的**: 验证系统在故障情况下的表现

**配置**:
```bash
# 测试各种故障场景
SCENARIOS=(rate_limit slow server_error flaky)
```

**预期输出**:
- Failover响应时间
- 错误恢复能力
- Sticky routing效果

**执行命令**:
```bash
# 已包含在comprehensive-loadtest.sh中
```

### 3.5 场景5：资源使用分析
**目的**: 监控系统资源消耗

**监控指标**:
```
- CPU使用率（per core）
- 内存使用（RSS, heap）
- Goroutine数量
- 数据库连接数
- 文件描述符数量
```

**执行命令**:
```bash
./scripts/resource-monitor.sh &
MONITOR_PID=$!
./scripts/comprehensive-loadtest.sh
kill $MONITOR_PID
```

---

## 4. 执行步骤

### 4.1 环境准备（Day 1上午）

#### Step 1: 检查依赖
```bash
cd /path/to/llm-gateway-go

# 检查必需工具
./scripts/comprehensive-loadtest.sh --check-deps

# 预期输出：
# ✅ python3
# ✅ psql
# ✅ curl
# ✅ jq
# ✅ go
```

#### Step 2: 启动数据库
```bash
# 启动PostgreSQL（如果未运行）
brew services start postgresql@14

# 创建测试数据库
psql -c "CREATE DATABASE llm_gateway_baseline;"
psql llm_gateway_baseline < db/schema.sql
```

#### Step 3: 配置环境变量
```bash
export DB_NAME=llm_gateway_baseline
export GATEWAY_PORT=8080
export LOG_LEVEL=info
```

#### Step 4: 验证环境
```bash
# 运行快速验证测试
./scripts/quick-start-loadtest.sh

# 预期：成功完成，无错误
```

### 4.2 基线测试执行（Day 1下午 + Day 2）

#### Test Run 1: 正常负载（约30分钟）
```bash
# 记录开始时间
date > baseline-test-1-start.txt

# 运行测试
./scripts/comprehensive-loadtest.sh 2>&1 | tee baseline-test-1.log

# 记录结束时间
date > baseline-test-1-end.txt
```

#### Test Run 2: 渐进压力（约1小时）
```bash
# 创建渐进测试脚本
./scripts/create-progressive-test.sh

# 运行
./scripts/progressive-load-test.sh 2>&1 | tee baseline-test-2.log
```

#### Test Run 3: 缓存效率（约30分钟）
```bash
# 确保缓存启用
export CACHE_ENABLED=true

# 运行缓存测试
./scripts/cache-efficiency-test.sh 2>&1 | tee baseline-test-3.log
```

#### Test Run 4: 重复验证（约30分钟）
```bash
# 重复运行Test 1以验证一致性
./scripts/comprehensive-loadtest.sh 2>&1 | tee baseline-test-4-repeat.log
```

### 4.3 数据分析（Day 3）

#### Step 1: 收集原始数据
```bash
# 聚合所有测试日志
./scripts/aggregate-test-results.sh

# 输出：baseline-results-raw.json
```

#### Step 2: 生成分析报告
```bash
# 运行分析脚本
./scripts/baseline-analysis.sh

# 输出：
# - baseline-summary.txt
# - baseline-charts.png
# - baseline-report.md
```

#### Step 3: 对比目标
```bash
# 对比当前值 vs P0-14目标
./scripts/compare-with-targets.sh

# 输出：optimization-gap-analysis.md
```

---

## 5. 数据收集清单

### 5.1 延迟数据
```
✅ P50延迟（中位数）
✅ P95延迟（95百分位）
✅ P99延迟（99百分位）
✅ P99.9延迟（极端情况）
✅ 平均延迟
✅ 最大延迟
```

### 5.2 吞吐量数据
```
✅ 平均QPS
✅ 峰值QPS
✅ 最低QPS
✅ QPS标准差
```

### 5.3 成功率数据
```
✅ 总请求数
✅ 成功请求数
✅ 失败请求数
✅ 成功率（%）
✅ 错误类型分布
```

### 5.4 缓存数据
```
✅ 缓存命中次数
✅ 缓存未命中次数
✅ 缓存命中率（%）
✅ 缓存命中延迟
✅ 缓存未命中延迟
```

### 5.5 资源使用数据
```
✅ 平均CPU使用率（%）
✅ 峰值CPU使用率（%）
✅ 平均内存使用（MB）
✅ 峰值内存使用（MB）
✅ Goroutine数量
✅ 数据库连接数
```

### 5.6 故障恢复数据
```
✅ Failover次数
✅ 平均Failover时间
✅ 最大Failover时间
✅ Sticky routing成功率
```

---

## 6. 成功标准

### 6.1 测试完整性
- ✅ 所有5个测试场景都已执行
- ✅ 每个场景至少执行2次（验证一致性）
- ✅ 所有数据点都已收集
- ✅ 日志完整，无数据丢失

### 6.2 数据质量
- ✅ 测试运行期间无系统崩溃
- ✅ 错误率 < 5%（排除预期的故障场景测试）
- ✅ 数据变异系数 < 20%（重复测试的一致性）
- ✅ 所有监控指标都有记录

### 6.3 报告完整性
- ✅ 基线报告包含所有关键指标
- ✅ 与P0-14目标的差距分析
- ✅ 瓶颈识别
- ✅ 优化建议（优先级排序）

---

## 7. 交付物

### 7.1 测试数据
```
baseline-test-results/
├── raw-data/
│   ├── test-1-normal-load.log
│   ├── test-2-progressive.log
│   ├── test-3-cache.log
│   └── test-4-repeat.log
├── processed-data/
│   ├── latency-distribution.json
│   ├── qps-timeline.json
│   ├── cache-stats.json
│   └── resource-usage.json
└── summary/
    ├── baseline-summary.txt
    └── baseline-metrics.json
```

### 7.2 报告文档
```
- performance-baseline-report.md（主报告）
- optimization-gap-analysis.md（差距分析）
- bottleneck-identification.md（瓶颈识别）
- optimization-recommendations.md（优化建议）
```

### 7.3 可视化
```
- latency-distribution-chart.png
- qps-over-time-chart.png
- cache-efficiency-chart.png
- resource-usage-chart.png
```

---

## 8. 风险与缓解

### 8.1 风险1：测试环境不稳定
**缓解措施**:
- 在专用测试环境运行
- 关闭不必要的后台进程
- 多次运行验证一致性

### 8.2 风险2：测试时间过长
**缓解措施**:
- 优先运行关键场景
- 使用较小的负载进行初步验证
- 必要时分批次执行

### 8.3 风险3：数据不完整
**缓解措施**:
- 使用自动化脚本收集数据
- 实时监控测试进度
- 保留完整日志以便事后分析

### 8.4 风险4：测试影响生产
**缓解措施**:
- **禁止在生产环境运行**
- 使用独立的测试数据库
- 使用mock providers而非真实API

---

## 9. 后续行动

### 9.1 基线建立后
1. 将基线数据提交到版本控制
2. 更新文档和仪表板
3. 设置性能监控告警
4. 制定性能回归测试计划

### 9.2 进入P0-14优化阶段
1. 根据瓶颈分析确定优化优先级
2. 实施优化（缓存/异步/并发等）
3. 每次优化后运行基线测试
4. 量化优化效果
5. 迭代直到达到目标

---

## 10. 附录

### 10.1 快速开始命令
```bash
# 一键运行完整基线测试
cd /path/to/llm-gateway-go
./scripts/run-baseline-tests.sh

# 预计耗时：2-3小时
# 输出：baseline-report.md
```

### 10.2 故障排查
```bash
# 如果测试失败，检查：
1. 数据库是否运行：psql -l
2. 端口是否被占用：lsof -i :8080
3. 依赖是否安装：./scripts/check-deps.sh
4. 日志查看：tail -f logs/gateway.log
```

### 10.3 联系信息
- 项目负责人：[姓名]
- 技术支持：[联系方式]
- 文档位置：[URL]

---

**批准**: [待填写]  
**审核**: [待填写]  
**执行**: [待填写]
