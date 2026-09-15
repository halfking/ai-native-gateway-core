# auto 匹配三轮观测报告 (2026-09-16)

> 前置:[2026-09-16-auto-matching-round2-handoff.md](2026-09-16-auto-matching-round2-handoff.md)(二轮交接)
> 观测基线:main=598ef9cb6;生产 245=2116-741bad4d / 154=2117-4e7f9810(两节点 2026-09-14 构建)
> 观测时间:2026-09-16 00:10–00:30 CST;数据源:252 生产 PG(245 跳板 psql)+ 两节点 journald

## 〇、部署身份核查(观测前置,关键发现)

> **2026-09-16 00:45 更新**:例行部署已执行完毕,两节点现已运行含二轮修复与 O4 指标接线的 main(3a68ff1f)血统构建——本节的"不含二轮修复"结论已被§七的部署记录取代,留档作观测前基线。

| 节点 | build_seq | git_sha | active 单元 | 含一轮修复(O1-O5) | 含二轮修复(f6ea772c6/cd09aee46) | 含 O4 指标接线(bac8b6e9e) |
|------|-----------|---------|-------------|-------------------|--------------------------------|---------------------------|
| 245 | 2116 | 741bad4d | llmgo-245-canary@8781(09-15 20:01 起) | ✓ | **✗** | **✗** |
| 154 | 2117 | 4e7f9810 | llm-gateway-go-canary@8781 | ✓ | **✗** | **✗** |

- main(598ef9cb6)已**内容级**包含全部二轮修复:8 处词表扩充(patterns.go:230 en 创意名词族)、N2 两道守卫(task_types_ext.go:38/168/220 工具上下文守卫+引用语境守卫)、LLM fallback 11 类 allowlist(classifier_llm.go:135 planning 在列)、指标接线(bac8b6e9e 已在 origin/main)。原二轮提交 SHA(f6ea772c6/cd09aee46)不在 main 提交图,系 squash 合入,内容在位,无缺口。
- **判定:T1/T4 的"部署后首周"窗口尚未开启**——二轮修复与指标接线随下次例行部署才进生产。本报告为"一轮代码面的生产基线",同时把"触发例行部署"列为三轮最优先行动。

## 一、T1 生产 fallback_used 分布(53%→30% 验证)

**结论:本窗口无法验证。** 生产 auto 流量近零,样本不具统计性;30% 读数仍属 E2E 证据面。

两面数据完全对账(auto_route_selections 父表 9 行 = routing_decision_log_2026_09 decision_trace 9 行,无写链断档):

- 近 7 天 auto 请求共 **9 条**,全部集中在 **09-15 00:17–00:37**(O5 修复部署后的验证爆发);此后约 24h 生产 auto 流量为 **0**。
- fallback_used:**4/9 = 44.4%**(一轮 E2E 口径为 53%)。
  - 非兜底 5 条:chat×1、planning×1(→deepseek-v4-flash,一轮"planning 转正常路径"修复在生产生效 ✓)、creative×3(→minimax-m2.7,爆发窗口前段)。
  - 兜底 4 条:全部 creative(→minimax-m3)。creative 词表无 cap 标签的结构性兜底(交接文档§五)主导了生产兜底计数。
- 爆发窗口中途 3 条 creative 从非兜底(m2.7)翻转为兜底(m3),与候选通道中途退避的行为一致,样本过小不定性。

验证 53%→30% 的前置条件:①二轮修复例行部署;②有机 auto 流量恢复(当前 model=auto 客户端流量为零是最大盲区,建议确认 auto 业务的客户端接入面)。

## 二、T2 O4 LLM fallback 采纳率

**结论:llm_gateway_llm_classifier_* 指标在生产仍未生效;替代观测面(Journal+selections)显示窗口内采纳 1/5,失败主因是上游中继超时;planning/code_audit/intent_classification 三类生产零样本,无法评估。**

- 指标通道:接线 bac8b6e9e 在 main 但**不在** 2116/2117 构建 → RecordLLMMetricCall 仍为 no-op;/metrics 端点 401 保护(无法匿名读,且读了也全 0)。指标观测需等例行部署。
- 替代面 7d 窗口(245 active 单元 journal;154 无 auto 流量 0 条日志):
  - escalation **5 次**,全部由 creative(heuristic conf 0.6)触发;
  - 失败 4 次:3 次 `context deadline exceeded`(8s 预算耗尽,上游 deepseek 中继延迟主导)、1 次 503 `no_candidate for glm-5.1`(残留旧 env 的自环配置,非缺陷,已在上轮登记);
  - 采纳 1 次:`classifier=llm_v2, conf 0.85`(09-15 00:37,selections 与 journal 互证)。
  - 部署日合计口径(含已轮换单元,上轮记录):8 升级/1 采纳/6 超时,熔断全程 closed。
