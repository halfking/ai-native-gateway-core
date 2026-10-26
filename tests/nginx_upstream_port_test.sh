#!/usr/bin/env bash
# =============================================================================
# nginx_upstream_port_test.sh — 「upstream 端口读写」这条事实链的门
#
# 覆盖两个被测件：
#   ① scripts/ops/nginx-upstream-port.py  （读 / 改 upstream 主 server 端口）
#   ② scripts/ops/deploy-doctor.sh        （只读体检：绑定是否还成立）
#
# ★ 为什么这些形态必须被钉住（2026-10-23，211 的 502 事故根修）：
#   nginx 的 upstream 块里**经常留着旧端口的注释**（245 实况原文）：
#       upstream llmgo_local_245 {
#           # Original hardcoded `server 127.0.0.1:8781;` did not allow nginx -s reload to
#           # switch traffic between the active (8781) and the canary (8782) candidate.
#           include /opt/llm-gateway-go/run/active-upstream.conf;
#       }
#   ⇒ ① **读侧**不剥注释 ⇒ 读到的「活跃端口」是注释里的旧端口 ⇒ 活跃槽判错；
#   ⇒ ② **写侧**不剥注释 ⇒ **改的是注释**并报 patched ⇒ nginx reload 后活的上游
#      根本没变 ⇒ 引擎以为「已切流」⇒ 随后停掉「旧槽」⇒ 流量落空 ⇒ **502**。
#   ⇒ 门必须钉住「注释里的端口不算数」。
#
# 零外部依赖：纯 bash + python3，不碰 nginx、不碰任何目标机。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 被测件可被环境变量覆盖 —— **只给变异验证用**（把改坏的副本喂给门，
# 证明门确实在测东西而不是恒绿）。日常运行不设，指向仓里的真件。
HELPER="${NGINX_UPSTREAM_PORT_HELPER:-$ROOT/scripts/ops/nginx-upstream-port.py}"
DOCTOR="${DEPLOY_DOCTOR:-$ROOT/scripts/ops/deploy-doctor.sh}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$HELPER" ]] || { echo "找不到 $HELPER"; exit 1; }
[[ -f "$DOCTOR" ]] || { echo "找不到 $DOCTOR"; exit 1; }

UP="ns_llm_gateway_upstream"

# ── ① 读侧：烤死形态（docker 形态 / 211）────────────────────────────
d="$TMP/read-baked"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    server 127.0.0.1:8782 max_fails=3 fail_timeout=10s;
    server 100.106.169.63:8782 backup;
}
CONF
check "① 读：烤死形态取到主端口 8782" "8782" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"

# ── ② 读侧：★ 注释陷阱（245 实况形态）───────────────────────────────
d="$TMP/read-comment"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    # 2026-08-31: Atomic blue-green fragment; deployer replaces this file and reloads nginx.
    # Original hardcoded `server 127.0.0.1:8781;` did not allow nginx -s reload to
    # switch traffic between the active (8781) and the canary (8782) candidate.
    server 127.0.0.1:8782 max_fails=3 fail_timeout=10s;
    server 100.106.169.63:8782 backup;
}
CONF
check "② 读：注释里的旧端口 8781 不算数（取真值 8782）" "8782" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"

# ── ③ 读侧：include 形态（systemd 形态 / 154、245）──────────────────
d="$TMP/read-include"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    # Original hardcoded `server 127.0.0.1:8781;` did not allow ...
    include active-upstream.conf;
    keepalive 32;
}
CONF
printf 'server 127.0.0.1:8782 max_fails=3 fail_timeout=10s;\n' > "$d/active-upstream.conf"
check "③ 读：include 形态取到生成文件里的端口 8782" "8782" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"
check "③ 读：include 目标可单独打印（**原始写法**，不做路径解析）" "active-upstream.conf" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP" --print-include)"

# ── ④ 读侧：backup 行不冒充主端口 ───────────────────────────────────
d="$TMP/read-backup"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    server 127.0.0.1:9999 backup;
    server 127.0.0.1:8781 max_fails=3 fail_timeout=10s;
}
CONF
check "④ 读：backup 行被跳过（取主 8781，不是 backup 的 9999）" "8781" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"

