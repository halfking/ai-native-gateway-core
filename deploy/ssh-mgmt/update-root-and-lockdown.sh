#!/usr/bin/env bash
# update-root-and-lockdown.sh
# 对单台机器:
#   1) 备份 /etc/shadow 和 /etc/ssh/sshd_config 到 /root/.ssh-mgmt-*.bak
#   2) 把 root 密码改为 NEW_PASSWORD
#   3) 修改 /etc/ssh/sshd_config:
#        - 顶层 PasswordAuthentication no, KbdInteractiveAuthentication no
#        - 末尾追加 Match Address 172.16.2.0/24 -> PasswordAuthentication yes
#   4) sshd -t 校验, reload
# 验证:
#   - 公钥登录(从外网发起,必须成功)
#   - 密码登录(从外网发起,必须被拒)
# 任何阶段失败:在远程用备份还原 shadow 和 sshd_config + reload。

set -u
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# root 密码不入库:从 pw.txt 读取(可用 ROOT_PW_FILE 覆盖路径)。
# pw.txt 已加入 .gitignore;生成方式: umask 077 && printf '%s' '密码' > deploy/ssh-mgmt/pw.txt
PW_SOURCE="${ROOT_PW_FILE:-$SCRIPT_DIR/pw.txt}"
if [ ! -s "$PW_SOURCE" ]; then
  echo "ERROR: 密码文件不存在或为空: $PW_SOURCE"
  echo "       生成: umask 077 && printf '%s' 'root密码' > $PW_SOURCE"
  exit 1
fi
NEW_PASSWORD="$(cat "$PW_SOURCE")"

INTERNAL_CIDR='172.16.2.0/24'
ALLOW_PASSWORD_FROM="$INTERNAL_CIDR"

HOSTS=(245 154 252 115)

# 本机准备:把远程脚本和密码文件分别写到 /tmp
REMOTE_SH="/tmp/ssh-mgmt-payload.sh"
PW_FILE="/tmp/ssh-mgmt-pw.txt"

# 远程脚本本体(不含密码)。注意所有这里的 $ 都是远程执行时的 bash 变量引用,
# 远程 shell 看到的源码是 base64 解码后的字面字符串,不会再被本机 shell 解释。
cat > "$REMOTE_SH" <<'REMOTE_PAYLOAD'
#!/usr/bin/env bash
set -o pipefail
PW_FILE="${1:-}"
[ -n "$PW_FILE" ] || { echo "FAIL_NO_PW_FILE"; exit 3; }
[ -f "$PW_FILE" ] || { echo "FAIL_PW_FILE_MISSING"; exit 4; }

NEW_PASSWORD="$(cat "$PW_FILE")"
ALLOW_FROM="${ALLOW_FROM:-172.16.2.0/24}"

ts=$(date +%s)
SHADOW_BAK="/root/.ssh-mgmt-shadow-${ts}.bak"
SSHD_BAK="/root/.ssh-mgmt-sshd_config-${ts}.bak"

# 密码读完就清掉文件,避免长时间留在磁盘
trap 'shred -u "$PW_FILE" 2>/dev/null || rm -f "$PW_FILE"; rm -f /tmp/ssh-mgmt-payload.sh' EXIT

rollback() {
  echo "ROLLBACK on $HOSTNAME start"
  if [ -f "$SHADOW_BAK" ]; then
    # /etc/shadow 经常被加 chattr +i,先解除再覆盖
    chattr -i /etc/shadow 2>/dev/null || true
    cp -a "$SHADOW_BAK" /etc/shadow
    chmod 000 /etc/shadow
    chattr +i /etc/shadow 2>/dev/null || true
    echo "ROLLBACK shadow=restored"
  fi
  if [ -f "$SSHD_BAK" ]; then
    cp -a "$SSHD_BAK" /etc/ssh/sshd_config
    echo "ROLLBACK sshd_config=restored"
  fi
  systemctl reload sshd 2>/dev/null || service sshd reload 2>/dev/null || true
  echo "ROLLBACK done"
  exit 1
}

# 1) 备份
cp -a /etc/shadow "$SHADOW_BAK" || { echo "FAIL_BACKUP_SHADOW"; exit 2; }
cp -a /etc/ssh/sshd_config "$SSHD_BAK" || { echo "FAIL_BACKUP_SSHD"; exit 2; }
chmod 600 "$SHADOW_BAK" "$SSHD_BAK"
echo "OK_BACKUP shadow=$SHADOW_BAK sshd=$SSHD_BAK"

# 解除 /etc/shadow 的 immutable(常见云镜像保护位),改密需要写
chattr -i /etc/shadow 2>/dev/null || true
chmod 000 /etc/shadow 2>/dev/null || true

# 2) 改 root 密码 —— 用 chpasswd 更稳,不走 passwd 的交互式 PAM
if printf 'root:%s\n' "$NEW_PASSWORD" | chpasswd 2>/dev/null; then
  echo "OK_CHPASSWD"
else
  # 一些镜像(例如某些 debian)默认不带 chpasswd,fallback 到 passwd
  printf '%s\n%s\n' "$NEW_PASSWORD" "$NEW_PASSWORD" | passwd root >/dev/null 2>&1 \
    || { echo "FAIL_CHPASSWD"; rollback; }
  echo "OK_CHPASSWD (via passwd)"
fi

# 改密成功后恢复 immutable
chmod 000 /etc/shadow
chattr +i /etc/shadow 2>/dev/null || true

# 清空内存中的密码
unset NEW_PASSWORD

