# 热区 R-9 + STORAGE_MODE 245/154 部署实证轮（2026-10-01）

- 任务来源：docs/audit/2026-10-01-hotzone-p1p2-critical-audit-r2.md §四.2 遗留清单
  （①R-9 部署级实证 ②245/154 显式 STORAGE_MODE=full + 三键四重证据 ③F3 量化）
- 开工前核验：D1（deploy-local-lib.sh:598 六键透传）/ O4（storage_mode_init.go:228
  同 defer 出口）/ D2（克隆 .env.local 字面量）三项修复在 de9870c10 全部在场，
  克隆与主树部署脚本逐字节 IDENTICAL，克隆 pull 快进 0/83 后复核仍生效
- 部署入口克隆：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
  （pull --rebase --autostash 到 de9870c10，部署脚本未带任何本地改动）
- 状态：①②闭环（本文）；③F3 见姊妹篇
  `2026-10-01-hotzone-f3-dual-write-quantification.md`

---

## 一、结论

**R-9 在 systemd 布局上天然成立并已实证**：245 两轮管线部署（2376→2377）间热区
文件 97→119 **零丢失**，L1.5 会话文件跨部署存活，第二个进程对同一会话
**l1_5.hits=1**（跨部署命中，非仅跨重启）。**②两环境激活闭环**：245（2376/2377）
与 154 生产（2378）均在 env 显式 `LLM_GATEWAY_STORAGE_MODE=full` 后装配五连、
`hotzone_enabled=true`、`mirror.hotzone_on=true`、mirror 计数增长 errors=0。
附带修复了 245 上一处**半完成切换态**（生产流量实际由弃用 unit 的 2372 老 bin
服务，详见 §三——本轮开工探针的头号意外发现）。

## 二、245 激活与四重证据（2376/2377 两轮部署）

### 2.1 激活方式（systemd 布局，与 docker 布局的差异）

seamless 管线不重写远端 env 文件（D1 六键透传是 deploy-local 的 docker 分支语义；
245/154 的 env 是远端持有的 /opt/llm-gateway-go/.env 与 /etc/llm-gateway-go/env），
激活 = 远端 env 追加 `LLM_GATEWAY_STORAGE_MODE=full`（0600 root:root）+ 管线部署。
候选进程经 `EnvironmentFile=` 读到该键，`initStorageMode` dispatcher 走 full 热区分支。
**该键跨部署持久，后续部署不再丢失**（与 docker 布局"每次容器重建 env 归零"对比）。

### 2.2 四重证据（D1 同款口径）

| # | 项 | 实证 |
|---|---|---|
| ① | 装配日志 | 10:55:52 trimmer 已装配（7h/1GB/30m）+ hotZoneOnly 装配 + telemetry mirror wired + session v2 mirror wired + **admin ingest mirror wired**（F5 第三写方随 83 提交差首次在 245 上线） |
| ② | hotzone_enabled=true | 2376 与 2377 两个进程均 true；mirror.hotzone_on=true（O4 live） |
| ③ | mirror.by_mode.full | total 33→111（约 11 分钟，含 probe 持续流量），**errors=0** |
| ④ | l1_5_by_mode.full.hits | **=1**（见 2.3 构造） |

### 2.3 ④ 与 R-9 的构造过程（可复现命令链）

1. 部署 #1（2376，active→8781）后经 admin API（/api/auth/token → /api/keys/24/reveal）
   取环境内既有 key，以 `X-Session-Id: hz-r9-sess-1` 发 2 个 chat 请求
   （deepseek-v4-flash，max_tokens=32）→ `cache/default/gw/gw_hz-r9-sess-1.json`
   Set 落盘 + requests/default/ 三件套镜像 + request_logs_hot 行（79c14866/d419c4b8）。
2. 基线快照（11:05:16）：hotzone 文件 97、L1.5 会话文件在、mirror.total=111。
3. 部署 #2（2377，active→8782，同码 seq+1）：蓝绿轮换，宿主 data/ 目录不触碰。
4. 部署后核验（11:07:14）：文件 **119**（零丢失，增量=部署间隙 probe 流量）；
   L1.5 文件原样存活（mtime 10:58 不变）；新进程装配日志在场、hotzone_enabled=true；
   同会话再发请求 → 日志 `session_compressor: v2 cache loaded successfully
   session=gw_hz-r9-sess-1 body_size=51`（L1 为空→L1.5 文件命中回填），
   **l1_5.hits=1**；该请求（网关 id cd95f875…）PG success=t + requests/default/
   三件套镜像齐。

