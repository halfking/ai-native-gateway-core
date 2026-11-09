# Preview/body 与 nginx IPC 后续优化

基线：main `9b2f4fbd0`，用户工作树干净；本轮在独立 worktree 执行。
路由：bugfix；先复现，再最小修正，真实 PG 验证，独立审查后小步合并推送。

## 计划与契约

- [ ] 用探针 HTTP 执行入口回归长 JSON、非 JSON、读取失败与降级响应。
  `go test ./bg -run TestProbeResponseBodyContract -count=1` 必须先红后绿。
- [ ] `RespPreview` / telemetry `response_preview` 保存脱敏、有限长文本；
  `ResponseBody` 仅保存完整读取、预算内且脱敏后仍合法的 JSON。
  不把预览包装成 JSON，不放宽遥测 JSONB 校验，不修改已应用迁移。
- [ ] 新旧探针路径与 Responses→Chat 降级都继承该契约，保留分类和冷却语义。
  完整响应中的 usage 不能被预览截断丢掉。
- [ ] 在隔离 PG 上实跑探针→emitter→telemetry→probe 表的正文/预览回读。
- [ ] nginx 先读取真实错误日志和资源限制，在独立 loopback 实例复现候选原因；
  生产 master/worker 不用于故障注入。证据不足时明确保留未决原因。
- [ ] 通过 build/vet、相关包回归、独立 code review 与 task-stop-audit 后收口。

## 初始现场（2026-11-09 16:36–16:38，Asia/Shanghai）

211/245/154 的应用 sha 均为 `998269fb`，build_seq 2978/2956/2957；
各自直连 /readyz 的 DB/Redis 均就绪。211 最近 30 分钟仍有 500 字节
response_body 被遥测丢弃。154 当前 master=366，worker=10672，磁盘与
运行中 nginx 二进制 SHA256 一致（1.26.1），无旧业务 worker 残留。

154 实际全局错误日志为 `/home/vcuser/logs/nginx/error.log`，不能用
`/var/log/nginx/error.log` 的零命中声称没有 IPC 错误。14:50:11 的旧 worker
10780 报 invalid ancillary data（level=-1738169575/type=21950），紧随
`1024 worker_connections are not enough`。软 FD 限制为 1024，硬限制 4096。
这支持资源耗尽假设，但尚未证明历史时刻的 FD 数或内核丢弃控制消息。

上游依据：[nginx 1.26.1 ngx_channel.c](https://github.com/nginx/nginx/blob/release-1.26.1/src/os/unix/ngx_channel.c)
检查未初始化的 cmsg 内容，读取错误关闭 channel；具体因果须动态验证。
