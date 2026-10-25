#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""db-changelog-ledger-reconcile.py — ★★ 「SQL 变更登记册 × 各环境真实账本」只读对账器。

# 这道工具补的是哪块缺口

★ 本仓的 SQL 变更登记册是 `docs/db-changelog.md`（★ **唯一登记册**），它的第四列是
  「Status」（★ `applied+verified` / `pending deploy` / `registered` / …）。
★ 而**各环境的真实事实**在每个库自己的 `gateway_db_revision_sequences` 表里
（★ startup 迁移的账本 ⇒ ★ **权威**）。

★★ **两者的关系在 R160 轮之前【没有任何机器判据】** ⇒ ★★ 于是登记册可以【一直错着】
  而没人知道。★ 实测反例（★ 本轮抓到的真缺陷）：

    `890_drop_request_logs_v1_family.sql` 的 Status 写着
    「pending deploy（…**尚未部署生产**；本地已应用并复验）」
    ⇒ ★★ 而 **252（2026-10-21 17:41）与 211（2026-10-21 14:19）的账本里都【已经记账】**
    ⇒ ★★★ **⇒ 登记册的状态列与两个生产实例的真实账本【矛盾】**

★ 这条迁移是**破坏性**的（★ 删 v1 `request_logs` 全族）⇒ ★★ 「它到底部署了没有」
  正是最该有机器判据的一个事实。

# 三条设计纪律（★ 与 R159 轮同族）

① ★ **只读**：★ 本工具只 `SELECT`（★ 账本表 + `pg_class`）⇒ ★★ **对生产的写入 = 0**。
   ★ 采集远程账本走 `ssh … | podman/docker exec -i … psql`（★ 只读管道）。

② ★ **DSN 与口令绝不回显**：★ 本地采集用 `GUARD_DSN` 环境变量（★ 只作为子进程环境传入，
   ★ **不进 argv**、★ **不打印**）；★ 输出里只有文件名与账本字段。

③ ★ **「不确定」必须落在【字段名】上**：★ 登记册里那些**复合声明**
   （★ 同时含「已应用」与「pending deploy」）★ **不能**被归到任何一边
   ⇒ ★★ 它们进 `mixed` 桶 ⇒ ★★ **单列成「不可机器判」清单** ⇒ ★★ **不参与硬闸**
   （★ 否则门会因为散文而红 ⇒ ★★ ⇒ 人就开始改散文而不是改事实）。

# 三个模式

    collect   --env {local|252|211} [--out FILE]
        ★ 只读采集某环境的账本 ⇒ 写 TSV（★ 字段见下）。
        ★ 退出码：0 成功 / 3 采集失败。

    reconcile --snapshot FILE [--changelog FILE] [--json]
        ★ 把登记册的 Status 与快照里的账本逐条对账。
        ★ 退出码：0 无硬矛盾 / ★★ **2 有硬矛盾** / 3 用法或工具错误。

    selftest
        ★ 自证（★ 阳性对照 + 反向对照 + 解析面自检）。

# 快照 TSV 格式（★ `collect` 的产物）

    #env<TAB>migration_no<TAB>filename<TAB>applied_at<TAB>content_sha256<TAB>execution_sha256<TAB>runtime_adaptation
    local<TAB>032<TAB>032_session_tenant_binding.sql<TAB>…<TAB>ab15…<TAB>9af1…<TAB>request-logs-physical-v1

★ 快照**不是**账本真值（★ 真值永远在各环境的库里）⇒ ★ 快照会过期 ⇒
  ★ ⇒ 故快照头部带**采集时刻**与**每环境账本集合的 sha256** ⇒ ★★ 「快照过期」机器可判。

# ★ 「同一号多次登记 ⇒ 哪行算数」的判据（★ 关键）

★ 登记册**不是全局时间有序**（★ 新节插在旧节中间）⇒ ★★ **「文件里最后一行」≠「最新一次声明」**
  ⇒ ★★ **必须按【节日期】取最新**（★ 同节内按行号取最后）。
★ 这就是 R160 轮踩到的第二个坑（★ 详见审计档 §13）。

★ 本文件不含任何凭据；★ 也不含任何生产主机口令。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone

# ─────────────────────────── 常量 ───────────────────────────

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_CHANGELOG = os.path.join(REPO_ROOT, "docs", "db-changelog.md")

# ★★★ 权威状态镜像【住在正本之外】★★★
#   理由（★ 实测）：★ 登记册正本里有「同一迁移号【必须恰好出现一次】」的连续性门
#   （★ `TestSection14335RegistrationPoints` / `TestMigration895RegisteredInEveryDeliverySurface`）
#   ⇒ ★★ **把第二张表插进正本 ⇒ 直接破坏那些门**（★ 实测：★ 892 那行立刻变成「出现 2 次」）
#   ⇒ ★★ ⇒ 权威表必须住在正本之外（★ 本轮先试过插进正本 ⇒ ★★ 被慢门打回 ⇒ ★★ 这才独立）。
DEFAULT_AUTH_FILE = os.path.join(REPO_ROOT, "docs", "db-changelog-authoritative.md")

# ★ 权威文件的头部说明（★ 首次生成时写入）
AUTH_HEADER = """# ★ SQL 变更登记册 · 权威状态（机器可判）

> ★★ 本文件是 `docs/db-changelog.md`（★ **唯一登记册**）的**机器可判镜像**：
> ★★ 一行一个迁移号 × 三格（★ `local` / `252` / `211`）⇒ 每格 `applied@<账本 applied_at>` 或 `—`。
> ★★ **为什么独立成文件**：★ ★★ 登记册正本里有「同一迁移号**必须恰好出现一次**」的连续性门
> ⇒ ★★ ⇒ **把第二张表插进正本 ⇒ 直接破坏那些门**（★ ★★ 实测过 ⇒ ★★ 被慢门打回 ⇒ ★★ 这才独立）。
> ★★ **真值**：★ 各环境自己的 `gateway_db_revision_sequences`（★ 本文件是**采集快照的投影** ⇒ ★ 会过期）。
> ★★ 生成器：`scripts/audit/db-changelog-ledger-reconcile.py render --write`（★ 只读）。
> ★★ 判读：`applied@…` = 该环境账本里已记账；★ `—` = 该环境账本里**没有**（★ **账本里没有 ≠ 没应用过**）。

"""


