# 热区复审三收口项:O5 回放缺口 / O1 首解析窗 / R-9 跨部署持久化

- 日期:2026-09-30(local 环境)
- 上游依据:docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md 的 O1/O5 挂账 + F4 对账口径;docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md R-A
- 分支:fix/o5-fallback-salvage(411ff21c7 + 9bc266f65);主树有并行会话(761 迁移 WIP),全程在独立 worktree

## 一、O5:PG 硬停窗口完成的请求,恢复后为何未回放

### 1.1 结论先行

**遥测条目没有丢。** 它们在 fallback 文件里躺了一天;「未回放」是两层原因叠加:

1. **设计层面:回放只存在手动触发路径,恢复监听器不做任何自动回放**(见 §1.4 门控)。演练当时恢复 PG 后无人调用手动端点,条目自然原地不动。
2. **缺陷层面(可复现,已修):即便当时有人触发文件恢复,也会整文件失败。** 多进程 O_APPEND 追加 + 容器硬杀跳过 gzip.Close 把日文件打成「孤儿 gzip header 连排 + 完整 member + 截断尾」的混合体,`FileReader.ReadRecords` 的单遍 `gzip.NewReader` 在第一个坏 member 即整体报错——0 条可恢复。

### 1.2 取证链(窗口内完成)

| 证据 | 值 |
| --- | --- |
| fallback 文件(宿主 bind mount,容器 21:47 重建未抹掉) | `~/kaixuan/llm-gateway-go/backups/backups/sessions-2026-09-30.jsonl.gz`,sha256 `cc3f33a7a8cf3d9aafecdb622aa470dcb5cb2207a9f1eed83a1affae3d5d748c`,6230 字节 |
| 文件结构(逐字节) | 孤儿 header @0/10/20/30 + member [40,6200) + 孤儿 header @6200/6210/6220;member 尾部截断(无有效 trailer),header@40 恰好顶替真 header@50 的位 |
| O5 请求 021da488 在文件内 | request_wal `:initial` ×2 @06:57:59Z、request_log `:insert` @06:57:59.590Z、`:update` @06:58:27.848Z |
| PG 侧核验(2026-09-30 22:40) | `request_logs_hot` / `request_wal_hot` 对 `021da488%` 均 0 行(llm_gateway 库) |
| 打捞结果(修复后 reader 跑真文件) | 55/55 条记录全回收(request_log 35 + request_wal 20),含 O5 两条;跳过坏行 2(交错写残行) |
| 演练窗网关日志 | 已随 21:47 部署轮转丢失(gateway.log 现存最早 13:29Z);「无镜像文件」一节无法补勘——旧容器可写层已销毁,如实记为不可复核 |

时间线核对:PG stop 14:58:02(CST,docker stop 完成时刻;SIGTERM 与连接终止先于此)→ insert 落 fallback 14:57:59.590(停机窗口内的 PG 报错,经「报错→fallback 兜底」分支)→ 终态 update 14:58:27.848 落 fallback(时刻与 degraded 旗标 ~30s 潜伏的翻转点接近,degraded 直写与报错兜底两分支的 fallback 落点一致,事后无法区分走哪支)→ PG 恢复后无任何回放调用。两条 fallback 分支都先于 PG 往返/降级早退**之前**写 H3 镜像(见 §1.5),镜像缺失因此仍属未解(见 §1.6)。

### 1.3 fallback 管线实勘(重试上限/丢弃条件清单)

写入侧(`domains/hooks/observability/telemetry/client.go`):

- `degraded=false` 时:worker flush → `persistRequestLog` → PG INSERT/UPDATE 失败 → `fallback.WriteRequestLog`(每条一次,无 PG 侧重试)。
- `degraded=true` 时:入口直接写 fallback,不再碰 PG。
- 队列满:`EmitRequestLog` 同步调 `persistRequestLog` 兜底,不丢条目(镜像照样先写)。
- 唯一静默丢弃点:`c.stopped`(进程关闭中)与 emit 前的 required_field_guard(空 request_id)。

fallback 汇(`MultiBackupWriter` = FileWriter + RingBuffer):

- FileWriter(磁盘,`LLM_GATEWAY_BACKUP_DIR`/`./data/backups`):重试 3 次,当日 gzip 文件,单文件 100MB / 日 50 文件上限;**进程硬杀时最后一个 member 无 trailer,且文件以 O_APPEND 复用**——正是本case的损坏源。
- RingBuffer(内存,cap=10000,drop-oldest):`dropped` 计数器 + Prometheus 告警;**随进程死亡清零**。

回放侧:

