---
archived_from: (legacy) docs/archive/2026-07/QUICK_START.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# 网关打包与下载 - 快速开始

> 本指南帮助你快速开始使用自动化打包系统

## 🚀 快速验证

### 1. 环境检查（必须）

```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 运行环境验证
bash scripts/build/test-build-dry-run.sh
```

**预期输出**: 所有检查项 ✅ 通过

### 2. 测试后端编译（5分钟）

```bash
# 编译所有平台
bash scripts/build/build-backend.sh $(pwd) "2.4.7-test-$(date +%s)"

# 查看产物
ls -lh build/bin/
```

**预期输出**: 5个平台的二进制文件

### 3. 查看文档

```bash
# 完整实施方案
open docs/分发与激活/15-自动化打包与分发实施方案.md

# E2E测试方案
open docs/分发与激活/16-E2E测试方案.md

# 实施总结
open docs/分发与激活/17-实施总结与下一步.md

# 实施报告
open BUILD_AUTOMATION_REPORT.md
```

## 📦 完整构建流程（暂不推荐执行）

> ⚠️ 注意：完整构建需要约30分钟，会占用大量磁盘空间（~5GB）

```bash
# 完整构建（包含后端、前端、Docker）
bash scripts/build/build-pipeline.sh HEAD ~/Downloads/deploy

# 查看产物
ls -lh build/releases/archives/
ls -lh build/releases/images/
```

## 📋 下一步工作

### Phase 2: 部署测试（本周）

需要实现：
- [ ] `scripts/deploy/deploy-to-245.sh` - 部署到245
- [ ] `scripts/deploy/health-check.sh` - 健康检查
- [ ] 集成 `deploy-245` skill

### Phase 3: 文件上传（下周）

需要实现：
- [ ] Cloudreve 集成
- [ ] 版本管理 API
- [ ] 数据库表创建

## 📚 相关文档

| 文档 | 说明 |
|------|------|
| [15-自动化打包与分发实施方案](docs/分发与激活/15-自动化打包与分发实施方案.md) | 完整技术方案 |
| [16-E2E测试方案](docs/分发与激活/16-E2E测试方案.md) | 测试方案 |
| [17-实施总结与下一步](docs/分发与激活/17-实施总结与下一步.md) | 进度和计划 |
| [BUILD_AUTOMATION_REPORT](BUILD_AUTOMATION_REPORT.md) | 实施报告 |

## 🛠️ 脚本清单

| 脚本 | 功能 | 状态 |
|------|------|------|
| `scripts/build/build-pipeline.sh` | 主构建流程 | ✅ |
| `scripts/build/build-backend.sh` | 后端编译 | ✅ |
| `scripts/build/build-frontend.sh` | 前端编译 | ✅ |
| `scripts/build/build-docker.sh` | Docker镜像 | ✅ |
| `scripts/build/package-release.sh` | 打包 | ✅ |
| `scripts/build/verify-binaries.sh` | 验证 | ✅ |
| `scripts/install/install.sh` | 用户安装 | ✅ |

## ❓ 常见问题

**Q: 构建失败怎么办？**
A: 查看日志文件 `build/logs/build-*.log`，根据错误信息排查。

**Q: 如何只编译某个平台？**
A: 编辑 `build-backend.sh`，注释掉不需要的平台。

**Q: 如何测试安装脚本？**
A: 在虚拟机或Docker容器中测试，避免影响本地环境。

**Q: Cloudreve 账号是什么？**
A: 56551681@qq.com / Veritrans&9527（需验证可用性）

## 📞 支持

如有问题，请查看：
1. 文档：`docs/分发与激活/`
2. 日志：`build/logs/`
3. 脚本注释：每个脚本都有详细注释