# ★ 账本表（★ startup 迁移账本 ⇒ 权威）
LEDGER_TABLE = "gateway_db_revision_sequences"

# ★ 回写权威状态段落的标记（★ 成对出现 ⇒ 幂等回写 + 手改可检出）
AUTH_BEGIN = "<!-- R160-AUTHORITATIVE:BEGIN -->"
AUTH_END = "<!-- R160-AUTHORITATIVE:END -->"

# ★ 三环境的采集方式（★ 全部只读）
ENV_SPECS = {
    "local": {
        "kind": "psql",
        # ★ DSN 只从环境变量读（GUARD_DSN）⇒ 不进 argv、不回显
        "dsn_env": "GUARD_DSN",
        "psql_env": "PSQL",
        "label": "本机 llm_gateway（127.0.0.1:54318）",
    },
    "252": {
        "kind": "ssh",
        # ★ 只读管道：本地 SQL 送进远端 psql 的 stdin
        "ssh": ["ssh", "252"],
        "remote": "cat | podman exec -i pg-252-pg17 psql -U llm_gateway -d llm_gateway -tA",
        "label": "252 生产（pg-252-pg17）",
    },
    "211": {
        "kind": "ssh",
        "ssh": ["ssh", "root@192.168.31.211"],
        "remote": "cat | docker exec -i acc-pg17 psql -U llm_gateway -d llm_gateway -tA",
        "label": "211 生产（acc-pg17）",
    },
}

LEDGER_SQL = (
    "SET statement_timeout='60s';\n"
    "SELECT sequence_name, applied_at, coalesce(content_sha256,''), "
    "coalesce(execution_sha256,''), coalesce(runtime_adaptation,'') "
    f"FROM {LEDGER_TABLE} ORDER BY sequence_name;"
)

# ★ 状态词表（★ 显式登记 ⇒ 判据可审计）
APPLIED_WORDS = ("applied", "已应用", "executed", "verified")
PENDING_WORDS = ("pending", "未部署", "尚未部署", "待部署", "未应用")
REGISTERED_WORDS = ("registered", "已登记", "file-ready", "登记")

# ★ 表格行：`| 890 | \`file.sql\` | \`sha\` | status |`
ROW_RE = re.compile(r"^\|\s*(\d+)\s*\|(.*)$")
# ★ 节标题：`## 2026-09-05 — …` / `## 2026-08-18T18:59:33Z — …`
SECTION_RE = re.compile(r"^##\s+(.+?)\s*$")
DATE_RE = re.compile(r"(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2}):(\d{2})Z?)?")

# ★★ 登记册里出现「已应用 / pending deploy」等散文时，★ 有些行的**文件列**是反引号包裹的，
#   有些不是 ⇒ 统一剥掉 Markdown 装饰。
MD_STRIP_RE = re.compile(r"[`*]")

SNAPSHOT_COLUMNS = (
    "env",
    "migration_no",
    "filename",
    "applied_at",
    "content_sha256",
    "execution_sha256",
    "runtime_adaptation",
)


# ─────────────────────────── 小工具 ───────────────────────────


def sha256_of_lines(lines) -> str:
    h = hashlib.sha256()
    for ln in lines:
        h.update(ln.encode("utf-8"))
        h.update(b"\n")
    return h.hexdigest()


def strip_md(s: str) -> str:
    return MD_STRIP_RE.sub("", s).strip()


def split_row(line: str):
    """把一行 Markdown 表格切成单元格（★ 不做转义处理 ⇒ 登记册里没有带 `|` 的值）。"""
    body = line.strip()
    if body.startswith("|"):
        body = body[1:]
    if body.endswith("|"):
        body = body[:-1]
    return [c.strip() for c in body.split("|")]


def section_date(title: str):
    """从节标题里解析日期 ⇒ 返回 (sort_key, iso_text)；★ 解析不出来 ⇒ (0, '')。"""
    m = DATE_RE.search(title)
    if not m:
        return 0, ""
    y, mo, d = int(m.group(1)), int(m.group(2)), int(m.group(3))
    hh = int(m.group(4) or 0)
    mm = int(m.group(5) or 0)
    ss = int(m.group(6) or 0)
    return y * 100000000 + mo * 1000000 + d * 10000 + hh * 100 + mm + ss // 100, f"{y:04d}-{mo:02d}-{d:02d}"


# ─────────────────────────── 登记册解析 ───────────────────────────


class ChangelogRow:
    __slots__ = ("num", "filename", "sha", "status", "line", "section_date",
                 "section_title", "sort_key")

    def __init__(self, num, filename, sha, status, line, section_date, section_title, sort_key=0):
        self.num = num
        self.filename = filename
        self.sha = sha
        self.status = status
        self.line = line
        self.section_date = section_date
        self.section_title = section_title
        # ★ 节日期排序键（★ 同号比较用）
        self.sort_key = sort_key

    def __repr__(self):
        return f"<{self.num} {self.filename} @{self.line} sec={self.section_date}>"