- RingBuffer:`POST /internal/telemetry/fallback-buffer/replay`(admin token);失败条目 requeue 不丢,注释明确「Replay 仅手动触发,无自动循环」。
- 文件:`GenericRecovery`(admin 端点)逐条 `ReplayFallback`,UPSERT 幂等;`request_log`→telemetryClient(带 R44 exactly-once hook 补发),`request_wal`→requestLogger。

### 1.4 门控条件(无自动回放的完整口径)

1. `dbMonitor` 10s×3 才翻转 degraded(~30s 潜伏);窗口内 PG 报错条目经错误分支同样进 fallback,无丢失、只是路径不同。
2. 恢复(`DBStatusAvailable`)监听器只做:`SetDegraded(false)` + `ttlManager.ExitDegradedMode`,**不触发任何回放**。
3. 手动回放的三个入口:ring buffer replay 端点、文件 GenericRecovery、会话 Recovery(admin)。
4. ring buffer 回放是破坏性弹出(失败 requeue);文件恢复成功且无损失才允许归档(本次起加护栏,见 §1.5)。
5. 进程重启 = ring buffer 内容清零;文件是唯一持久层 → **文件可恢复性是 O5 类场景的生死线**(本次缺陷正在这里)。

如需「恢复即自动回放」,属行为变更(回放风暴/顺序/幂等边界),本文档不做,留 owner 决策(2026-10-01 后续轮已出决策材料:docs/audit/2026-10-01-auto-replay-on-recovery-decision.md);当前手动 runbook:PG 恢复后 → `GET /internal/telemetry/fallback-buffer/stats` 看积压 → `POST .../replay`;文件侧走 admin degradation recovery 端点。

### 1.5 修复(411ff21c7)

`domains/dbdegradation`:新增 `file_reader_salvage.go`,`ReadRecords` → `ReadRecordsWithStats`:

- 逐字节扫 gzip magic;完整 member 用「指数扩张+二分」精确定位端点(谓词单调,偏大跳后续/偏小重扫都不可接受);截断 member 宽容解码已刷出部分后重同步。
- member 边界强制丢弃残行——实测截断 member 的残行会与下一 member 首条记录**粘连成坏行**,吞掉好记录。
- 坏行跳过计数;`SalvageStats{Members,PartialMembers,SkippedLines,LostBytes}`。
- **归档护栏**:`GenericRecovery`/`Recovery` 在 `HasLoss()`(SkippedLines>0 或 LostBytes>0)时不得归档/删除原文件。
- 超过 256MB 的文件退回纯流式(无打捞),内存有界;打捞路径单行上限 10MB 与旧 scanner 对齐(复审补,防超长垃圾行无界吃内存)。
- 测试 12 项全绿(干净/多 member/孤儿 header 前缀/截断+完整/纯截断现场形态/坏行/纯垃圾/超长行/Payload 持有完整性/真文件可选回归/归档护栏×2);真文件验证 55/55 含 O5 两条,且已固化为 `TestSalvage_O5EvidenceFileOptional`(env 守卫,文件在即跑,默认指 /tmp 取证副本)。

### 1.6 遗留(如实记)

- O5 那条的**镜像文件缺失**无法补勘(旧容器可写层已销毁);候选归因(镜像 tenant 目录粒度分裂/条目体为空)留待下次演练窗口内对照取证。
- FileWriter 侧的根治(每进程独立文件名或启动时检测孤儿 header)未做,打捞 reader 已让既有格式可恢复;列后续候选(2026-10-01 后续轮已出决策材料:docs/audit/2026-10-01-filewriter-rootfix-decision.md)。
- `/tmp/o5-evidence-sessions-2026-09-30.jsonl.gz` 为窗口内取证副本;含探针/请求载荷,**不入库**。

### 1.7 F4 对账假设验证:「镜像可能多于 PG」成立

- telemetry 侧镜像在 `persistRequestLog` 入口、任何 PG 往返与 degraded 早退之前 fire(PG 失败=镜像已在);v2 侧镜像在 tx.Commit 之后(R-A)。两侧都是「镜像先于/独立于 PG 成败」→ 镜像 ≥ PG,单向容差成立。
- 反向(有 PG 行无镜像)不可达:条目进 fallback 的两个分支都在镜像之后;回放路径不再补镜像,但回放条目源自已镜像的 emit,PG 只是向镜像收敛,不会越出。
- 对账脚本口径(沿用 F4-①②):豁免 PG 侧 null/{} 行;注意镜像 tenant 目录粒度(ApplicationCode 优先、回退 TenantID,同请求可分裂两目录);热区 data/ 未挂载期间的镜像随部署清零(见 §三 R-9)。

## 二、O1:启动后首解析窗 candidates_count=0

### 2.1 结论

**不是网关侧预热窗,是数据侧真空被日志如实反映。**

