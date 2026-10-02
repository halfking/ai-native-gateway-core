# 2026-10-01 三十七轮续五 —— 802 断链收口 + 推送门三缺陷根修 + core.bare 毒化根因终审

会话：主分支合并同步轮收尾接力（09-30→10-01 交接续）。起于 f58e11541（交接时三方全等点），终态见 §六。

## 一、802 installer 断链收口（交接待办 ①）

- 上游 fc10b91c5（迁移765+installer 五点同步）在 main.go:585 `go:embed embeddata/startup/802_session_turn_details_gw_task_id_index.sql` + startupFiles map + runner.go 三腿齐备而文件腿缺失，installer module 自 f58e11541 起全线编译失败。
- 并行会话 e076f6381 已按真库 pg_indexes.indexdef/obj_description **逐字回填** `sql/migrations/startup/802_…sql`（内容由此不再"不可知"），但 installer embeddata 拷贝腿仍缺。
- 本会话 8dd26c771：按五点同步不变量（800/801 两对拷贝逐字节等价实证）补齐 embeddata 拷贝（cmp 字节等价），installer module `go build ./...` 恢复 + embed 门测试绿。无 down 文件沿 800 索引类先例，未造。
- 并行会话随后独立补了同一文件（795b1bbfb，与 8dd26c771 字节等价，合并零冲突自动同值归并）——重复劳动一次，内容无分歧。

## 二、pre-push 钩子三缺陷（交接待办 ④ 扩容）

1. **套件日志互覆盖**（c17aef532）：所有套件共写 `/tmp/test-$$.log`，多套同败只剩最后一套，且整体失败分支还会 `rm` 掉唯一诊断。改为逐套 `/tmp/test-<suite>-$$.log`，败留过清。本轮 github 门 4 败实证修复生效。
2. **gitdir 副本漂移**：安装于 module gitdir 的 pre-push 是全量拷贝，已落后 tracked 源一个版本（§三#14 前移的 core.bare 自愈在非 github 推送上全死——正是交接登记的"自愈仅在 github 推送时生效"根因）。gitdir 副本改为薄包装（exec tracked `.githooks/pre-push`，pre-commit 同款定式），漂移类根治。tracked 源补可执行位 100644→100755。
3. **GIT_DIR 泄漏毒化**（见 §三，12db02c86）。

## 三、core.bare 毒化根因终审（数周悬案关闭）

**机制**：真实 `git push` 时 git 向 pre-push 钩子导出 `GIT_DIR=<module gitdir>`；钩子拉起的测试套件继承之；`tests/bump_version_collision_test.sh:34` 的 `git init -q "$dir"`（自建假 checkout fixture）在 GIT_DIR 注入下 **reinit 本仓 module gitdir 并写入 core.bare=true**，随后一切 git 依赖操作（其余套件、go build VCS stamping）exit 128。

**证据链**：
- 毒化严格与 github 推送门运行窗口耦合（01:43:00 / 01:50:33+54 / 01:54:36+56 / 01:58:20+38 四轮全中），单跑套件、空闲窗口零复发；成对出现 ~20s 间隔与套件内两次 init 吻合。
- `GIT_DIR=$(git rev-parse --absolute-git-dir) bash tests/bump_version_collision_test.sh` **三秒复现**。
- 修复（12db02c86）：测试门运行前 `unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_QUARANTINE_PATH`；修复后同条件全套门 28 套件全绿零毒化；真实 push（02:23）门禁全绿一次通过。

**归因勘误**（本节为三轮误判的正式翻转）：
- 上一会话 github 门 3 套件"环境互踩"（deploy_local_contract/nocgo/sops 单跑全绿）——真因即本机制：门首套件放毒，后续 git 依赖套件连坐；单跑无 GIT_DIR 故绿。
- 本会话早段 "Sourcetree 嵌套仓刷新 bug" 嫌疑——冻结期毒化仍发，证伪。
- "Synology Drive 双向同步回滚" 假设——被 GIT_DIR 复现取代，非毒化者。
- 历届 §0 自愈（pre-commit/pre-push）是治疗不是诊断；本次为机制级根因闭环。

**纵深防御保留**：`scripts/corebare-watchdog.sh` + launchd `cn.kxpms.llm-gateway-corebare-watchdog`（5s 巡检 module config，治愈+留痕 `~/Library/Logs/llm-gateway-corebare-watchdog.log`）。§0 事件驱动自愈照旧。

## 四、github 镜像当前拦截（登记待用户拍板，非本会话可决）

codeup 新增 `envs.samples/`（e4e2838f6，并行会话）被 §1 敏感信息扫描拦截：10×SECRET_FILE（.env 样式）+ DB_CONNSTRING 示例串。**分诊**：值大多规范占位（RFC5737 203.0.113.x、sk-e2e-example-*、openssl rand 占位），但 07-ops-servers 含真实内网拓扑 IP（10.0.0.154/252、10.177.0.x、192.168.1.10）。与 09-24 "GitHub 镜像被敏感扫描拦截待用户决策" 同类：baseline 放行 or 内容再脱敏 or 目录移出镜像范围，owner 拍板。本会话不代决。

## 五、登记不修（卫生项）

- `com.halfking.acc-swarm-drill` LaunchAgent：/tmp 包装脚本已消失，自 09-29 起空转失败（/tmp/acc-swarm.log 0 字节）。
- `cn.kxpms.llm-gateway-maintain` LaunchAgent：指向 `/var/folders/.../client-deploy-smoke.TK0ic9/install5/scripts/backup-db.sh`（临时冒烟目录，随时被系统清理，3AM 档每日必败）。
- Synology Drive 对 `/Users/xutaohuang/workspace/` 全量**双向**同步（session_table id=3）：含 `.git` 对象库与 gitdir，冲突回滚风险长期存在（本轮虽非毒化者，但双仓元数据入 NAS 双向同步仍是数据完整性隐患）；建议排除 `.git` 或改单向备份。
- bump_version_collision_test.sh 自身可加 GIT_DIR 消毒（钩子层已兜底，双保险归后续轮）。

## 六、终态

- **main = codeup/main = a8b76072e**（含本会话 8dd26c771 / c17aef532 / 12db02c86 及并行会话 docs/线全部内容；多次赛跑合并均零冲突）。
- **github/main = 12db02c86**：本会话全部内容已上公共镜像（02:23 门禁全绿推送）；落后 1 个合并点（a8b76072e，内容=并行会话 envs.samples 等 docs 提交）——被 §四 策略门拦截，待拍板。
- 双 module go build 绿；ledger/ursm/deploy_local_contract 单跑绿；全套门 28 套件绿（GIT_DIR 注入条件复验）。
- 并行会话全程活跃（自 8dd26c771 起连续 5 次抢推 codeup），本会话按赛跑纪律逐次 re-merge，无互相覆盖。
