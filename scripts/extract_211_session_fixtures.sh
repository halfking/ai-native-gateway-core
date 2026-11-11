#!/usr/bin/env bash
# 从 211 生产库提取真实会话 → 回放测试夹具（2026-11-11 审计轮）
#
# ── 数据来源 ───────────────────────────────────────────────────────────
# 211 的网关把会话体落在 PostgreSQL（acc-pg17 容器）的 session_bodies：
#   request_delta jsonb —— 客户端**当轮**的消息数组（增量）
#   outbound_body jsonb —— 网关**出站**给上游的消息数组（累积全量）
#   request_id / session_id / turn_no —— 与 request_logs_with_current_month
#       关联可取到 client_protocol（客户端协议）。
#
# ⚠️ 网络注意：`acc-pg17` 是**同宿主 docker 容器**，宿主的 DNS 把它解析到
#  公网名（acc-pg17.home.itestu.cn → 115.29.212.252），从宿主连会超时。
#   ⇒ 正确做法：psql **在 postgres 容器内执行**（docker exec）。
#
# ── 夹具形态 ───────────────────────────────────────────────────────────
# 每行一个 JSON：
#   {session_id, turn_no, client_protocol, model,
#    client_messages:[...],   ← request_delta（客户端形态）
#    egress_messages:[...]}   ← outbound_body（网关实际出站形态）
#
# ── 脱敏（默认开启）────────────────────────────────────────────────────
# 真实生产会话含用户内容（源码片段、对话、路径…）。夹具只用于验证**转换
# 语义**，转换器不关心字面内容 ⇒ 默认把文本替换为等长占位标记，保留：
#   · 消息角色与顺序
#   · content 块类型与嵌套结构
#   · tool 名 / tool_call_id / tool_use_id
#   · 字符串长度（占位符等长 ⇒ 体量分布不变）
# 去掉：字面文本、base64 载荷、URL/路径。
#
# 用法：
#   bash scripts/extract_211_session_fixtures.sh            # 脱敏（默认）
#   bash scripts/extract_211_session_fixtures.sh --raw     # 不脱敏（本地排查用）
#   bash scripts/extract_211_session_fixtures.sh --limit 40

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

OUT_DIR="internal/ir/testdata/production_sessions"
SSH_HOST="${SSH_HOST:-211}"
PG_CONTAINER="${PG_CONTAINER:-acc-pg17}"
PG_USER="${PG_USER:-acc_super}"
PG_DB="${PG_DB:-llm_gateway}"

LIMIT=60
REDACT=1
VARIETY=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --raw)    REDACT=0; shift ;;
        --limit)  LIMIT="$2"; shift 2 ;;
        # 2026-11-12（审计第二轮批 4）：--variety 按（协议 × 会话形态）轮转取样
        # —— 形态桶：role_violation / thinking / multimodal / long_conv /
        # tool_heavy / general。每桶配额 = LIMIT/5，总样本 ≈ 3 协议 × 6 桶 × 配额。
        --variety) VARIETY=1; shift ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

mkdir -p "$OUT_DIR"
# ⚠️ mktemp 的 XXXXXX 必须位于模板**末尾**（BSD/macOS 与 GNU 皆如此），
#    写成 .XXXXXX.sql 会被当成字面量而失败。
TMP_SQL=$(mktemp /tmp/ir-fixture-sql.XXXXXX)
TMP_OUT=$(mktemp /tmp/ir-fixture-out.XXXXXX)
cleanup() { rm -f "$TMP_SQL"; }
trap cleanup EXIT

