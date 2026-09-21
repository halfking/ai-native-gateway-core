# 2026-09-12 request_logs 视图补 system_fingerprint 列（迁移 696）——漂移检测器读面切视图

## 概要

`request_logs_with_current_month` 基础包装的 hot∩parent 列交集在 603 给 hot 表补 `system_fingerprint` 之前冻结，链上从未重建——视图缺列导致 `bg/integrity_fingerprint_drift.go`（7 天指纹漂移检测器）被钉在裸父表（近期窗口失明教义的有意排除项）。迁移 696 按 577/610 同款 lateral 形状把列补进 canonical 视图，检测器读面随之切到视图，排除项翻转为包含守卫。

## 关键设计

- **双形状幂等**：冻结 pre-603 链（生产现网）→ lateral 追加；动态重建链（680 引导/db.go 自愈，交集已含列）→ no-op。无条件 CREATE OR REPLACE 在后一形状会因列序报 "column already exists"（真库验证抓到后加守卫）。
- **无 DROP**：CREATE OR REPLACE 原地替换，读者只见短暂阻塞不见 42P01；尾部追加列对全部 50+ 显式列名消费者无影响（已审计无 SELECT *）。
- **通道**：apply-db-revision-sequence.sh 增补 696；编号前查生产双账本（跨项目 694 撞号教训的落实）；db.ensureRequestLogsCurrentMonthView 镜像同步。

## 生产应用（2026-09-12 05:33）

通道脚本对 252 PG 全序执行成功；视图 112 列（111→112）、fp 列恰一次、双账本对齐（schema_migrations '696' + 通道标记）。真库双形状验证：列唯一、HOT_ONLY caller_id 不泄漏、hot-first lateral 语义保持。

## 新发现的预存缺口（未修，挂账）

`X-System-Fingerprint` 响应头由 streaming integrity detector 捕获后写入 **context JSONB**（detector.go），从未写入 487 建的专用列——`request_logs.system_fingerprint` 全表零行（hot/parent 均然），漂移检测器自 2026-07-28 起一直在空集上空转。writer→列 的接线修复属后续工作；完成前本迁移的正确性不受影响（读者空集行为与切换前一致）。