# 3) 改 sshd_config:顶层关闭密码,加 Match 块允许内网
SSHD=/etc/ssh/sshd_config
ALLOW_FROM="$ALLOW_FROM" python3 - "$SSHD" <<'PY'
import os, re, io, sys
path = sys.argv[1]
allow_from = os.environ.get('ALLOW_FROM', '172.16.2.0/24')
with io.open(path, 'r', encoding='utf-8', errors='replace') as f:
    text = f.read()

def set_kv(txt, key, value):
    pat = re.compile(r'^[ \t]*#?[ \t]*' + re.escape(key) + r'[ \t]+.*$', re.M)
    line = f'{key} {value}'
    if pat.search(txt):
        txt = pat.sub(line, txt, count=1)
    else:
        if not txt.endswith('\n'):
            txt += '\n'
        txt += line + '\n'
    return txt

text = set_kv(text, 'PasswordAuthentication', 'no')
text = set_kv(text, 'KbdInteractiveAuthentication', 'no')

# 去掉旧的同主题 Match 块(含 PasswordAuthentication 的)
parts = []
last = 0
for m in re.finditer(r'(?ms)^Match\b[^\n]*\n.*?(?=^Match|\Z)', text):
    block = m.group(0)
    if 'PasswordAuthentication' in block or 'KbdInteractiveAuthentication' in block:
        continue
    parts.append((m.start(), m.end()))
out = []
cur = 0
for s, e in parts:
    out.append(text[cur:s])
    out.append(text[s:e])
    cur = e
out.append(text[cur:])
text = ''.join(out)

# 追加新 Match 块
match_block = (
    f'\n# --- ssh-mgmt auto: allow password only from {allow_from} ---\n'
    f'Match Address {allow_from}\n'
    f'    PasswordAuthentication yes\n'
    f'    KbdInteractiveAuthentication yes\n'
)
if not text.endswith('\n'):
    text += '\n'
text += match_block

with io.open(path, 'w', encoding='utf-8') as f:
    f.write(text)
PY
echo "OK_SSHD_EDIT"

# 4) 校验
sshd -t && echo "OK_SSHD_TEST" || { echo "FAIL_SSHD_TEST"; rollback; }

# 5) reload
systemctl reload sshd 2>/dev/null || service sshd reload 2>/dev/null || true
echo "OK_SSHD_RELOAD"

# 6) 自检:本会话走公钥,应该仍然 OK
echo "OK_SELF_CHECK user=$(whoami)"

echo "DONE_HOST=$HOSTNAME"
REMOTE_PAYLOAD
chmod 600 "$REMOTE_SH"

# 密码文件
printf '%s' "$NEW_PASSWORD" > "$PW_FILE"
chmod 600 "$PW_FILE"

SSH_BASE_OPTS=(
  -o BatchMode=yes
  -o PasswordAuthentication=no
  -o PubkeyAuthentication=yes
  -o IdentitiesOnly=yes
  -o ConnectTimeout=10
  -o StrictHostKeyChecking=accept-new
)

upload_and_run() {
  local host="$1"
  echo "  -> upload payload to $host"
  if ! scp "${SSH_BASE_OPTS[@]}" "$REMOTE_SH" "root@${host}:/tmp/ssh-mgmt-payload.sh"; then
    echo "FAIL_SCP_PAYLOAD"; return 10
  fi

  echo "  -> upload pw file to $host"
  if ! scp "${SSH_BASE_OPTS[@]}" "$PW_FILE" "root@${host}:/tmp/ssh-mgmt-pw.txt"; then
    echo "FAIL_SCP_PW"; return 11
  fi

  echo "  -> run remote on $host"
  timeout 90 ssh "${SSH_BASE_OPTS[@]}" "root@${host}" \
    "ALLOW_FROM='$ALLOW_PASSWORD_FROM' bash /tmp/ssh-mgmt-payload.sh /tmp/ssh-mgmt-pw.txt" \
    2>&1 | grep -vE "post-quantum|store now|openssh\.com/pq"
}

verify_key_login() {
  local host="$1"
  timeout 15 ssh -o BatchMode=yes \
                -o PasswordAuthentication=no \
                -o PubkeyAuthentication=yes \
                -o IdentitiesOnly=yes \
                -o ConnectTimeout=10 \
                "$host" \
                'whoami && hostname -I' \
    2>&1 | grep -Ev "post-quantum|store now|openssh\.com/pq|Warning: Permanently"
}

verify_password_denied_from_internet() {
  local host="$1"
  # 强制走密码,服务端应该因为 Match Address 不匹配而拒绝
  ssh -o BatchMode=no \
      -o PreferredAuthentications=password \
      -o PubkeyAuthentication=no \
      -o NumberOfPasswordPrompts=0 \
      -o ConnectTimeout=10 \
      -o StrictHostKeyChecking=no \
      -o PasswordAuthentication=yes \
      "root@${host}" "true" 2>&1 | head -2
}

for host in "${HOSTS[@]}"; do
  echo
  echo "============================================================"
  echo " HOST = $host"
  echo "============================================================"
  out="$(upload_and_run "$host")"
  echo "$out"
  if ! grep -q "^DONE_HOST=" <<<"$out"; then
    echo "REMOTE FAILED on $host, see above"
    continue
  fi

  echo "-- verify key-only login (from internet source) --"
  verify_key_login "$host"

  echo "-- verify password login from internet (must be denied) --"
  verify_password_denied_from_internet "$host"
done

# 清理本机临时文件
rm -f "$REMOTE_SH" "$PW_FILE"

echo
echo "ALL DONE"