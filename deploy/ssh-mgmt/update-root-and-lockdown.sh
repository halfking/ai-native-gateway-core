#!/usr/bin/env bash
# update-root-and-lockdown.sh
# 对单台机器:
#   1) 备份 /etc/shadow 和 /etc/ssh/sshd_config 到 /root/.ssh-mgmt-*.bak
#   2) 把 root 密码改为 NEW_PASSWORD
#   3) 修改 /etc/ssh/sshd_config:
#        - 顶层 PasswordAuthentication no, KbdInteractiveAuthentication no
#        - 末尾追加 Match Address 172.16.2.0/24 -> PasswordAuthentication yes
#   4) sshd -t 校验, reload
#   5) 生效配置闸: sshd -T -C addr=<外网IP> 渲染最终生效配置, 必须
#      PasswordAuthentication no 才算成功 —— 该检查天然覆盖
#      /etc/ssh/sshd_config.d/*.conf Include drop-in 的 first-match-wins
#      (只改主文件时 drop-in 里的 PasswordAuthentication yes 会压过本脚本
#      的 no, 且 sshd -t 语法校验发现不了)。
# 验证:
#   - 远端生效配置断言(权威): OK_EFFECTIVE_PASSWORDAUTH_NO
#   - 公钥登录(从操作机发起, 真实退出码, 失败计入总失败)
#   - 密码登录(从操作机发起, 仅信息性; 权威判定以上一条生效配置闸为准)
# 任何阶段失败:在远程用备份还原 shadow 和 sshd_config + reload。
# 防锁死前置闸:/root/.ssh/authorized_keys 必须已有非注释密钥行,
# 否则改密照常、禁密码直接回滚 —— 对"只密码登录"的机器禁外网密码=锁死。

set -u
set -o pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# root 密码不入库:从 pw.txt 读取(可用 ROOT_PW_FILE 覆盖路径)。
# pw.txt 已加入 .gitignore;生成方式(密码学随机、不进 shell history):
#   umask 077 && openssl rand -base64 24 > deploy/ssh-mgmt/pw.txt
PW_SOURCE="${ROOT_PW_FILE:-$SCRIPT_DIR/pw.txt}"
if [ ! -s "$PW_SOURCE" ]; then
  echo "ERROR: 密码文件不存在或为空: $PW_SOURCE"
  echo "       生成: umask 077 && openssl rand -base64 24 > $PW_SOURCE"
  exit 1
fi
NEW_PASSWORD="$(cat "$PW_SOURCE")"

INTERNAL_CIDR='172.16.2.0/24'
ALLOW_PASSWORD_FROM="$INTERNAL_CIDR"

HOSTS=(245 154 252 115)

# 本机准备:把远程脚本和密码文件分别写到 /tmp —— mktemp 随机名 + umask 077,
# 杜绝固定名 symlink/TOCTOU 与 644 明文窗口。
REMOTE_SH="$(mktemp /tmp/ssh-mgmt-payload.XXXXXX)"
PW_FILE="$(mktemp /tmp/ssh-mgmt-pw.XXXXXX)"
trap 'rm -f "$REMOTE_SH" "$PW_FILE"' EXIT

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

# 1.5) 防锁死前置闸:authorized_keys 必须已有非注释密钥行。
# 对"一直密码登录、未配密钥"的机器禁外网密码 = 外网入口锁死,只剩 VNC 救援。
if [ ! -s /root/.ssh/authorized_keys ] \
   || ! grep -qvE '^[[:space:]]*(#|$)' /root/.ssh/authorized_keys; then
  echo "FAIL_NO_AUTHORIZED_KEY (refusing to disable password auth; seed a key first)"
  rollback
fi
echo "OK_AUTHORIZED_KEY_PRESENT"

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

# 5) reload —— 失败必须回滚,严禁吞掉:Debian/Ubuntu 单元名是 ssh(CentOS 系
# 是 sshd),四路都试;全失败说明新配置未生效,报告成功就是假成功。
if ! { systemctl reload sshd 2>/dev/null || systemctl reload ssh 2>/dev/null \
       || service sshd reload 2>/dev/null || service ssh reload 2>/dev/null; }; then
  echo "FAIL_SSHD_RELOAD"
  rollback
fi
echo "OK_SSHD_RELOAD"

# 5.5) 生效配置闸(权威):以外网地址上下文渲染 sshd 最终生效配置,必须
# PasswordAuthentication no。203.0.113.9 是 TEST-NET-3 文档地址,必不落在
# 内网白名单内。该检查覆盖 Include drop-in 的 first-match-wins —— 只改主
# 文件时 sshd_config.d/*.conf 里的 PasswordAuthentication yes 会压过本脚本
# 的 no,sshd -t 与文件检查都发现不了。
EFF_PW="$(sshd -T -C user=root,host=203.0.113.9,addr=203.0.113.9 2>/dev/null \
          | awk 'tolower($1)=="passwordauthentication"{print tolower($2); exit}')"
if [ "$EFF_PW" != "no" ]; then
  echo "FAIL_EFFECTIVE_PW_NOT_NO val=${EFF_PW:-<empty>} (drop-in Include overrides? see /etc/ssh/sshd_config.d/)"
  rollback
fi
echo "OK_EFFECTIVE_PASSWORDAUTH_NO"

# 6) 自检:本会话走公钥,应该仍然 OK(信息性 —— 真实验证是操作机侧
# verify_key_login 的退出码,见主脚本)。
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
  # 真实退出码:ssh 的,不是管道末端 grep 的。禁密码后密钥登不上 = 锁死前兆。
  return "${PIPESTATUS[0]}"
}

verify_password_denied_from_internet() {
  local host="$1"
  # 信息性:NumberOfPasswordPrompts=0 时客户端根本不出示密码,无论服务端
  # 是否放行都必然 Permission denied —— 本函数的输出不能作为判定依据。
  # 权威判定是远端 OK_EFFECTIVE_PASSWORDAUTH_NO 生效配置闸。
  ssh -o BatchMode=no \
      -o PreferredAuthentications=password \
      -o PubkeyAuthentication=no \
      -o NumberOfPasswordPrompts=0 \
      -o ConnectTimeout=10 \
      -o StrictHostKeyChecking=no \
      -o PasswordAuthentication=yes \
      "root@${host}" "true" 2>&1 | head -2
}

FAILED_HOSTS=""
for host in "${HOSTS[@]}"; do
  echo
  echo "============================================================"
  echo " HOST = $host"
  echo "============================================================"
  out="$(upload_and_run "$host")"
  echo "$out"
  if ! grep -q "^DONE_HOST=" <<<"$out"; then
    echo "REMOTE FAILED on $host, see above"
    FAILED_HOSTS="$FAILED_HOSTS $host"
    continue
  fi
  if ! grep -q "^OK_EFFECTIVE_PASSWORDAUTH_NO$" <<<"$out"; then
    echo "EFFECTIVE-CONFIG GATE NOT PASSED on $host"
    FAILED_HOSTS="$FAILED_HOSTS $host"
    continue
  fi

  echo "-- verify key-only login (from internet source) --"
  if ! verify_key_login "$host"; then
    echo "VERIFY_FAIL: key login broken on $host — restore access before disconnecting!"
    FAILED_HOSTS="$FAILED_HOSTS $host"
  fi

  echo "-- verify password login from internet (informational only) --"
  verify_password_denied_from_internet "$host"
done

echo
if [ -n "$FAILED_HOSTS" ]; then
  echo "COMPLETED WITH FAILURES on:$FAILED_HOSTS"
  exit 1
fi
echo "ALL DONE"