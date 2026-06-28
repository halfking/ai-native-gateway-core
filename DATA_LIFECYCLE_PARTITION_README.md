# 数据生命周期管理 - 分区表列存储归档功能

## 快速开始

在 https://llmgo.kxpms.cn/admin/data-lifecycle 中新增了分区表列存储归档管理功能，支持将历史数据自动迁移到高压缩比的 columnar 存储。

### 核心功能

1. **查看分区状态** - 查看所有分区表及其可归档分区
2. **手动归档** - 单次归档指定月份的分区
3. **批量归档** - 一次归档多个月份
4. **试运行模式** - 安全预览归档操作

### 支持的表

- `request_logs` → `request_logs_archive` (已存在)
- `request_wal` → `request_wal_archive` (新增)

### API 示例

```bash
# 1. 查看可归档的分区
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions

# 2. 试运行归档（推荐）
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive

# 3. 执行归档
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":false}' \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive
```

## 文件变更

### 新增文件

```
admin/
├── data_lifecycle_partition.go      # 核心功能实现 (~530 行)
└── data_lifecycle_partition_test.go # 单元测试 (~150 行)

db/migrations/
├── 305_partition_archive_functions.sql      # 创建 request_wal 归档支持
└── 305_partition_archive_functions.down.sql # 回滚脚本

docs/
├── data-lifecycle-partition-archive.md              # 功能文档
└── data-lifecycle-partition-implementation-summary.md # 实现总结
```

### 修改文件

```
admin/handler.go  # 添加 3 个 API 路由（第 351-356 行）
```

## 部署清单

- [ ] 应用数据库迁移 305
- [ ] 验证归档函数已创建
- [ ] 重启服务
- [ ] 验证 API 可访问
- [ ] 试运行归档测试
- [ ] 配置监控告警（可选）

## 详细文档

- **功能使用指南**: [data-lifecycle-partition-archive.md](./data-lifecycle-partition-archive.md)
- **实现总结**: [data-lifecycle-partition-implementation-summary.md](./data-lifecycle-partition-implementation-summary.md)
- **原始需求**: [data-lifecycle-management.md](./data-lifecycle-management.md)

## 测试状态

✓ 所有单元测试通过  
✓ 编译成功  
✓ API 路由已注册  
✓ 数据库迁移已创建  

## 预期效果

- **存储压缩**: 15-40x 压缩比（columnar 存储）
- **示例**: 4GB 分区归档后约 100-200MB
- **自动化**: PartitionManager 每月自动归档 2 个月前的数据

## 权限要求

- **查询分区**: platform_ops 或 super_admin
- **执行归档**: super_admin（高风险操作）

## 联系支持

遇到问题请查看故障排查指南：[data-lifecycle-partition-archive.md#故障排查](./data-lifecycle-partition-archive.md#故障排查)