- 候选解析 = `candidates_resolved` → `resolveCandidatesForRequest` → `provider.Client.getCandidates` → 进程内缓存(非空 30s/空 5s TTL)+ 单飞 → **纯 PG 查询**(`candidateQuerySQL`,model_offers×`v_routable_credential_models` 视图)。链路上没有任何异步预热门挡解析;URSM v2 ready gate 存在,但演练窗口内请求实际到达了解析点(窗口内有 candidates_resolved 日志为证),故该 gate 未参与此现象。
- 对照实证:2026-09-30 21:29(CST)启动的那代容器(21:47 被并行部署替换;gateway.log 13:29:33Z 起的行属它),进程起来 64s 内真实模型 count=10/6 解析成功,**无窗口**;count=0 全部伴随 1ms 内 `[candidate_diag] db_empty` WARN,且都是 probe 直连杂模型(它们本无 offers)。
- 演练环境的窗口 = **`v_routable_credential_models` 视图按 `node_probe_state` 排除绑定**(故障场景触发节点探针,失败置 last_direct_ok=FALSE + next_retry_at 指数退避;候选 SQL 正 join 该视图;恢复函数 db_recover_mock_bindings 的注释与函数体均实证「不清理该表,恢复 healthy 后仍被路由排除数分钟」)。空结果仅缓存 5s,503 窗口 ≈ 数据真空窗口,退避到期+探针成功即自愈(演练 r4 的 10s 后 count=7 = 排除解除)。

### 2.2 运维等待判据(日志锚点)

```
# 就绪:目标模型出现非零解析
grep '"msg":"candidates_resolved"' gateway.log | grep '"model":"<目标模型>"'... 
# 实际字段在 db_empty 行;更直接的等价判据:
grep '\[candidate_diag\] db_empty' gateway.log | grep '<model>'   # 有=未就绪
# 就绪=该模型 db_empty 停止增长且出现 candidates_count>0 的 candidates_resolved
```

- 启动健康前置锚点:`fsstore: PG probe ok` → `CHECKPOINT: ...` → `database status changed ... available`。
- 首窗 503 的处置:确认 db_empty 的 model 字段——若为预期模型,等数据侧(绑定/目录);**不要调探针超时窗口**(09-23 已有同型教训)。

## 三、R-9:热区跨部署持久化(9bc266f65;2026-10-01 真机实证收口)

- `scripts/deploy-local.sh` gateway_bind_args 增 `data/` 挂载。镜像 WORKDIR 相对默认
  `./data/hotzone` 随之落到宿主;此前它是唯一未挂载状态目录,每次蓝绿切换清零。
- 试错记录:曾按「相对+绝对双约定」同源双挂载(/app/data 与 /opt/llm-gateway-go/data),Docker Desktop 上 opt 路径写入未落宿主,弃用,单挂载对准实际在用的相对约定。
- trimmer 预算与挂载无关,不漂移(已实勘 `bg/hot_zone_trimmer.go`:retention/maxBytes 来自 config `HotZone.RetentionHours/MaxSizeGB` 原子热更新,配额按受管子树字节计,无 statfs/文件系统依赖);持久化只改变文件寿命=预期语义。注意:宿主 `data/` 内**白名单外**子树无任何清理机制(trimmer 只管 cache/session_bodies/requests),跨部署累积风险挂账。
- Docker 语义验证:一次性容器经挂载写 marker → `docker rm -f` → 新容器重挂载读取,marker 存活 ✓。
- 同步:official-deploy 克隆 `services/llm-gateway-go/scripts/deploy-local.sh` 已同补(部署真入口,不 push 不生效的老坑;该克隆内 VERSION/version.json 等既有未提交改动属并行会话,未动)。

### 三·R-9 真机实证(2026-10-01,证据档 /tmp/r9-verify/EVIDENCE-SUMMARY.txt)

原「待办:真实蓝绿切换的部署级实证」已完成,并**抓出并修复首版挂载目标错误**:

1. **勘误(修复,当日)**:首版按「镜像 WORKDIR=/app」挂 `data/:/app/data`——勘的是
   构建期 `Dockerfile`(WORKDIR /app);实际运行的 runtime 镜像由部署时生成的
   `run/runtime.Dockerfile` 定义,`WORKDIR=/opt/llm-gateway-go`。部署级实证:env 注入
   激活键+6/6 流量后热区 **402 文件全部落在容器可写层** `/opt/llm-gateway-go/data/hotzone`
   (docker cp 取证副本在案),`/app/data` 恒空——首版挂载是死的,R-9 原问题
   (切换清零)实际未修。已改挂 `/opt/llm-gateway-go/data`(worktree + 克隆双同步)。
