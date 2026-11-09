# 2026-11-09 48h审计与三环境发布 handoff

## 结论与范围

本轮审计、修正、三环境发布及登录/界面验收完成；不等于全部存量风险消失。完整报告：`docs/audit/2026-11-09-48h-remediation-report.md`；来源清单：`docs/audit/evidence/2026-11-09-48h-commit-file-inventory.md`。
工作区 `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor`。
初始main bde2baa598→ff1fa2ea592，保留并审查10tracked+1untracked WIP；固定11-07 12:56→11-09 12:56，65非merge提交/271文件。初始本地/两远端没有历史未合并子分支。原两笔10月stash与`/private/tmp/lgw-probe/cursor`未动。

## 改动与根因

- 统计：重扫整键覆盖含秒上界造成2→1截断；改为完整闭分钟、PG微秒截止。
- MaaS：gift预算、并发首购确认、expires_at漏读、参考价错误上抛、源价失败不闭期、模型×模式锁与连续价期。
- MCP：租户/owner/委派包络/缓存隔离、停用执法、touch不复活吊销key、bootstrap token和原子一次性领取。
- 953：非法COMMENT拼接、漏投递、事务/预算/账本；211旧checksum=0，未改已登记字节。
- 954/trace：363失败请求均在probe_hot，原只写业务两腿，每条四次无效重试；新增探针trace列与独立读写腿，持久化阶段事件后才删Redis。
- legacy probe：空headers写空字符串JSONB→无条件正规化为{}；真实runOne回归。
- 前端：真实compact卡片保留字段/动作/合计/打印，七类规范、i18n与死键接线；未抬baseline。
- 部署：source_hostname保持宿主真值；211迁移显式走容器TCP，避免宿主DNS误连252。
- 154：旧nginx worker在reload出现IPC异常后仍接受旧8781流量，产生混合502；核对PID/parent/comm后SIGQUIT，保留新worker。新增20次独立连接四字段精确身份门，失败reload后完整重验，仍失败回滚；不自动杀worker。

## 发布真值与验证

| 目标 | 应用sha / seq / 活跃端口 | 验收 |
|---|---|---|
| 211 | 998269fb / 2978 / 8781 | readyz DB+Redis；管理员重新登录；营销四页签卡片、定价、无效公开报价 |
| 245 | 998269fb / 2956 / 8781，traffic-only | 同上；脚本登录200、17凭据解密0失败 |
| 154 | 998269fb / 2957 / 8782，active | 同上；故障恢复后local20/public30全200；新增身份门live20通过 |

0cadc4f0a之后只有部署脚本/回归与文档，应用代码仍为998269fb，未伪报三台二进制等于最后文档提交。
211新窗口trace16条/阶段事件101；Redis清理后管理接口source=postgres/events=6；闭分钟对账63/63与61/61。未执行历史全天重建或生产付费推理压测，未制造生产订单/临时key。
154不是零中断：handoff17.501s，抓到持续502后才定位并恢复；底层IPC成因未确定。登录后data-horizon首次读取警告可重试清除，已登录接口正常。
本地：build/vet过，核心361包过，ursmcheck不重叠两组354.496s+309.171s过；PG race统计/MaaS/trace/legacy过；MCP47/47含Maestro；web298文件3034测试过（原1文件2测试skip），build/typecheck及七门过；secret scan rc0/BLOCK0。后增迁移transport9通过1opt-in未执行；nginx2测试含3负控、identity-env20/20、pipefail7.176s通过，独立review无阻塞。

## 遗留与下一轮

P1：preview截断响应不是完整JSON，新窗口仍7条response_body丢弃；定义preview/body契约后修复，不伪造完整响应。P1：查nginx IPC异常，20次采样不是永久一致证明。252 scheduled_tasks参数integer/bigint冲突SQL不在本仓，先找owner。
旧trace超过RedisTTL不可恢复；MaaS附属价期best-effort；MCP FileStore单写者和AuthSecret持久化限制；旧bootstrap需重申；旧census“缺列即退役”误判须以DROP证据改造。
部署门SSOT本地实库账号28P01，候选/基线同红，门判无新增失败，不称这些用例通过。Toolkit全局跨项目envs-coverage未全绿。
回滚：245/154 `bash scripts/deploy-seamless.sh rollback <target>`；211旧b容器/镜像保留，start并ready后回切upstream8783。兼容性新增列默认保留，不随应用回滚删trace。

## 资产与记录

原始日志仓外`/tmp/lgw-48h-audit/`，不提交密码/DSN/业务payload。环境SSOT现位于`deploy/envs/loader.sh`；旧injector路径与211映射缺口已记录，继续用同SSOT，不复制凭据。
受管integration checkout位于`~/.codex/worktrees/gateway-audit-integration/llm-gateway-go-cursor`；app拒绝归档“primary checkout”，保持干净detached并同步最终main，不手工删除。
session `sess_efcc348a-898a-472d-a5a3-f64115a3f431`已append/pin并archive到memora，memory_id=69b022a7-7128-4df5-a20f-e7fa1d9832c4，degraded=false/vector_ready。秘密只通过仓外修正profile访问。

## 下一轮提示词

读取本handoff及完整报告，先核对main和三台/version、readyz及新日志。优先处理preview/body契约、nginx IPC异常与退役量具误判；scheduled_tasks先确认owner。保持先复现、最小修正、真PG/浏览器验证、小步合并推送，不把旧绿数或静态证据当生产验收，不修改已应用迁移字节。
