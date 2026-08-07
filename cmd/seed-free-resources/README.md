# 种子数据导入工具

用于导入 OmniFree 免费资源的初始数据。

## 使用方法

### 1. 构建

```bash
go build -o seed-free-resources ./cmd/seed-free-resources
```

### 2. 导入所有数据

```bash
./seed-free-resources \
  --db-url "postgres://llm_gateway:password@localhost/llm_gateway_dev" \
  --catalog ../../docs/omnifree/seed/free_resource_catalog.json \
  --templates ../../docs/omnifree/seed/auto_combo_templates.json \
  --keyless ../../docs/omnifree/seed/keyless_providers.json
```

### 3. 仅导入免费资源目录

```bash
./seed-free-resources \
  --db-url "postgres://..." \
  --catalog ../../docs/omnifree/seed/free_resource_catalog.json
```

### 4. 试运行（不实际写入）

```bash
./seed-free-resources \
  --db-url "postgres://..." \
  --catalog ../../docs/omnifree/seed/free_resource_catalog.json \
  --dry-run
```

### 5. 指定租户 ID

```bash
./seed-free-resources \
  --db-url "postgres://..." \
  --catalog ../../docs/omnifree/seed/free_resource_catalog.json \
  --tenant-id 2
```

## 参数说明

- `--db-url`: 数据库连接 URL (必填)
- `--catalog`: 免费资源目录 JSON 文件路径
- `--templates`: Auto Combo 模板 JSON 文件路径
- `--keyless`: Keyless 提供商 JSON 文件路径
- `--tenant-id`: 租户 ID (默认 1)
- `--dry-run`: 试运行模式，不实际写入数据库

## 幂等性保证

所有插入操作使用 `ON CONFLICT ... DO UPDATE`，可以安全地重复运行。

## 依赖

- `github.com/lib/pq`: PostgreSQL 驱动

如果缺少依赖，运行:
```bash
go mod tidy
```