# ── SQL：挑选「有多轮、跨协议、体量适中」的会话轮次 ──────────────────────
# 挑选口径（登记在案）：
#   ① request_delta 是数组（可解析）；outbound_body **可选**（见下）
#   ② outbound_body 体量 1KB..200KB（太小无信息量，太大不适合入库）
#   ③ 每个会话取**最后一轮**（累积最全 ⇒ 覆盖最长的上下文形态）
#   ④ 按 client_protocol **轮转**取样，保证协议多样性
#
# ⚠️ 实测口径（2026-11-11）：outbound_body（出站黄金样本）**只有 openai-chat
#    有**（34188 行）；anthropic-messages 有 45 行客户端样本但 0 行出站体；
#    openai-responses 同理无出站体。
#    ⇒ 出站体缺失的样本仍可用：它们验证「客户端形态 → IR → 目标协议」的
#      正确性（不与生产出站对拍）。
# ── 轮转策略 ─────────────────────────────────────────────────────────────
# 默认（round-1 口径）：按协议轮转（pr），每协议取 LIMIT 条。
# --variety（批 4）：按（协议 × 形态）轮转，每桶配额 = LIMIT/5 ⇒ 总样本
# ≈ 3 协议 × 6 桶 × 配额（≈LIMIT×3.6）。形态桶保证夹具覆盖：role 违规
# （智能体会话形态）/ thinking 块 / 多模态 / 长会话 / 工具密集。
if [[ "$VARIETY" == "1" ]]; then
  SHAPE_QUOTA=$(( LIMIT / 5 ))
  ROTATED_SQL="rotated AS (
  SELECT r.*,
         row_number() OVER (PARTITION BY r.client_protocol, r.shape
                            ORDER BY r.pr, r.session_id) AS rr
  FROM ranked r
  WHERE r.pr <= ${LIMIT}
)"
  FINAL_WHERE="c.rr <= ${SHAPE_QUOTA}"
  FINAL_ORDER="c.client_protocol, c.shape, c.rr"
else
  ROTATED_SQL="rotated AS (
  SELECT r.*,
         row_number() OVER (ORDER BY r.pr, r.client_protocol) AS rr
  FROM ranked r
  WHERE r.pr <= ${LIMIT}
)"
  FINAL_WHERE="c.rr <= ${LIMIT}"
  FINAL_ORDER="c.rr"
fi

cat > "$TMP_SQL" <<SQL
\\pset pager off
\\pset format unaligned
\\pset tuples_only on
WITH cand AS (
  SELECT b.session_id,
         b.turn_no,
         b.request_id,
         b.request_delta,
         b.outbound_body,
         l.client_protocol,
         coalesce(l.outbound_model, l.client_model) AS model,
         row_number() OVER (PARTITION BY b.session_id ORDER BY b.turn_no DESC) AS rn
  FROM session_bodies b
  JOIN request_logs_with_current_month l ON l.request_id = b.request_id
  WHERE jsonb_typeof(b.request_delta) = 'array'
    AND l.client_protocol IS NOT NULL
),
last_turn AS (
  SELECT * FROM cand WHERE rn = 1
),
eligible AS (
  SELECT t.*,
         (jsonb_typeof(t.outbound_body) = 'array'
          AND octet_length(t.outbound_body::text) BETWEEN 1024 AND 200000) AS has_egress
  FROM last_turn t
  -- ⚠️ 出站体**可选**：anthropic-messages / openai-responses 的 outbound_body
  --    不是数组（实测 anthropic 45 行客户端样本、0 行数组出站体）⇒ 不能用
  --    「出站体是数组」做硬过滤，否则这些协议的样本全被滤掉（轮转失效）。
  WHERE t.outbound_body IS NULL
     OR jsonb_typeof(t.outbound_body) <> 'array'
     OR octet_length(t.outbound_body::text) BETWEEN 1024 AND 200000
),
shaped AS (
  -- 2026-11-12（批 4）：会话形态分类（--variety 轮转用；非 variety 模式
  -- 不影响取样，只是多算几列）。
  --   role_violation：消息序列存在相邻同 role（客户端智能体会话形态）
  --   thinking      ：含 thinking / redacted_thinking 块或 reasoning_content
  --   multimodal    ：含 image / audio / file 载荷
  --   long_conv     ：≥ 40 条消息
  --   tool_heavy    ：≥ 6 条 tool 结果消息
  --   general       ：以上皆非
  SELECT r.*,
         CASE
           WHEN rv.role_violations > 0 THEN 'role_violation'
           WHEN r.request_delta::text LIKE '%"type":"thinking"%'
             OR r.request_delta::text LIKE '%"type":"redacted_thinking"%'
             OR r.request_delta::text LIKE '%reasoning_content%' THEN 'thinking'
           WHEN r.request_delta::text LIKE '%"type":"image_url"%'
             OR r.request_delta::text LIKE '%"type":"image"%'
             OR r.request_delta::text LIKE '%inline_data%'
             OR r.request_delta::text LIKE '%"type":"input_audio"%' THEN 'multimodal'
           WHEN jsonb_array_length(r.request_delta) >= 40 THEN 'long_conv'
           WHEN tm.tool_msgs >= 6 THEN 'tool_heavy'
           ELSE 'general'
         END AS shape
  FROM eligible r
  CROSS JOIN LATERAL (
    SELECT count(*) FILTER (WHERE prev = cur) AS role_violations
    FROM (
      SELECT el->>'role' AS cur,
             lag(el->>'role') OVER (ORDER BY ord) AS prev
      FROM jsonb_array_elements(r.request_delta) WITH ORDINALITY AS a(el, ord)
    ) seq
  ) rv
  CROSS JOIN LATERAL (
    SELECT count(*) AS tool_msgs
    FROM jsonb_array_elements(r.request_delta) AS el
    WHERE el->>'role' = 'tool'
  ) tm
),
ranked AS (
  SELECT e.*,
         -- 优先挑「可与生产出站对拍」的轮次：出站体存在 且 消息数相等。
         -- （本轮实测：客户端当轮消息数与出站条数相等的样本才能逐条对拍；
         --   不相等的是因为该轮客户端只发了部分上下文 / 网关做了会话补齐。）
         row_number() OVER (PARTITION BY e.client_protocol
                            ORDER BY e.has_egress DESC,
                                     (e.outbound_body IS NOT NULL
                                      AND jsonb_typeof(e.outbound_body) = 'array'
                                      AND jsonb_array_length(e.outbound_body)
                                          = jsonb_array_length(e.request_delta)) DESC,
                                     e.session_id) AS pr
  FROM shaped e
),
${ROTATED_SQL}
SELECT jsonb_build_object(
         'session_id',   c.session_id,
         'turn_no',      c.turn_no,
         'client_protocol', c.client_protocol,
         'model',        coalesce(c.model, ''),
         'client_messages', c.request_delta,
         'egress_messages', CASE
           WHEN c.has_egress THEN c.outbound_body
           ELSE NULL
         END
       )::text