# ── ⑤ 读侧：没有这个 upstream 块 ⇒ 空 ───────────────────────────────
d="$TMP/read-none"; mkdir -p "$d"
printf 'server { listen 80; }\n' > "$d/c.conf"
check "⑤ 读：没有该 upstream 块 ⇒ 空" "" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"

# ── ⑥ 写侧：★ 改的是**活的上游行**，注释原样不动 ───────────────────
d="$TMP/write-comment"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    # Original hardcoded `server 127.0.0.1:8781;` did not allow ...
    server 127.0.0.1:8782 max_fails=3 fail_timeout=10s;
    server 100.106.169.63:8782 backup;
}
CONF
out="$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP" --switch 8783 --backup "$d/c.conf.bak")"
check "⑥ 写：判定=patched" "patched" "$out"
# ★ 用**同一个读回器**复核（不能用 grep：grep 是注释盲视的 ⇒ 它会读到
#   注释里那个 8781 —— 这正是本门要钉住的那类错误，断言自己不能犯同样的错）。
check "⑥ 写：活的上游行改到 8783（用读回器复核）" "8783" \
  "$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP")"
check "⑥ 写：注释里的 8781 **原样保留**（没被当成端口改掉）" "1" \
  "$(grep -c 'Original hardcoded `server 127.0.0.1:8781;`' "$d/c.conf" || true)"
check "⑥ 写：backup 行没被动" "1" \
  "$(grep -c '100.106.169.63:8782 backup' "$d/c.conf" || true)"
check "⑥ 写：备份里是改前内容（含 8782）" "1" \
  "$(grep -c 'server 127.0.0.1:8782' "$d/c.conf.bak" || true)"

# ── ⑦ 写侧：幂等（已是目标端口 ⇒ already，文件不动）─────────────────
d="$TMP/write-already"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    server 127.0.0.1:8783 max_fails=3 fail_timeout=10s;
}
CONF
before="$(md5 -q "$d/c.conf")"
out="$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP" --switch 8783 --backup "$d/c.conf.bak")"
check "⑦ 写：已是目标端口 ⇒ already" "already" "$out"
check "⑦ 写：文件未变" "$before" "$(md5 -q "$d/c.conf")"

# ── ⑧ 写侧：include 形态 ⇒ 明确 noop 且**不改文件**（fail-closed）────
d="$TMP/write-include"; mkdir -p "$d"
cat > "$d/c.conf" <<'CONF'
upstream ns_llm_gateway_upstream {
    include active-upstream.conf;
}
CONF
before="$(md5 -q "$d/c.conf")"
out="$(python3 "$HELPER" --conf "$d/c.conf" --upstream "$UP" --switch 8783 --backup "$d/c.conf.bak")"
check "⑧ 写：include 形态 ⇒ noop-upstream-uses-include" "noop-upstream-uses-include" "$out"
check "⑧ 写：include 形态下文件未被改" "$before" "$(md5 -q "$d/c.conf")"

# ═══════════════════════════════════════════════════════════════════════
# ⑨ 医生（只读体检）：绑定脱节 ⇒ 退出 2 + 点名占用者
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/doctor"; mkdir -p "$d"
cat > "$d/fx-desync.txt" <<'EOF'
UPSTREAM_PORT=8782
PORT_OWNER=ai-session-manager-asm-1
CONTAINER=llm-gateway-211-a|127.0.0.1:8781->8781/tcp|Up 2 days
CONTAINER=ai-session-manager-asm-1|127.0.0.1:8782->8782/tcp|Up 30 minutes
STATE_SLOT=b
EOF
out="$(bash "$DOCTOR" --facts-file "$d/fx-desync.txt" --slot-a 8781 --slot-b 8783 2>&1)"; rc=$?
check "⑨ 脱节 ⇒ 退出码 2" "2" "$rc"
if grep -q 'ai-session-manager-asm-1' <<<"$out"; then
  ok "⑨ 点名了占用该端口的容器"
else
  bad "⑨ 没点名占用者"
fi
if grep -q '不属于任何配置槽位' <<<"$out"; then
  ok "⑨ 结论说清了「端口不属于任何槽位」"