- **三类目标分类的空窗**:planning/code_audit/intent_classification 在窗口内 **0 条 LLM 分类样本**——escalation 只被 creative 低置信触发过,这三类的低置信生产路径从未命中。启发式 vs LLM 准确率对比在生产不可评估,证据面维持 E2E 100 例(完成样本 98.9%)+契约单测。
- 观察点:escalation 触发面偏斜(仅 creative)值得持续关注——若二轮部署后依旧只有 creative 升级,O4 的实际价值面比设计预期窄。

## 三、T3 Claude-5 单价补录验证

**结论:运营补录尚未发生,G4 重跑前置不满足;按交接口径生成"补录前成本不可验证"标注(已写入 list-v2 文档 N6 节)。**

生产核查(credential_model_bindings ⋈ provider_models,2026-09-16 00:15):suyun#39 / apinext#61 / 智码#35 的 claude 系绑定共 **16 行**(claude-sonnet-5 / opus-5 / fable-5 / opus-4.x,含 #61 的 claude/ 斜杠别名),`unit_price_in_per_1m` / `unit_price_out_per_1m` / `pricing_source` / `pricing_updated_at` **全部为空**。

- G4 不重跑:重算前置(单价)不存在,重跑只会复现"无单价通道排除"的旧边界。
- 标注生效口径:补录前,该三通道 Claude-5 不参与 G4 成本均衡判定;G4"top-3 窗口零>10 分偏差"的结论继续以无单价通道排除为边界。
- 该三通道生产 active 且此前实测被 auto 选中(code/reasoning primary 面),单价空缺同时阻塞成本核算与候选资格终审,建议向运营升级催办并给出截止时间。

## 四、T4 长文编排 planning 误判残余

**结论:0 case——但是流量近零下的平凡真,不构成 N2 守卫的生产品验证。**

检索(routing_decision_log_2026_09,7d,`prompt_tokens>=40000 AND decision_trace->>'task_type' IN ('planning','agent','long_context')`):**0 行**。

- 窗口内生产 auto 流量仅 9 条且全部短文本验证流量,"无残余"没有否定力。
- mix_60k_planning_words / mix_agent_plus_intentword 类信号的实机回归条件:二轮部署(cd09aee46 守卫)+真实 60k+ agent 编排流量。维持 E2E/离线套件(100 例全绿)为当前唯一证据面。

## 五、T5 kimi-k3 通道恢复评估

**结论:形式退避全部解除,实测部分恢复——#50/#49 具备候选面恢复价值,其余 4+ 通道探测仍全败。**

| 通道 | 绑定状态 | 48h 探测(成功/总数) | last ok |
|------|----------|---------------------|---------|
| 商汤#3 | available=t | 2/11 (18%) | 09-14 15:02 |
| 商汤#4 | available=t | **0/17** | - |
| 联界#29 | available=t | **0/21** | - |
| 联界#45 | available=t(恢复点 09-15 22:42) | **0/12** | - |
| #49 | available=t | 6/28 (21%) | 09-15 20:38 |
| #50 | available=t | **9/23 (39%)** | 09-15 18:11 |
| #58 | available=t(kimi-k2.6 恢复点 09-15 21:02) | **0/29**(k2.6/k2.7/k3 三模型全败) | - |

- 形式面与实测面背离:绑定 available=true、consecutive_failures=0(自愈探测已把退避行清掉),但 node_probe_runs 48h 实测显示 #4/#29/#45/#58 探测零成功——门控会在真实请求时再次规避它们,行为安全。
- **1M 上下文候选面**:kimi-k3 的差异化价值在 1M 长上下文;#50(39%)/#49(21%)恢复度足以列入"观察候选",建议不进 auto 主选、仅作为长上下文场景的备选面观察对象;#3 偶通,#4/#29/#45/#58 维持 N5"不移除、门控观察"。
- 持续观察建议:对 #50/#49 保持探测成功率周观察,连续一周 >50% 可提请升级为长上下文备选。

## 六、三轮判定汇总

