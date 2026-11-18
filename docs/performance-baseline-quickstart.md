# LLM Gateway 性能基线测试 - 快速开始指南

**目的**: 帮助您快速执行性能基线测试  
**预计时间**: 2-3小时  
**难度**: 中等

---

## 前提条件

### 必需的工具
```bash
# 检查是否已安装
python3 --version    # Python 3.x
psql --version       # PostgreSQL 客户端
go version           # Go 1.18+
curl --version       # curl
jq --version         # jq (JSON处理)
```

### 安装缺失的工具
```bash
# macOS
brew install python3 postgresql go curl jq

# Ubuntu/Debian
sudo apt-get install python3 postgresql-client golang curl jq

# Windows (使用WSL)
# [按Ubuntu/Debian方式安装]
```

---

## 快速开始（3步走）

### Step 1: 准备环境（10分钟）

#### 1.1 克隆/更新代码
```bash
cd /path/to/llm-gateway-go
git pull  # 确保代码最新
```

#### 1.2 启动数据库
```bash
# 如果PostgreSQL未运行
brew services start postgresql@14  # macOS
# 或
sudo systemctl start postgresql    # Linux

# 创建测试数据库
createdb llm_gateway_baseline

# 应用schema
psql llm_gateway_baseline < db/schema.sql
```

#### 1.3 设置环境变量
```bash
export DB_NAME=llm_gateway_baseline
export DB_HOST=localhost
export DB_PORT=5432
export DB_USER=$USER
export GATEWAY_PORT=8080
```

### Step 2: 运行测试（1-2小时）

#### 2.1 一键运行
```bash
cd /path/to/llm-gateway-go
./scripts/run-baseline-tests.sh
```

脚本会自动：
- ✅ 检查环境
- ✅ 创建结果目录
- ✅ 运行4个测试场景
- ✅ 生成汇总报告

#### 2.2 监控进度
在另一个终端查看：
```bash
# 查看Gateway日志
tail -f logs/gateway.log

# 查看测试进度
tail -f baseline-test-results/raw-data/test-*.log
```

### Step 3: 查看结果（30分钟）

#### 3.1 查看汇总
```bash
cat baseline-test-results/summary/baseline-summary-*.txt
```

#### 3.2 提取关键指标
```bash
# P95延迟
grep "P95" baseline-test-results/raw-data/*.log

# QPS
grep "QPS" baseline-test-results/raw-data/*.log

# 成功率
grep "success rate" baseline-test-results/raw-data/*.log
```

#### 3.3 填写报告
```bash
# 使用模板填写详细报告
cp docs/performance-baseline-report-template.md docs/performance-baseline-report.md
# [编辑报告，填入实际数据]
```

---

## 常见问题

### Q1: 数据库连接失败
**错误**: `could not connect to database`

**解决**:
```bash
# 检查PostgreSQL是否运行
brew services list | grep postgresql

# 检查连接
psql -h localhost -p 5432 -U $USER -c "SELECT 1"

# 检查数据库是否存在
psql -l | grep llm_gateway
```

### Q2: 端口被占用
**错误**: `port 8080 already in use`

**解决**:
```bash
# 查找占用端口的进程
lsof -i :8080

# 杀掉进程或更换端口
export GATEWAY_PORT=8081
```

### Q3: 测试运行很慢
**原因**: 可能是系统资源不足或配置太高

**解决**:
```bash
# 降低并发数（快速测试）
export NUM_CLIENTS=50
export NUM_ROUNDS_PER_CLIENT=20

# 重新运行
./scripts/comprehensive-loadtest.sh
```

### Q4: Python模块缺失
**错误**: `ModuleNotFoundError: No module named 'aiohttp'`

**解决**:
```bash
pip3 install aiohttp
```

---

## 手动运行（如果自动脚本失败）

### 方式1：单独运行核心测试
```bash
cd /path/to/llm-gateway-go
./scripts/comprehensive-loadtest.sh 2>&1 | tee baseline-manual.log
```

### 方式2：自定义配置运行
```bash
# 设置自定义参数
export NUM_CLIENTS=100
export NUM_ROUNDS_PER_CLIENT=50
export NUM_MOCK_PROVIDERS=10

# 运行
./scripts/comprehensive-loadtest.sh
```

---

## 测试后清理

### 保留数据（推荐）
```bash
# 压缩测试结果
tar -czf baseline-results-$(date +%Y%m%d).tar.gz baseline-test-results/

# 移动到归档目录
mv baseline-results-*.tar.gz archives/
```

### 清理测试数据
```bash
# 删除测试数据库
dropdb llm_gateway_baseline

# 删除测试结果（谨慎！）
rm -rf baseline-test-results/
```

---

## 下一步

### 完成基线测试后
1. ✅ 填写 `docs/performance-baseline-report.md`
2. ✅ 对比P0-14目标，确定差距
3. ✅ 识别性能瓶颈
4. ✅ 制定优化计划
5. ✅ 开始P0-14优化工作

### 开始优化
```bash
# 根据瓶颈分析，优先优化最严重的问题
# 每次优化后，重新运行基线测试验证效果

# 示例：如果缓存是瓶颈
# 1. 优化缓存策略
# 2. 重新测试
./scripts/run-baseline-tests.sh
# 3. 对比前后数据
./scripts/compare-baseline-results.sh before.log after.log
```

---

## 帮助与支持

### 获取帮助
```bash
# 查看脚本帮助
./scripts/run-baseline-tests.sh --help

# 查看详细文档
less docs/performance-baseline-test-plan.md
```

### 报告问题
如果遇到问题，请提供：
1. 错误日志
2. 系统信息（`uname -a`, `go version`）
3. 测试配置
4. 复现步骤

---

## 时间预算

| 阶段 | 时间 | 描述 |
|------|------|------|
| 环境准备 | 10分钟 | 安装依赖、配置数据库 |
| 测试执行 | 1-2小时 | 运行4个测试场景 |
| 结果分析 | 30分钟 | 提取指标、填写报告 |
| **总计** | **2-3小时** | 完整的基线建立流程 |

---

## 检查清单

完成基线测试前，确认：
- [ ] 所有依赖已安装
- [ ] 数据库已创建并应用schema
- [ ] 环境变量已设置
- [ ] 有足够的磁盘空间（至少5GB）
- [ ] 测试期间系统负载较低
- [ ] 关闭不必要的后台程序

完成基线测试后，确认：
- [ ] 所有测试场景都成功执行
- [ ] 测试日志已保存
- [ ] 关键指标已提取
- [ ] 报告已填写
- [ ] 数据已备份

---

**祝测试顺利！🚀**