else
  bad "⑨ 结论没说出脱节"
fi

cat > "$d/fx-healthy.txt" <<'EOF'
UPSTREAM_PORT=8781
PORT_OWNER=llm-gateway-211-a
STATE_SLOT=a
EOF
out="$(bash "$DOCTOR" --facts-file "$d/fx-healthy.txt" --slot-a 8781 --slot-b 8783 2>&1)"; rc=$?
check "⑨ 绑定正常 ⇒ 退出码 0" "0" "$rc"

# ★ 2026-10-24 12h 审计轮（问题④）：**211 事故形态**的门。
#   upstream 指向 8782，而 8782 恰好被配成了 b 槽端口，
#   但**监听它的是另一个产品**（ai-session-manager-asm-1）⇒
#   「端口 ∈ 槽位」成立，可「网关在服务这个端口」**不成立**。
#   ⇒ 原判据会在这里判 OK ⇒ 本门钉住「必须判出来」。
cat > "$d/fx-squat.txt" <<'EOF'
UPSTREAM_PORT=8782
PORT_OWNER=ai-session-manager-asm-1
CONTAINER=llm-gateway-211-a|127.0.0.1:8781->8781/tcp|Up 2 days
CONTAINER=ai-session-manager-asm-1|127.0.0.1:8782->8782/tcp|Up 30 minutes
STATE_SLOT=b
EOF
out="$(bash "$DOCTOR" --facts-file "$d/fx-squat.txt" --slot-a 8781 --slot-b 8782 2>&1)"; rc=$?
check "⑨ 端口落在槽位上但被别的容器占用 ⇒ 退出码 2（不是 OK）" "2" "$rc"
if grep -q '别的容器' <<<"$out"; then
  ok "⑨ 结论说清了「被别的容器占用」"
else
  bad "⑨ 结论没说出被别的容器占用：$out"
fi
if grep -q 'OK（upstream 在槽' <<<"$out"; then
  bad "⑨ 仍被判成 OK（端口∈槽位 的配置自洽性检查冒充了事实检查）"
else
  ok "⑨ 没有把「端口落在槽位上」误判成 OK"
fi

printf 'UPSTREAM_PORT=\nNO_NGINX_CONF=1\n' > "$d/fx-none.txt"
out="$(bash "$DOCTOR" --facts-file "$d/fx-none.txt" --slot-a 8781 --slot-b 8783 2>&1)"; rc=$?
check "⑨ 取不到事实 ⇒ 退出码 3（**不是**「正常」）" "3" "$rc"

# ═══════════════════════════════════════════════════════════════════════
# ⑩ 主机连不上 ⇒ 必须说清「连不上」，且退出码 3（**不是**「conf 读不到」）
#   ★ 用替身 ssh 模拟不可达（DOCTOR_SSH_CMD）：真实连网不可测。
#   ★ 这条钉的是 2026-10-23 实测踩到的坑：采集过程里往 stdout 冒出来的
#     辅助输出（可恢复删除工具的提示行）被当成了「现场事实」⇒
#     「连不上」被误报成「conf 读不到」。
# ═══════════════════════════════════════════════════════════════════════
cat > "$d/fake-ssh-fail" <<'SH'
#!/bin/sh
# 模拟 ssh 连不上（ssh 的失败退出码就是 255）
exit 255
SH
chmod +x "$d/fake-ssh-fail"
out="$(DOCTOR_SSH_CMD="$d/fake-ssh-fail" bash "$DOCTOR" --ssh unreachable-host \
        --slot-a 8781 --slot-b 8783 2>&1)"; rc=$?
check "⑩ 主机不可达 ⇒ 退出码 3" "3" "$rc"
if grep -q '连不上' <<<"$out"; then
  ok "⑩ 明确说了「连不上」（没被误报成 conf 读不到）"
else
  bad "⑩ 连不上被误报成了别的原因：$out"
fi
if grep -q '全新主机' <<<"$out"; then
  bad "⑩ 连不上时却给出了「全新主机？」这种误导结论"
else
  ok "⑩ 连不上时没有给出误导结论"
fi

echo
echo "nginx upstream port: $pass 通过 / $fail 失败"
[[ "$fail" -eq 0 ]]
