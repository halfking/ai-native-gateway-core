#!/usr/bin/env python3
"""simple-overview-mutation.py — 简易总览页判据的变异自证（2026-11-17）

# 这道脚本证明什么

**判据有牙**：把「简易总览页」的判据逐条改坏，钉测必须**变红**。
改完还原，必须**变绿**。
⇒ 「测试全绿」本身不是证据；「把判据改坏它就红」才是。

# 为什么不用 shell 做替换

shell 做「在唯一锚点处改坏 + 一字不差地还原」要面对 tab / 引号 / 反斜杠 / 换行的
四重转义地狱 —— 本仓 `scripts/audit/_mutate_helper.py` 的文件头已经把这三种踩坑
写清楚了（grep -c 按行计数会虚高命中数；锚点含真实 tab 写不出来；替换物含 `\\$1`
会让代码编译红 ⇒ **编译红不算数**，只说明代码坏了，不说明判据有牙）。
⇒ 本脚本用 python 做替换，沿用同一套纪律：
  **锚点必须恰好命中 1 次**，命中数不是 1 ⇒ 立即失败并退出（**假阴性不许混过去**）。

# 用法

    python3 scripts/audit/simple-overview-mutation.py

退出码 0 = 全部变异都被钉测抓到；非 0 = 有变异没被抓到（判据没牙）。
"""

import io
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
WEB = os.path.join(ROOT, 'web')
TARGET = os.path.join(WEB, 'src', 'composables', 'simpleOverviewModel.ts')
TEST = 'src/composables/simpleOverviewModel.test.ts'

# ── 变异清单 ────────────────────────────────────────────────────────────────
# (编号, 说明, 锚点, 替换物, 必须失败的用例名关键字)
MUTATIONS = [
    (
        'M1',
        '上游没有凭据也算「可用」（把「没探活」并进健康）',
        """    if (credentialCount === 0) {
      // 没有凭据 ⇒ 从没接通过，不是「不健康」，是「还没接」
      unknown++
      rows.push(row)
      continue
    }""",
        """    if (credentialCount === 0) {
      usable++
      row.tone = 'ok'
      rows.push(row)
      continue
    }""",
        'never counts a provider without credentials as healthy',
    ),
    (
        'M2',
        '「读不到上游状态」被当成「没有可用上游」（读不到 ⇒ blocked）',
        """    issues.push('provider_status_unavailable')""",
        """    issues.push('no_usable_provider')""",
        'treats an unreadable upstream status as unknown',
    ),
    (
        'M3',
        '状态点的可信度恒为真（读不到也宣称正常）',
        """    confident: !providerHealth.unavailable && !(statsDegraded || statsMissing),""",
        """    confident: true,""",
        'treats an unreadable upstream status as unknown',
    ),
    (
        'M4',
        '统计降级时用 0 顶替（把「读不到」渲染成 0）',
        """  const s = degraded ? null : summary""",
        """  const s = summary""",
        'blanks every number when the summary is degraded',
    ),
    (
        'M5',
        '自检失败不再计入异常',
        """      issues.push('selfcheck_failed')""",
        """      void 0""",
        'warns on unreachable upstreams and on a failed self check',
    ),
    (
        'M6',
        '「已接入客户端」用「已签发密钥数」代替「真的被调用过」',
        """    {
      key: 'client',
      done: keysInUse > 0,
      count: keysInUse,
    },""",
        """    {
      key: 'client',
      done: keysEnabled > 0,
      count: keysInUse,
    },""",
        'is not complete while a key exists but no client calls the gateway',
    ),
    (
        'M7',
        '千分位分组失效',
        """    if (i > 0 && (s.length - i) % 3 === 0) out += ','""",
        """    if (false && i > 0 && (s.length - i) % 3 === 0) out += ','""",
        'groups thousands and keeps the sign',
    ),
    (
        'M8',
        '美元金额改成截断（四舍五入变截断）',
        """  const cents = Math.round(Math.abs(v) * 100)""",
        """  const cents = Math.floor(Math.abs(v) * 100)""",
        'renders usd with two decimals',
    ),
    (
        'M9',
        '「不可达」被并进「有告警」（严重度降级）',
        """      case 'unreachable':
      case 'manual_disabled':
        unreachable++
        row.tone = 'danger'
        break""",
        """      case 'unreachable':
      case 'manual_disabled':
        warning++
        row.tone = 'warn'
        break""",
        'separates usable from warning, unreachable and unknown',
    ),
    (
        'M10',
        '引导卡永远判定为「已完成」（引导不再消失）',
        """  return steps.length > 0 && steps.every((s) => s.done)""",
        """  return true""",
        'keeps the steps pending when nothing is connected yet',
    ),
]


def read(path):
    with io.open(path, encoding='utf-8') as fh:
        return fh.read()


def write(path, text):
    with io.open(path, 'w', encoding='utf-8') as fh:
        fh.write(text)


def run_test():
    proc = subprocess.run(
        ['npx', 'vitest', 'run', TEST],
        cwd=WEB,
        capture_output=True,
        text=True,
    )
    return proc.returncode, proc.stdout + proc.stderr


def main():
    original = read(TARGET)

    print('=== 负控：未变异时必须全绿 ===')
    rc, out = run_test()
    if rc != 0:
        print('❌ 基线就是红的，变异自证无从谈起：')
        print(out[-3000:])
        return 2
    print('✅ 基线绿')

    failures = []
    for mid, desc, anchor, repl, expect in MUTATIONS:
        hits = original.count(anchor)
        if hits != 1:
            print('❌ %s 锚点命中 %d 次（必须恰好 1 次）⇒ 变异压根没发生，判定为假阴性，不算数'
                  % (mid, hits))
            failures.append(mid)
            continue

        write(TARGET, original.replace(anchor, repl, 1))
        rc, out = run_test()
        red = rc != 0
        caught = red and expect in out
        if caught:
            print('✅ %s 变异后测试变红（%s）' % (mid, desc))
        else:
            print('❌ %s 变异后测试%s ⇒ 判据没牙（%s）' % (
                mid, '仍绿' if not red else '红了但失败的不是目标用例', desc))
            failures.append(mid)

        write(TARGET, original)
        rc2, _ = run_test()
        if rc2 != 0:
            print('❌ %s 还原后测试不绿 ⇒ 还原失败' % mid)
            failures.append(mid + '-restore')

    print()
    if failures:
        print('❌ %d/%d 个变异没被钉测抓到：%s' % (len(failures), len(MUTATIONS), ', '.join(failures)))
        return 1
    print('✅ %d/%d 个变异全部被钉测抓到，还原后全绿' % (len(MUTATIONS), len(MUTATIONS)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
