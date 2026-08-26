# internal/dbx — pgx-first 数据访问框架

> 状态：CURRENT（Phase 1/2 交付 + Phase 3 探针/元数据门禁/真实表试点 manifest 已通过；生产接入暂缓，见下）
> 设计文档：[数据库处理框架方案](../../docs/03-design/04-data-design/数据库处理框架方案.md)
>
> 真实表试点现状：`tenant_model_policies` 的 manifest 与影子读测试已在宿主侧
> `db/dbxmanifest/` 交付，但未注册进生产 `DefaultRegistry`——生产 dump
> (`sql/schema/01-schema.sql`) 显示该表缺 PK 约束（与 migration 024 声明漂移），
> 元数据门禁按 `pk_mismatch` fail closed。候选核对结论见设计文档 §8。

## 模块边界（硬约束）

本模块按“可整体抽取为独立项目”设计，三条规则：

1. **禁止 import 宿主业务包**。允许的依赖只有：Go 标准库、`github.com/jackc/pgx/v5`（pgconn/pgtype/pgxpool）、测试用 `pgxmock`/`testcontainers`/`testify`。校验：

   ```bash
   ! grep -rn "kaixuan/llm-gateway-go/" internal/dbx --include='*.go' | grep -v "internal/dbx"
   ```

2. **不含业务实体与业务 SQL**。表清单（manifest）与 `ScopeConfig` 装配在宿主侧 `db/dbxmanifest/`；探针验证实体只存在于本目录 `_test.go`。
3. **代码集中**。全部框架代码与测试都在本目录树内。

## 组成

| 文件 | 职责 |
|---|---|
| `executor.go` | `DBTX` / `TxBeginner` 执行缝（pgxpool.Pool、pgx.Tx、pgxmock 通吃） |
| `errors.go` | typed errors（unknown table/field、protected、scope、conflict、unique violation 等） |
| `manifest.go` | `TableManifest` / `ColumnSpec` + 自校验（深拷贝输入，注册后与调用方隔离） |
| `registry.go` | 不可变 manifest 注册表快照（Lookup 返回防御性拷贝） |
| `identifiers.go` | 标识符白名单校验（63 字节上限）与安全引用 |
| `scope.go` | `ScopeConfig`（GUC 注入式）+ `ScopeRunner`（租户/特权/只读事务） |
| `tx.go` | `WithTx` / `WithReadOnlyTx` / savepoint / 有限重试（回滚用脱离请求的 ctx，取消不烧连接） |
| `patch.go` | 严格 patch：missing/null/zero 三态、未知字段拒绝、`WithLenientUnknown` 宽松模式回传 dropped |
| `crud.go` | 注册表驱动 Insert/Select/Update/Delete + RETURNING（23505 → `ErrUniqueViolation`） |
| `jsonb.go` | JSONB 校验（UTF-8/大小前置/结构/NaN/Inf/顶层 null 拒绝） |
| `observer.go` | 脱敏 `QueryFact`（结构上无参数值） |
| `introspect.go` | 只读 `information_schema`/`pg_catalog`/`pg_index` 快照（分区父/子、FORCE RLS、单列唯一键）与不可变缓存 |
| `diff.go` | `CompareManifest`/`CompareSnapshot` 漂移报告与 blocking 判定（不执行修复、不 DDL） |

## 安全规则（代码强制）

- 表/列/排序标识符仅来自 manifest 白名单；值一律 `$n` 占位绑定；
- 租户值只来自调用方可信上下文；缺失即 `ErrMissingScope`，**不回退默认租户**；
- 租户列由框架注入，调用方传入即 `ErrProtectedField`；
- 无软删除列的表 Delete 直接拒绝，绝不降级为硬删除；
- readonly 表（columnar 历史/视图）不可写；
- 观测结构不含参数、密钥与正文。

## 元数据门禁（接入前置）

注册 manifest ≠ 可写。接入前用 `MetadataReader` 对账 live 目录：
`CompareManifest`/`CompareSnapshot` 报告漂移，blocking 项（列缺失/类型/可空、
identity 列被声明可写、**主键非单列唯一键**、分区父/子表、可写表挂非 heap
access method、RLS 未启用、可写表未 FORCE RLS、快照缺表）存在时必须 fail
closed；修复只能走 reviewed migration。额外物理列仅报告（manifest 是显式投影）。

## 抽取为独立仓库

1. 新建 `go.mod`，依赖仅 `github.com/jackc/pgx/v5`（可选 `go.opentelemetry.io/otel/trace`）；
2. 整目录拷贝 `internal/dbx` → 新仓库根（或 `dbx/`）；
3. 宿主把 `db/dbxmanifest` 的装配指向新 module path；
4. `go test ./...` 在新仓库独立通过（测试依赖 pgxmock/testcontainers/testify）。