FROM rotated c
WHERE ${FINAL_WHERE}
ORDER BY ${FINAL_ORDER};
SQL

echo "▸ 在 ${SSH_HOST} 上执行提取（容器 ${PG_CONTAINER}）..."
if ! scp -q "$TMP_SQL" "$SSH_HOST:/tmp/ir-fixture-sql.sql"; then
  echo "✗ scp 失败：无法把 SQL 送到 ${SSH_HOST}" >&2
  exit 1
fi

# 在 postgres 容器内跑 psql（宿主直连 acc-pg17 会超时，见文件头说明）
ssh -o BatchMode=yes "$SSH_HOST" "
  sudo -n docker cp /tmp/ir-fixture-sql.sql ${PG_CONTAINER}:/tmp/ir-fixture-sql.sql >/dev/null &&
  sudo -n docker exec ${PG_CONTAINER} psql -U ${PG_USER} -d ${PG_DB} -f /tmp/ir-fixture-sql.sql
" > "$TMP_OUT"

LINES=$(wc -l < "$TMP_OUT" | tr -d ' ')
if [[ "$LINES" -lt 1 ]]; then
  echo "✗ 未取到任何样本（SQL 无输出）" >&2
  exit 1
fi
echo "▸ 取到 $LINES 条原始样本"

# ── 脱敏（python 就地处理）─────────────────────────────────────────────
OUT_FILE="$OUT_DIR/sessions.jsonl"
python3 - "$TMP_OUT" "$OUT_FILE" "$REDACT" <<'PY'
import json, sys

src, dst, redact = sys.argv[1], sys.argv[2], sys.argv[3] == "1"

