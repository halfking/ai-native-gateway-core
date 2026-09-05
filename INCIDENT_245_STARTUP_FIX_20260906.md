# 245 启动失败修复记录 (2026-09-06)

## 问题摘要

245 服务器在 2026-09-06 00:10 启动时遇到数据库 schema 迁移失败，导致服务进入 `postgres disabled` 状态。

## 症状

- 启动日志显示：`"postgres disabled","error":"context deadline exceeded","retry_budget":"20s"`
- 重复出现错误：`"cannot drop columns from view (SQLSTATE 42P16)"`
- 服务最终启动但数据库连接被禁用
- 影响：无法处理请求，所有依赖数据库的功能失效

## 根本原因

### 背景

1. **Migration 656** (2026-09-05): 创建 `auto_route_selections_hot` 表和 `auto_route_selections_all` 视图
   - 视图包含基础列 + `storage_tier` 列
   - 使用 `CREATE OR REPLACE VIEW`

2. **Migration 658** (2026-09-05): 添加 15 个结构化特征列
   - 给 `auto_route_selections` 和 `auto_route_selections_hot` 表添加 15 个新列
   - **正确地使用 `DROP VIEW + CREATE VIEW` 重建视图**（因为 PostgreSQL 不允许 `CREATE OR REPLACE VIEW` 改变列位置）

3. **db.go 的 ensureAutoRouteSelectionsHotSchema()**: 启动时确保 schema 存在
   - 目的：在二进制升级但未重新运行迁移的情况下，自动补齐缺失的表结构
   - 问题：**仍然使用 656 的旧视图定义**，缺少 658 的 15 个新列
   - 使用 `CREATE OR REPLACE VIEW` 尝试覆盖视图

### 失败序列

1. 数据库已经通过 658 迁移升级，`auto_route_selections_all` 视图包含 15 个新列
2. 服务启动，执行 `ensureAutoRouteSelectionsHotSchema()`
3. 尝试用旧的 656 视图定义（缺少 15 列）覆盖现有视图
4. PostgreSQL 拒绝：`cannot drop columns from view`
   - 原因：`CREATE OR REPLACE VIEW` 不允许删除列或改变列位置
5. Schema 迁移失败，重试 2 次后超时
6. 服务进入降级模式：`postgres disabled`

### PostgreSQL 视图限制

```sql
-- ✗ 错误：尝试用少列的定义替换多列的视图
CREATE OR REPLACE VIEW v AS SELECT a, b FROM t;  -- 视图有 a, b
CREATE OR REPLACE VIEW v AS SELECT a FROM t;     -- ERROR: cannot drop column b

-- ✓ 正确：先删除再重建
DROP VIEW IF EXISTS v;
CREATE VIEW v AS SELECT a FROM t;
```

## 修复方案

### 代码更改 (commit 1738dcac8)

更新 `db/db.go` 中的 `autoRouteSelectionsHotEnsureSQL` 常量：

1. **添加 15 个结构化特征列的迁移逻辑**：
   ```sql
   DO $$
   BEGIN
     IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                    WHERE table_name = 'auto_route_selections_hot' 
                    AND column_name = 'detected_language') THEN
       ALTER TABLE public.auto_route_selections_hot ADD COLUMN detected_language TEXT;
     END IF;
     -- ... (14 more columns)
   END $$;
   ```

2. **修复视图重建逻辑**：
   ```sql
   -- 旧代码：
   CREATE OR REPLACE VIEW public.auto_route_selections_all AS ...
   
   -- 新代码：
   DROP VIEW IF EXISTS public.auto_route_selections_all;
   CREATE VIEW public.auto_route_selections_all AS ...
   ```

3. **视图 SELECT 包含所有 658 新列**：
   ```sql
   SELECT id, request_id, ..., assignment_key_hash,
     detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
     has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
     intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
     feature_version, content_hash, 'hot'::text AS storage_tier
   FROM public.auto_route_selections_hot
   ```

4. **更新文档注释**：说明此函数镜像 656+658 两个迁移

### 15 个结构化特征列

