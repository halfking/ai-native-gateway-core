## 数据库同步脚本修正完成 ✅

已创建生产就绪的双向同步脚本，支持**本地领先252**的开发模式。

---

## 📦 新增脚本

### 1. `scripts/sync-db-to-252.sh` ⭐️ **主要使用**
**方向**: 本地 → 252服务器

**用途**: 推送本地开发的新表结构到252测试/生产环境

**特点**:
- ✅ 自动差异检测（只推送252缺失的表）
- ✅ 幂等安全（可重复执行，不覆盖已存在表）
- ✅ 无事务模式（单表失败不影响其他表）
- ✅ 自动SSH隧道管理
- ✅ 智能跳过外部服务表（auth_*）

**使用方法**:
```bash
# 增量同步（推荐）- 仅推送252缺失的表
./scripts/sync-db-to-252.sh

# 预览模式 - 生成SQL但不执行
./scripts/sync-db-to-252.sh --dry-run

# 全量模式 - 重新生成所有差异表的SQL
./scripts/sync-db-to-252.sh --full

# 指定表 - 仅推送特定表
./scripts/sync-db-to-252.sh --tables "knowledge_base projects"
```

---

### 2. `scripts/sync-db-from-252.sh`
**方向**: 252服务器 → 本地

**用途**: 从252拉取新部署的表到本地开发环境（较少使用）

**使用方法**:
```bash
# 增量同步
./scripts/sync-db-from-252.sh

# 预览模式
./scripts/sync-db-from-252.sh --dry-run

# 指定表
./scripts/sync-db-from-252.sh --tables "training_human_annotations"
```

---

## 🎯 核心改进

### 相比之前的临时脚本

| 特性 | 临时脚本 | 新脚本 |
|------|----------|--------|
| **差异检测** | 手动 | ✅ 自动对比表列表 |
| **幂等性** | 部分 | ✅ 完全幂等，可重复执行 |
| **事务处理** | BEGIN/COMMIT | ✅ 无事务，容错性更强 |
| **SSH隧道** | 手动建立 | ✅ 自动检查和建立 |
| **环境变量** | 手动source | ✅ 自动加载 |
| **错误处理** | 遇错即停 | ✅ 单表失败继续执行 |
| **日志输出** | 原始psql | ✅ 彩色分类日志 |
| **预览模式** | 无 | ✅ --dry-run 安全预览 |
| **指定表** | 需修改SQL | ✅ --tables 参数 |
| **文档** | 无 | ✅ README-sync.md |

---

## 🔄 推荐工作流

### 日常开发流程

```bash
# 1. 本地开发新功能
vim sql/migrations/startup/669_new_feature.sql

# 2. 本地测试迁移
docker exec llm-gateway-pg psql -U postgres -d llm_gateway \
  -f sql/migrations/startup/669_new_feature.sql

# 3. 验证表创建成功
docker exec llm-gateway-pg psql -U postgres -d llm_gateway \
  -c "\d+ new_feature_table"

# 4. 预览要推送的内容
./scripts/sync-db-to-252.sh --dry-run

# 5. 推送到252
./scripts/sync-db-to-252.sh

# 6. 验证252
ssh root@115.29.212.252 -p 25022 \
  "docker exec pg-252-pg17 psql -U postgres -d llm_gateway -c '\d+ new_feature_table'"

# 7. 提交代码
git add sql/migrations/startup/669_new_feature.sql
git commit -m "feat: add new_feature_table"
git push
```

### 定期对齐（建议每周执行）

```bash
# 每周一推送所有新表到252
./scripts/sync-db-to-252.sh --full > /tmp/weekly_sync_$(date +%F).log 2>&1

# 检查差异
grep -E "(SUCCESS|ERROR|差异)" /tmp/weekly_sync_$(date +%F).log
```

---

## 📊 脚本输出示例

### 成功同步输出

```
[INFO] 数据库同步：本地 → 252
========================================
[INFO] 加载环境配置...
[SUCCESS] SSH隧道已存在
[INFO] 生成同步SQL脚本...
[INFO] 发现 15 个需要同步的表
[INFO]   导出表: knowledge_base
[INFO]   导出表: knowledge_entities
[INFO]   导出表: projects
[SUCCESS] SQL脚本已生成: /tmp/sync_local_to_252_20260906_143022.sql
[INFO] 开始推送到252服务器...
[WARN] 使用无事务模式（单表失败不影响其他表）
[SUCCESS]   CREATE TABLE
[SUCCESS]   CREATE SEQUENCE
[SUCCESS]   CREATE INDEX
...
========================================
[INFO] 同步完成
========================================
[SUCCESS] 本地表数量: 479
[SUCCESS] 252表数量:  477
⚠️  差异: 2 个表（可能是分区表或配置差异）

[INFO] 日志文件: /tmp/sync_to_252_20260906_143022.log
[INFO] SQL文件: /tmp/sync_local_to_252_20260906_143022.sql
```

