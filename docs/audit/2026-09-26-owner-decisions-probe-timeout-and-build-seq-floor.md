# owner 决策两项执行轮：探针 154/seamless 对齐 600 + build_seq 撞号根治（2026-09-26）

## 0. 范围

本轮执行两项挂账 owner 决策（"请按建议执行"授权）：

1. **探针默认 ①**：deploy-154.sh / deploy-seamless.sh 前向探针默认 120s → 600s，对齐 245（d5eeb71eb 先例）。
2. **版本戳 ②**：bump-version.sh 机器级 floor 计数器，根治并行双 checkout 同名版本互覆盖（09-25 seq 2250 撞号实录）。

主 checkout（并行 v2 会话 WIP）全程未碰；全部改动在临时 worktree 完成并推送。

## 1. 探针 600s（owner 决策①）

改动三处、**保留一处不动**：

| 位置 | 改动 | 理由 |
|---|---|---|
| deploy-154.sh:68 | `:-120` → `:-600` | 154 wrapper 入口；154 与 245 共享 252 PG，冷启动 ensure 链 90-150s 的同一失败模式在 245 压垮过两次（09-19、09-23 seq 2221），154 一直暴露在同等风险下；不再依赖出事后 `PROBE_TIMEOUT_SECS=600` 人工救场 |
| deploy-seamless.sh forward 探针（原 1171） | `:-120` → `:-600` | 直接 `deploy-seamless.sh deploy 154` 入口的兜底默认；wrapper 的 export 会抢占此值，两处都改才全链路覆盖 |
| deploy-seamless.sh host_rollback fallback（原 1509） | **保持 `:-120`，注释更新** | 有意保留：回滚候选是已预热旧版本（ensure 热路径 60-90s）；回滚是故障恢复动作，600s 只会把失败恢复拖长十分钟 |
| deploy-245.sh | 无改动 | 已是 600（d5eeb71eb） |

**systemd 预算已提前铺好**：deploy_readiness_contract_test 早已断言 154/245 两个 canary unit 模板 `TimeoutStartSec=700s`（9cbd60e40），600s 探针窗口不会被 systemd 提前击杀。第二窗口 PROBE_RETRY_TIMEOUT_SECS 保持 60s（真坏候选爆炸半径不变：探针期不接流量 + 660s 最坏总时长，与 245 现状一致）。

**契约同步**：deploy_blue_green_contract_test 的断言从钉 `:-120` 更新为双断言（forward `:-600` + host_rollback `:-120`）——旧断言原本会靠 rollback 行的字面量"假绿"（正是该测试注释里警告的 R49 陈旧断言模式）。

## 2. build_seq 撞号根治（owner 决策②）

**根因**：bump-version.sh 的 `NEW_SEQ = version.json.build_seq + 1`，而 version.json 是**各 checkout 各自的副本**。两个 checkout 同读 2249 → 同产 `*-2250` 同名 release 目录/镜像 tag → 部署到同一目标根时互相覆盖 → cutover 可能切到对方的二进制（09-20 00:42 实录 + 09-25 seq 2250 撞号）。已有的本地/远端部署锁只防"部署交错"，防不了"序号同源"。

**修法**：机器级 floor 计数器（bump-version.sh 内联，无新依赖）：

- 位置：`$HOME/.llm-gateway-go/build_seq.floor`（跨 checkout 共享，机器级唯一；可用 `LLM_GATEWAY_BUILD_SEQ_FLOOR_DIR` 覆盖，测试用）。
- 算法：锁内取 `NEW_SEQ = max(floor, version.json.build_seq) + 1`；`--seq N` 仍尊重且推进 floor；floor 在 version 文件写盘**之前**落盘（持锁段毫秒级，崩溃后 seq 自然跳过，计数器只增不减语义不变）。
- 锁：mkdir 原子语意（macOS 无 flock(1)、Git Bash 同样可用；与 deploy-lib/lock.sh 的 fallback 同型，但 bump-version.sh 是仓库内脚本，不引入对 ~/workspace/ai-native-tools/deploy-lib 外部 SSOT 的耦合）。等待有界 15s，超时**退回无 floor 旧行为并告警**，绝不阻塞部署；持锁崩溃残留的 hold 目录 60s 后可被安全接管。
- 降级路径：`$HOME` 不可写 → 静默退回旧行为。
- 连续性：全新机器 floor 缺失 → `max(0, current)+1`，首个 bump 无跳号。

**生效链**：commit+push → official-deploy 克隆 `git pull --ff-only`（部署真入口）→ 12 份脚本副本中其余 11 份随各自 checkout 的 pull 自然跟进。

## 3. 测试证据（全绿）

```
tests/bump_version_collision_test.sh      5/5（新增）
  - 并行双 checkout 同读 2249 并发 bump → {2250, 2251} 无重复无跳号（撞号回归钉子）
  - --seq 3000 推进 floor 后默认 bump = 3001（floor 持久）
  - floor 不可用 → 退回旧行为 2249→2250（不阻塞）
  - 全新机器 current=500 floor 缺失 → 501（连续性）
tests/deploy_port_rotation_test.sh        15/15
tests/deploy_promotion_test.sh            9/9
tests/deploy_lock_test.sh                 51/51
tests/deploy_local_contract_test.sh       exit 0
tests/deploy_blue_green_contract_test.sh  exit 0（新断言生效）
tests/deploy_readiness_contract_test.sh   exit 0（700s 预算双目标确认）
```

## 4. 遗留与边界

- 撞号防护范围是**同机多 checkout**（本机 12 份副本的实证场景）。跨机同 seq 进同一部署根在理论上仍可能，属"seq 漂移接受"域，由 upload_release"拒绝覆盖活跃 release"守卫兜底——维持原设计边界。
- official-deploy 克隆内有一份未跟踪的 `docs/audit/2026-09-26-r66-48h-audit-round.md`（并行 r66 会话产物），pull --ff-only 不受影响也不触碰它。
- 12 份副本中 9 份开发/同步仓的旧脚本不主动清理——各 checkout 按自身节奏 pull，部署行为只取决于 official-deploy 克隆一份。
