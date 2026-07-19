# request-logs 详情 500：VIEW 缺 routing_attempts

**日期**: 2026-07-20  
**现象**: `GET /api/logs/:id` 返回 500，前端显示 `query failed`  
**根因**: V350 对 `request_logs` / `request_logs_hot` `ADD COLUMN routing_attempts/summary` 后，`request_logs_with_current_month` VIEW 的 `SELECT *` 列清单未自动包含新列 → `SQLSTATE 42703`  
**修复**: startup migration `448_request_logs_view_routing_attempts.sql` 重建 VIEW 并暴露两列（已在 252 PG `schema_migrations` 记录，applied_at 2026-07-20 00:41）  
**验证**: 245/154 鉴权后 `getLog` HTTP 200；旧 request_id 若不在表中为 404（非 500）  
**部署**: 245 seq=1191 → 154（本 changelog 对应 release bump）