| 列名 | 类型 | 说明 |
|------|------|------|
| `detected_language` | TEXT | 检测到的语言（zh, en, ja, mixed 等） |
| `prompt_length_bucket` | TEXT | 提示词长度分桶 |
| `context_length_bucket` | TEXT | 上下文长度分桶 |
| `turn_count_bucket` | TEXT | 对话轮次分桶 |
| `has_code_indicator` | BOOLEAN | 是否包含代码 |
| `has_math_indicator` | BOOLEAN | 是否包含数学公式 |
| `has_table_indicator` | BOOLEAN | 是否包含表格 |
| `has_multimedia_indicator` | BOOLEAN | 是否包含多媒体 |
| `intent_category` | TEXT | 意图分类（question, instruction 等） |
| `domain_hint` | TEXT | 领域提示（general, technical 等） |
| `complexity_bucket` | TEXT | 复杂度分桶 |
| `latency_sensitive` | BOOLEAN | 是否延迟敏感 |
| `cost_sensitive` | BOOLEAN | 是否成本敏感 |
| `feature_version` | TEXT | 特征版本（默认 'v1'） |
| `content_hash` | TEXT | 内容哈希（用于去重） |

## 部署验证

### 部署信息
- **时间**: 2026-09-06 00:18-00:20
- **版本**: 2.5.3-b35cec66-20260905-1955 (sequence 1955)
- **服务器**: 8.136.114.245
- **切换时间**: 13s

### 验证结果

✅ 所有检查通过：

```
[verify] ✓ 远端 PG 连通
[verify] ✓ DB 就绪 (1s, authenticated background-tasks=200)
[verify] ✓ healthz + DB + running release 通过，标记 verified
[verify] 凭据解密冒烟: providers=18,1,587 creds=15 failed=0
```

### 日志确认

- ✅ 无 "cannot drop columns from view" 错误
- ✅ 无 "postgres disabled" 错误
- ✅ Schema 迁移成功
- ✅ 服务正常启动并通过所有健康检查

## 影响范围

### 受影响的环境
- **245 (8.136.114.245)**: 已修复并验证
- **154 生产环境**: 需要关注，如果也运行了 658 迁移，会遇到同样的问题

### 幂等性保证
- 对已完整运行 656+658 迁移的数据库：所有 `ALTER TABLE ADD COLUMN IF NOT EXISTS` 和 `DROP VIEW IF EXISTS` 都是幂等的
- 对未运行 658 的数据库：会自动补齐缺失的列和视图定义
- 对新数据库：会完整创建所需的表结构和视图

## 经验教训

1. **视图 schema 演化必须使用 DROP + CREATE**
   - PostgreSQL 的 `CREATE OR REPLACE VIEW` 有严格限制
   - 不能删除列、不能改变列位置、不能改变列类型
   - 安全的做法：`DROP VIEW IF EXISTS` + `CREATE VIEW`

2. **ensure 函数必须与最新迁移保持同步**
   - `ensureAutoRouteSelectionsHotSchema()` 的设计目的是在二进制升级时自动补齐 schema
   - 必须包含所有已发布迁移的 schema 变更
   - 需要在添加迁移时同步更新 ensure 函数

3. **视图变更的测试覆盖**
   - 应该测试"已有旧视图 + 执行新 ensure"的场景
   - 特别是视图列数变化的情况

4. **启动失败的快速诊断**
   - 关键错误：`cannot drop columns from view`
   - 定位方法：`journalctl -u <service> | grep -E "cannot drop|postgres disabled"`
   - 根因分析：对比迁移 SQL 和 ensure SQL 中的视图定义

## 相关文件

- `db/db.go`: `ensureAutoRouteSelectionsHotSchema()` 和 `autoRouteSelectionsHotEnsureSQL`
- `sql/migrations/startup/656_auto_route_selections_hot.sql`: 初始 hot 表和视图
- `sql/migrations/startup/658_auto_route_structured_features.sql`: 15 个特征列 + 视图重建
- Commit: `1738dcac8` - fix(db): 656+658 视图 schema 对齐修复

## 后续行动

- [x] 修复代码并部署到 245
- [x] 验证 245 服务正常运行
- [x] 推送修复到远程仓库
- [ ] 监控 154 生产环境是否有类似问题
- [ ] 考虑添加视图 schema 变更的测试覆盖
- [ ] 审查其他 ensure 函数是否有类似的 schema 滞后问题