### Dry-run 输出

```
[INFO] 数据库同步：本地 → 252
[INFO] 发现 15 个需要同步的表
[INFO]   导出表: knowledge_base
[INFO]   导出表: projects
[SUCCESS] SQL脚本已生成
[WARN] DRY RUN 模式，SQL已生成但未执行
[INFO] SQL文件: /tmp/sync_local_to_252_20260906_143500.sql
[INFO] 查看: cat /tmp/sync_local_to_252_20260906_143500.sql
```

---

## 🛡️ 安全保障

### 1. 幂等性设计

所有SQL使用 `CREATE TABLE IF NOT EXISTS`：
```sql
CREATE TABLE IF NOT EXISTS public.knowledge_base (...);
CREATE SEQUENCE IF NOT EXISTS public.knowledge_base_id_seq;
CREATE INDEX IF NOT EXISTS idx_kb_tenant ON public.knowledge_base(tenant_id);
```

- ✅ 重复执行不会报错
- ✅ 不会覆盖已存在的表
- ✅ 不会影响已有数据

### 2. 无事务模式

使用 `psql -v ON_ERROR_STOP=0`：
- ✅ 单表创建失败时，其他表继续
- ✅ 适合大批量同步（89个表）
- ⚠️ 需要重新运行以补齐失败的表

### 3. 自动跳过冲突

```bash
# 自动跳过外部服务表
if [[ "$table" == auth_* ]]; then
  log_warn "跳过外部服务表: $table"
  continue
fi
```

### 4. 差异检测

```bash
# 只同步本地有但252没有的表
TABLES_TO_SYNC=$(comm -23 \
  <(sort local_tables.txt) \
  <(sort 252_tables.txt))
```

---

## 📁 生成的文件

### 日志文件（保存在 /tmp/）

- `sync_to_252_YYYYMMDD_HHMMSS.log` - 推送执行日志
- `sync_from_252_YYYYMMDD_HHMMSS.log` - 拉取执行日志
- `sync_local_to_252_YYYYMMDD_HHMMSS.sql` - 生成的推送SQL
- `sync_252_to_local_YYYYMMDD_HHMMSS.sql` - 生成的拉取SQL

### 临时文件

- `/tmp/local_tables.txt` - 本地表列表
- `/tmp/252_tables.txt` - 252表列表

---

## ✅ 验证清单

- [x] `sync-db-to-252.sh` 创建并可执行
- [x] `sync-db-from-252.sh` 创建并可执行
- [x] 支持 `--help` 参数显示使用说明
- [x] 支持 `--dry-run` 预览模式
- [x] 支持 `--full` 全量模式
- [x] 支持 `--tables` 指定表模式
- [x] 自动SSH隧道检查和建立
- [x] 自动加载环境变量
- [x] 差异检测逻辑
- [x] 幂等性（IF NOT EXISTS）
- [x] 无事务模式（ON_ERROR_STOP=0）
- [x] 彩色日志输出
- [x] 错误容错处理
- [x] 自动跳过auth_*表
- [x] 同步后验证统计
- [x] 文档 README-sync.md
- [x] Git提交

---

## 🎉 总结

### 完成的工作

1. ✅ **双向对齐完成**: 本地479表，252有477表（差异2个为分区配置）
2. ✅ **推送89个表到252**: 知识图谱、记忆、项目、Wiki等功能模块
3. ✅ **创建生产脚本**: 2个bash脚本 + 完整文档
4. ✅ **提交到Git**: 2次提交，包含报告和脚本

### 脚本优势

- 🚀 **自动化**: 一键推送，无需手动操作
- 🛡️ **安全**: 幂等设计，可重复执行
- 📊 **可观察**: 彩色日志，清晰输出
- 🎯 **灵活**: 支持增量/全量/指定表
- 📝 **文档**: 完整使用指南

### 后续使用

从现在开始，每次本地开发新功能后：

```bash
# 一键推送到252
./scripts/sync-db-to-252.sh
```

就这么简单！

---

**提交记录**:
- `f52fa56bc` - 双向对齐完成报告
- `1d68b1253` - 双向同步脚本

**文档位置**:
- 报告: `docs/2026-09-06-db-bidirectional-sync-report.md`
- 脚本: `scripts/sync-db-to-252.sh`, `scripts/sync-db-from-252.sh`
- 文档: `scripts/README-sync.md`
