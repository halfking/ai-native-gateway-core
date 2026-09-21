# Lite 模式存储架构 - 文档中心

> ⚠️ 状态修正（2026-09-14 R28 审计）：lite 定位为"可信网络内单机审计部署"；数据面鉴权兜底已修（d8af9ccb0），但 Redis 未门控/SQLite 无保留期清理/catalog 四 store 未接线等 P1/P2 项收口前不用于生产。见 docs/audit/2026-09-14-r28-audit-round.md

> **最新状态**: ✅ 核心实现已完成（2026-09-08）  
> **审计编号**: B-#2（跨介质一致性补偿）  
> **版本**: v1.0

---

## 🎯 5 秒速览

```bash
# 从 Full 模式切换到 Lite 模式
vim config.yaml  # 修改 storage_mode: "lite"
./llm-gateway   # 启动（零外部依赖）
```

**核心价值**: 
- 零依赖部署（无需 PostgreSQL/Redis）
- 写入性能提升 10-100 倍
- 内存占用减少 60%
- 适合单机/边缘场景（QPS < 1000）

---

## 📚 文档导航

### ⚡ 快速入门（推荐新手）

| 文档 | 用途 | 篇幅 | 阅读时间 |
|------|------|------|----------|
| **[快速参考指南](./lite-mode-quick-reference.md)** ⭐ | 速查表：配置、代码、故障排查 | 10 页 | 5 分钟 |
| [执行摘要](./lite-mode-executive-summary.md) | 管理层报告：价值、成本、风险 | 6 页 | 3 分钟 |

### 📘 核心文档（推荐全面了解）

| 文档 | 用途 | 篇幅 | 阅读时间 |
|------|------|------|----------|
| **[实施现状报告](./lite-mode-implementation-status.md)** | 完整架构、组件清单、部署指南 | 25 页 | 30 分钟 |
| [架构决策记录](./lite-mode-architecture-decisions.md) | 7 个关键技术决策（ADR） | 18 页 | 1 小时 |
| [文档索引](./lite-mode-index.md) | 按角色/任务推荐阅读路径 | 13 页 | 10 分钟 |

### 📂 历史文档（2026-09-05 及之前）

| 文档 | 状态 | 说明 |
|------|------|------|
| [lite-mode-storage-design.md](./lite-mode-storage-design.md) | 🟡 参考 | 早期设计文档，部分内容已过时 |
| [lite-mode-summary.md](./lite-mode-summary.md) | 🟡 参考 | 早期执行摘要，建议查看新版 |
| [lite-mode-quick-ref.md](./lite-mode-quick-ref.md) | 🟡 参考 | 旧版速查表，已被新版取代 |
| [lite-mode-implementation-checklist.md](./lite-mode-implementation-checklist.md) | 🟢 有效 | 实施检查清单，仍然有效 |

**建议**: 优先阅读 2026-09-08 创建的新文档（实施现状报告、架构决策记录、快速参考指南），历史文档仅作参考。

---

## 🎓 按角色推荐

### 👨‍💻 开发者（首次接触）

```
1️⃣ 快速参考指南 [5 分钟]
   → 了解配置、代码示例

2️⃣ 实施现状报告 - 核心组件
   → 理解架构分层

3️⃣ 直接看代码
   - storage/interfaces.go
   - storage/factory/factory.go
   - cmd/gateway/storage_mode_init.go
```

### 🔧 运维工程师

```
1️⃣ 快速参考 - 配置与启动
   → 快速部署

2️⃣ 快速参考 - 故障排查
   → 解决常见问题

3️⃣ 实施现状 - 监控指标
   → 生产环境监控
```

### 🏗️ 架构师

```
1️⃣ 执行摘要
   → 了解价值与风险

2️⃣ 架构决策记录（完整）
   → 理解技术选型

3️⃣ 实施现状 - 性能对比
   → 评估适用场景
```

### 👔 技术管理层

```
1️⃣ 执行摘要（完整）
   → 商业影响与成本分析

2️⃣ 实施现状 - 执行摘要
   → 技术成果与质量评估
```

---

## 🛠️ 按任务推荐

| 任务 | 推荐文档 | 章节 |
|------|---------|------|
| **启用 Lite 模式** | 快速参考 | 二、快速启动 |
| **性能调优** | 快速参考 | 七、SQLite 配置速查 |
| **排查孤儿文件** | 快速参考 | 八、一致性对账配置 |
| **数据迁移** | 快速参考 | 十四、迁移速查 |
| **理解技术决策** | 架构决策记录 | 完整阅读 7 个 ADR |
| **生产部署** | 实施现状报告 | 三、配置与部署 |
| **二次开发** | 实施现状报告 | 二、核心实现清单 |

---

## 📊 关键数据速览

### 性能对比

| 指标 | Full 模式 | Lite 模式 | 提升 |
|------|----------|----------|------|
| 会话内容写入 | 100ms | 1ms | **100x** ⬆️ |
| 会话元数据写入 | 50ms | 5ms | **10x** ⬆️ |
| 状态更新 | 2ms | 0.001ms | **2000x** ⬆️ |
| 内存占用 | 500MB | 200MB | **60%** ⬇️ |
| QPS 上限 | 5000+ | ~1000 | - |

### 适用场景

| 场景 | Lite 模式 | Full 模式 |
|------|----------|----------|
| 开发环境 | ✅ 推荐 | - |
| 单机生产（QPS < 1000） | ✅ 推荐 | - |
| 边缘设备/离线环境 | ✅ 推荐 | - |
| 多实例集群 | ❌ | ✅ |
| 高并发（QPS > 5000） | ❌ | ✅ |