**R-9 就此从"docker 布局挂载修复"升级为"两布局实证"**：systemd 布局 WorkingDirectory
=/opt/llm-gateway-go → `./data/hotzone` 天然落宿主持久盘，部署轮换只动 releases/
与 slots/ 符号链接，无需挂载。docker 布局（local）的 data/ bind-mount 勘误
（a2c721a9b）已有 10-01 真机三查全绿。

## 三、245 半完成切换态（开工探针意外发现，已修复）

> 时序证据：nigix upstream fragment（run/active-upstream.conf）05:06 已写 8782，
> 但 nginx 上次 reload 信号 05:06:34 之后 access log 200 条全部 upstream=8781；
> 8781 上跑的是**弃用 unit `llmgo-245.service`**（05:07:07 被一次性拉起）的
> 2372 老 bin；slots/8782 指向 2374 新 bin。即 09-30 2372→2374 部署的切换链
> 中断在半途，生产流量实际持续由旧 bin 服务了 ~5h。

修复（顺序敏感）：验 8782(2374) healthz → `systemctl reload nginx`（切流到 8782）
→ `systemctl stop + disable llmgo-245.service`（legacy 释放 8781，防下轮候选撞口）
→ 验证 8781 释放、生产 healthz 走 2374。修复后 2376 部署候选正常落 8781。

**三点运维认知（防复发）**：
1. `run/active-port` / `run/active-upstream.conf` 写入 ≠ nginx 已 reload——部署中断
   （如 handoff 超时被杀）会留下"文件说新、流量走旧"的态。仲裁以 access log 的
   upstream 字段或对比两端口行为为准，勿信单文件。
2. 弃用 unit `llmgo-245.service` 仍 enabled，可被带外手段拉起并抢占契约端口
   （deploy-245 skill 明令勿用；本轮已 disable，建议后续部署工单把它 mask）。
3. admin `/api/system/version` 读**根** `/opt/llm-gateway-go/version.json`
   （admin/misc.go versionJSONCandidates 硬编码），与 healthz（读
   LLM_GATEWAY_VERSION_FILE=slots/%i/version.json）口径分叉：半切换态留下真文件
   时 admin 端点恒陈旧；正常部署重建 `version.json → current/version.json`
   symlink 后自愈（本轮实测：部署 #2 后 symlink 恢复、端点报 2377）。

## 四、154 生产激活（2378）

- 部署：`deploy-154.sh --no-frontend`，2356(2d750fb4)→**2378(4e9eb2c7)**，总 74s、
  切换 13s，蓝绿 8782→8781，凭据解密冒烟过，部署前置 245 两轮全绿。
- env：/etc/llm-gateway-go/env 追加 `LLM_GATEWAY_STORAGE_MODE=full`（:333，远端持久）。
- 核验（11:11）：装配五连在场（11:09:55-11:10:01）；hotzone_enabled=true、
  mirror.hotzone_on=true；mirror.total=65 errors=0（激活后 ~1 分钟，生产流量）；
  磁盘 `data/hotzone/` 58 文件落宿主盘（requests/ 三租户 default/ide/openpocket
  + cache/）。
- l1_5 hits 未构造（生产环境不发合成会话流量；misses=23 证明读路径在走，
  命中语义已由 245 跨部署实测钉死）。

## 五、F3 量化采样（生产流量，详见姊妹篇）

- 245 t0 11:09:35：148 files / 90,334 B（mirror.total=63，部署 #2 后计数器）
- 154 t0' 11:11:18：62 files / 74,675 B（default 32 / ide 3 / openpocket 27）
- t1 回填：见姊妹篇 §三。

## 六、遗留与交接

1. ~~R-9~~ 闭环（两布局实证）。docker 布局的管线默认是否设 STORAGE_MODE=full
   仍留 owner（本次 systemd 两环境已显式激活）。
2. 245 半切换态的根因（09-30 2372→2374 部署中断点在哪、被什么杀）未考古——
   现象与修复已钉，建议部署工单加"切换后 access-log upstream 仲裁"步骡。
3. `llmgo-245.service` 已 disable；**建议 mask**（防 systemd 升级/带外操作复活）。
4. admin 版本端点与 healthz 的 version.json 口径分叉（§三.3）——正常部署自愈，
   半切换态暴露，低优挂账（admin/misc.go versionJSONCandidates 首位插入 env
   路径可根治，未动）。
5. 测试痕迹：245 key#24 未动（既有 key，仅 reveal 观测）；hz-r9-sess-1 会话的
   3 个请求与镜像文件留档（真实数据，可作为热区内容样例）；远端临时凭据文件
   已删。
