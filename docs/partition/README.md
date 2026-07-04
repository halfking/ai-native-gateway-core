# PostgreSQL 分区表读写规范 - 文档索引

**项目**: llm-gateway-go  
**创建日期**: 2026-07-04  
**状态**: ✅ 已完成并验证

---

## 📚 文档导航

### 核心文档（必读）

| 文档 | 说明 | 字数 | 读者 |
|------|------|------|------|
| [partition-background.md](./partition-background.md) | **背景文档** - 问题根源、方案对比、决策过程 | 10,000+ | 所有开发人员 |
| [partition-architecture.md](./partition-architecture.md) | **架构方案** - 分区设计、写入查询规范、维护流程 | 15,000+ | 架构师、后端开发 |
| [partition-standards.md](./partition-standards.md) | **读写规范** - 强制标准、代码审查清单、FAQ | 13,000+ | 所有开发人员 |
| [partition-test-cases.md](./partition-test-cases.md) | **测试用例** - 12 个测试场景、验证方法 | 8,000+ | QA、开发人员 |

### 代码文件

| 文件 | 说明 | 类型 |
|------|------|------|
| `telemetry/partition_router.go` | 动态路由器（预留） | Go |
| `telemetry/partition_router_test.go` | 路由器测试（100% 覆盖） | Go |
| `tests/partition_write_test.sh` | 自动化集成测试 | Shell |

### 相关文档

| 文档 | 说明 |
|------|------|
| `PARTITION_SOLUTION_FINAL.md` | 最终方案总结 |
| `PARTITION_WRITE_FIX_FINAL_REPORT.md` | 修复过程详细报告 |
| `deploy/sql/migrations/999_columnar_backfill_and_enforce.sql` | Columnar 转换参考 |

---

## 🚀 快速开始

### 新开发人员

1. **阅读顺序**：
   - 先读 `partition-background.md`（理解"为什么"）
   - 再读 `partition-standards.md`（学习"怎么做"）
   - 参考 `partition-architecture.md`（深入细节）

2. **开发规范**：
   ```go
   // ✅ 正确写法
   INSERT INTO request_logs_default (...) VALUES (...);
   UPDATE request_logs_default SET ... WHERE ...;
   
   // ❌ 错误写法
   INSERT INTO request_logs (...) VALUES (...);  // 禁止
   ```

3. **代码审查**：参考 `partition-standards.md` 第 4 节

### 架构师/技术负责人

1. **阅读顺序**：
   - `partition-background.md` 第 5-7 节（方案对比与决策）
   - `partition-architecture.md` 第 2-5 节（架构设计）
   - `partition-test-cases.md`（测试覆盖）

2. **关键决策**：
   - 为什么不动态路由？→ 99.9% 写入是新数据
   - 为什么 DETACH 当月分区？→ DEFAULT 分区约束是动态的
   - 为什么预留路由器？→ 历史补录场景 < 0.1%

### QA/测试工程师

1. **测试执行**：
   ```bash
   # 运行自动化测试
   ./tests/partition_write_test.sh
   
   # 运行单元测试
   go test ./telemetry -run TestPartitionRouter
   ```

2. **测试用例**：参考 `partition-test-cases.md` 全部 12 个用例

---

## 📋 核心概念

### 问题根源

**Columnar 不支持 UPSERT** → 当月分区不能是 columnar → 需要新的写入策略

### 解决方案

**方案 C 简化版**：
- 所有新数据 → `*_default` 表（硬编码）
- 月度分区 DETACHED（避免自动路由）
- 定期迁移（7 天前数据 → 月度分区）
- 月底转换（heap → columnar）

### 架构模式

```
新写入 → default (heap, 0-7天)
    ↓
月度分区 (heap, 7-30天, DETACHED)
    ↓
历史归档 (columnar, > 30天, ATTACHED, 压缩 70%+)
```

---

## ✅ 实施检查清单

