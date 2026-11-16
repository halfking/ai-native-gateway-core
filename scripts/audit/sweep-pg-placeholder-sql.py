#!/usr/bin/env python3
"""sweep-pg-placeholder-sql.py —— 扫全仓带 pg 占位符的 SQL，拿真库 PREPARE 一遍。

★ 抓什么
  **parse/plan 期**就能暴露、而 mock 驱动的单测永远抓不到的 SQL 缺陷：
    - 42P08 `could not determine data type of parameter $n`
      （参数只出现在没有类型上下文的位置 ⇒ PG 推断不出类型 ⇒ 整条语句失败）
    - 42P10 / 42P03 `inconsistent types deduced for parameter $n`
    - 42883 `operator does not exist`（例如 `CURRENT_DATE - $n` 的日期算术歧义：
      PG 把未定类型参数解析成 date ⇒ 结果是整数天数差 ⇒ `date >= integer`）
  这类缺陷的共同特征：**只在"那条路径从没被成功调用过"时暴露**，
  表现为运行期的 500 / 500-ish 错误，而单元测试全绿。

★ 不证明什么（**必读**）
  1. **拼接型 SQL 会误报**：本脚本按「反引号字面量」切分，取到的是**片段**。
     形如 `q := "…$2)"` 后面再 `q += " AND task_id=$3"` 的写法，片段里
     `$1` 可能根本没有上下文 ⇒ 报「推断不出类型」⇒ **假阳性**。
     ⇒ 命中之后**必须回代码看拼好的完整语句**再下结论（本仓已有 3 例假阳性）。
  2. **schema 漂移会误报**：42P01（表不存在）/ 42703（列不存在）既可能是
     真缺陷（**全新安装就会炸**：列只在 `sql/migrations/domain/` 通道创建，
     而安装器**不跑** domain 通道），也可能只是本机沙箱的 schema 旧。
     ⇒ 这两类单独成桶，要人判断。
  3. 只扫**反引号字面量**；双引号拼接、`fmt.Sprintf` 拼出来的语句扫不到。

用法：
    SWEEP_DSN='postgres://user@host:5432/db' python3 scripts/audit/sweep-pg-placeholder-sql.py
    # 需要一台**能连**的 PostgreSQL（PREPARE 只 parse+plan，不写任何行）。
"""
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
DSN = os.environ.get("SWEEP_DSN", "")
PSQL = os.environ.get("SWEEP_PSQL", "psql")

RAW = re.compile(r"`([^`]{40,})`", re.S)
SQL_HEAD = re.compile(r"^\s*(WITH|SELECT|INSERT|UPDATE|DELETE)\b", re.I)
PLACEHOLDER = re.compile(r"\$\d")

SKIP_DIRS = {".git", "node_modules", "vendor", "dist", "testdata", ".git"}
SKIP_FILES = ("_test.go",)  # 测试文件里的夹具 SQL 噪声大，单独人工看

# 错误分类：actionable = 真缺陷的候选；schema = 要人判断；noise = 已知假阳性
ACTIONABLE = {"42P08", "42P10", "42P03", "42883"}
SCHEMA = {"42P01", "42703"}


def dsn_parts():
    if not DSN:
        return None
    m = re.match(r"postgres(?:ql)?://([^@/]*)(?:@)?([^/]*)/([^?]+)", DSN)
    if not m:
        return None
    userinfo, hostport, dbname = m.groups()
    user = userinfo.split(":")[0] if userinfo else ""
    host, port = ("/tmp", "5432")
    if hostport:
        if ":" in hostport:
            host, port = hostport.rsplit(":", 1)
        else:
            host = hostport
    return user, host, port, dbname


def collect():
    out = []
    for root, dirs, files in os.walk(REPO):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for f in files:
            if not f.endswith(".go"):
                continue
            p = os.path.join(root, f)
            try:
                src = open(p, encoding="utf-8").read()
            except Exception:
                continue
            for m in RAW.finditer(src):
                sql = m.group(1)
                if not PLACEHOLDER.search(sql):
                    continue
                if not SQL_HEAD.match(sql):
                    continue
                if "%s" in sql or "?" in sql:
                    continue
                line = src[: m.start()].count("\n") + 1
                out.append((os.path.relpath(p, REPO), line, sql.strip()))
    return out


