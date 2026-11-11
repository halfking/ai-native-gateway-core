# 测试脚本使用指南

本文档说明如何使用 llm-gateway-go 项目的综合测试脚本。

## 快速开始

### 方式1：交互式菜单（推荐）

```bash
bash scripts/run-tests.sh
```

这将启动交互式菜单，可以选择不同的测试场景。

### 方式2：命令行快捷方式

```bash
# 快速检查（仅Maestro UI测试）
bash scripts/run-tests.sh quick

# 性能测试（仅k6负载测试）
bash scripts/run-tests.sh perf

# 完整测试（Maestro + k6 并行）
bash scripts/run-tests.sh full

# 主题测试（明暗皮肤验证）
bash scripts/run-tests.sh theme

# 211环境测试
bash scripts/run-tests.sh 211
```

## 测试脚本说明

### 1. run-tests.sh

**主测试启动器**，提供交互式菜单和快捷命令。

**功能**：
- 快速检查（~10分钟）
- 性能测试（~25分钟）
- 完整测试（~30分钟）
- 主题测试（~5分钟）
- 单个flow/场景测试
- 211环境测试

### 2. parallel-test-runner.sh

**并行测试运行器**，同时执行多个测试任务。

**用法**：
```bash
# 本地环境
bash scripts/parallel-test-runner.sh local --with-k6

# 211环境
bash scripts/parallel-test-runner.sh 211 --with-k6
```

**输出**：
- `test-reports/parallel-<timestamp>/` - 测试报告目录
- `index.html` - HTML报告首页
- `runner.log` - 运行日志
- `*.log` - 各任务日志
- `*.exit` - 各任务退出码

### 3. generate-test-summary.sh

**测试总结生成器**，从测试结果生成Markdown报告。

**用法**：
```bash
bash scripts/generate-test-summary.sh test-reports/parallel-20261111-143022
```

**输出**：
- `TEST_SUMMARY.md` - 综合测试报告

## 测试套件组成

### Maestro UI测试

位置：`maestro/flows/`

| Flow | 描述 | 页数 |
|------|------|------|
| 01-public-guest.yaml | 公开页面测试 | 12 |
| 02-overview-live.yaml | 总览和实时拓扑 | 6 |
| 03-providers-models.yaml | 供应商和模型 | 9 |
| 04-tenants-portal.yaml | 租户门户 | 13 |
| 05-routing.yaml | 路由域 | 22 |
| 06-keys-requests.yaml | 密钥和请求 | 9 |
| 07-admin-config.yaml | 后台配置 | 13 |
| 08-admin-data-ops.yaml | 数据运维 | 19 |
| 09-ops-maintain.yaml | 运维中心 | 6 |
| 98-theme-validation.yaml | 主题测试 | 20截图 |
| 90-dev-only.yaml | 开发专用 | 3 |

**总计**：112条运行时路由

### k6负载测试

位置：`k6-scripts/`

| 脚本 | 描述 | 持续时间 |
|------|------|----------|
| api-load-test.js | API负载测试 | ~12分钟 |
| streaming-load-test.js | 流式接口测试 | ~9分钟 |
| admin-load-test.js | 管理后台测试 | ~9分钟 |
| mixed-load-test.js | 混合负载测试 | ~23分钟 |

## 环境要求

### 必需

- **Bash** 4.0+
- **macOS** 或 Linux
- **本地网关运行** 在 `http://127.0.0.1:8782`

### Maestro测试

```bash
# 安装Maestro
curl -fsSL https://get.mobile.dev | bash

# 验证安装
maestro --version

# 环境变量（在Java 17环境下）
export JAVA_HOME="$HOME/kaixuan/jdk17/Contents/Home"
export PATH="$JAVA_HOME/bin:$PATH"
```

### k6测试

```bash
# 安装k6
brew install k6

# 或从官网下载
# https://k6.io/docs/get-started/installation/

# 验证安装
k6 version
```

### 可选工具

```bash
# jq - 用于解析JSON报告
brew install jq
```

## 配置

### 环境变量

创建 `.env.local` 文件（不会提交到Git）：

```bash
# 本地环境
BASE_URL=http://127.0.0.1:8782
ADMIN_USER=admin
ADMIN_PASSWORD=your_password

# 211环境
BASE_URL_211=http://10.211.55.4:8782
```

### API Key配置

在 `k6-scripts/` 的测试脚本中配置，或通过环境变量：

```bash
export API_KEY="sk-xxx"
```

## 测试报告

### 报告位置

所有测试报告保存在 `test-reports/` 目录：

```
test-reports/
├── parallel-20261111-143022/     # 并行测试报告
│   ├── index.html               # HTML首页
│   ├── TEST_SUMMARY.md          # Markdown总结
│   ├── runner.log               # 运行日志
│   ├── maestro-01.log           # Maestro flow日志
│   ├── k6-api-load.json         # k6性能数据
│   └── ...
├── k6-20261111-150000/          # 单独k6测试
└── theme-20261111-160000/       # 主题测试截图
```

### 查看报告

```bash
# HTML报告
open test-reports/parallel-*/index.html

# Markdown总结
cat test-reports/parallel-*/TEST_SUMMARY.md

# 单个任务日志
tail -f test-reports/parallel-*/maestro-01.log
```

## 常见问题

### Q: Maestro测试失败，提示"Chrome not found"

**A**: 确保Chrome浏览器已安装，并且可以通过CDP端口9222访问。

### Q: k6测试报错"connection refused"

**A**: 确认本地网关正在运行：

```bash
curl http://127.0.0.1:8782/health
```

### Q: 并行测试时资源占用过高

**A**: 调整并行任务数量，修改 `parallel-test-runner.sh` 中的启动间隔。

### Q: 211环境测试需要VPN吗？

**A**: 是的，确保可以访问211服务器：

```bash
ping 10.211.55.4
curl http://10.211.55.4:8782/health
```

## 性能基准

### 预期指标

| 指标 | 目标值 |
|------|--------|
| API吞吐量 | ≥1000 req/s |
| 响应时间 P95 | <500ms |
| 响应时间 P99 | <1000ms |
| 错误率 | <1% |
| 并发用户 | 支持500+ |

### 实际测试结果

运行完整测试后，查看 `TEST_SUMMARY.md` 获取实际指标。

## 相关文档

- [综合测试计划](../docs/testing/comprehensive-test-plan-2026-11-11.md)
- [Maestro测试说明](../maestro/README.md)
- [项目README](../README.md)
- [数据库修复工具](./README.md) - 原有文档

---

**维护者**：测试团队  
**最后更新**：2026-11-11