| 任务 | 判定 | 阻塞项 / 下步 |
|------|------|--------------|
| T1 兜底 53%→30% | **无法验证**(样本 9 条、非有机、44.4%) | 例行部署二轮修复 + 有机 auto 流量;部署后按附录 SQL 周观测 |
| T2 O4 采纳率 | **1/5 采纳**(creative 面);三类零样本不可评估 | 指标接线随例行部署生效;关注 escalation 触发面偏斜 |
| T3 Claude-5 单价 | **补录未发生** → 不可验证标注已落 list-v2 N6 | 运营补录(建议给截止时间)后重跑 G4 |
| T4 长文残余 | **0 case**(平凡真) | 二轮部署 + 真实长文流量后复查 |
| T5 kimi-k3 | **部分恢复**(#50/#49 可列观察候选) | 周观察探测成功率,>50% 提请升级备选 |

**三轮最优先行动:触发例行部署**(把 main=598ef9cb6 的二轮修复+指标接线带上生产)——T1/T2/T4 三项的观测窗口都以它为开启条件。

## 附:复跑方式

```bash
# 1. 部署身份
ssh root@8.136.114.245 'curl -s http://127.0.0.1:8781/healthz'   # 端口随蓝绿轮换,先 list-units
# 2. 生产 SQL(245 跳板;set -a; source /etc/llm-gateway-go/env 后 psql "$LLM_GATEWAY_DATABASE_URL")
#    兜底分布(双面对账):
#    SELECT date_trunc('day',ts) d, count(*), count(*) FILTER (WHERE fallback_used)
#      FROM auto_route_selections WHERE ts>now()-interval '7 days' GROUP BY 1;
#    SELECT decision_trace->>'task_type', count(*),
#           count(*) FILTER (WHERE decision_trace->>'fallback_used'='true')
#      FROM routing_decision_log_2026_09 WHERE ts>now()-interval '7 days'
#      AND decision_trace->>'source'='auto_route' GROUP BY 1;
#    注:selections 近期行会被 promote 出 _hot,查父表;rdl 查当月分区(父表查询会超时)。
# 3. LLM fallback 日志(单元名随蓝绿轮换,先 list-units 再 journalctl):
#    journalctl -u <active-canary-unit> --since '7 days ago' | grep 'escalating to LLM fallback\|LLM fallback failed'
# 4. Claude-5 价格:
#    SELECT b.credential_id, pm.raw_model_name, b.unit_price_in_per_1m, b.unit_price_out_per_1m
#      FROM credential_model_bindings b JOIN provider_models pm ON pm.id=b.provider_model_id
#      WHERE b.credential_id IN (39,61,35) AND pm.raw_model_name ILIKE 'claude%';
# 5. kimi 探测:
#    SELECT credential_id, raw_model_name, count(*), count(*) FILTER (WHERE success)
#      FROM node_probe_runs WHERE credential_id IN (3,4,29,45,49,50,58)
#      AND raw_model_name ILIKE '%kimi%' AND created_at>now()-interval '48 hours' GROUP BY 1,2;
```

---

**观测人**:ZCode
**日期**:2026-09-16
**状态**:五项任务首轮观测完成;T1/T2/T4 待例行部署开启窗口,T3 待运营补录,T5 部分恢复持续观察

## 七、例行部署执行记录(2026-09-16 00:36–00:50,补录)

**T1/T2/T4 观测窗口自此开启。**

### 7.1 部署过程

| 项 | 结果 |
|----|------|
| 前置门禁 | env-injector 双设备注入通过(245=aliyun-frontend-245,154=aliyun-gateway-154);go build ✓;go test bg/admin/autoroute 9 包全绿;无并行 deploy 进程 |
| 245 第一跳 | 本会话 `deploy-245.sh` 部署 **2122-040207f7** 成功(139s,切换 38s;040207f7=598ef9cb6+本报告文档提交) |
| 245 竞争覆盖 | 约 5 分钟后并行审计线覆盖部署 **2125-3a68ff1f**——3a68ff1f 是 origin/main HEAD,血统含 598ef9cb6(二轮修复)✓、bac8b6e9e(O4 指标接线)✓、040207f7c(本报告)✓,另含并行线免费凭据容量优化(8f00bde9c)/本地免费激活(c0f71b440)/gitignore;**部署目标(二轮修复+接线上生产)由 2125 达成**,内容级复核通过(patterns.go 词表/N2 守卫/11 类 allowlist/main_types.go 接线全在位) |
| 154 | `deploy-154.sh` 部署 **2123-3a68ff1f** 成功(129s,切换 14s),active 8782,healthz ready=true |
| 公网 L4 | `https://llmgo.kxpms.cn/healthz` → 200,2125-3a68ff1f ✓ |

⚠️ **共享节点并行部署竞争**:245 在 5 分钟窗口内被两条部署线先后覆盖(2122→2125)。两条线部署的都是同一 origin/main 血统,最终态正确;但该竞争模式与本地共享网关 N4 同型,若两线部署不同血统将产生真冲突,建议人工协调部署窗口或按 N4 评估实例隔离。

### 7.2 部署后冒烟

- auto route listener 存活:2125 构建启动后对 credential_model_bindings/credentials NOTIFY 正常刷新索引(O5 修复的装配面在新构建延续生效)。
- deployment-gate key(api_keys id=105)于部署门禁期间(00:44)产生真实请求流量,网关链路健康。
- 部署后基线快照(00:50):selections 7 天窗口仍为 9 条/4 兜底(无新 auto 流量,符合部署前判断)。

### 7.3 侧发现(非本轮缺陷,建议另开修复轮)

1. **`request_logs_with_current_month` 视图生产缺失**(SQLSTATE 42P01):`auto_summary_generator` 的"summary persisted but title task lookup failed"持续 ERROR——摘要本身已持久化,仅标题任务查询失败,非致命。仓内 `sql/objects/views/request_logs_with_current_month.sql` 有定义,生产库未建,属 schema 漂移;按 pg 日志审计修复闭环惯例应走"新编号迁移+部署清单+installer 三处同步"修复。
2. **auto-route settle 基线 NULL 扫描 WARN**(`cannot scan NULL into *string (col: task_type)`):cohort 基线查询遇到 task_type NULL 行时降级用中性基线,影响调优信号质量,不影响决策。

### 7.4 周观测机制

每日 09:33 定时任务(automation-39d46377,共 7 次)执行只读观测并追加到 [2026-09-16-round3-weekly-observation-log.md](2026-09-16-round3-weekly-observation-log.md)(D0 基线行已落)。观测内容:双节点构建身份、selections 7d 兜底分布、decision_trace task_type 分布、journal escalation/failed 24h 计数。统计达标判据:某日 7d 总数 ≥30 且兜底占比 ≤35% 记"达标信号"。

## 八、D0 夜间补记(2026-09-16 02:23,部署后 ~1.5h)

### 8.1 O4 指标读数通道验证(T2 前置,已打通)

§二"指标通道…401 保护(无法匿名读)"的阻塞已随例行部署解除,当晚实测:

- 两节点 `/metrics` 以 admin key 鉴权读取均 **HTTP 200**(245 共 1620 条 llm_gateway_* 序列,154 共 837 条)。`llm_gateway_llm_circuit_breaker_state/_consecutive_failures`(0=closed)与 `llm_gateway_llm_classifier_latency_seconds` histogram 族**在位且为 0**;`llm_gateway_llm_classifier_total` 是 CounterVec,首次 escalation 落第一个 outcome 标签后才会出现在输出——**当前全 0 与"部署后无 auto 流量"一致,不构成接线失效证据**(接线在位已由 2125/2123 内容级复核+auto_route_wiring_guard_test 保证)。
- 两节点进程 env 均**已配置** `LLMGatewayAutoLLMEndpoint`(Model=deepseek-v4-flash):escalation 一旦发生,计数器即按 success/failure/timeout 落数,disabled 分支不适用。
- ⚠️ **admin key 取值配方(踩坑留档)**:`/etc/llm-gateway-go/env` **不可 source**(第 26 行非 shell 安全行,报"9527: 未找到命令",与 §附-4 /opt/.env 的 `&` 问题同族;文件 mtime 2026-08-25 未变,定时任务的 psql 步骤不受影响——DSN 在第 13 行,source 中止前已导出);且该文件内 `LLM_GATEWAY_ADMIN_API_KEY=` 行携带杂质(截取 67 字符),与进程真实值(245=25/154=51 字符)不符,拿去鉴权 401。**权威来源是运行进程的 environ**:
  ```bash
  PID=$(systemctl show -p MainPID <active-canary-unit> | cut -d= -f2)   # 154 的 systemctl 不支持 --value
  KEY=$(tr '\0' '\n' < /proc/$PID/environ | grep '^LLM_GATEWAY_ADMIN_API_KEY=' | cut -d= -f2-)
  curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:$(cat /opt/llm-gateway-go/run/active-port)/metrics
  ```

### 8.2 其余三项复核

- **部署后 journal(00:40 起)**:两节点 escalation/LLM fallback failed 均 **0**(无 auto 流量,符合预期)。
- **T3 复核**:suyun#39/apinext#61/智码#35 的 16 行 Claude 系绑定单价/pricing_source/pricing_updated_at **仍全空**——补录未发生,§三"不可验证"标注继续有效。
- **T5 复核(48h 探测窗,02:20 读数)**:#49 **9/32(28%,较 00:15 读数 6/28=21% 回升,last probe 01:49)**、#50 9/23(39%)维持但 **09-15 18:11 后无新探测**(探测端重退避)、#3 2/12 偶通、#4 0/17、#29 0/21、#45 0/12、#58 三模型 0/29 全败——维持"#50/#49 为 1M 长上下文观察候选、其余门控观察"结论不变。
- **selection 双面复读(02:23)**:7d 窗口 9 条/4 兜底(44.4%)与 D0 基线完全一致,rdl 分区面(creative×7 兜4+chat+planning)依旧对账,部署后仍零新 auto 流量。
