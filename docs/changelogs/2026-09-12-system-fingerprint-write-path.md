# 2026-09-12 system_fingerprint 写路径贯通（迁移 697）——漂移检测器复活

## 概要

X-System-Fingerprint 响应头由 streaming integrity 捕获后只写入 detector 的 context JSONB；603 建的 `request_logs_hot/parent.system_fingerprint` 专用列自建成起全表零行，7 天指纹漂移检测器（integrity_fingerprint_drift）自 2026-07-28 空转。本批三层贯通：**handler 盖章 → telemetry 落列 → promote 携带入分区**。

## 三层改动

1. **handler 盖章**（domains/streaming/handler.go emitTelemetry）：流式/非流式共用出口统一提取 `X-System-Fingerprint`，盖章 `reqLog.SystemFingerprint`（detector Observe 复用同一提取值）。
2. **telemetry 落列**（telemetry/client.go）：新增 `entry.SystemFingerprint` 字段 + `persistSystemFingerprint`（仿 upsertProtocolMetadata 先例的独立小 UPDATE，零参数重排风险）；挂在 insert/update 两路径 upsertProtocolMetadata 之后——update 路径的调用点在 RowsAffected==0 回落 INSERT 之后，回落路径同获覆盖；无指纹 no-op，历史语句集合不变。
3. **promote 携带**（迁移 697）：695 体（含 final-success 自愈 demote）三列清单尾部追加 system_fingerprint；位置对齐由 TestMigration697ColumnListsAligned（migration602Columns 三列对齐钉）守护。

## 同步面

- 通道：apply-db-revision-sequence.sh +697（697 已查生产双账本空闲——694 跨项目撞号教训落实）。
- 基线三面同步到 697 体：sql/objects/functions/promote_request_logs_*.sql、deploy/sql/schemas/baseline/01-schema.sql、installer embeddata/01-schema.sql（顺带补齐 695 demote 的基线漂移）。
- installer embeddata/startup 补 695/696/697 三个缺失迁移（695/696 为并行线重编号时的预存遗漏）。

## 验证

- 先红：TestMigration697* 在未实现时失败（文件/通道缺失）。
- 真库端到端（一次性 PG17）：promote 后 `system_fingerprint='fp-aaa'` 落入 request_logs_2026_09（697 前会丢为 NULL）；NULL 行不受影响；hot 排空。
- pgxmock：带指纹的终态更新含指纹小 UPDATE；无指纹语句集合与历史完全一致。
- go build ./... + migrations/bg/db/telemetry/admin 关键包全绿。