def prepare(sql):
    script = "PREPARE sweep_probe AS " + sql.rstrip().rstrip(";") + ";\n"
    try:
        p = subprocess.run(
            [PSQL, "-v", "ON_ERROR_STOP=1", "-f", "-"],
            input=script, capture_output=True, text=True, timeout=30,
            env={**os.environ, "PGCONNECT_TIMEOUT": "5"},
        )
    except subprocess.TimeoutExpired:
        return "other", "timeout"
    if p.returncode == 0:
        return "ok", ""
    lines = (p.stderr or p.stdout).strip().splitlines()
    msg = next((l for l in lines if "ERROR:" in l), lines[0] if lines else "")
    m = re.search(r"SQLSTATE (\w+)", msg)
    code = m.group(1) if m else ""
    if not code and "does not exist" in msg:
        code = "42P01"
    return ("pg:" + code if code else "other"), msg


def main():
    parts = dsn_parts()
    if not parts:
        print("需要 SWEEP_DSN（形如 postgres://user@host:5432/db）；本次**没有**跑扫描", file=sys.stderr)
        return 2
    user, host, port, dbname = parts
    base = [PSQL, "-h", host, "-p", str(port), "-d", dbname, "-v", "ON_ERROR_STOP=1", "-f", "-"]
    if user:
        base.insert(1, "-U")
        base.insert(2, user)

    def run(sql):
        script = "PREPARE sweep_probe AS " + sql.rstrip().rstrip(";") + ";\n"
        try:
            p = subprocess.run(base, input=script, capture_output=True, text=True, timeout=30)
        except subprocess.TimeoutExpired:
            return "other", "timeout"
        if p.returncode == 0:
            return "ok", ""
        lines = (p.stderr or p.stdout).strip().splitlines()
        msg = next((l for l in lines if "ERROR:" in l), lines[0] if lines else "")
        m = re.search(r"SQLSTATE (\w+)", msg)
        code = m.group(1) if m else ""
        if not code and "does not exist" in msg:
            code = "42P01"
        return ("pg:" + code if code else "other"), msg

    cands = collect()
    buckets = {}
    for path, line, sql in cands:
        status, msg = run(sql)
        buckets.setdefault(status, []).append((path, line, msg))

    ok = len(buckets.get("ok", []))
    print(f"候选 SQL 片段：{len(cands)} 条（PREPARE 通过 {ok} 条）")
    print(f"真库：{host}:{port}/{dbname}\n")

    def dump(title, codes):
        rows = []
        for c in codes:
            rows.extend(buckets.get("pg:" + c, []))
        if not rows:
            return 0
        print(f"=== ★ {title}：{len(rows)} 条 ===")
        for path, line, msg in rows:
            print(f"  {path}:{line}\n      {msg[:160]}")
        print()
        return len(rows)

    n_action = dump("可执行类型缺陷候选（42P08/42P10/42P03/42883）", sorted(ACTIONABLE))
    n_schema = dump("schema 相关（42P01/42703）：可能是全新安装缺口，也可能是本机 schema 旧", sorted(SCHEMA))

    noise = {k: len(v) for k, v in buckets.items() if k not in ("ok",) and not k.startswith("pg:")}
    other_pg = {k: len(v) for k, v in buckets.items() if k.startswith("pg:") and k[3:] not in ACTIONABLE and k[3:] not in SCHEMA}
    print(f"=== 已知噪声（片段被拆开导致的多语句/语法错等）：{sum(noise.values())} 条 ===")
    for k, v in sorted(noise.items()):
        print(f"  {k}: {v}")
    if other_pg:
        print(f"=== 其它 pg 错误码：{sum(other_pg.values())} 条 ===")
        for k, v in sorted(other_pg.items()):
            print(f"  {k}: {v}")
    print()
    print("★ 提醒：命中项**必须回代码看拼好的完整语句**再下结论"
          "（拼接型 SQL 会误报，见文件头「不证明什么」）。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