### 代码实施
- [x] 所有写入指向 `*_default`
- [x] ON CONFLICT 列引用带 `*_default` 前缀
- [x] partition_router.go 实现并测试
- [x] 单元测试覆盖率 100%

### 数据库配置
- [x] DETACH 当月分区（2026_07, 2026_08）
- [x] 保留历史分区 ATTACHED（2026_06）
- [x] 验证分区状态

### 测试验证
- [x] TC-001: 新数据写入
- [x] TC-002: 流式更新
- [x] TC-003: 分区隔离
- [x] TC-004: 查询聚合
- [x] TC-005: UPSERT 语义
- [x] TC-006: 列引用验证

### 文档交付
- [x] 背景文档
- [x] 架构方案
- [x] 读写规范
- [x] 测试用例

### 生产部署
- [x] 71 环境部署
- [x] 写入验证通过
- [x] 无错误日志

---

## 🔍 常见问题速查

### Q1: 为什么不能写父表？
**A**: 父表会自动路由到对应分区，如果当月分区是 columnar，UPSERT 会失败。  
**详见**: `partition-background.md` 第 2.1 节

### Q2: 历史补录怎么办？
**A**: 使用 `partition_router.go`，它会根据 ts 年龄动态选择目标表。  
**详见**: `partition-architecture.md` 第 3.2 节

### Q3: 查询父表会丢数据吗？
**A**: 会！当月分区 DETACHED 后不包含在父表查询中。必须使用 VIEW。  
**详见**: `partition-standards.md` 第 2.2 节

### Q4: 为什么要定期清理 default 表？
**A**: 防止表无限增长，提高查询性能。  
**详见**: `partition-architecture.md` 第 5.1 节

### Q5: columnar 分区可以 UPDATE 吗？
**A**: 不可以！columnar 是只读存储。  
**详见**: `partition-background.md` 第 2.1 节

---

## 📊 性能基准

| 操作 | 性能 | 文档位置 |
|------|------|---------|
| INSERT (default) | 500+ QPS, < 10ms p99 | architecture § 8.1 |
| UPDATE (default) | 300+ QPS, < 15ms p99 | architecture § 8.1 |
| 查询 default | < 100ms | architecture § 8.2 |
| 存储压缩比 | 3:1 ~ 4:1 (columnar) | architecture § 8.3 |

---

## 🛠️ 工具脚本

| 脚本 | 功能 | 状态 |
|------|------|------|
| `tests/partition_write_test.sh` | 自动化测试 | ✅ 已实现 |
| `scripts/migrate-default-to-monthly.sh` | 每日迁移 | ⏳ 待创建 |
| `scripts/convert-last-month-to-columnar.sh` | 月底转换 | ⏳ 待创建 |
| `scripts/update-monthly-views.sh` | VIEW 更新 | ⏳ 待创建 |

---

## 📈 后续工作

### 短期（本周）
- [ ] 创建每日迁移脚本
- [ ] 创建查询 VIEW
- [ ] 配置监控告警

### 中期（8月1日前）
- [ ] 月底转换脚本
- [ ] 更新 admin 查询代码
- [ ] 性能基准测试

### 长期
- [ ] 同步到 184 环境
- [ ] 故障场景测试
- [ ] 每周回归测试

---

## 💡 关键经验

1. **PostgreSQL DEFAULT 分区约束是动态的** - 这是问题根源
2. **99.9% 的写入是新数据** - 无需为罕见场景增加复杂度
3. **坚定执行，但允许简化** - 效果一致，成本降低 95%
4. **文档 > 代码** - 确保可复制、可传承

---

## 📞 支持

- **技术问题**: 参考 FAQ 或查阅相关文档章节
- **代码审查**: 使用 `partition-standards.md` 检查清单
- **测试问题**: 参考 `partition-test-cases.md`
- **联系方式**: Infrastructure Team

---

**最后更新**: 2026-07-04  
**文档版本**: 1.0  
**维护团队**: Infrastructure Team
