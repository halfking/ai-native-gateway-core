#!/usr/bin/env python3
"""payload 膨胀巡检脚本的行为门变异验证。

门是 Go 测试，被测对象是 shell 脚本。纪律同前：
  1. 变异后逐字节还原（md5）
  2. 必须让**目标门**转红
  3. 打出实际跑的用例数；0 条直接判为脚本错误
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(REPO, "scripts/252-monitor/ursm-snapshot-payload-bloat.sh")

MUTATIONS = [
    ("M39",
     '[ "$n" -gt 0 ]    || { echo "ABORT: 样本为 0 行（表空？）—— 没有参照系，本次不出结论。" >&2; exit 3; }',
     '[ "$n" -ge 0 ]   || { echo "ABORT: 样本为 0 行（表空？）—— 没有参照系，本次不出结论。" >&2; exit 3; }',
     "TestBloatScriptZeroRowsIsNoConclusionNotHealthy",
     "★ 去掉空样本守卫 ⇒ 表被清空时报「健康」。这正是 go-cache-guard 事故的同款形态："
     "「查不到」被记成「没问题」，天天报绿"),

    ("M40",
     '[ "$nfield" = "4" ] || { echo "ABORT: 期望 4 个字段，实得 $nfield" >&2; exit 3; }',
     '[ "$nfield" -ge 1 ] || { echo "ABORT: 期望 4 个字段，实得 $nfield" >&2; exit 3; }',
     "TestBloatScriptWrongFieldCountAborts",
     "放松字段数校验 ⇒ psql 输出意外时下游把缺失字段当 0 ⇒ 报 OK"),

    ("M41",
     'if [ "$newest_age" -gt "$STALE_SECONDS" ] 2>/dev/null; then',
     'if false; then',
     "TestBloatScriptStaleSampleIsNoConclusion",
     "去掉样本新鲜度守卫 ⇒ 写入停了之后仍拿历史数据当现状报"),

    ("M42",
     'BLOAT_BYTES=${BLOAT_BYTES:-64}',
     'BLOAT_BYTES=${BLOAT_BYTES:-64000}',
     "TestBloatScriptBloatExitsOne",
     "阈值放大到永不触发 ⇒ 膨胀永远检不出，脚本退化成永远 OK"),

    ("M43",
     'if [ "$p95_b" -le "$BLOAT_BYTES" ]; then',
     'if [ "$p95_b" -le "$BLOAT_BYTES" ] || grep -q "updated_at_ms" /dev/null; then',
     "TestBloatScriptVerdictDoesNotDependOnKeyNames",
     "让判据开始看键名 ⇒ 键名一漂就静默恒绿（该门钉住的就是这一点）"),

    # ------------------------------------------------------------------
    # --footprint 模式（M48~M58）
    # ------------------------------------------------------------------
    ("M48",
     'n_live_tup=${fr_live}）——"',
     'n_live_tup=$fr_live）——"',
     "TestFootprintModeUnreliableDenominatorIsNoConclusion",
     "★ 原样复原开发期真抓到的那个 bug：`$fr_live` 紧跟全角「）」时 bash 把括号"
     "高位字节吞进变量名 ⇒ set -u 报错 ⇒ 整个 ABORT 分支不执行 ⇒ "
     "把「量具坏了」报成「行宽偏高」(exit 1)。门必须对此有牙"),

    ("M49",
     '[ "$fr_n" -gt 0 ] || { echo "ABORT: 表里 0 行',
     '[ "$fr_n" -ge 0 ] || { echo "ABORT: 表里 0 行',
     "TestFootprintModeEmptyTableNotOutrankedByPlausibleDownstreamNumbers",
     "去掉空表守卫 ⇒ 行数为 0 但下游数字看着正常时照样出结论。"
     "★ 输入选得很讲究：必须让**两道守卫只有这一道**能拦住。"
     "最初用 `0|0|0 kB|...` 时分母守卫把空表守卫顶住了 ⇒ 门照样绿 ⇒ "
     "看起来像「空表守卫无用」，其实只是两道在这个输入上重叠（等价于只有一道）"),

    ("M50",
     'if [ "$fr_bpr" -le 0 ] || [ "$fr_live" -le 0 ]; then',
     'if [ "$fr_bpr" -le 0 ]; then',
     "TestFootprintModeUnreliableDenominatorIsNoConclusion",
     "去掉 n_live_tup 一致性校验 ⇒ 统计采集器没跟上时（n_live_tup=0）仍给出"
     "「每行字节巨大 ⇒ 偏高 ⇒ 有空闲可回收」的**误导性结论**"),

    ("M51",
     "'BEGIN{exit !(d<=t)}'; then",
     "'BEGIN{exit !(d>=t)}'; then",
     "TestFootprintModeInToleranceExitsZero",
     "容差判定反转 ⇒ 正常态被报成偏离、异常态被报成 OK（判定整体失效）"),

    ("M52",
     '  echo "                    偏高 ⇒ 堆里有可观空闲，825 的回收价值比 §12.6 估的大"',
     '  echo "                    （方向解释已移除）"',
     "TestFootprintModeHighBytesIsDeviation",
     "去掉「偏高」这一侧的解读 ⇒ 偏离方向压不成可执行的结论，人只知道"
     "「偏了」却不知道该做什么（偏低不是坏事、偏高才是）"),

    ("M53",
     '  echo "                    偏低 ⇒ 行宽比解析值更小（例如 payload 进一步瘦身），不是坏事"\n  exit 1',
     '  echo "                    偏低 ⇒ 行宽比解析值更小（例如 payload 进一步瘦身），不是坏事"\n  exit 0',
     "TestFootprintModeLowBytesIsAlsoDeviationButNotBad",
     "偏离报 OK ⇒ cron 收到 exit 0 就当健康，行宽异常悄悄长期漂移"),

    ("M54",
     '''"$([ "$fr_ispart" = 1 ] && echo "分区父表（$fr_parts 个分区，825 已落地）" || echo "普通表（825 未落地）")"''',
     '''"普通表（825 未落地）"''',
     "TestFootprintModeReportsPartitionedShape",
     "抹掉分区形态读数 ⇒ 825 落地后仍报「普通表」，这条命令就失去了"
     "「核验 825 是否真的生效」的唯一手段"),

    ("M55",
     '''    echo "ABORT: 占用核验查询失败: ${fr//$'\\n'/ }" >&2
    exit 3''',
     '''    echo "ABORT: 占用核验查询失败: ${fr//$'\\n'/ }" >&2
    exit 1''',
     "TestFootprintModeQueryFailureAborts",
     "查询失败从 3 塌成 1 ⇒ 「量具不可用」与「检出偏离」不可区分。"
     "cron 里两者都非 0，人分不出该修数据还是修查询（同 M39 的纪律）"),

    ("M56",
     """fr_parts=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $6}')""",
     """fr_parts=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $5}')""",
     "TestFootprintModeReportsPartitionedShape",
     "字段解析错位一格 ⇒ 分区数读成 ispart 标志（恒为 0/1），"
     "「8 个分区」会被报成「0 个分区」或「1 个分区」"),

    ("M57",
     'if strings.Contains(combined, "偏高") || strings.Contains(combined, "偏低") {',
     'if strings.Contains(out, "偏高") || strings.Contains(out, "偏低") {',
     "TestFootprintModeUnreliableDenominatorIsNoConclusion",
     "★ 单独回退「不得给出方向判定」这一条 ⇒ 它改查 stdout（此路径下为空串）"
     "⇒ 恒过、**无牙**：脚本哪天真的打出「偏高」也照样绿。"
     "只回退这一条、不动「必须解释」那条，才知道是哪条没牙",
     "scripts/ursmcheck/ursm_snapshot_payload_bloat_test.go", "GREEN"),

    ("M58",
     '''    echo "ABORT: 行数估算不可用（reltuples 推出的每行字节=${fr_bpr}, n_live_tup=${fr_live}）——" >&2
    echo "      分母不可信时每行字节是除零产物，本次不出结论。" >&2
    echo "      处理：ANALYZE public.ursm_node_snapshot_min; 后重跑。" >&2
''','''    echo "静默退出" >&2
''',
     "TestFootprintModeUnreliableDenominatorIsNoConclusion",
     "让脚本在判「没有结论」时**什么都不解释** ⇒ 「必须解释为什么没有结论」那条"
     "必须抓到。★ 这条同时推翻了本轮最初的猜测：我原以为断言 2 的牙也来自"
     "「查 stdout+stderr 合并流」，实测它改成查 stdout 后是**恒红**（!Contains "
     "在空串上为 true）⇒ 天生有牙。只有「不得给出方向判定」那条（M57）的牙"
     "才真正来自合并流。两条断言的来源必须分开验，整块换掉只会看红绿、归因归错"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd):
    # errors="replace" 不是可选的礼貌参数：M48 那条变异会让 bash 打出
    # "fr_live<半个字符>: 未绑定的变量"，其中含非 UTF-8 字节。
    # 严格解码会在这里抛 UnicodeDecodeError，**把变异脚本自己打断** ——
    # 于是「有牙的那条」根本跑不到结论，只留下一个 traceback。
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True,
                          text=True, errors="replace")


