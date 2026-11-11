# LLM Gateway 测试套件

## 概述

本测试套件提供全面的功能验证、性能测试和并发压力测试，确保 LLM Gateway 在各种场景下稳定可靠。

## 测试架构

```
tests/
├── maestro/           # UI 自动化测试（Maestro）
│   ├── flows/        # 测试流程定义
│   └── fixtures/     # 测试数据
├── k6/               # 性能与负载测试
│   ├── scenarios/    # 测试场景
│   └── utils/        # 工具函数
├── concurrent/       # 并发测试
│   ├── agents/       # 多代理测试脚本
│   └── fixtures/     # 并发测试数据
├── reports/          # 测试报告输出
└── screenshots/      # UI 截图

```

## 测试类型

### 1. UI 自动化测试（Maestro）

使用 Maestro 进行端到端 UI 测试，覆盖 16 个核心功能流程：

**公开访问功能（4个流程）**
- 公开试用申请流程
- 访客浏览公开内容
- 试用密钥获取与使用
- 公开文档访问

**管理员功能（6个流程）**
- 供应商管理（增删改查）
- 租户管理与权限配置
- 密钥生成与管理
- 监控面板查看
- 路由策略配置
- 系统配置管理

**租户门户功能（4个流程）**
- 租户登录与仪表板
- API 密钥管理
- 使用统计查看
- 账单信息查看

**实时功能（2个流程）**
- 实时监控面板
- 会话详情查看

**运行测试：**
```bash
# 单个流程
maestro test tests/maestro/flows/01-public-trial.yaml

# 所有流程
maestro test tests/maestro/flows/
```

### 2. 性能测试（k6）

使用 k6 进行负载与性能测试：

**测试场景：**
- API 负载测试（并发请求、吞吐量）
- 流式响应压力测试（SSE 长连接）
- 管理端点性能测试
- 数据库查询性能测试

**运行测试：**
```bash
# API 负载测试
k6 run tests/k6/scenarios/api-load.js

# 流式压力测试
k6 run tests/k6/scenarios/streaming-load.js

# 完整性能测试套件
./tests/scripts/run-performance-tests.sh
```

### 3. 并发测试（多代理）

模拟多个独立代理同时操作，测试系统并发处理能力：

**测试维度：**
- 多租户并发操作
- 凭据探测并行执行
- 路由决策竞争条件
- 数据库并发写入

**运行测试：**
```bash
# 并发测试
./tests/scripts/run-concurrent-tests.sh --agents 10

# 指定场景
./tests/scripts/run-concurrent-tests.sh --scenario multi-tenant --agents 5
```

### 4. UI 主题一致性测试

验证明暗主题下的视觉一致性：

**检查项：**
- 配色方案一致性
- 无明显反色区域
- 菜单配色与皮肤匹配
- 与 RedClaw 风格对齐

**运行测试：**
```bash
./tests/scripts/verify-theme-consistency.sh
```

## 环境配置

### 本地 Docker 环境

```bash
# 启动服务
docker-compose up -d

# 等待服务就绪
./tests/scripts/wait-for-services.sh

# 运行测试
./tests/scripts/run-tests.sh --env local
```

### 211 生产环境

```bash
# 配置环境变量
export BASE_URL=http://172.16.2.211:8780
export ADMIN_TOKEN=your_admin_token

# 运行测试
./tests/scripts/run-tests.sh --env 211
```

## 测试报告

测试完成后，报告生成在 `tests/reports/` 目录：

- `summary.html` - 测试摘要（可视化）
- `detailed.json` - 详细结果（JSON）
- `performance.csv` - 性能指标（CSV）
- `screenshots/` - UI 截图

## 快速开始

```bash
# 1. 安装依赖
npm install -g @maestro/cli
brew install k6

# 2. 启动本地环境
docker-compose up -d

# 3. 运行完整测试套件
./tests/scripts/run-all-tests.sh

# 4. 查看报告
open tests/reports/summary.html
```

## CI/CD 集成

GitHub Actions 工作流配置示例：

```yaml
- name: Run Test Suite
  run: |
    ./tests/scripts/run-all-tests.sh --ci
    
- name: Upload Reports
  uses: actions/upload-artifact@v3
  with:
    name: test-reports
    path: tests/reports/
```

## 故障排查

### Maestro 测试失败
- 检查 UI 元素选择器是否变化
- 查看 `tests/screenshots/` 下的失败截图
- 增加等待时间或重试次数

### k6 性能测试超时
- 检查网络连接
- 调整虚拟用户数和并发度
- 查看服务器资源使用情况

### 并发测试不稳定
- 检查数据库连接池配置
- 查看日志文件识别竞争条件
- 隔离运行单个场景定位问题

## 维护指南

- 新增功能时同步更新测试用例
- 定期审查测试覆盖率
- 性能基线每月更新
- 保持测试数据时效性

## 参考资料

- [Maestro 官方文档](https://maestro.mobile.dev/)
- [k6 官方文档](https://k6.io/docs/)
- [LLM Gateway 架构文档](../docs/architecture/)
- [API 接口文档](../docs/api/)
