# 2026-11-09 48h 审计与三环境发布 handoff

## 任务与授权

用户要求审计最近48小时修正、同步主干、处置近一周且超过一小时未活动的未合并分支、逐项总结审查提交/WIP、分析系统与DB日志并实施优化、小步提交合并推送，按211→245→154部署并真实登录验证，更新报告与下一轮提示词。完整目标仍保持，不得缩为“代码修好即完成”。

## 基线与范围

工作区 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor`。
初始main `bde2baa598`，10 tracked WIP+1 untracked文档；已保存原始patch、status及逐文件备份。ff到 `1fa2ea592`，未覆盖原WIP。固定窗口11-07 12:56→11-09 12:56，65非merge提交/271文件。origin与github远端heads仅main，本地无历史子分支需处置。两笔旧stash与detached `/private/tmp/lgw-probe/cursor`未动。

当前工作分支 `codex/audit-48h-20261109`；执行中尚未完成发布，不应引用本记录声称已部署。

## 已修复

- 统计：整键重扫只包含完整闭分钟，PG微秒边界；真实2→1截断已复现并修复。
- MaaS：gift预算、首购确认并发/幂等、expires_at漏读、参考价失败传播、源价读失败不闭期、价格期连续与空账本并发锁。
- MCP：控制面tenant/owner/create-vs-manage、完整委派包络、审计/统计隔离、停用执法、租户cache、原子touch不复活吊销key、bootstrap token及一次性领取/审批最终身份核验。
- 953：COMMENT非法拼接、部署清单漏投递、事务/预算、自记账、installer/objects/台账。211原有schema记录是人工操作，checksum953=0，未改已登记字节。
- 954：探针trace第三读写腿；nullable JSONB，不恢复legacy。211363失败ID全部在probe_hot，业务hot/cold=0；原每条4次重试。
- legacy probe：空headers不再`''::jsonb`，真实runOne回归通过。新DSN所有host/fallback限定loopback和_test库。
- 前端：新页面真实compact卡片/动作/合计/打印；颜色、图标、字号、文案、未使用键接线及两键×8语种。未抬baseline。
- 部署锁：source_hostname与共享SSOT一致，不受逻辑SOURCE_HOST覆盖。
- 派生台账：admin683、post683、db expect/gap/reachability889、manifest355、文档三面行号与pipefail100候选。

## 已取得证据

根模块build/vet rc0；361包排除单独慢门全过；MCP三层47/47含Maestro；真实PG race营销/价格/统计/trace/legacy probe过；bg全包35.411s；web完整298文件/3034测试过（既有1文件/2测试skip）、build/typecheck过；锁parity与contract过。

首次完整ursmcheck 754.196s正常结束，发现前端七类读方/部署锁/post台账失败，均已逐项修正；当前最终两组hollowout在串行运行，结果见 `/tmp/lgw-48h-audit/hollowout-final.log`，不能提前报绿。首次600s超时留下的变异backup已按HEAD逐字恢复，无残留。

## 待完成

1. 最终hollowout结果；补任何新鲜失败；必要的非变异Go门与派生快照复核。
2. 审查staged/unstaged/deleted/untracked全量面，刷新Task Stop Summary；按逻辑切片提交、merge --no-ff main并push。
3. canonical `scripts/deploy-211.sh` 发布，readyz/版本/新探针trace/新日志及browser-use验证；成功后245，再154。
4. 回填报告/本handoff/AGENTS；收口版本构建副作用，确认远端真值与干净工作树；完成session归档及下一轮提示词。

## 决策与风险

- 所有部署已被用户授权，不再请求重复许可；遵循健康后切流，保留一行回滚。
- 正确域名：211 `https://llm.itestu.cn`，245 `https://llmgo.kxpms.cn`，154 `https://llm.kxpms.cn`。旧test-llmgo域证书不匹配。三台部署前均已browser-use真实登录，211状态检查200/null；登录密码不得入仓。
- envs实体是 `deploy/envs`；旧env-injector默认死路径，需设置ENV_INJECTOR_ENVS_ROOT。211别名旧injector无映射，已直接用同SSOT loader指定192.168.31.211；不复制凭据。
- 252是Podman兼容docker；`docker logs`全量/尾读会137，改从inspect.HostConfig.LogConfig.Path有界tail 2MiB，不重启PG。样本04:42→13:30，包含旧恢复期告警，不能当最后2小时全部新错。
- 252 scheduled_tasks参数4 integer/bigint冲突在本仓无对应SQL，待归属；65次JSON错误明确对应本仓legacy probe。
- Toolkit全局verify因工作区其它项目envs登记等仍失败，不能把本仓绿替代跨仓绿。
- gap工具旧census缺featured会把新索引误判retired，发布以真库证据为准。
- 旧trace过TTL不可补回；按量价期附属快照仍best-effort；FileStore不是跨进程强一致存储；旧bootstrap需重申；AuthSecret持久化限制仍登记。

## 证据与会话

报告 `docs/audit/2026-11-09-48h-remediation-report.md`；清单 `docs/audit/evidence/2026-11-09-48h-commit-file-inventory.md`；原始命令/日志都在仓外 `/tmp/lgw-48h-audit/`，不提交秘密和业务payload。

sessionstore的旧profile credential路径随目录重组失效；已用仓外profile指向同凭据迁移后的真实位置，不改全局配置。中央项目platform-integration下会话 `sess_efcc348a-898a-472d-a5a3-f64115a3f431`，append/pin已成功；archive留到真实完成。

## 下一步提示词

继续本goal，先读取本handoff与报告，核对实际Git状态及hollowout-final.log。保持已授权完整范围，完成最终门禁后分批合并推送，按211→245→154发布并登录验证；用上线后新请求证明trace及统计修复，随后更新文档和归档。不要恢复旧WIP或用旧文档替代新鲜运行证据。
