# R48 遗漏项收口 —— 2026-10-06（R48 报告 §五 优先项落地）

> 输入：`docs/12小时内修订审计-20261006-0213.md`（R48 轮，基线 `bdd797ffa`，
> 主修复已随 `bc60cab71` 落地）。
> 本轮对象：该报告**未动项与 §五下一轮优先项**中可代码化/文档化的部分。

---

## 零、一句话版

R48 报告结论表唯一标「未动」的 **R48-A5（v1_write_liveness 生产零接线，P1）**
本轮接线落地；§五优先项中 E5 / B1-CI / D4 / C7 / B4 五项一并收口；
§五-2（bodies 双腿真库数据门）与 §五-8（属主动作）显式延后，理由见 §三。

---

## 一、已修明细

### 1. R48-A5 / §五-1 —— v1_write_liveness 生产接线（P1，本轮首位）

`cmd/gateway/v1_write_liveness_worker.go`（新）：

- **常驻 worker**：每 5m 一 tick（窗口 30m 沿用 §9.264 选择），快照存
  atomic.Value，`CheckedAt` 即「worker 还活着吗」的读数（lastrun mtime 同惯例）。
- **信号策略**（§9.238「全程无任何信号」的直接否定）：
  dead **每 tick 一条 ERROR**（持续信号，不是转换点响一次）；dead→alive/quiet
  恢复一条 WARN；测量失败每 tick 一条 WARN；steady alive/quiet 静默
  （「没人用」不是事件——与三值不合并同一理由作用在信号层）。
- **admin 拉取面**：`GET /internal/v1-write-liveness`，鉴权同
  `/internal/telemetry/fallback-buffer`；verdict=dead → **503**（curl -f 族
  零解析告警），alive/quiet/unknown → 200；worker 未启动（db 关闭模式）
  不挂路由（fail-closed 同 ringBuffer 惯例）。
- 快照层第 4 值 `unknown`（测量失败）**只在 worker 快照出现**，分类器
  三值契约不动。
- main.go 三点接线（构造+启动 / 路由 / 优雅停机），**独立成块只看
  dbConn**——不挂 cred-recovery / probe-worker / data-plane 任何一个
  配置门（§9.238 的失效恰恰发生在「没人主动去看」的组合态）。
- 刻意不做：不掺 `/healthz`（那是进程活着的语义）；不挂 `/metrics` gauge
  （对外表面扩张仍属属主决定）。

门 ×6（`cmd/gateway/v1_write_liveness_worker_test.go`）：dead 持续信号 /
恢复单响 / quiet 静默 / 测量失败 unknown / 端点状态码矩阵 /
**main.go 接线守卫**（防「分类器存在但零调用点」复发）。接线守卫三点
变异检验全红（整块摘除=编译仍过的 R48-A5 原始形态、裸门加条件、删路由）。

文档回写：`docs/audit/2026-10-06-v1-write-liveness-signal.md` §五「本轮没有
接线」bullet 已改为接线记录（保留其余限度：窗口未论证、单时点实测）。

### 1a. 连带：rowsguard 豁免行漂移改键

接线块插入使 main.go 的 credential_model_bindings→provider_models DISTINCT
扫描循环 4719→4736，`internal/rowsguard` 按该豁免自带的漂移协议改键并
追加漂移史（逐字核对仍是同一处 `for rows.Next()`，上游下移 17 行，
非「另找一处顶上」）。

### 2. §五-4 / E5 —— LEGIT/DEBT 棘轮软肋（P2）

`internal/sqlreadguard/guard_test.go` 新门 `TestAllowlistReasonsHaveJustifiedShape`：

- 白名单理由必须是三档前缀之一：`LEGIT:` / `DEBT(R##):` / `TOOLING:`，
  冒号后必须非空——此前 LEGIT 桶零门，新债随手写个词即可不进基线。
