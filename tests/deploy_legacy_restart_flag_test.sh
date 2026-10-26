#!/usr/bin/env bash
# deploy_legacy_restart_flag_test.sh —— --legacy-restart 旗标的防回归门（2026-10-24 批判审计新增）
#
# 背景：deploy-seamless.sh:404 曾有一行主流程级无条件 `LEGACY_RESTART=false`，
# 把参数解析（:88 `--legacy-restart) LEGACY_RESTART=true`）的结果覆盖回 false
# ⇒ 该应急旗标自引入起就是死的——2026-10-24 实测带旗标跑仍走蓝绿预热路径
# （日志 0 次 "legacy"），且此前没有任何测试覆盖它 ⇒ 死了没人知道。
#
# 本门钉三件事（源码形态契约，无需真跑部署）：
#   ① 参数解析存在：非注释行里 `--legacy-restart) LEGACY_RESTART=true` 恰好 1 处；
#   ② 消费点存在：`$LEGACY_RESTART == true` 分支至少 1 处；
#   ③ ★ 核心判据：非注释行里的 `LEGACY_RESTART=false` **只允许出现在参数解析区
#      的默认值行**（行号 < 100）——参数解析之后任何位置再对它赋 false 都会
#      静默杀死旗标（当年的 bug 形态）。
#
# ⚠ 本文件自身的坑（写第一版时踩到，留证）：中文全角括号紧跟 `$n` 时 bash 会把
#   `n）` 当变量名 ⇒ set -u 下报「未绑定的变量」——这正是 guards-shell
#   （check-shell-fullwidth-var.sh）在防的形态 ⇒ 变量一律 `${}` 界定。
set -u
SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/scripts/deploy-seamless.sh"
pass=0; fail=0
check() { # check <desc> <cond(0=ok)>
  if [ "$2" = "0" ]; then pass=$((pass+1)); else fail=$((fail+1)); echo "FAIL: ${1}"; fi
}

# ① 解析行恰好 1 处（只数非注释行：2026-10-24 修复的注释原文引用会造成第 2 处命中）
n=$(grep -v '^[[:space:]]*#' "$SCRIPT" | grep -c -F -- '--legacy-restart) LEGACY_RESTART=true')
[ "${n}" = "1" ]; check "参数解析行恰好 1 处（实际 ${n}）" $?

# ② 消费点至少 1 处
n=$(grep -c 'LEGACY_RESTART" == true\|\$LEGACY_RESTART == true' "$SCRIPT")
[ "${n}" -ge 1 ]; check "消费点至少 1 处（实际 ${n}）" $?

# ③ 核心：非注释行的 false 字面量只许出现在前 100 行（解析区默认值）
violations=$(grep -v '^[[:space:]]*#' "$SCRIPT" | grep -n 'LEGACY_RESTART=false' | awk -F: '$1 > 100' | wc -l | tr -d ' ')
[ "${violations}" = "0" ]; check "解析区之后无 LEGACY_RESTART=false 重置（当年死旗标形态，实际 ${violations} 处）" $?

