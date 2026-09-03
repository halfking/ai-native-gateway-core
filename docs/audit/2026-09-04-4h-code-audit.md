# 2026-09-04 近四小时代码修正行审计报告

## 审计范围

- 时间窗口：2026-09-04 最近 4 小时提交与合并内容。
- 基线：`origin/main`，包含 `9c20a6e51` 及其后的集成审计、安装器和离线升级交付修正。
- 关注面：流程/数据/反馈闭环、IR 与多协议转换、会话轮次与附件元数据、hot+columnar 分区、供应商错误与凭据质量、并发与资源生命周期、部署和安装器交付一致性。

## 已修正并验证

1. Turn Digest/L2：统一 `latency_ms` 字段；dual shadow 使用独立 500ms context；L2 短非空流报告 `undecided`；缺失缓存安全降级 off；shadow flush 观测不再漏记。
2. Autoroute：合并时保留单一 `annotateTreatment` 实现，消除重复方法定义导致的编译阻断。
3. 质量聚合：从 `request_logs_hot` 读取真实字段，按上游状态、错误类型、流首块时间和 token/cost 字段聚合到 `provider_metrics_minute`，采用幂等 upsert。
4. Hot/分区归档：补齐 651–654 启动迁移，修复凭据模型索引归档返回契约，并改用 detach+drop 兼容 columnar 分区。
5. 交付闭环：将 649、651–654 同步加入安装器嵌入、startup runner 和离线升级包清单，避免源码迁移与安装产物漂移。
6. ModelTier：`Stop` 使用 `sync.Once` 防止并发 close panic；周期刷新 timeout 继承服务 context；新增并发 Stop 与上下文来源回归测试。

## 数据/流程闭环结论

- 请求主链路可追踪至 `request_logs_hot`，再由 8 小时热窗口 promote 到分区表；质量分钟聚合明确从 hot 读取，满足实时性要求。
- 会话轮次链路保留 tree 基线，dual 通过 shadow 提供 V2 对比元数据；响应不携带 request/response body，符合 metadata-only 约束。
- `internal/ir`、`internal/irconv`、附件存储及多协议转换测试均通过；本窗口没有发现新的 IR 字段丢失或序列化/反序列化不对称证据。生产级多供应商协议覆盖仍需真实流量验证。
- 供应商错误具备错误类型、状态和凭据关联路径；真实上游错误表/会话展示链路仍需目标环境验证，不能以源码测试替代。

## 未在本次安全范围内直接修改的后续项

- `admin/provider_refresh.go` 的共享 refresh 状态仍应在同一互斥锁内完成读写，避免并发状态请求与后台更新产生 data race。
- unified turn list 的 child request 查询缺少总量/每 parent 上限；应增加有界投影和分页契约。
- 供应商上游文本虽已截断并清理控制字符，仍建议改为错误类型/allowlist 摘要，避免把任意响应文本写入管理状态或日志。
- 654 归档函数在“复制成功、detach/drop 失败”后重试时仍需显式去重或校验，才能宣称部分失败完全幂等。
- 安装器/离线迁移已做源码级和测试级校验；真实 PostgreSQL/Citus 执行、目标主机部署、生产供应商故障切换、TCP 长会话和密钥可用性属于 `manual_required`。

## 验证结果

- `go test ./...`：通过。
- `go test ./bg -run 'TestModelTier_' -count=1`：通过。
- `go test -race ./bg -run 'TestModelTier_' -count=1`：通过。
- 安装器相关测试：通过。
- `bash -n`、部署契约测试、迁移编号门禁、`git diff --check`：通过。

## 结论

本次主分支同步、冲突处理、核心编译阻断修复和安装/离线交付闭环已完成。源码与自动化测试达到可合并状态；真实数据库、生产部署和供应商故障场景仍需按上面的 `manual_required` 清单执行，不能标记为已完成。