- 负控制内建（10 个违规形态探针 + 4 个合法形态正控制）——检查器失效与
  「全部合规」不可区分（201 号 §102 教训）。
- 定位是防绕过不是内容审查：`LEGIT: 裸母表` 格式上仍合规，实质审查靠
  review 与守卫本体。

### 3. §五-3 / B1 后续 —— installer 门进跨模块门禁（P2）

`Makefile`：新目标 `guards-installer`（`cd installer && go test ./...`，
嵌套模块必须 cd 进跑——R45 教训），并作为依赖挂进 `guards` 主门。
tsv 缺 831 那类「根目录全绿下的红」从此在 15+1 守卫门里可见。
实测全模块约 23s。

### 4. §五-6 / R48-D4 —— RouteIncidentDrawer 导出失败文案 i18n（P3）

- 新 locale 模块 `routeIncidentDrawer.ts` ×8（zh-CN/zh-TW/en-US/ja-JP/
  de-DE/fr-FR/es-ES/ar-SA），两条键：`exportFailedPrep` / `exportNoChannel`。
- 组件接 `useI18n`，替换两条硬编码串（其一原为英文硬编码——R48-D1 修复
  时新增未收口的那条）。
- 棘轮锁进改进：CJK 基线 5531 → 5529（`i18n-cjk-count.mjs --update-baseline`）。
- 存量其余硬编码串仍在基线棘轮内逐批清偿；D-5 门禁镜像 web-mobile
  维持原计划（19 §4 移动腿落地时一并）。

### 5. §五-7 / C7-P3-2 —— cron 管道吃退出码口径（P3，改文档）

`scripts/252-monitor/etc.cron.d.pg17` + runbook §10.31.2 各补一段：
`| tee -a` 使 cron 层拿到的 rc 是 tee 的 0，0/1/3 分级到不了 cron job
状态——「丢退出码=没巡检」只对 log/状态文件成立。判**无实害**：告警在
脚本内推（notify.sh 转换点去重）、存活看 lastrun mtime，都不消费 cron rc。
未来要接 cron rc 告警须改管道形态并同步 ursmcheck cron 注册门。

### 6. §五-5 / R48-B4 尾 —— README 二进制计数口径（P3）

三数并存（34 / 44 / 46）先定口径再改数：**binaries = `cmd/` 下 main 包数**，
`go list` 实测 44（find main.go 双证一致；46 含 2 个非 main 辅助包）。
README 已订正为 44 并把口径写进括号。

---

## 二、门禁终态（本机实测）

| 门 | 结果 |
|---|---|
| `go build ./...` | 绿 |
| `go vet`（cmd/gateway / internal/sqlreadguard） | 绿 |
| `make guards`（15 守卫包 + installer 全模块） | 绿 |
| cmd/gateway 新门 ×6 + 既有 v1_write_liveness 门族 | 绿（接线守卫三点变异全红验证） |
| scripts/ursmcheck（cron 注册门族，含 C7 注释新增后复跑） | 绿 |
| web `vue-tsc --noEmit` | EXIT 0 |
| web i18n 三门（parity / keys_referenced / CJK 基线 5529） | 13/13 绿 |
| gofmt（本轮触及 Go 文件） | 清 |

---

## 三、显式延后项（非本轮范围）

| 项 | 理由 |
|---|---|
| §五-2 bodies 双腿真库数据门（P2） | 13 个 switch-only 读方的数据等价性门需要独立轮次设计真库夹具（两侧灌数 + 逐读方断言），塞进本轮会做成「只有 SQL 文本没有数据」的假门——恰是该项要补的东西。建议下轮首位。 |
| §五-8 R47 §七未动项 | 825-828 真实升级路径演练属运维动作；GitHub 镜像 PushProtection 属 Owner 放行——均非代码修复。 |
| D-5 门禁镜像 web-mobile | 维持 R48 原计划：19 §4 移动腿落地时一并。 |