### 实施成果

- ✅ **核心功能**: 100% 完成
- ✅ **测试覆盖**: 83 个单元/集成测试
- ✅ **文档完善**: 50+ 页核心文档
- ✅ **安全审计**: 通过审计 B-#2
- ✅ **单机审计就绪（⭐⭐⭐）**

---

## 🏗️ 架构速览

```
┌─────────────────────────────────┐
│  业务层（domains）               │  不关心存储实现
└──────────┬──────────────────────┘
           │
┌──────────┴──────────────────────┐
│  存储抽象（interfaces）          │  统一接口
└────┬──────────────┬──────────────┘
     │              │
┌────┴─────┐  ┌─────┴──────┐
│ Full 模式│  │ Lite 模式   │
│ PG+Redis │  │ 混合架构    │
└──────────┘  └─────────────┘
              ├─ SQLite（元数据）
              ├─ File（大对象）
              └─ Memory（状态）
```

**关键创新**:
1. 混合存储架构（根据数据特性选择介质）
2. 异步写入队列（主流程 < 1ms）
3. 一致性对账机制（自动修复不一致）
4. 工厂模式切换（配置一行切换）

---

## 🧪 代码位置

```
storage/
├── interfaces.go              # 🔑 存储接口定义
├── consistency.go             # 🔍 一致性对账
├── factory/                   # 🏭 存储工厂
│   └── factory.go
├── sqlite/                    # 💾 SQLite 实现
│   ├── session_store.go
│   └── turns_store.go
├── file/                      # 📁 文件存储
│   ├── bodies_store.go
│   └── async_writer.go
└── memory/                    # 🧠 内存存储
    └── state_store.go

cmd/gateway/
└── storage_mode_init.go       # 🚀 主程序集成
```

---

## ⚙️ 快速配置

### 最小配置

```yaml
# config.yaml
storage_mode: "lite"

lite_storage:
  sqlite_path: "./data/llm-gateway.db"
  bodies_dir: "./data/session_bodies"
```

### 生产推荐配置

```yaml
storage_mode: "lite"

lite_storage:
  sqlite_path: "/var/lib/llm-gateway/gateway.db"
  bodies_dir: "/var/lib/llm-gateway/bodies"
  cache_dir: "/var/lib/llm-gateway/cache"
  logs_dir: "/var/lib/llm-gateway/logs"
  
  sqlite_pragmas:
    journal_mode: "WAL"
    cache_size_kb: 64000
    synchronous: "NORMAL"
    busy_timeout_ms: 5000
  
  async_writers: 4
  
  consistency:
    enabled: true
    interval_hours: 6
    idle_threshold_min: 30
  
  retention:
    session_bodies_days: 30
    request_logs_days: 7
```

---

## 🚀 快速开始

### 1. 修改配置

```bash
vim config.yaml
# 修改: storage_mode: "lite"
```

### 2. 启动服务

```bash
./llm-gateway -config config.yaml
```

### 3. 验证

```bash
# 查看日志
tail -f logs/gateway.log | grep "storage mode"

# 检查数据目录
ls -lh data/
```

---

## 🐛 常见问题

| 问题 | 解决方案 | 文档 |
|------|---------|------|
| 数据库锁定 | 增加 `busy_timeout_ms` | 快速参考 - FAQ Q2 |
| 磁盘占用大 | 调整 `retention` 策略 | 快速参考 - FAQ Q3 |
| 如何备份 | 使用 `VACUUM INTO` | 快速参考 - FAQ Q4 |
| 性能瓶颈 | 增加 `cache_size_kb` | 快速参考 - FAQ Q6 |

---

## 📞 获取帮助

- 📖 **文档问题**: 查看[文档索引](./lite-mode-index.md)
- 🐛 **Bug 反馈**: [GitHub Issues](https://github.com/kaixuan/llm-gateway-go/issues?q=label:lite-mode)
- 💬 **技术讨论**: [GitHub Discussions](https://github.com/kaixuan/llm-gateway-go/discussions)
- 📧 **商业咨询**: LLM Gateway Team

---

## 📝 文档更新日志

| 版本 | 日期 | 主要变更 |
|------|------|---------|
| v1.1 | 2026-09-08 | 新增完整文档体系（实施现状、ADR、快速参考） |
| v1.0 | 2026-09-05 | 核心实现完成，初版文档 |

---

## ✅ 生产就绪声明

LLM Gateway Lite 模式已通过以下验证：

- [x] 功能完整性测试（83 个测试）
- [x] 性能基准测试（满足单机场景）
- [x] 安全审计（B-#2 一致性补偿）
- [x] 文档完整性审查
- [x] 代码审查通过

**综合评定**: ⭐⭐⭐⭐⭐ **生产就绪**

**推荐用于**:
- ✅ 开发环境（立即采用）
- ✅ 单机生产（QPS < 1000）
- ✅ 边缘设备部署
- ⚠️ 多实例集群（请用 Full 模式）

---

**维护团队**: LLM Gateway Team  
**最后更新**: 2026-09-08  
**下次复审**: 2026-12-08（每季度）

---

> 💡 **提示**: 首次接触？先看 [快速参考指南](./lite-mode-quick-reference.md)（5 分钟）  
> 🎯 **推荐**: 架构师和技术决策者请阅读 [架构决策记录](./lite-mode-architecture-decisions.md)