def main():
    orig = {SCRIPT: md5(SCRIPT)}
    failures = []
    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]

    for mid, old, new, target, desc, *rest in selected:
        # 可选的第 6 位指定被改动的文件；默认改被测脚本。
        # 有些缺陷只存在于**判据本身**（例如断言查了错误的输出流），
        # 那种变异必须改测试文件才验得到 —— 否则「门本身无牙」这件事永远测不出。
        path = os.path.join(REPO, rest[0]) if rest else SCRIPT
        # 第 7 位 "GREEN" 表示**期望门变绿**：这类变异不是"破坏实现看门会不会抓"，
        # 而是"回退判据自身的一个决定，看那条断言是否随之失去作用"。
        # 它们成功的样子就是绿 —— 门绿着，正说明那条断言没有牙。
        # 方向搞反会把「门确实无牙」报成「变异失败」，等于把量具的缺陷藏起来。
        expect_green = len(rest) > 1 and rest[1] == "GREEN"
        with open(path, encoding="utf-8") as f:
            body = f.read()
        orig.setdefault(path, md5(path))
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，脚本自身失效：{desc}")
            failures.append(f"{mid}: anchor not found")
            continue
        with open(path, "w", encoding="utf-8") as f:
            f.write(body.replace(old, new, 1))
        try:
            if path == SCRIPT:
                b = run("bash -n scripts/252-monitor/ursm-snapshot-payload-bloat.sh")
                if b.returncode != 0:
                    print(f"[{mid}] SYNTAX-BROKEN 不算证据：{desc}")
                    failures.append(f"{mid}: syntax broken")
                    continue
            else:
                b = run("go vet ./scripts/ursmcheck/")
                if b.returncode != 0:
                    print(f"[{mid}] VET-BROKEN 不算证据：{desc}")
                    failures.append(f"{mid}: vet broken")
                    continue
            r = run(f"go test ./scripts/ursmcheck/ -run {target} -v -count=1")
            out = r.stdout + r.stderr
            ran = out.count("=== RUN")
            if ran == 0:
                print(f"[{mid}] 跑了 0 条 ⇒ 不可判定")
                failures.append(f"{mid}: 0 tests ran")
                continue
            red = r.returncode != 0
            if expect_green:
                ok = not red
                print(f"[{mid}] {'确认无牙 ✓' if ok else '意外变红 ✗ 该断言其实有牙'}  ran={ran}  {desc}")
                if not ok:
                    failures.append(f"{mid}: expected green (no teeth) but gate turned red")
            else:
                print(f"[{mid}] {'RED ✓' if red else 'GREEN ✗ 无牙'}  ran={ran}  {desc}")
                if not red:
                    failures.append(f"{mid}: gate stayed green")
        finally:
            with open(path, "w", encoding="utf-8") as f:
                f.write(body)

    for path, digest in orig.items():
        ok = md5(path) == digest
        print(f"\n--- 逐字节还原 ---\n{'OK  ' if ok else 'FAIL'} "
              f"{os.path.basename(path)} {digest[:12]}")
        if not ok:
            failures.append(f"{os.path.basename(path)} not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
