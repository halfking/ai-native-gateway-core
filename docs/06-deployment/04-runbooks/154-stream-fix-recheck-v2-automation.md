llm-gateway 154 流式修复轮定时复核 v2（read-only，不部署不改配置、不创建新自动化）。按运行日期分支。

【通用注意事项】/opt/llm-gateway-go/logs/gateway-canary-<port>.log 文件是跨 build 追加的（8781 文件混有 2211 洪水尾+2235 内容；8782 文件混有 2211 洪水+2234 内容），**严禁对文件做无时间过滤的 grep -c 计数**；计数一律加时间前缀过滤（行首格式 {"time":"2026-09-24T…）或用 journalctl --since（注意 journald 保留期仅 ~36h，9/24 查 9/23 晚只能看到部分，以文件计数为主）。当前 active 端口读 /opt/llm-gateway-go/run/active-port（9/23 晚为 8781）。

【若运行日=2026-09-24 → 「24h 复核」】
背景：2234（19:48）→2235=git b0c77269d（20:44）部署于 154；修复=洪水(Info→Debug)+retryable:true+failure_detail_code 落库+双终态帧(wrapAttemptWriter Unwrap 复用+not-found 保装饰层)+L2 shadow env。
1. ssh -p 25022 root@47.97.111.154（密钥已配）：
   a) curl -fsS http://127.0.0.1:$(cat /opt/llm-gateway-go/run/active-port)/healthz → 确认 ≥2235 未回退。
   b) 事件计数（时间过滤！）：P=<活跃端口文件>；grep survival_resume_blocked $P | grep -cE "\"time\":\"2026-09-24T" ；同法统计 2026-09-23T2[0-3]（2235 起）。journalctl -u "llm-gateway-go-canary@*" --since "2026-09-23 20:44" 作交叉核对。
   c) 保留期：ls -lah /opt/llm-gateway-go/logs/（轮转跨度/体积）+ 60s 两次 stat -c %s。⚠️ 上轮 5.5MB/h 是深夜低负载 1 分钟采样，白天负载可能 3-5 倍；判定线=平均速率 ≤20MB/h（1GB/48h）才可说保留期≥48h，否则提出上调 LLM_GATEWAY_LOG_MAX_BACKUPS 或源头降噪建议。
   d) 机制哨兵（每事件必须全过，这是「修复生效」的判定，取代任何数值阈值）：洪水线 grep "live stream delta push" 计数（时间过滤，应为 0）；grep -c survival_terminal_already_rendered（>0=双终态帧守卫在工作）；抽最近 1 条 survival_resume_blocked：确认同请求有 failure_detail_code 落库（见 2）且日志含 retryable 相关信封；若能取到该请求的客户端 wire（无则跳过）确认单终态帧。
   e) grep minimax_tool_text_coerced 计数（时间过滤；>0=minimax 泄漏复发，触发启发式方案）；grep -c "sessionv2mirror: V2 shadow write failed"（时间过滤，趋势记录）。
2. PG：ssh root@115.29.212.252 'docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT count(*) FROM request_logs_bodies_hot b JOIN request_logs_hot l ON l.request_id=b.request_id WHERE b.ts>=<窗口起> AND l.failure_detail_code='"'"'gateway_survival_resume_blocked'"'"';"'（窗口=2026-09-23 20:44 起；勿用 t0_arrived_at，多为 NULL）。
3. 判定口径（如实陈述，不吹）：部署前 154 的逐小时 resume_blocked 基数不可重建（journald 被洪水烧毁、DB 修复前不打 failure_detail_code 标），因此「24h 下降」不做数值阈值判定；达标=①洪水线为 0 ②每个 resume_blocked 事件满足落库+retryable+单帧机制 ③事件量与同夜 A/B（2234 触发 20/24 断流 vs 2235 24/24 干净，注意存在上游波动混杂）方向一致。如实报告三者证据。
4. 报告+写入 memory（llm-gateway 项目记忆）。

【若运行日=2026-09-25 → 「L2 shadow 48h 评估」（docs/design/resume-blocked-long-stream-recovery.md P2 验收）】
1. ssh 154：a) grep LLM_GATEWAY_RECOVERY_L2_MODE /etc/llm-gateway-go/env（=shadow）；b) survival_l2_shadow 计数（时间过滤，窗口 2026-09-23 20:44 起）；c) 结果分布 grep -oE '"result":"[a-z_]+"' | sort | uniq -c；d) score_bp p50/p90（仅有打分事件时）；e) 48h 窗口 survival_resume_blocked 计数与 survival_attempt_outcome 里 committed_output 占比。
2. 【判定口径】shadow 只观察不调度重放（enforce 才排 replay），目标人群 attempt 1 即终态 → 预期多为 result=unavailable，这是「as-built shadow 无法对目标人群打分」的结构性结论而非故障。若出现 aligned/miss 打分（organic attempt≥2）：命中率=aligned/(aligned+miss)，≥60% 支持 P3（minimax-m3 enforce 灰度）、30-60% 延长、<30% 停 shadow 转 L4。
3. 若全是 unavailable：结论=P2 需补 shadow-replay 变体（重放但全缓冲丢弃零转发）或限时 enforce canary（miss 自动回退 resume_blocked 信封）。把 unavailable 数、resume_blocked 数、模型分布与建议写入 memory。
4. PG：48h 窗口 failure_detail_code='gateway_survival_resume_blocked' 按 outbound_model 分组（bodies_hot.ts 时间锚）。
