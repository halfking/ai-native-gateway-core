# 154 流式、工具与凭据节点修复上线验证（2026-10-19）

修复已上线，MiniMax-m3 主动验证通过；现场仍有业务请求取消、GLM 空响应及状态写入超时，不能宣布全部故障清零。时间均为北京时间，原始证据中的 UTC 与之相差 8 小时。

## 运行身份与发布范围

- 最终核验时点 **23:14:40**：release `2744-99aca741`，端口 `8782`，PID `25747`；`/proc/25747/exe` 与 release 二进制 SHA256 均为 `9b18c0f62a54364ea6fd73afd0d0adabea23b3cf8573a50f6a5e87344aad6135`。readyz 的 DB/Redis 均连接成功，nginx HTTPS 版本与实际进程一致。
- 本轮先将已审计的 `126419af` 部署为 `2742`（22:25:40 切流），完成 11 项正向模型请求。观察期间另一会话发布 `2743-99aca741`；已核实 `126419af` 是其祖先，本任务 40 个 Go 文件 blob 不变。
- 随后以**现网已更新的 `99aca741c05eff7aa9ec3545bf6f251e53f0c0b2`** 为基线发布 `2744`，保留并发代码更新及线上 web/web-mobile；本次没有修改产品源码。
- 两轮均使用 `scripts/deploy-154.sh --no-frontend`，重新编译 Linux 二进制并通过部署身份、数据库、nginx、管理员登录与解密门禁（17 个凭据，解密失败 0）。2744 总耗时 116 秒，23:03:12 切流，handoff **21.532 秒**，超过 2 秒目标；不能宣称零中断。
- 之前完整根门禁通过的证据适用于修复基线；2743 继承的其他模块改动本轮只经过编译和部署门禁，没有重新执行整仓完整门禁。具体基线见 [原审计报告](2026-10-19-stream-node-audit.md)。

## 已修复的运行配置问题

首次真实队列任务 `4033040`（run `706532`）显示 direct 200、gateway 401；节点健康由 direct 决定，因此此前的“任务成功”不代表网关轮成功。

已备份 `/etc/llm-gateway-go/env` 与远端 secrets 文件，仅将失效的 `LLM_GATEWAY_SELF_CHECK_API_KEY` 替换为运行中、经真实模型请求验证有效的 `LLM_GATEWAY_API_KEY`，通过 2744 重新加载。没有创建新密钥或修改数据库鉴权；secrets 文件不包含此配置项且未修改。报告仅保留文件指纹和相等布尔，不保存密钥值。

配置备份：`/opt/llm-gateway-go/backups/selfcheck-20261019T150145Z`。初始发布前备份：`/opt/llm-gateway-go/backups/stream-node-20261019T142154Z`。

## 2744 的实际模型验证

全部通过公网 `https://llm.kxpms.cn`，请求模型 `minimax-m3`；请求 ID 对应的上游日志均命中 provider 14、credential 21、`MiniMax-M3`。

| 验证 | 结果 |
|---|---|
| Chat / Responses / Anthropic 的流式结构化工具调用 | 3/3 通过，ID、参数与终止事件合法 |
| 三协议工具结果回传 | 3/3 通过；随机 receipt 在首轮返回后生成，仅放入 tool_result，模型确实读到返回值 |
| 公网长流 | 130.719 秒，首个 SSE 35.955 秒，833 个 SSE 事件，其中 829 个正文增量、23477 个正文字符；最终标记完整，单 stop + 单 DONE |

工具名称按标准逐帧拼接，不去重掩盖错误；Responses 实际出现 5 个参数 delta，已比对 delta/done/completed 一致性。Chat 与 Anthropic 此次各只有 1 个参数帧，不声称线上覆盖了它们的多帧形态。7 个主动请求日志无本次检索的错误家族，不能据此推断自然流量全无错误。

原 2742 的工具回传测试把预期值同时写进提示和工具参数，只能算协议冒烟；2744 的 fresh receipt 正控已补足“消费工具返回值”的证据。旧长流脚本第一次因仍锁定 2742 身份而在请求模型前拒绝执行，更新预期身份后重跑通过；未降低内容增量阈值。Magpie/ZCode 的真实客户端界面和历史原始工具帧本轮未重放。

## 节点状态与读取面

| 凭据/模型 | 真实队列任务与探测 | 权威状态与路由 |
|---|---|---|
| 21 / MiniMax-M3 | task 4033239，run 706733：direct/gateway 均 200、ok | NPS healthy、失败计数 0，`is_routable=true` |
| 42 / MiniMax-M3 | task 4033240，run 706726：direct/gateway 均 200、ok | NPS healthy、失败计数 0，`is_routable=true` |
| 29 / minimax-m3 | 保留既有 http_410、失败计数 119、下一轮 10-20 02:42:12 | `is_routable=false`，原因 `node_probe_failed`，未强制复活 |

