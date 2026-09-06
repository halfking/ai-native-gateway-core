# 📖 LLM Gateway 本地部署测试文档索引

欢迎使用 LLM Gateway 本地部署测试文档！本索引帮助您快速找到所需信息。

---

## 🎯 快速导航

### 我想...

| 需求 | 推荐文档 | 预计时间 |
|------|---------|---------|
| **快速部署并验证** | [快速参考 →](./LOCAL_DEPLOYMENT_TESTING_README.md) | 5 分钟 |
| **了解完整部署流程** | [完整指南 第 3 章 →](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#3-部署流程) | 15 分钟 |
| **排查部署问题** | [故障排查 →](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查) | 按需 |
| **了解验证层级** | [验证章节 →](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#4-验证层级) | 10 分钟 |
| **运行自动化测试** | [验证脚本 →](../scripts/verify-local-deployment.sh) | 1 分钟 |
| **查看测试场景** | [测试场景 →](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#5-测试场景) | 20 分钟 |
| **了解文档体系** | [文档总结 →](./LOCAL_DEPLOYMENT_TESTING_SUMMARY.md) | 5 分钟 |

---

## 📚 文档列表

### 1. 核心文档

#### [LOCAL_DEPLOYMENT_TEST_GUIDE.md](./LOCAL_DEPLOYMENT_TEST_GUIDE.md)
**完整的本地部署测试指南** — 50+ 页

```
适合: 首次部署、完整学习、深度参考
内容: 部署流程、验证层级、测试场景、故障排查、高级用法
```

**章节导航**:
- [1. 快速开始](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#1-快速开始) — 3 步上手
- [2. 前置条件](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#2-前置条件) — 环境准备
- [3. 部署流程](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#3-部署流程) — 详细步骤
- [4. 验证层级](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#4-验证层级) — L1-L4 分层验证
- [5. 测试场景](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#5-测试场景) — Mock、压力测试
- [6. 故障排查](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查) — 问题解决
- [7. 清理与重置](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#7-清理与重置) — 数据管理
- [8. 高级用法](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#8-高级用法) — 自定义部署

---

#### [LOCAL_DEPLOYMENT_TESTING_README.md](./LOCAL_DEPLOYMENT_TESTING_README.md)
**快速参考指南** — 单页概览

```
适合: 快速查找、命令速查、新用户入门
内容: 文档索引、脚本用法、验证层级、故障速查
```

---

#### [LOCAL_DEPLOYMENT_TESTING_SUMMARY.md](./LOCAL_DEPLOYMENT_TESTING_SUMMARY.md)
**文档创建总结** — 文档体系说明

```
适合: 了解文档结构、更新日志、维护指南
内容: 文档组织、覆盖范围、最佳实践、已知限制
```

---

### 2. 可执行脚本

#### [scripts/verify-local-deployment.sh](../scripts/verify-local-deployment.sh)
**自动化验证脚本**

```bash
# 完整验证 (L1-L4)
./scripts/verify-local-deployment.sh

# 快速验证 (L1-L2, ~5 秒)
./scripts/verify-local-deployment.sh --quick

# 包含 Mock Provider 测试
./scripts/verify-local-deployment.sh --with-mock

# 指定端口
./scripts/verify-local-deployment.sh --port 8783

# 查看帮助
./scripts/verify-local-deployment.sh --help
```

**输出**:
- 控制台彩色输出（实时）
- Markdown 报告（`/tmp/llm-gateway-verify-report-*.md`）

---

### 3. 相关测试文档

#### 测试策略
- [test-matrix.md](./05-testing/01-strategy/test-matrix.md) — 测试矩阵

#### 测试方案
- [comprehensive-test-plan.md](./05-testing/02-test-plans/testing/comprehensive-test-plan.md) — 全方位测试方案
- [客户端会话保持-测试方案与用例.md](./05-testing/02-test-plans/会话优化v4/客户端会话保持-测试方案与用例.md) — 会话优化测试

#### 测试用例
- [TC-GOAL-CONTINUE.md](./05-testing/03-test-cases/TC-GOAL-CONTINUE.md) — `gw-continue` 测试
- [TC-GOAL-HANDOFF.md](./05-testing/03-test-cases/TC-GOAL-HANDOFF.md) — `gw-handoff` 测试

---

## 🚀 快速开始（3 步）

### 第 1 步: 部署
```bash
# 克隆或进入项目目录
cd llm-gateway-go

# 一键部署
./scripts/deploy-local.sh deploy
```

### 第 2 步: 验证
```bash
# 自动验证
./scripts/verify-local-deployment.sh

# 或快速验证（5 秒）
./scripts/verify-local-deployment.sh --quick
```

### 第 3 步: 使用
```bash
# 访问 UI
open http://127.0.0.1:8782

# 测试 API
curl http://127.0.0.1:8782/healthz
```

---

## 📊 验证层级速览

| 层级 | 名称 | 检查内容 | 时间 |
|------|------|---------|------|
| **L1** | 基础健康检查 | `/healthz`, `/readyz`, `/version` | < 1s |
| **L2** | 依赖连通性 | PostgreSQL, Redis, Docker | < 2s |
| **L3** | 功能链路 | Admin API, OpenAI API, 前端 | < 3s |
| **L4** | 业务场景 | 日志、进程、数据库表、Mock | < 5s |

**总计**: 完整验证约 10 秒

---

## 🔍 常见问题速查

| 问题 | 可能原因 | 快速解决 |
|------|---------|---------|
| `connection refused` | 网关未启动 | `./scripts/deploy-local.sh start` |
| `database: error` | PostgreSQL 未运行 | `docker start llm-gateway-pg` |
| `redis: error` | Redis 未运行 | `docker start llm-gateway-redis` |
| `port already in use` | 端口被占用 | `lsof -i :8782` 并停止占用进程 |
| `permission denied` | 脚本无执行权限 | `chmod +x scripts/*.sh` |

详细故障排查: [完整指南第 6 章 →](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查)

---

## 📁 文件组织

```
docs/
├── LOCAL_DEPLOYMENT_TEST_GUIDE.md          # 主文档 (50+ 页)
├── LOCAL_DEPLOYMENT_TESTING_README.md      # 快速参考 (单页)
├── LOCAL_DEPLOYMENT_TESTING_SUMMARY.md     # 文档总结
├── LOCAL_DEPLOYMENT_TESTING_INDEX.md       # 本文档 (索引)
└── 05-testing/                             # 相关测试文档
    ├── 01-strategy/test-matrix.md
    ├── 02-test-plans/
    │   ├── testing/comprehensive-test-plan.md
    │   └── 会话优化v4/客户端会话保持-测试方案与用例.md
    └── 03-test-cases/
        ├── TC-GOAL-CONTINUE.md
        └── TC-GOAL-HANDOFF.md

scripts/
├── deploy-local.sh                         # 主部署脚本
├── verify-local-deployment.sh              # 验证脚本 (新)
└── comprehensive-data-test-current.sh      # 压力测试脚本

输出/
└── /tmp/llm-gateway-verify-report-*.md     # 验证报告 (自动生成)
```

---

## 🎓 推荐学习路径

### 新用户（从未部署过）

1. ⏱️ **5 分钟**: 阅读 [快速参考](./LOCAL_DEPLOYMENT_TESTING_README.md)
2. ⏱️ **3 分钟**: 执行 [快速开始 3 步](#快速开始3-步)
3. ⏱️ **10 秒**: 运行 `./scripts/verify-local-deployment.sh`
4. ⏱️ **15 分钟**: 浏览 [完整指南第 4 章](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#4-验证层级)（了解验证原理）

**总计**: 约 25 分钟可以完成首次部署和验证

---

### 开发者（日常使用）

1. 修改代码
2. 执行 `./scripts/deploy-local.sh deploy --no-frontend`
3. 运行 `./scripts/verify-local-deployment.sh --quick`（5 秒）
4. 如有问题，查阅 [故障排查](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查)

---

### 测试工程师（深度测试）

1. 阅读 [完整指南](./LOCAL_DEPLOYMENT_TEST_GUIDE.md)（全部章节）
2. 理解 [验证层级](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#4-验证层级)（L1-L4）
3. 执行 [测试场景](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#5-测试场景)（Mock + 压力测试）
4. 参考 [comprehensive-test-plan.md](./05-testing/02-test-plans/testing/comprehensive-test-plan.md)（业务场景）

---

## 🔗 外部资源

### 官方文档
- [README.md](../README.md) — 项目主文档
- [VERSION](../VERSION) — 当前版本号

### 配置文件
- `.env.example` — 环境变量示例
- `docker-compose.yml` — Docker 编排配置

### 其他脚本
- `scripts/bump-version.sh` — 版本管理
- `scripts/comprehensive-data-test-current.sh` — 综合测试

---

## 📞 获取帮助

### 文档内查找
```bash
# 搜索关键词
grep -r "keyword" docs/LOCAL_DEPLOYMENT*.md

# 查看特定章节
open docs/LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查
```

### 命令行帮助
```bash
# 部署脚本帮助
./scripts/deploy-local.sh --help

# 验证脚本帮助
./scripts/verify-local-deployment.sh --help
```

### 日志诊断
```bash
# 查看实时日志
tail -f ~/kaixuan/llm-gateway-go/logs/gateway-8782.log

# 查看错误日志
grep -E "ERROR|PANIC|FATAL" ~/kaixuan/llm-gateway-go/logs/gateway-8782.log
```

### 验证报告
```bash
# 查看最新验证报告
ls -lt /tmp/llm-gateway-verify-report-*.md | head -1 | awk '{print $NF}' | xargs cat
```

---

## 🎉 开始使用

准备好了吗？选择您的路径：

- 🚀 [快速开始](#快速开始3-步) — 立即部署
- 📖 [阅读完整指南](./LOCAL_DEPLOYMENT_TEST_GUIDE.md) — 深入学习
- 🔧 [运行验证脚本](#2-可执行脚本) — 自动检查
- 🔍 [故障排查](./LOCAL_DEPLOYMENT_TEST_GUIDE.md#6-故障排查) — 解决问题

---

**文档版本**: v1.0  
**最后更新**: 2026-09-06  
**维护者**: LLM Gateway Team  

祝部署顺利！🎊
