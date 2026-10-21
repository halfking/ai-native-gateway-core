#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.59 登记册新门的变异测试：每条变异都必须让门**变红**。

纪律（来自仓内既有教训）：
  · 文件级备份 + md5 逐字还原校验
  · 变异必须**编译干净**（编译红不算"门有牙"，那条变异要重做）
  · needle 必须唯一；0 命中 = 失败（SKIP）
"""
import hashlib
import os
import re
import shutil
import subprocess
import sys

ROOT = "/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4"
GATE_FILE = os.path.join(ROOT, "admin/section_14359_retention_registry_test.go")
TSV_FILE = os.path.join(ROOT, "scripts/audit/retention_shape_findings.tsv")
BACKUP = "/private/tmp/p14359/mut_backup"

RUN = ["go", "test", "./admin/", "-run", "TestSection14343|TestSection14359", "-count=1"]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def snapshot():
    os.makedirs(BACKUP, exist_ok=True)
    for p in (GATE_FILE, TSV_FILE):
        shutil.copy2(p, os.path.join(BACKUP, os.path.basename(p)))


def restore():
    for p in (GATE_FILE, TSV_FILE):
        shutil.copy2(os.path.join(BACKUP, os.path.basename(p)), p)


def run_gate():
    r = subprocess.run(RUN, cwd=ROOT, capture_output=True, text=True, timeout=900)
    return r.returncode, r.stdout + r.stderr


def compile_ok():
    r = subprocess.run(["go", "vet", "./admin/"], cwd=ROOT, capture_output=True, text=True, timeout=900)
    return r.returncode == 0, r.stdout + r.stderr


def apply(path, old, new, count=1):
    s = open(path, encoding="utf-8").read()
    n = s.count(old)
    if n != count:
        raise SystemExit("needle 命中 %d 次（期望 %d）⇒ 失败：%r" % (n, count, old[:80]))
    open(path, "w", encoding="utf-8").write(s.replace(old, new, count))


MUTATIONS = [
    ("M1", "量级区间判据恒真（in 永远返回 true）", GATE_FILE,
     """	if b.Lo == 0 {
		return v == 0
	}
	return v > b.Lo && v <= b.Hi""",
     """	return true"""),
    ("M2", "相对量分母算错（permille 恒 0）", GATE_FILE,
     """	return bytes * 1000 / total""",
     """	return 0"""),
    ("M3", "登记册删掉第一大对象的行（route_incident_events / 252）", TSV_FILE,
     """252\tRETENTION_GAP_OBJECT\tpublic.route_incident_events\tr\t1832998""",
     """252\tRETENTION_GAP_OBJECT\tpublic.route_incident_events\tr\t0"""),
    ("M4", "机制形态被改写（NOT_WIRED ⇒ TARGET_EMPTY）", TSV_FILE,
     """1213685760\t0\tno\tNOT_WIRED\tMECHANISM_NOT_WIRED""",
     """1213685760\t0\tno\tTARGET_EMPTY\tMECHANISM_TARGET_EMPTY"""),
    ("M5", "读数日期戳被改旧（2026-10-22 ⇒ 2020-01-01）", TSV_FILE,
     """# reading-date: 2026-10-22""",
     """# reading-date: 2020-01-01"""),
    ("M6", "★ 解析【真实路径】恒真（registryReadingDate 永远返回今天）", GATE_FILE,
     """		parsed, val, ok := parseReadingStamp(line)
		if !ok {
			continue
		}
		return parsed, val""",
     """		parsed, val, ok := parseReadingStamp(line)
		if !ok {
			continue
		}
		_ = parsed
		return time.Now(), val"""),
    ("M7", "陈旧度判据恒真（永远判未过期）", GATE_FILE,
     """	return int64(today.Sub(readDate).Hours() / 24)""",
     """	return 0"""),
]


def main():
    snapshot()
    base = {p: md5(p) for p in (GATE_FILE, TSV_FILE)}
    rc, out = run_gate()
    if rc != 0:
        print("基线不绿（exit=%d）⇒ 停：\n%s" % (rc, out[-3000:]))
        return 1
    print("基线：绿")

    results = []
    for name, desc, path, old, new in MUTATIONS:
        restore()
        try:
            apply(path, old, new)
        except SystemExit as e:
            print("%s %-46s ⇒ 失败：%s" % (name, desc, e))
            results.append((name, desc, "NEEDLE_FAIL"))
            continue
        if path == GATE_FILE:
            ok, out = compile_ok()
            if not ok:
                print("%s %-46s ⇒ 编译红 ⇒ 该条变异必须重做" % (name, desc))
                results.append((name, desc, "COMPILE_RED"))
                restore()
                continue
        rc, out = run_gate()
        if rc == 0:
            print("%s %-46s ⇒ ★ 门仍然绿 ⇒ 门没有牙" % (name, desc))
            results.append((name, desc, "STILL_GREEN"))
        else:
            head = [l for l in out.splitlines() if "FAIL" in l or "--- FAIL" in l]
            print("%s %-46s ⇒ 变红 ✓  %s" % (name, desc, (head[0] if head else "")[:120]))
            results.append((name, desc, "RED"))
        restore()

    # 还原校验：md5 必须与基线逐字一致。
    ok = True
    for p in (GATE_FILE, TSV_FILE):
        if md5(p) != base[p]:
            print("还原失败：%s md5 %s != 基线 %s" % (p, md5(p), base[p]))
            ok = False
    print("还原校验：%s" % ("md5 逐字一致 ✓" if ok else "★ 不一致"))
    rc, out = run_gate()
    print("还原后基线：%s" % ("绿 ✓" if rc == 0 else "★ 红"))
    bad = [r for r in results if r[2] != "RED"]
    print("汇总：%d 条变异，%d 条变红，异常 %d 条" % (len(results), len(results) - len(bad), len(bad)))
    for b in bad:
        print("  ★ %s %s ⇒ %s" % (b[0], b[1], b[2]))
    return 0 if (ok and rc == 0 and not bad) else 1


if __name__ == "__main__":
    sys.exit(main())