网关轮 request `0beee65836c143606d8060e309876efb` 实际命中凭据 42，`b62328313b15c30fb78030fd97246d1a` 实际命中 21；pipeline 日志有上游 200，`request_context_attrs` 同时确认 `origin_stage=node_probe / is_probe=true`。据此核验保留了指定凭据，避免落到健康默认节点后产生假绿。传统 `_request_logs_hot/_request_logs` 对这两次时间窗口查询为空，来源标签使用独立 context attrs 证明。

74 个凭据的 `manual_disabled` 与 `quota_state` 在部署前后逐项一致。29 的凭据级健康检查对象是 grok-4.5，不能据此把其 MiniMax 节点的 broken 状态覆盖。部署前快照的 CMB/路由投影不完整，不能声称这些表前后全部字段一致。

**读取面仍有残留**：旧 Redis `llmgw:avail` 的 21/M3 条目停在 22:25:35 的 `suspicious`（available=true），42/M3 没有条目；29 条目仍 broken 但计数落后。它与 NPS/URSM 和路由视图未完全同步。该缓存存在诊断及兼容读取，不能泛称完全无人消费，也不能宣称所有状态面一致；本轮未用手工写 Redis 掩盖滞后。

## 部署后日志结论与未解决项

严格窗口 **23:03:12.714992—23:14:39.355563**，新端口 8782 共 5238 条结构化日志；旧 8781 的退出/排空单独统计。日志行可能来自重试和多层记录，不等于失败轮次。

- 未命中 `gateway_survival_resume_blocked`、`committed_output`、partial-output-discard、`eof_without_done/stream_read_error`、工具缺终止/缺结果、prompt budget 这些结构化错误家族。这个有限窗口不能证明以后不再发生，也不覆盖非 JSON 栈输出。
- **GLM-5.3 仍有问题**：1 个关联请求出现 empty-response 4 行、stream-interrupted 3 行；另有该模型槽位饱和告警。HTTP 200 本身不代表 SSE 内容成功。
- **MiniMax-m3 仍有 2 次业务请求取消**：`498e0ce4fca1efb5b3bc4fbc51a676fb`、`dc40915b0ebd2279949fdc0f49df6b24`，均为非流式上游请求、最终 HTTP 502/context canceled，耗时约 30.053 秒及 14.778 秒。context attrs 为 business；不能无证据归因于上游故障或某客户端超时设置。
- **状态写入尚未全绿**：5 次 `credential probe v2: writeHealth failed`，凭据 18/8/34/35，均 context deadline exceeded；1 次 URSM flush 失败，`cid=73/model=glm-4` 的 insert 超时；1 次凭据 8 的 node_probe_runs 同步审计写入超时。
- routing health 检查 1 次 SQLSTATE 57014/statement timeout；provider profile 采集 1 次失败（凭据 35 三次探测失败）。另有不带请求 ID 的 network/overload 信号各 1 行，不能推为具体业务失败次数。
- 保留日志的当日历史搜索只检出上述一次 URSM flush 超时，无法证明它早于继承的 writer 改动，亦不能仅凭同一时点证明是该改动导致。它和健康写入超时均作为未定位的持久化风险保留。

机器仍 ready 不等于这些写入成功。此次发布与指定模型验证完成，**剩余错误和缓存一致性问题没有被宣告修复**。安全聚合、请求关联、原始证据 SHA256 见 [验证清单](evidence/2026-10-19-154-rollout-summary.json)；私有原件位于 `/tmp/llmgw-deploy154-20261019`，未提交原始用户请求、日志正文或任何密钥。

## 回滚与复核

在本机仓库执行一行配置加代码回滚（恢复 env 后，canonical rollback 会启动上一已验证 release 并加载该 env；本轮没有实际回滚健康实例）：

```bash
ssh 154 'cp /opt/llm-gateway-go/backups/selfcheck-20261019T150145Z/env /etc/llm-gateway-go/env' && bash scripts/deploy-seamless.sh rollback 154
```

仅回滚代码：`bash scripts/deploy-seamless.sh rollback 154`。后续需重新读取 active-port、current、实际 PID/exe 指纹及公网 version；2744 是上述时点的事实，不是可永久沿用的现网身份。

本轮可复用经验：长观察期间必须检测并发发布；工具回传正控须让预期值仅存在工具结果中；direct 成功不能代替 gateway 自检和 pin 实测。已保存 agentmemory `mem_mvfefz6f_577fe4fe4575`；当前工具未提供 Memora session.append/pin/archive，未声称已执行这些接口。