# ── 2026-10-26 增补：legacy 分支的实况解析契约 ──────────────────────────
# 背景：2026-10-24 seq 2823 现场实证，legacy 路径对蓝绿迁移过的目标双重断链——
#   契约 service_name 是弃用遗留 unit（245 = llmgo-245.service，且已 mask，
#   stop 直接失败）；canary 的 ExecStart 跑 slots/%i/gateway，
#   host_atomic_switch 翻的 current/顶层软链 canary 根本不读。
#   ⇒ 修复形态：ps 归属 > run/active-service 记录 > 契约 service_name
#   三级解析实况 unit；canary 形态走 slots 重指 + restart 实况 unit。
#
# ★★★ 2026-10-26 再订正（12h 审计轮）：本段原 ⑤⑦ 两条**把缺陷钉死了** ——
#   ⑤ 钉「用 slots/$legacy_port 重指」：★ 事实是 canary unit 的
#     ExecStart = /opt/llm-gateway-go/slots/%i/gateway ⇒ slot 的键是
#     **systemd 实例名（%i）**，而 %i 与监听端口是**两个不同的来源**
#     （llmgo-245-canary@2823.service ⇒ %i=2823，端口是 8781/8782）
#     ⇒ 用端口重指 = **重指了一个 nobody 读的目录** ⇒ restart 起的仍是旧 binary
#     ⇒⇒ **静默 no-op 部署**。⇒ 本段改钉「slot 由 %i 推导 + 与端口一致性校验」。
#   ⑦ 钉「candidate_service 改指实况 unit」：★ 那三行赋值（candidate_port/
#     candidate_service/active_service）**是死代码** —— legacy 分支写完就结束，
#     而 [9/9] 探针与 host_mark_verified **全在蓝绿 else 分支里**
#     ⇒ ⇒ 提交说明写的「共享 [9/9] 探针/诊断变量改指实况 unit」**没兑现**。
#     ⇒ 本段改钉**真正缺失的那件事**：legacy 路径自己的验证 + 标记 verified。
#
# ⚠ 判定窗口：legacy 分支已显著变长（补了验证/回滚/输入校验）
#   ⇒ 窗口不能是固定的「+60 行」⇒ 改为**按 else 关键字定位分支边界**
#   （legacy 分支以 2 空格缩进的 `else` 结束 ⇒ 用缩进特征定位，不依赖行号）。
start=$(grep -n -F 'if [[ "$LEGACY_RESTART" == true ]]; then' "$SCRIPT" | head -1 | cut -d: -f1)
# 分支体 = start 行之后、下一个 2 空格缩进的 `else` 之前
win=$(awk -v s="$start" '
  NR<=s { next }
  /^  else$/ { exit }
  { print }
' "$SCRIPT")
# ★★ 只看**代码位**：本分支的注释里会提到 `slots/$legacy_port`、`systemctl restart`
#   （讲「原实现怎么错的」），⇒ 若连注释一起数，「反向钉」会**恒红**，
#   ⇒ 而「反向钉」的价值恰恰在于它必须真的红 ⇒ ⇒ 必须剥掉注释行。
win_code=$(printf '%s\n' "$win" | grep -vE '^[[:space:]]*#')

# ④ stop/start 前必须解析实况 unit，且解析失败要拒绝盲切（不许静默回契约）
n=$(printf '%s\n' "$win" | grep -cF 'if ! detect_active_side; then')
[ "${n}" = "1" ]; check "legacy 分支先跑 detect_active_side（实际 ${n}）" $?
n=$(printf '%s\n' "$win" | grep -cF '拒绝盲切')
[ "${n}" -ge 1 ]; check "解析失败必须拒绝盲切而非静默继续（实际 ${n}）" $?

# ⑤ ★★ canary 形态的 slot 必须由 **unit 实例名（%i）** 推导，且须与端口一致
#    （原判据钉「按端口重指」= 钉死了一个静默 no-op 部署，见上）
n=$(printf '%s\n' "$win_code" | grep -cF 'legacy_slot="$REMOTE_ROOT/slots/$legacy_instance"')
[ "${n}" = "1" ]; check "canary slot 由实例名拼出（不是端口）（实际 ${n}）" $?
# ★★ 一致性校验（2026-10-26 变异实测补）：实例名（%i）与实况监听端口必须一致。
#   ★ 不一致 ⇒ %i 推出的 slot 与正在监听的那个 slot 可能是**两个**目录
#   ⇒ ⇒ 重指了一个 nobody 读的目录 ⇒ 又是**静默 no-op 部署**。
#   ⇒⇒ 这一条必须单独钉（变异 M4：删掉它时门原本照样绿 ⇒ 门有洞）。
n=$(printf '%s\n' "$win_code" | grep -cF 'if [[ "$legacy_instance" != "$legacy_port" ]]; then')
[ "${n}" = "1" ]; check "实例名与实况端口一致性校验在场（实际 ${n}）" $?
n=$(printf '%s\n' "$win_code" | grep -cF 'legacy_instance="${legacy_unit#*@}"')
[ "${n}" = "1" ]; check "canary slot 的实例名由 unit 名推导（实际 ${n}）" $?
n=$(printf '%s\n' "$win_code" | grep -cF "ln -sfn '\$REMOTE_ROOT/releases/\$version' '\$legacy_slot'")
[ "${n}" = "1" ]; check "canary 形态按实例 slot 重指（实际 ${n}）" $?
# ★ 反向钉：legacy 窗口内**不许**再出现按端口拼 slot 的写法
n=$(printf '%s\n' "$win_code" | grep -cF "slots/\$legacy_port")
[ "${n}" = "0" ]; check "legacy 窗口内无「按端口拼 slot」的写法（实际 ${n}）" $?
# ★ restart 必须打在**实况 unit** 上：数的是**执行**语句（`remote_ssh "systemctl restart ..."`），
#   不是错误提示里那句回滚建议（那也是 `systemctl restart` 但不是执行点）。
n=$(printf '%s\n' "$win_code" | grep -cE 'remote_ssh "systemctl restart .\$legacy_unit')
[ "${n}" = "1" ]; check "restart 打在实况 unit 上（实际 ${n}）" $?

# ⑥ 三级回退链：ps 归属之后必须先试 run/active-service 记录，最后才契约 service_name
n=$(printf '%s\n' "$win_code" | grep -cF "run/active-service")
[ "${n}" -ge 1 ]; check "回退链含 run/active-service 记录（实际 ${n}）" $?
n=$(printf '%s\n' "$win_code" | grep -cF '回退契约 service_name')
[ "${n}" = "1" ]; check "最终回退是契约 service_name 且带 warn（实际 ${n}）" $?

# ⑦ ★★★ 2026-10-26 再订正：legacy 路径必须**自己**做验证并标记 verified
#   （原判据钉「candidate_service 改指实况 unit」= 钉死了三行死代码）
#   ⇒ 真正缺的是：legacy 重启后**不探活**、**不核对运行中版本**、**不标记 verified**
#   ⇒⇒ 结果：起不来的 binary 也照样打印「✅ 部署完成」，且该 release 永远
#      verified:false ⇒ 下一个部署不敢回滚到它 ⇒ 闭环缺最后一环。
#
# ★★★ 判据口径：**逐分支**断言，不是「整段 ≥1 次」。
#   ★ 原因（2026-10-26 变异实测踩到）：legacy 分支有**两条**切换路径
#     （canary 形态 / host 形态）⇒ 若只断言「整段里 ≥1 次」，
#     ⇒ 删掉其中**一条**路径的验证时，门**照样绿** ⇒ **门有洞**。
#   ⇒ ⇒ 拆成两段分别断言 ⇒ 删任一条都红。
canary_win=$(printf '%s\n' "$win_code" | awk '
  /if \[\[ -n "\$canary_prefix"/ { inb=1 }
  inb { print }
  inb && /^    else$/ { exit }
')
host_win=$(printf '%s\n' "$win_code" | awk '
  /^    else$/ { inb=1; next }
  inb { print }
')
for probe_name in canary_win host_win; do
  eval "seg=\$$probe_name"
  n=$(printf '%s\n' "$seg" | grep -cF 'remote_probe "$legacy')
  [ "${n}" -ge 1 ]; check "${probe_name}: 自己探活 /healthz（实际 ${n}）" $?
  n=$(printf '%s\n' "$seg" | grep -cF '_verify_running_release "$version"')
  [ "${n}" -ge 1 ]; check "${probe_name}: 核对运行中 release 身份（实际 ${n}）" $?
  n=$(printf '%s\n' "$seg" | grep -cF 'host_mark_verified "$SSH_CMD" "$TARGET" "$version"')
  [ "${n}" -ge 1 ]; check "${probe_name}: 标记 release verified（实际 ${n}）" $?
done
# ★ 反向钉：不得再出现那三行死代码赋值
n=$(printf '%s\n' "$win_code" | grep -cE '^\s*(candidate_port|candidate_service|active_service)="\$legacy_(port|unit)"')
[ "${n}" = "0" ]; check "legacy 窗口内无「写完即弃」的探针变量赋值（实际 ${n}）" $?

# ⑧ canary 前缀剥离必须用参数展开：BSD sed 对 s/@\.service$//@ 报 bad flag
#   （2026-10-26 部署现场实测）⇒ 解析成空 ⇒ canary 判定静默走错分支。
#   钉两层：展开式必须在场 + legacy 窗口内不许出现对该后缀的 sed 替换。
n=$(printf '%s\n' "$win_code" | grep -cF 'canary_prefix="${canary_prefix%@.service}"')
[ "${n}" = "1" ]; check "canary 前缀用参数展开剥离（实际 ${n}）" $?
n=$(printf '%s\n' "$win_code" | grep -c "sed.*@\\\\.service")
[ "${n}" = "0" ]; check "legacy 窗口内无 sed 版后缀剥离（实际 ${n}）" $?

# ⑨ ★ 2026-10-26 审计新增：远端读来的 unit/port 必须校验后才准拼进 shell 命令
#   （它们来自 run/active-service / run/active-port 文件 ⇒ 被写坏就会拼出变形命令）
n=$(printf '%s\n' "$win_code" | grep -cE 'legacy_port" =~ \^\[0-9\]\{1,5\}\$')
[ "${n}" = "1" ]; check "实况端口有格式校验（实际 ${n}）" $?
n=$(printf '%s\n' "$win_code" | grep -cE 'legacy_unit" =~ \^\[A-Za-z0-9_')
[ "${n}" = "1" ]; check "实况 unit 有格式校验（实际 ${n}）" $?

echo "deploy legacy-restart flag: ${pass} 通过 / ${fail} 失败"
[ "${fail}" = "0" ]