# ── 脱敏规则（2026-11-11 第一版有严重缺陷，已修正）─────────────────────
# 第一版把「所有裸字符串」都 mask 了 ⇒ role / type / tool 名 等**结构判别值**
# 也被打码 ⇒ 夹具失去了测试价值（转换语义恰恰体现在这些值上）。
#
# 修正后的规则：
#   ① **只** mask 载荷键（payload keys）下的字符串值；
#   ② 结构键（role / type / name / id / tool_call_id …）**原样保留** ——
#      它们是转换语义的载体，且本身不含用户内容；
#   ③ 载荷文本**截断**到 MAX_TEXT 字符（而非等长）⇒ 夹具体量可控
#      （真实会话单条可达 3.6MB，等长 mask 会让仓库夹具爆到 57MB）。
#
# 载荷键（值被打码）：用户文本 / 代码 / 推理 / 参数 / 载荷 / 标识内容
PAYLOAD_KEYS = {
    # ⚠️ content 必须在载荷键里（OpenAI 的 content 是**裸字符串**，不在此列
    #    ⇒ 用户文本原样入库，夹具从 6MB 降到 ~1MB）。列表形态由
    #    mask_payload 处理 ⇒ 块的 type/name 判别值仍被保留。
    "content",
    "text", "reasoning_content", "refusal", "arguments", "data", "signature",
    "thinking", "output_text", "output", "instructions", "prompt", "system",
    "input", "url", "image_url", "file_path", "path", "filename", "email",
}
# 结构键（原样保留）：判别值与标识，转换测试要断的就是它们
STRUCTURAL_KEYS = {
    "role", "type", "name", "id", "tool_call_id", "tool_use_id", "index",
    "finish_reason", "stop_reason", "format", "tool_use", "index_json",
}
MAX_TEXT = 128
MAX_MESSAGES = 60


def mask_text(s):
    """载荷文本 → 截断的占位符（去掉字面内容，保留类型与体量感）。"""
    if not isinstance(s, str) or len(s) == 0:
        return s
    n = min(len(s), MAX_TEXT)
    return "�" * n


def mask_node(node):
    """递归打码载荷值；保留结构键的判别值。"""
    if isinstance(node, dict):
        out = {}
        for k, v in node.items():
            if k in STRUCTURAL_KEYS:
                out[k] = mask_node(v)          # 结构键：值本身不打码
            elif k in PAYLOAD_KEYS:
                out[k] = mask_payload(v)
            else:
                out[k] = mask_node(v)
        return out
    if isinstance(node, list):
        return [mask_node(x) for x in node]
    return node          # ⚠️ 裸字符串叶子**不**打码（第一版就是错在这）


def mask_payload(v):
    """载荷键的值：字符串打码；列表/字典逐项处理（保留块类型）。"""
    if isinstance(v, str):
        return mask_text(v)
    if isinstance(v, list):
        return [mask_payload(x) if isinstance(x, str) else mask_node(x) for x in v]
    if isinstance(v, dict):
        return mask_node(v)
    return v


def cap_messages(msgs):
    """超长会话只保留尾部 MAX_MESSAGES 条（上下文越长越难处理 ⇒ 取最难的）。"""
    if not isinstance(msgs, list):
        return msgs
    if len(msgs) <= MAX_MESSAGES:
        return msgs
    return msgs[-MAX_MESSAGES:]


kept = 0
by_proto = {}
skipped_big = 0
with open(src, encoding="utf-8") as fh, open(dst, "w", encoding="utf-8") as out:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if redact:
            rec["client_messages"] = cap_messages(mask_node(rec.get("client_messages")))
            rec["egress_messages"] = cap_messages(mask_node(rec.get("egress_messages")))
            rec["model"] = "redacted-model"
            rec["session_id"] = "redacted-session"
        else:
            rec["client_messages"] = cap_messages(rec.get("client_messages"))
            rec["egress_messages"] = cap_messages(rec.get("egress_messages"))
        # 单条夹具体量上限：超了就丢（避免仓库夹具爆量）
        if len(json.dumps(rec, ensure_ascii=False)) > 400_000:
            skipped_big += 1
            continue
        proto = rec.get("client_protocol", "?")
        by_proto[proto] = by_proto.get(proto, 0) + 1
        out.write(json.dumps(rec, ensure_ascii=False, sort_keys=True) + "\n")
        kept += 1

print(f"▸ 写入 {kept} 条夹具 → {dst}")
for p, n in sorted(by_proto.items()):
    print(f"    {p}: {n}")
PY

echo "✓ 完成：$OUT_FILE"
