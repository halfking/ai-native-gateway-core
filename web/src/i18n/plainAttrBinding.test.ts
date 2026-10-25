// plainAttrBinding.test.ts — 「普通属性里放 i18n 调用」的门（2026-10-25 新增）
//
// 判的是什么：**Vue 3 的普通属性不做插值**。
//   <span title="$t('common.close')">  →  title 的值是字符串 "$t('common.close')"
//   <span :title="$t('common.close')"> →  绑定形式才求值
//
// 它**不报错**：编译过、vue-tsc 过、运行时也过，
// 只是用户看到的 tooltip / placeholder 变成字面量。
// ★ 而且字面量比原文长得多 ⇒ 还会把版面撑宽。
//
// 这道门的存在理由还有一个更近的：**上一轮在模板里插了 563 个 `$t()`** ——
// 任何一处落进普通属性，都会静默地把键路径显示给用户，而且没有门会报。
//
// 纪律（沿用仓内 i18n 门的既有约定）：
//   ① 判定层必须**双向自证**（会触发 + 不误触发各半）——恒绿门与恒红门同样没用。
//   ② 必须有**非空下限**——「一个属性都没扫到」也是绿的（假绿）。
//   ③ 阴性结论（0 违例）要当缺陷一样复核——先怀疑量具坏了。

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  scanPlainAttrBindingsInDir,
  scanPlainAttrBindingsWithStats,
  listVueFiles,
  selfCheckPlainAttrBinding,
} from './plainAttrBinding'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const SRC_DIR = join(__dirname, '..')

describe('i18n 普通属性插值（plainAttrBinding）', () => {
  it('判定层自证 10 条双向全过（量具必须有牙）', () => {
    const cases = selfCheckPlainAttrBinding()
    const failed = cases.filter((c) => !c.ok)
    expect(
      failed.map((c) => `${c.id} ${c.desc} — ${c.actual}`),
      '判定层自证失败',
    ).toEqual([])
    expect(cases.length).toBeGreaterThanOrEqual(10)
  })

  it('★ 硬闸：普通属性里不得出现 i18n 调用或 mustache 插值', () => {
    const hits = scanPlainAttrBindingsInDir(SRC_DIR)
    expect(
      hits.map((h) => `${h.file}:${h.line} ${h.attr}="${h.value}"（${h.reason}）`),
      `普通属性里的 i18n 调用会被 Vue 3 当静态字符串渲染（${hits.length} 处）：\n` +
        hits.map((h) => `  ${h.file}:${h.line}  ${h.attr}="${h.value}"`).join('\n'),
    ).toEqual([])
  })

  it('★ 非空下限：必须真的扫到了大量 .vue 文件与属性（否则「什么都没量」也是绿的）', () => {
    const files = listVueFiles(SRC_DIR)
    expect(files.length, '.vue 文件数').toBeGreaterThan(150)
    let attrsScanned = 0
    for (const f of files) {
      attrsScanned += scanPlainAttrBindingsWithStats(readFileSync(f, 'utf8'), f).attrsScanned
    }
    // ★ 这是「量具真的在干活」的证据：模板里的属性值被逐个解析过。
    expect(attrsScanned, '扫过的属性数').toBeGreaterThan(3000)
  })
})