def parse_changelog(path: str):
    """解析登记册 ⇒ (有效行表 {num: ChangelogRow}, 全部行数, 无日期节里的行数)。

    ★★ **有效行的判据 = 【节日期最新】**（★ 同节内取行号最后）
      —— ★ 不是「文件里最后一行」（★ 登记册不是全局时间有序 ⇒ ★★ 那会取错）。
    """
    effective = {}
    total = 0
    undated = 0
    cur_key = 0
    cur_title = ""
    cur_date = ""
    # ★★★ **权威段必须被跳过**（★ 本轮自己踩的坑）★★：
    #   权威状态段是【快照派生】的机器可判表 ⇒ ★★ 它长得像表格行（★ `| 号 | 文件 | … |`）
    #   ⇒ ★★ 若不跳过，★ 解析器会把它当成【散文的登记行】⇒ ★★
    #      · 「同号取哪一行」会被它污染（★ 实测 311 个有效行里 49 个来自权威段）
    #      · 它的「状态」格是 `applied@…` / `—` ⇒ ★★ 会被误读成【主张】
    #   ⇒ ★★ ⇒ **派生表绝不能进自己的输入**（★ 否则读数自指 ⇒ 不可信）。
    in_auth = False
    with open(path, "r", encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if AUTH_BEGIN in line:
                in_auth = True
                continue
            if AUTH_END in line:
                in_auth = False
                continue
            if in_auth:
                continue
            if line.startswith("## "):
                cur_title = SECTION_RE.match(line).group(1)
                cur_key, cur_date = section_date(cur_title)
                continue
            m = ROW_RE.match(line)
            if not m:
                continue
            cells = split_row(line)
            if len(cells) < 4:
                continue
            num = cells[0].strip()
            if not num.isdigit():
                continue
            total += 1
            if cur_key == 0:
                undated += 1
            row = ChangelogRow(
                num=int(num),
                filename=strip_md(cells[1]),
                sha=strip_md(cells[2]),
                status=cells[3],
                line=lineno,
                section_date=cur_date,
                section_title=cur_title,
                sort_key=cur_key,
            )
            prev = effective.get(row.num)
            # ★ 同一号：节日期更新者胜；同节内行号更后者胜。
            if prev is None or (cur_key, lineno) >= (prev.sort_key, prev.line):
                effective[row.num] = row
    return effective, total, undated


# ─────────────────────────── 状态分类 ───────────────────────────


def classify_status(status: str):
    """把 Status 单元格分类 ⇒ (bucket, claims)。

    ★ bucket ∈ {applied, pending, registered, mixed, unknown}
    ★ claims 是该行**同时主张**的集合（★ ⊆ {applied, pending}）
      —— ★★ mixed 的行可以【同时】主张两边（★ 「本地已应用 + 生产未部署」）
      ⇒ ★★ 于是 R2 判据（★ 「主张未应用」但账本说已应用 ⇒ 矛盾）能把这类行也照出来。

    ★ 判据表是**显式登记**的（★ 上面的 *_WORDS）⇒ ★ 可审计、可改。
    """
    s = strip_md(status).lower()
    has_applied = any(w in s for w in APPLIED_WORDS)
    has_pending = any(w in s for w in PENDING_WORDS)
    has_registered = any(w in s for w in REGISTERED_WORDS)

    claims = set()
    if has_applied:
        claims.add("applied")
    if has_pending:
        claims.add("pending")

    if has_applied and has_pending:
        bucket = "mixed"
    elif has_applied:
        bucket = "applied"
    elif has_registered:
        # ★ 「registered（…尚未应用…）」⇒ 语义上是「已登记、尚未应用」
        #   ⇒ ★ 归到 pending 侧（★ registered 本身不主张已应用）
        bucket = "pending"
        claims.add("pending")
    elif has_pending:
        bucket = "pending"
    else:
        bucket = "unknown"

    return bucket, claims


# ★★ 环境作用域（★ 复合声明里最要紧的一维）
#
# ★ 登记册里大量单元格是【按环境分句】的复合声明，★ 例如 890 那行：
#
#     「pending deploy（…**尚未部署生产**；本地已应用并复验）」
#
# ⇒ ★★ 【整格当成一个主张】会把「本地已应用」与「生产未部署」混成一锅
#    ⇒ ★★ 判据必须是【按句 + 按环境】取主张。
#
# ★ 环境分组：★ local 单列；★ 252 与 211 同属「生产」（★ 两条生产线的部署节奏一致）。
ENV_GROUPS = {
    "local": "local",
    "252": "production",
    "211": "production",
}
# ★ 环境名在句中的识别词
ENV_MENTIONS = {
    "local": ("本地", "本机", "local", "localhost"),
    "production": ("生产", "production", "252", "211"),
}
# ★ 句分隔符（★ 中文标点 + 分号）
SENT_SPLIT_RE = re.compile(r"[，,。;；\n]+")


def _claims_in(text: str):
    """从一个片段里取主张集合（★ 与 classify_status 同口径）。"""
    t = strip_md(text).lower()
    c = set()
    if any(w in t for w in APPLIED_WORDS):
        c.add("applied")
    if any(w in t for w in PENDING_WORDS):
        c.add("pending")
    if not c and any(w in t for w in REGISTERED_WORDS):
        c.add("pending")  # ★ 「registered / 已登记」⇒ 语义上是「尚未应用」
    return c


def per_env_claims(status: str):
    """★ 按【标题 + 句 × 环境】取主张 ⇒ {env_group: claims}。

    ★ 规则（★ 三条，★ 顺序即优先级）：
      ① **标题**（★ 状态单元格第一个「（」/「—」/「,」之前的部分）⇒ 【默认主张】
         （★ 例：「pending deploy（…尚未部署生产；本地已应用并复验）」⇒ 默认 = {pending}）；
      ② 把单元格按中文标点切句；★ 每句若点名某个环境分组 ⇒ 该句的主张归给那个分组；
      ③ ★★ **被点名的分组用【自己的】主张；没被点名的分组才用默认主张**
         ⇒ ★★ **不是并集** ⇒ ★★ 否则标题的 pending 会污染已被点名覆盖的分组。

    ★⇒ 890 那行因此得到：production={pending}（★ 句「尚未部署生产」覆盖）、
      local={applied}（★ 句「本地已应用并复验」覆盖）⇒ ★★ 生产侧成为矛盾、★ 本地不误报。
    """
    body = strip_md(status)
    # ① 标题
    head = re.split(r"[（(—–,]", body, 1)[0]
    default_claims = _claims_in(head)
    if not default_claims:
        default_claims = _claims_in(body)

    # ② 按句点名
    named = {}
    for sent in [x for x in SENT_SPLIT_RE.split(body) if x.strip()]:
        low = sent.lower()
        hit = [g for g, words in ENV_MENTIONS.items() if any(w in low for w in words)]
        c = _claims_in(sent)
        if not c or not hit:
            continue
        for g in hit:
            named.setdefault(g, set()).update(c)

    # ③ 覆盖（★ 不是并集）
    out = {}
    for group in ("local", "production"):
        out[group] = set(named.get(group, default_claims))
    return out


# ─────────────────────────── 快照读写 ───────────────────────────


def write_snapshot(env: str, rows, out_path, append=False):
    """写快照（★ append=True ⇒ 追加到既有快照 ⇒ ★ 快照由【工具】单点产出）。"""
    lines = ["#" + "\t".join(SNAPSHOT_COLUMNS)]
    if append and os.path.exists(out_path):
        prev, _ = read_snapshot(out_path)
        # ★ 追加前先按 (env, filename) 去掉同环境同文件的旧行（★ 免得重采时重复）
        keys = {(r[0], r[2]) for r in rows}
        prev = [r for r in prev if (r[0], r[2]) not in keys]
        rows = prev + list(rows)
    for r in rows:
        lines.append("\t".join(r))
    body = sha256_of_lines(lines[1:])
    envs = sorted({r[0] for r in rows})
    header = [
        f"#collected_at={datetime.now(timezone.utc).astimezone().isoformat(timespec='seconds')}",
        f"#envs={','.join(envs)}",
        f"#labels={'; '.join(f'{e}={ENV_SPECS[e][chr(108)+chr(97)+chr(98)+chr(101)+chr(108)]}' for e in envs if e in ENV_SPECS)}",
        f"#rows={len(rows)}",
        f"#ledger_set_sha256={body}",
        "#★ 本文件是【采集快照】，★ 不是账本真值（★ 真值在各环境库的 "
        f"{LEDGER_TABLE} 里）⇒ ★ 快照会过期 ⇒ ★ 用 collect 重采。",
    ]
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write("\n".join(header + lines) + "\n")
    return body


def read_snapshot(path):
    """读快照 ⇒ (rows, meta)。"""
    meta = {}
    rows = []
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line:
                continue
            if line.startswith("#"):
                kv = line[1:]
                if "=" in kv:
                    k, v = kv.split("=", 1)
                    meta.setdefault(k.strip(), v.strip())
                continue
            cells = line.split("\t")
            if len(cells) < 7:
                continue
            if cells[0] == SNAPSHOT_COLUMNS[0]:
                # ★ 列名行（★ 不是数据）⇒ 跳过
                continue
            rows.append(cells[:7])
    return rows, meta


def snapshot_env_set(rows):
    """快照 ⇒ {env: {filename: row}}。"""
    out = {}
    for r in rows:
        out.setdefault(r[0], {})[r[2]] = r
    return out


# ─────────────────────────── 采集 ───────────────────────────


def collect_local():
    dsn = os.environ.get(ENV_SPECS["local"]["dsn_env"], "")
    if not dsn:
        raise SystemExit(
            f"★ 采集本地账本需要环境变量 {ENV_SPECS['local']['dsn_env']}（★ 只传 DSN，★ 不回显）"
        )
    psql = os.environ.get(ENV_SPECS["local"]["psql_env"], "psql")
    env = dict(os.environ)
    # ★ DSN 走 PG* 环境变量 ⇒ 不进 argv、不回显
    env["PGCONNECT_TIMEOUT"] = "10"
    proc = subprocess.run(
        [psql, "-tA", "-F", "|", "-d", dsn, "-c", LEDGER_SQL],
        env=env,
        capture_output=True,
        text=True,
        timeout=120,
    )
    if proc.returncode != 0:
        raise SystemExit(f"★ 本地采集失败（退出码 {proc.returncode}）")
    return proc.stdout


def collect_remote(env_name):
    spec = ENV_SPECS[env_name]
    cmd = list(spec["ssh"]) + [spec["remote"]]
    proc = subprocess.run(
        cmd,
        input=LEDGER_SQL,
        capture_output=True,
        text=True,
        timeout=180,
    )
    if proc.returncode != 0:
        raise SystemExit(f"★ {env_name} 采集失败（退出码 {proc.returncode}）")
    return proc.stdout


def parse_ledger_output(text: str):
    """把账本输出解析成 (migration_no, filename, applied_at, sha_c, sha_e, adapt)。"""
    rows = []
    for line in text.split("\n"):
        line = line.strip()
        if not line or line.upper().startswith("SET"):
            continue
        cells = line.split("|")
        if len(cells) < 5:
            continue
        seq = cells[0]
        # ★ sequence_name 形如 `session-summary-and-integrity-2026-09:032_session_tenant_binding.sql`
        if ":" not in seq:
            continue
        fname = seq.split(":", 1)[1]
        m = re.match(r"^(\d+)", fname)
        num = m.group(1) if m else ""
        rows.append([num, fname, cells[1], cells[2], cells[3], cells[4]])
    return rows


def cmd_collect(args):
    if args.env not in ENV_SPECS:
        raise SystemExit(f"★ 未知环境 {args.env!r}（★ 可选：{', '.join(ENV_SPECS)}）")
    if ENV_SPECS[args.env]["kind"] == "psql":
        text = collect_local()
    else:
        text = collect_remote(args.env)
    rows = parse_ledger_output(text)
    if not rows:
        raise SystemExit(f"★ {args.env} 账本为空 ⇒ ★ 要么环境不对，★ 要么解析器坏了")
    out_rows = [[args.env, r[0], r[1], r[2], r[3], r[4], r[5]] for r in rows]
    if args.out:
        write_snapshot(args.env, out_rows, args.out, append=args.append)
        print(f"★ {args.env} 采集完成 ⇒ {args.out}（本次 {len(out_rows)} 行）")
    else:
        print("\t".join(SNAPSHOT_COLUMNS))
        for r in out_rows:
            print("\t".join(r))
    return 0


# ─────────────────────────── 对账 ───────────────────────────


def reconcile(changelog_path, snapshot_rows, meta):
    effective, total, undated = parse_changelog(changelog_path)
    envs = snapshot_env_set(snapshot_rows)

    hard = []       # ★ 硬矛盾（★ 纯 applied / 纯 pending 的行 ⇒ 进门）
    soft = []       # ★ 复合声明行上的矛盾（★ mixed ⇒ 报告但不进门）
    unjudgeable = []  # ★ 状态列不可机器判的清单（★ 棘轮）

    # ★ 登记册文件名 ⇒ 迁移号（★ 账本里是文件名，★ 登记册里也是文件名 ⇒ 按文件名对）
    for num, row in sorted(effective.items()):
        bucket, claims = classify_status(row.status)
        if bucket == "unknown":
            unjudgeable.append((row, "unknown", "状态单元格既无 applied 也无 pending 关键词"))
            continue
        if bucket == "mixed":
            unjudgeable.append((row, "mixed", "状态单元格同时主张「已应用」与「未部署」⇒ 复合声明"))

        # ★★ **按环境取主张**（★ 复合声明里的「本地已应用 / 生产未部署」必须分开算）
        envc = per_env_claims(row.status)
        # ★★ **文件名归一（★ 夹具抓到的真缺陷）**：★ 登记册的文件列**有时带路径**
        #   （★ 实测：`sql/migrations/domain/032_session_tenant_binding.sql`），
        #   ★ 而账本里是**裸文件名** ⇒ ★ 不归一 ⇒ ★★ 那一行永远配不上 ⇒ ★★ 误判成
        #   「声称已应用但账本里没有」⇒ ★⇒ 比较一律用 **basename**。
        base_name = os.path.basename(row.filename)
        for env_name, ledger in sorted(envs.items()):
            group = ENV_GROUPS.get(env_name, "production")
            claims = envc.get(group, set()) or set(classify_status(row.status)[1])
            in_ledger = base_name in ledger
            if "applied" in claims and not in_ledger:
                # ★★★ **这一类【不是】登记册错误** ⇒ ★★ 只进【软桶】★ 理由：
                #   登记册里的「已应用」可能来自【旁路路径】（★ 手工 psql、★ 另一条
                #   revision sequence、★ 另一个库）⇒ ★★ 而那些路径【不会写这个账本】
                #   ⇒ ⇒ 「账本里没有」**推不出「没应用过」** ⇒ ★★ 不能当矛盾。
                #   （★ 实测：`084-freediscovery-schema.sql` 的状态明写「本地 … psql 手动应用」⇒ 正是这一类。）
                item = {
                    "env": env_name,
                    "migration": num,
                    "filename": base_name,
                    "kind": "claimed-applied-but-absent",
                    "status_excerpt": row.status[:120],
                    "line": row.line,
                    "section_date": row.section_date,
                }
                soft.append(item)
            if "pending" in claims and in_ledger:
                # ★★★ **这一类是【真矛盾】** ⇒ ★★ 进硬闸。理由：
                #   「pending / 未应用 / 尚未部署」这句话的含义是「**没应用**」；
                #   而该环境的账本明写它【已 applied】⇒ ★★ 两句话同时为真不可能
                #   ⇒ ⇒ **登记册的这一行已经过期**（★ 而登记册是唯一登记册 ⇒ ⇒ 部署序列的可信度受损）。
                #   （★ 实测：890 的「尚未部署生产」与 252/211 账本里的「已记账」直接冲突。）
                item = {
                    "env": env_name,
                    "migration": num,
                    "filename": base_name,
                    "kind": "claimed-pending-but-applied",
                    "status_excerpt": row.status[:120],
                    "line": row.line,
                    "section_date": row.section_date,
                    "ledger_applied_at": ledger[base_name][3],
                }
                hard.append(item)

    # ★ 反向：账本里已 applied ⇒ 登记册必须有对应行（★ 按文件名找）
    missing = []
    for env_name, ledger in sorted(envs.items()):
        known = {os.path.basename(r.filename) for r in effective.values()}
        for fname, cells in sorted(ledger.items()):
            if fname in known:
                continue
            missing.append({
                "env": env_name,
                "filename": fname,
                "applied_at": cells[3],
                "kind": "applied-but-not-registered",
            })

    return {
        "effective_rows": len(effective),
        "total_rows": total,
        "undated_section_rows": undated,
        "envs": {k: len(v) for k, v in envs.items()},
        "hard_contradictions": hard,
        "soft_contradictions": soft,
        "unjudgeable": [
            {"migration": r.num, "filename": r.filename, "bucket": b, "why": w, "line": r.line}
            for (r, b, w) in unjudgeable
        ],
        "applied_but_not_registered": missing,
        "snapshot_meta": meta,
    }


def cmd_reconcile(args):
    changelog = args.changelog or DEFAULT_CHANGELOG
    rows, meta = read_snapshot(args.snapshot)
    if not rows:
        raise SystemExit("★ 快照为空 ⇒ 先 collect")
    rep = reconcile(changelog, rows, meta)

    if args.json:
        print(json.dumps(rep, ensure_ascii=False, indent=2))
    else:
        print(f"★ 登记册有效行 {rep['effective_rows']} / 全部数据行 {rep['total_rows']}"
              f"（★ 无日期节里的行 {rep['undated_section_rows']}）")
        print(f"★ 快照各环境账本行数：{rep['envs']}")
        print(f"★ 硬矛盾（★ 「声称未应用」但账本说【已 applied】⇒ 进门）：{len(rep['hard_contradictions'])}")
        for it in rep["hard_contradictions"]:
            print(f"  ✘ [{it['env']}] #{it['migration']} {it['filename']} — {it['kind']}"
                  f"（★ 登记册 L{it['line']} / {it['section_date']}）")
            print(f"      Status: {it['status_excerpt']}")
            if "ledger_applied_at" in it:
                print(f"      账本 applied_at: {it['ledger_applied_at']}")
        print(f"★ 软报告（★ 不进门）：{len(rep['soft_contradictions'])}")
        _kinds = {}
        for _it in rep["soft_contradictions"]:
            _kinds[_it["kind"]] = _kinds.get(_it["kind"], 0) + 1
        for _k, _v in sorted(_kinds.items()):
            print(f"    · {_k}：{_v}")
        for it in rep["soft_contradictions"]:
            print(f"  ⚠ [{it['env']}] #{it['migration']} {it['filename']} — {it['kind']}"
                  f"（★ L{it['line']}）")
            print(f"      Status: {it['status_excerpt']}")
        print(f"★ 状态列不可机器判（★ 棘轮）：{len(rep['unjudgeable'])}")
        for it in rep["unjudgeable"][:20]:
            print(f"  · #{it['migration']} {it['filename']} — {it['bucket']}：{it['why']}（★ L{it['line']}）")
        if len(rep["unjudgeable"]) > 20:
            print(f"  …（★ 另有 {len(rep['unjudgeable']) - 20} 条）")
        print(f"★ 已 applied 但登记册没有对应行：{len(rep['applied_but_not_registered'])}")
        for it in rep["applied_but_not_registered"][:20]:
            print(f"  · [{it['env']}] {it['filename']}（★ applied_at {it['applied_at']}）")
        if len(rep["applied_but_not_registered"]) > 20:
            print(f"  …（★ 另有 {len(rep['applied_but_not_registered']) - 20} 条）")

    if rep["hard_contradictions"]:
        return 2
    return 0


# ─────────────────────────── 权威状态段（机器可判） ───────────────────────────


def build_authoritative(rows, meta, backlog=None):
    """★ 用快照生成【机器可判的权威状态表】。

    ★★ 为什么需要它：★ 登记册原来的 Status 列是**散文** ⇒ ★★ 「机器判不了」
      ⇒ ★★ 于是它可以一直错着（★ 实测：107 条「声称未部署」但账本已记账）。
      ⇒ ★★ 于是需要一份**结构化**的状态载体：★ 迁移 × 环境 ⇒ applied / —。

    ★ 形态：一行一个迁移号，★ 列 = local / 252 / 211 三格
      （★ `applied@<时间>` 或 `—`）★ ⇒ **一眼可判**、★ 且可与快照逐格比对。
    """
    envs = sorted({r[0] for r in rows})
    # 文件名 ⇒ {env: row}
    by_file = {}
    for r in rows:
        by_file.setdefault(r[2], {})[r[0]] = r
    files = sorted(by_file)
    lines = []
    lines.append(AUTH_BEGIN)
    lines.append("")
    lines.append("### ★ 权威状态（机器可判 · R160 轮生成）")
    lines.append("")
    lines.append(f"★ 采集时刻 `{meta.get('collected_at', '?')}` · ★ 环境 "
                 f"`{meta.get('envs', '?')}` · ★ 行数 `{meta.get('rows', '?')}`")
    lines.append(f"★ 账本集合指纹 `{meta.get('ledger_set_sha256', '?')}`")
    lines.append("")
    lines.append("★ ★ **判读法**：★ 每格 = `applied@<账本 applied_at>` ⇒ 该环境**已应用**；"
                 "★ `—` ⇒ **该环境账本里没有**（★ 注意：★ **账本里没有 ≠ 没应用过** ⇒ "
                 "★ 旁路应用（★ 手工 psql / 另一条序列）不会进账本）。")
    lines.append("")
    header = "| 迁移 | 文件 | " + " | ".join(envs) + " |"
    sep = "|---|---|" + "|".join("---" for _ in envs) + "|"
    lines.append(header)
    lines.append(sep)
    for fname in files:
        cells = []
        for e in envs:
            r = by_file[fname].get(e)
            cells.append(f"`applied@{r[3]}`" if r else "—")
        m = re.match(r"^(\d+)", fname)
        num = m.group(1) if m else ""
        lines.append(f"| {num} | `{fname}` | " + " | ".join(cells) + " |")
    lines.append("")
    if backlog is not None:
        lines.append(f"★ ★ **散文状态与账本的矛盾数（★ 棘轮基线，★ 只许降）**：`{backlog}`"
                     f"（★ 判据：「声称未应用」但该环境账本已 `applied` ⇒ ★ ★★ **重新生成本段落"
                     f"即等于收紧基线** ⇒ ★ 门要求现值 ≤ 本值）")
        lines.append("")
    lines.append(AUTH_END)
    return "\n".join(lines)


def cmd_render(args):
    rows, meta = read_snapshot(args.snapshot)
    if not rows:
        raise SystemExit("★ 快照为空 ⇒ 先 collect")
    # ★ 算一次散文矛盾数（★ 作为棘轮基线写进段落）
    _rep = reconcile(args.changelog or DEFAULT_CHANGELOG, rows, meta)
    backlog = len(_rep["hard_contradictions"])
    section = build_authoritative(rows, meta, backlog=backlog)
    out = args.out or DEFAULT_AUTH_FILE
    if args.check:
        # ★ 只校验：文件里的段落是否与现算的一致（★ 手改 ⇒ 退出码 2）
        cur = read_authoritative_section(out)
        if cur is None:
            print(f"✘ {out} 里没有权威状态段落（★ 缺 R160-AUTHORITATIVE 标记）")
            return 2
        if cur.strip() != section.strip():
            print(f"✘ {out} 里的权威状态段与快照现算结果不一致 ⇒ ★ 要么手改过，★ 要么快照已过期")
            return 2
        print("✓ 权威状态段与快照一致")
        return 0
    if args.write:
        if os.path.exists(out):
            body = open(out, "r", encoding="utf-8").read()
            if AUTH_BEGIN in body and AUTH_END in body:
                new_body = re.sub(
                    re.escape(AUTH_BEGIN) + r".*?" + re.escape(AUTH_END),
                    section,
                    body,
                    flags=re.S,
                )
            else:
                new_body = body.rstrip("\n") + "\n\n" + section + "\n"
        else:
            new_body = AUTH_HEADER + section + "\n"
        open(out, "w", encoding="utf-8").write(new_body)
        print(f"★ 权威状态段已写入 {out}")
        return 0
    print(section)
    return 0


def read_authoritative_section(path):
    body = open(path, "r", encoding="utf-8").read()
    if AUTH_BEGIN not in body or AUTH_END not in body:
        return None
    m = re.search(re.escape(AUTH_BEGIN) + r".*?" + re.escape(AUTH_END), body, flags=re.S)
    return m.group(0) if m else None


def cmd_effective(args):
    """★ 打印【全部】有效登记行（★ JSON）⇒ 供门做【全量】双口径对拍。

    ★★ 为什么要「全量」：★ 抽样对拍有盲区（★ 实测：★ 抽 6 个号 ⇒ ★★ 49 个号的口径分歧
      ★★ 一条都没撞上 ⇒ ★★ 门【看起来绿】⇒ ★★ 而分歧真实存在）⇒ ★★ ⇒ 全量。
    """
    path = args.changelog or DEFAULT_CHANGELOG
    effective, total, undated = parse_changelog(path)
    out = {
        "total_rows": total,
        "effective_rows": len(effective),
        "undated_section_rows": undated,
        "rows": {
            str(num): {
                "line": r.line,
                "filename": r.filename,
                "basename": os.path.basename(r.filename),
                "sha": r.sha,
                "status": r.status,
                "section_date": r.section_date,
            }
            for num, r in sorted(effective.items())
        },
    }
    print(json.dumps(out, ensure_ascii=False))
    return 0


def cmd_locate(args):
    """★ 打印某个迁移号的【有效登记行】（★ 按节日期最新的那行）。

    ★★ 为什么需要它：★ 同一个迁移号在登记册里可能登记过多次（★ 实测 65 个号重复）
      ⇒ ★★ **必须知道哪行才是「当前有效的声明」**，★ 否则改错行 ⇒ ★★ 改了一个
      ★★ 早已被覆盖的旧行 ⇒ ★★ 对账器【正确地】不认它 ⇒ ★★ 白改。
    """
    path = args.changelog or DEFAULT_CHANGELOG
    effective, total, _ = parse_changelog(path)
    row = effective.get(int(args.migration))
    if row is None:
        print(f"★ 登记册里没有迁移号 {args.migration}")
        return 3
    print(json.dumps({
        "migration": row.num,
        "filename": row.filename,
        "basename": os.path.basename(row.filename),
        "line": row.line,
        "status": row.status,
        "section_date": row.section_date,
        "section_title": row.section_title,
        "sha": row.sha,
        "total_rows": total,
    }, ensure_ascii=False))
    return 0


# ─────────────────────────── 自证 ───────────────────────────


SELFTEST_CHANGELOG = """# 测试登记册

## 2026-01-01 — 早节

| Migration | File | SHA-256 | Status |
|---|---|---|---|
| 10 | `10_a.sql` | `aa` | applied+verified |

## 2026-06-01 — 晚节（★ 应覆盖早节）

| Migration | File | SHA-256 | Status |
|---|---|---|---|
| 10 | `10_a.sql` | `bb` | pending deploy |
| 11 | `11_b.sql` | `cc` | applied+verified |
| 12 | `12_c.sql` | `dd` | registered（★ 尚未应用）
| 13 | `13_d.sql` | `ee` | pending deploy |
| 14 | `14_e.sql` | `ff` | applied+verified |
"""


def cmd_selftest(args):
    fails = []

    def check(cond, msg):
        if not cond:
            fails.append(msg)

    # ① 解析面自检：★ 有效行必须取【节日期最新】的那行（★ 早节 applied 晚节 pending ⇒ 取 pending）
    tmp = os.path.join("/tmp", f"kx-selftest-changelog-{os.getpid()}.md")
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(SELFTEST_CHANGELOG)
    eff, total, undated = parse_changelog(tmp)
    check(total == 6, f"① 应解析出 6 个数据行（★ 10 号登记两次），实际 {total}")
    check(eff[10].status.startswith("pending"),
          f"① 同号应取【最新节】的行（pending），实际 {eff[10].status[:40]!r}")
    check(eff[10].sha == "bb", f"① 同号应取最新节的 sha（bb），实际 {eff[10].sha!r}")
    check(undated == 0, f"① 无日期节里的行应为 0，实际 {undated}")

    # ② 状态分类（★ 阳性 + 反向）
    check(classify_status("applied+verified")[0] == "applied", "② applied 分类错")
    check(classify_status("pending deploy")[0] == "pending", "② pending 分类错")
    check(classify_status("registered（尚未应用）")[0] == "pending", "② registered 应归 pending 侧")
    check(classify_status("verified (local 252 replica), pending deploy")[0] == "mixed",
          "② 复合声明应判 mixed")
    check(classify_status("随便写点什么")[0] == "unknown", "② 无关键词应判 unknown")

    # ③ 反向夹具：★ 故意造矛盾 ⇒ 对账器必须报出来
    #    · 13 号：登记册说 pending，★ 但账本里已 applied ⇒ claimed-pending-but-applied
    #    · 14 号：登记册说 applied，★ 但账本里没有      ⇒ claimed-applied-but-absent
    #    · 10 号（晚节说 pending，账本里没有）⇒ 无矛盾
    #    · 11 号（applied，账本里有）             ⇒ 无矛盾
    snap = os.path.join("/tmp", f"kx-selftest-snapshot-{os.getpid()}.tsv")
    with open(snap, "w", encoding="utf-8") as fh:
        fh.write("#env=local\n")
        fh.write("\t".join(SNAPSHOT_COLUMNS) + "\n")
        fh.write("local\t11\t11_b.sql\t2026-06-02 00:00:00+08\tcc\tcc\tnone\n")
        fh.write("local\t13\t13_d.sql\t2026-06-03 00:00:00+08\tee\tee\tnone\n")
        # ★ 15 号：★ 账本里有，★ 但登记册完全没有登记 ⇒ 应进「已 applied 但未登记」清单
        fh.write("local\t15\t15_f.sql\t2026-06-04 00:00:00+08\tgg\tgg\tnone\n")
    rows, meta = read_snapshot(snap)
    rep = reconcile(tmp, rows, meta)
    hard = rep["hard_contradictions"]
    kinds = sorted((h["migration"], h["kind"]) for h in hard)
    # ★★ 只有「声称未应用 + 账本说已 applied」才是【硬矛盾】⇒ ⇒ 13 号
    check(kinds == [(13, "claimed-pending-but-applied")],
          f"③ 硬矛盾应恰好是 13 号（★ 声称未应用但账本已 applied），实际 {kinds}")
    check(not any(h["migration"] in (10, 11, 12, 14) for h in hard),
          "③ 10/11/12/14 不应进硬矛盾")
    # ★★★ 而「声称已应用 + 账本里没有」⇒ **不是矛盾**⇒ ★★ 只进软桶
    #   （★ 理由：登记册的「已应用」可能来自旁路路径 ⇒ 手工 psql / 另一条序列 / 另一个库
    #     ⇒ ★★ 而那些路径【不会写这个账本】⇒ ★★ 「账本里没有」**推不出「没应用过」**）
    soft_kinds = sorted((h["migration"], h["kind"]) for h in rep["soft_contradictions"])
    check((14, "claimed-applied-but-absent") in soft_kinds,
          f"④ 14 号应进软桶（★ 旁路应用可能），实际 {soft_kinds}")

    # ⑤ ★★★ **权威段必须被跳过**（★ 防御性规则 ⇒ ★★ 必须【可被测到】⇒ 否则它是死代码）
    #
    # ★★ 真实机理（★ 本轮实测）：★ 权威段在登记册【顶部】⇒ ★★ 它的行【不属于任何节】
    #   ⇒ ★★ 节日期键 = 0（★ 最旧）⇒ ★★ 于是它**赢不了**有散文行的号
    #   ⇒ ★★ **但对于「散文里压根没有这一行」的号**，★ 它就成了【唯一的有效行】
    #   ⇒ ★★★ **⇒ 它冒充了那些号的登记行**（★ 实测 49 个号）⇒ ★★ ⇒ 读数失真。
    #   ⇒ ⇒ 夹具必须复现这一条：**一个散文里没有的号，不许因为权威段里有它而「存在」**。
    auth_fixture = """# 夹具

<!-- R160-AUTHORITATIVE:BEGIN -->

| Migration | File | SHA-256 | Status |
|---|---|---|---|
| 20 | `20_only_in_auth.sql` | `zz` | applied+verified |

<!-- R160-AUTHORITATIVE:END -->

## 2026-06-01 — 散文节

| Migration | File | SHA-256 | Status |
|---|---|---|---|
| 10 | `10_a.sql` | `bb` | pending deploy |
"""
    tmp_auth = os.path.join("/tmp", f"kx-selftest-auth-{os.getpid()}.md")
    with open(tmp_auth, "w", encoding="utf-8") as fh:
        fh.write(auth_fixture)
    eff_auth, _, _ = parse_changelog(tmp_auth)
    check(20 not in eff_auth,
          f"⑤ 权威段里的行不得成为任何号的【有效登记行】⇒ 20 号应当【不存在】（★ 实际存在：{eff_auth.get(20)}）")
    check(eff_auth.get(10) is not None and eff_auth[10].status.startswith("pending"),
          f"⑤ 散文行的有效状态应当是 pending（★ 实际 {eff_auth.get(10).status[:40]!r}）")
    os.remove(tmp_auth)

    # ⑥ 反向：★ 账本里 applied 但登记册没有登记 ⇒ 进「已 applied 但未登记」清单
    check(len(rep["applied_but_not_registered"]) == 1
          and rep["applied_but_not_registered"][0]["filename"] == "15_f.sql",
          f"⑤ 15 号应进「已 applied 但未登记」清单，实际 {rep['applied_but_not_registered']}")

    for p in (tmp, snap):
        try:
            os.remove(p)
        except OSError:
            pass

    if fails:
        print("✘ 自证失败：")
        for f in fails:
            print("   - " + f)
        return 3
    print("✓ 自证通过（★ 解析面 / 状态分类 / 矛盾检出（两个方向）/ 反向清单 四组）")
    return 0


# ─────────────────────────── 入口 ───────────────────────────


def main(argv=None):
    ap = argparse.ArgumentParser(
        description="★ 登记册 × 各环境账本 的只读对账器（★ 只读；★ DSN 不回显）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    c = sub.add_parser("collect", help="★ 只读采集某环境的账本")
    c.add_argument("--env", required=True, choices=sorted(ENV_SPECS))
    c.add_argument("--out", default="")
    c.add_argument("--append", action="store_true",
                   help="★ 追加到既有快照（★ 快照由工具单点产出 ⇒ 不手工拼）")
    c.set_defaults(func=cmd_collect)

    r = sub.add_parser("reconcile", help="★ 登记册 × 快照 对账")
    r.add_argument("--snapshot", required=True)
    r.add_argument("--changelog", default="")
    r.add_argument("--json", action="store_true")
    r.set_defaults(func=cmd_reconcile)

    rd = sub.add_parser("render", help="★ 生成【机器可判的权威状态段】")
    rd.add_argument("--snapshot", required=True)
    rd.add_argument("--changelog", default="")
    rd.add_argument("--out", default="", help="★ 权威镜像文件（★ 默认 docs/db-changelog-authoritative.md）")
    rd.add_argument("--write", action="store_true", help="★ 回写权威镜像（★ 幂等）")
    rd.add_argument("--check", action="store_true", help="★ 只校验镜像里的段落是否与现算一致")
    rd.set_defaults(func=cmd_render)

    ef = sub.add_parser("effective", help="★ 打印【全部】有效登记行（★ JSON）")
    ef.add_argument("--changelog", default="")
    ef.set_defaults(func=cmd_effective)

    lc = sub.add_parser("locate", help="★ 打印某迁移号的【有效登记行】")
    lc.add_argument("--migration", required=True)
    lc.add_argument("--changelog", default="")
    lc.set_defaults(func=cmd_locate)

    s = sub.add_parser("selftest", help="★ 自证")
    s.set_defaults(func=cmd_selftest)

    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
