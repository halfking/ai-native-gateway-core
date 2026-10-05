#!/usr/bin/env python3
"""assets 心跳门控的两个门的变异验证。

门 A：apihub/upsert_heartbeat_contract_test.go —— 文本层
门 B：scripts/.asset-heartbeat-realdb.sh        —— 语义层（走 252 的 TEMP TABLE）

纪律同前：
  1. 变异后逐字节还原（md5）
  2. 必须让**目标门**转红；目标门在第 5 元组里
  3. 打出实际跑的条数；0 条直接判脚本错误
  4. 变异必须仍能编译 / 仍能被提取器解析 —— BUILD-BROKEN 不算证据
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GO_SRC = os.path.join(REPO, "apihub/pg_store.go")

CONTRACT = "TestUpsertSQLHasHeartbeatGuard|TestUpsertSQLHeartbeatWindowGuard|TestAssetHeartbeatRefreshIntervalIsFiveMinutes"

# (mid, old, new, target_kind, desc)
#   target_kind: "both" 契约门+真库门都要红
#                "real" 只有真库门红（文本门验不出语义）
#                "text" 只有文本门红
MUTATIONS = [
    ("M59",
     """WHERE public.assets.last_seen_at < now() - ($12 * interval '1 second')
   OR (public.assets.tenant_id,    public.assets.name,        public.assets.owner,
       public.assets.team,         public.assets.cost_center, public.assets.tags,
       public.assets.health_state, public.assets.version,     public.assets.metadata)
      IS DISTINCT FROM
       (EXCLUDED.tenant_id,       EXCLUDED.name,        EXCLUDED.owner,
        EXCLUDED.team,            EXCLUDED.cost_center, EXCLUDED.tags,
        EXCLUDED.health_state,    EXCLUDED.version,     EXCLUDED.metadata)""",
     "",
     "both",
     "删掉整个 WHERE 条件 ⇒ 回到病根：每天 924.9 万次无变化行重写。"
     "文本门（找不到 WHERE）与语义门（S2 相同内容被重写）都必须红"),

    ("M60",
     "IS DISTINCT FROM",
     "<>",
     "both",
     "用 `<>` 代替 IS DISTINCT FROM。owner 'alice'→NULL 时 `('alice') <> NULL` "
     "求值为 NULL 而非 true ⇒ 整行条件为 NULL ⇒ 不更新 ⇒ **变更被静默吞掉**。"
     "★ 原以为文本门抓不到（它只是找子串），实测文本门**也**红了 —— "
     "因为子串 'IS DISTINCT FROM' 真的消失了。分类写错的是我，不是门。"
     "两个门都抓得到，但这不代表文本门够用：见 M64"),

    ("M61",
     "WHERE public.assets.last_seen_at < now() - ($12 * interval '1 second')\n   OR (public.assets.tenant_id,",
     "WHERE (public.assets.tenant_id,",
     "both",
     "★ 只删掉「心跳已过期」那一半，保留「业务字段真变了」。"
     "这是**看起来更安全**的改法（只有真变了才写），但会让 last_seen_at "
     "永远停在插入那一刻 ⇒ 6 小时后全部资产被误判为已消失。"
     "★ 原以为文本门抓不到，实测也红了（子串 'public.assets.last_seen_at < now()' "
     "消失了）。同样是我分类写错。S4 抓的是**行为**、文本门抓的是**存在性**，"
     "两者在 M59/M61 里恰好重合，直到 M64 才分道扬镳"),

    ("M62",
     "assetHeartbeatRefreshInterval = 5 * time.Minute",
     "assetHeartbeatRefreshInterval = 0 * time.Minute",
     "both",
     "窗口设成 0 ⇒ 条件 `last_seen_at < now()` 恒真 ⇒ 等于没加门控。"
     "★ 注意这不是「0 分钟」而是「永远刷新」—— 语义完全反了，"
     "而文本门若只看子串存在就会放过它；契约门靠钉住取值抓住，真库门靠 S2 抓住"),

    ("M64",
     ")\n   OR (public.assets.tenant_id,",
     ")\n   AND (public.assets.tenant_id,",
     "real",
     "★★ 证明语义门**不可替代**的那一条：把 OR 改成 AND。"
     "文本门检查的每一个子串（public.assets.last_seen_at / IS DISTINCT FROM / "
     "EXCLUDED.metadata / public.assets.name …）**全都还在** ⇒ 文本门必然绿。"
     "但语义整个反了：只有「心跳过期」**且**「字段变了」才更新 ⇒ "
     "S3（改了 name 但心跳未过期）不再落库。"
     "★ 也就是说：文本门能证明条件块**存在**，语义门才能证明它**对**。"),
    ("M63",
     "assetHeartbeatRefreshInterval = 5 * time.Minute",
     "assetHeartbeatRefreshInterval = 1 * time.Minute",
     "text",
     "把窗口从 5 分钟改成 1 分钟。功能完全正常、门控照常生效 —— "
     "这正是它**危险**的地方：只有钉住取值本身的契约门能发现它。"
     "改它等于把 stale 判定误差从 1.4% 提到 16.7%，属于契约变更不该顺手做"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd, **kw):
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True,
                          text=True, errors="replace", **kw)


def gate_text():
    return run(f"go test ./apihub/ -run '{CONTRACT}' -count=1")


def gate_real():
    return run("bash scripts/.asset-heartbeat-realdb.sh")


def main():
    orig = md5(GO_SRC)
    failures = []
    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]

    for mid, old, new, kind, desc in selected:
        body = open(GO_SRC, encoding="utf-8").read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，变异脚本自身失效")
            failures.append(f"{mid}: anchor not found")
            continue
        open(GO_SRC, "w", encoding="utf-8").write(body.replace(old, new, 1))
        try:
            b = run("go build ./apihub/")
            if b.returncode != 0:
                print(f"[{mid}] BUILD-BROKEN 不算证据")
                failures.append(f"{mid}: build broken")
                continue
            t = gate_text()
            # 文本门跑了多少条 —— 0 条说明 -run 没匹配上，不可判定
            tn = run(f"go test ./apihub/ -run '{CONTRACT}' -v -count=1")
            ran_t = (tn.stdout + tn.stderr).count("=== RUN")
            if ran_t == 0:
                print(f"[{mid}] 文本门跑了 0 条 ⇒ 不可判定")
                failures.append(f"{mid}: text gate 0 tests")
                continue
            red_t = t.returncode != 0

            r = gate_real()
            out = r.stdout + r.stderr
            ran_r = 0
            for line in out.splitlines():
                if line.startswith("ran="):
                    ran_r = int(line.split()[0].split("=")[1])
            if r.returncode == 3 or ran_r == 0:
                print(f"[{mid}] 真库门不可用（量具坏了，不是被测对象红）：{out.strip()[:160]}")
                failures.append(f"{mid}: real gate unusable")
                continue
            red_r = r.returncode != 0

            want = {"both": (red_t and red_r),
                    "real": (red_r and not red_t),
                    "text": (red_t and not red_r)}[kind]
            # 额外要求：标 text 的变异，真库门**允许**绿，但不允许不可用
            if want:
                print(f"[{mid}] RED ✓  文本门 red={red_t}(ran={ran_t})  真库门 red={red_r}(ran={ran_r})  {desc}")
            else:
                print(f"[{mid}] GREEN ✗ 无牙  文本门 red={red_t}(ran={ran_t})  "
                      f"真库门 red={red_r}(ran={ran_r})  {desc}")
                failures.append(f"{mid}: target gate stayed green")
        finally:
            open(GO_SRC, "w", encoding="utf-8").write(body)

    ok = md5(GO_SRC) == orig
    print(f"\n--- 逐字节还原 ---\n{'OK  ' if ok else 'FAIL'} pg_store.go {orig[:12]}")
    if not ok:
        failures.append("pg_store.go not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
