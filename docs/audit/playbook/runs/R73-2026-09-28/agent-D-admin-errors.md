# D admin面与供应商错误域子代理报告（窗口：383c4b976..3909d56e0）

> R73 审计轮只读子代理原文。主代理处置：D-1/D-2/D-3/D-4/D-5/D-10 已修
> （守卫 v2 + 二梯队收口 ~50 处，含代理清单外的 annotation/session_management/
> session_task_project/systemmonitor %v 形态 19 处与 free_discovery 10 处）；
> D-6（M-5）已修 SQLSTATE 白名单；D-7（M-2）已修；D-8/D-9 登记；A-7 转交项已修。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| D-1 | P1 | **守卫 echoRe 只认 `.Error()` 形态，fmt `%v` 回显全部漏网**。亲读确认 5 处：node_operations.go:459、session_export.go:189/:349、session_summary_v2.go:112/:133。主代理扩展扫描后发现同形态还有 annotation_handler（8，含 qerr 变体）、session_task_project_api（4）、session_management_api（6）、systemmonitor_handlers（2）共 25 处 | admin/internal_error_guard_test.go:33；上述位点 | 收口 + echoRe 扩展 → **已落地（守卫 v2 + 25 处）** |
| D-2 | P1 | **守卫 same-line AND 匹配可被多行调用拆分绕过**。11 处在逃：tool_registry_api 3、tool_policy_api 5、session_compare 2、modules 1（主代理实测 tool_policy 第 6 处 :54 是 400 位按政策保留、modules 2 处中 :1232 是 200 诊断位按设计保留，实际收口 10+1） | admin/internal_error_guard_test.go:58；上述位点 | 收口 + 窗口扫描 → **已落地** |
| D-3 | P1 | **守卫 os.ReadDir(".") 不递归，admin 5 子目录整体漏扫**。logsearch 实锤泄漏 1 处；dashboardapi 靠 writeErrorJSON writer 层兜底（types.go:201-205 status>=500 剥 details 并 slog）实际不漏但守卫盲区结构性存在 | admin/logsearch/logsearch.go:100；admin/dashboardapi/types.go:201-205；守卫 :29 | WalkDir 递归 + logsearch 收口 + dashboardapi 7 文件白名单（兜底架构注释）→ **已落地** |
| D-4 | P2 | **动态状态码间接绕过**：`writeError(w, fdStatusFor(err), err.Error())` 行内无字面 500；fdStatusFor 对 DB 故障落 500。实测 10 处（代理报 5） | admin/free_discovery.go:138/:164/:180/:190/:359/:380/:436（fdStatusFor :499-514） | writeFDErr 辅助：哨兵臂保留文案、DB 臂 writeInternalErr → **已落地** |
| D-5 | P2 | **M-7 现状确认 + 同型新发现**。credential_models.go 三处 404 回显现状无敏感串（assertCredentialBelongs 固定文案）但 (a) 透传模式一次改动即漏 (b) 真 Scan 错误被吞零日志。新发现 node_operations.go:74 404 回显 DB 错误串 | admin/credential_models.go:22-29,38,206,221；admin/node_operations.go:74 | assertCredentialBelongs 加 slog + node_operations 拆固定文案 → node_operations :459 已随 %v 批收口；credential_models 登记遗留 |
| D-6 | P2 | **M-5 现状确认**：IsStorageUnavailable 无 PgError 分支，08xxx/53300/57xxx 一律 500 | admin/storage_degraded.go:46-72 | SQLSTATE 白名单（08/53/57P03；57014 保持 500）→ **已落地 + 6 用例钉桩** |
| D-7 | P2 | **M-2 现状确认**：窗口只给 unified 首查询臂接了分类器，5 个迭代错误点仍固定 500 + 零 slog | admin/session_turns_unified.go:99,119,155,164,180 | 五点补 IsStorageUnavailable 分叉 → **已落地** |
| D-8 | P2 | session_list.go v4 的 loadSessions/loadSessionDetail 迭代后无 rows.Err()，scan 错误 continue 静默截断 → 200 假完整列表（缓冲：R69 标注生产零构造点仅测试可达） | admin/session_list.go:186-226,411-439 | 随 E-7 deprecated 标记（已标），删除性重构时消亡 → **登记** |
| D-9 | Obs | E6c 终态评估：flag-off Debug 日志字段够用、match_rule 可区分；但默认 Info 部署下灰度判读看不到 fallback 流量 | domains/streaming/executors/executor_dispatch.go:222-227 vs 256-267 | 可选 Prometheus 计数器 endpoint_selector_flag_off_fallback_total → **登记** |
| D-10 | Obs | self_check trigger 的 503/200 体回显错误串 | admin/self_check_handlers.go:691,746 | 503 臂已收口+slog；200 per-model 诊断 map 保留文案+slog 锚点（诊断面取舍）→ **已落地** |

## 二、核实为健康的面

- E5「21 处收口」终态亲读 ≥6 处全部落 helper；剩余固定文案 500 均有伴随 slog。
- E6b session_list slog 在位；internal_error.go 三 helper 设计 + slogCaller 锚点。
- jsonb 修复终态（57adc2343+c4369a491）：alerts details / provider_events 序列真实走对路径。
- session_detail_v2 窗口改动：409 哨兵、固定文案、503 统一、body_status 真字节分类、resolveSessionID 双腿 UNION、租户钉扎保持。
- D08 三链路未被窗口破坏：①错误落 candidate_failure_logs + monitor 聚合告警/auto-cool（09-25 probe_direct_% 排除修复是正向）②LogFailure best-effort 3s 独立超时不上抛、failover 继续会话 ③凭据详情呈现链路（failure_rate/error_kinds/recent-failures）未断。
- 降级观测统一面：nil-pool 与 query 错误在活路径 v2/unified 全部 503+storage_status；指标配对正确。
- 双轨一致性：v4 已 Deprecated 仅测试可达；活路径错误处理形态一致。

## 三、未覆盖项与原因

1. 其他域大块改动仅抽验交界点。
2. dashboarddegrade/distlock/reorder_scope_revision 仅 500 回显扫描（零命中）。
3. D08 前端 candidate-failures 呈现为推断（web/src 对 /api/candidate-failures* 零消费），凭据详情实际走 credential-monitor.ts——标待亲验。
4. SessionDetailPage.vue 117 行窗口改动只审了 body_status 横幅段。
5. mock-probe 生产入口 / taskprofile round2 / 752-754 迁移正文未在本域展开。