2. **修正后三项验证全绿**(容器级蓝绿重建,同镜像 2363;管线部署当日被并行会话
   718/719 迁移 WIP 的共享 PG 索引中间态阻断——`attach session_turns digest_null
   child indexes` SQLSTATE 55000——按零干扰纪律未动其作业区,改走演练 §6 同款
   容器重建路径,旧代无恙):
   - 挂载在场:新容器 Mounts 含 `/opt/llm-gateway-go/data` ✓
   - 跨切换存活:71/71 文件 hash 清单跨 `docker rm -f` 重建后全部存活
     (diff 仅 model-quality 同路径被新代续写=持久化语义本身)✓
   - trimmer 正常回收:预埋 `hotzone/cache/r9-trim-marker.json`(58B,mtime-8h,
     超 7h 保留期)在新代**首 tick 即被回收**(`清理完成 deleted_files=1
     freed_bytes=58`,trimmer 启动即执行一次 TrimOnce 的既有语义);hotzone 子树
     容器=宿主 46=46 同源视图 ✓
3. **激活前置(部署口径)**:例行管线 env 六键全空=D1 语义「空=不激活」(dispatcher
   default 分支走历史装配),热区激活需 `LLM_GATEWAY_STORAGE_MODE=full`
   (+可选 `LLM_GATEWAY_HOTZONE_ENABLED=true`)。本轮验证用 env 注入激活;**是否把
   激活键设为管线默认(.env.local)留 owner**。未激活期间 data/ 已持久但无热区写入;
   再激活时 trimmer 首 tick 会排空历史过期文件,自洽。

## 四、证据与窗口纪律

- fallback 原文件 hash 与打捞输出在 §1.2;取证副本在 /tmp(不入库)。
- 主树/克隆均有并行会话活动(本地 main 在本次会话内被推进一次;克隆内 21:47 部署轮转了日志)——全程独立 worktree,未触碰共享工作区。
- 演练窗网关日志已灭失(部署轮转),O5 镜像子问题与 O1 演练侧原始日志不可补勘,均如实挂账。

## 五、批判式复审(2026-09-30 第二轮,自审修正)

对首轮交付逐声明核验,修正如下(诚实账):

1. **真缺陷(已修)**:打捞路径无单行上限——旧 scanner 路径 10MB 报错终止,打捞路径的 pending 会随无换行垃圾字节无界增长。补 `salvageMaxLineBytes=10MB`(跳过计数+丢弃到行尾+discarding 模式)+ `TestSalvage_OverlongLineDropped`。
2. **文档性错误(勘误)**:首轮代码注释与本文档声称「Payload 是 json.RawMessage 别名内部缓冲、回调必须同步消费」——**错**,`json.RawMessage.UnmarshalJSON` 是拷贝语义,回调可安全持有记录。注释改正并以 `TestSalvage_RetainedRecordPayloadIntact` 钉死(若未来改零拷贝此测试即红)。
3. **小缺陷(已修)**:`flushTail` 吞回调错误 → 改为返回并上抛。
4. **数字勘误**:首轮报告称「测试 10 项」,实际首轮 9 项;本轮 12 项(见 §1.5)。
5. **表述精度**:① §1.2 时间线——insert 落 fallback 时刻(14:57:59.590)早于演练记录的停机完成时刻(14:58:02),自洽解释为 SIGTERM/连接终止先于 docker stop 完成时刻;update 走 degraded 直写还是报错兜底两分支事后不可区分,已改口径。② §2.1「21:29 本次启动」实为 21:47 被替换的**上一代容器**(gateway.log 13:29Z 起的行属它),结论不变、归属改准。③ URSM gate 表述从「挡住时不是 count=0 形态」(未验证)收敛为「窗口内请求实际到达解析点,该 gate 未参与此现象」(有日志为证)。
6. **证据升级**:O1 机制从「套件注释」升级为函数体+SQL 视图实证(`node_probe_state` 排除 → `v_routable_credential_models`);R-9「trimmer 预算不漂移」从推断升级为代码实勘(`bg/hot_zone_trimmer.go`)。
7. **可复现性补强**:真文件验证固化为 `TestSalvage_O5EvidenceFileOptional`(env 守卫,默认 /tmp 取证副本,缺席自动 skip)——首轮该验证是一次性临时测试,跑完即删,不可复现。
8. ~~仍未覆盖(如实挂账)~~(2026-10-01 后续轮收口)~~:>256MB 超限回退流式路径无测试~~ 已补:FileReader 增 `salvageCapBytes` 测试注入点,三构造用例(超限干净多 member 全量交付+stats 恒零/超限损坏现场整体失败 0 条可恢复并对照同现场不超限可打捞/大小恰等于上限走打捞钉死 <= 语义),79d34fcaa;宿主 `data/` 白名单外子树无清理机制(见 §三)依旧挂账,并因挂载修正后 data/ 真持久而加重(model-quality/keystore 同树累积);O5 镜像缺失子问题依旧不可补勘。
