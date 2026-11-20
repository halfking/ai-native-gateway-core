// HelpManualView.test.ts — 帮助手册页的装配与覆盖度钉测。
//
// 这道门守两件事：
//   ① 手册必须覆盖约定的主题章节（下载/安装/部署/供应商/凭据/租户/用户/
//      API Key/客户端调用/统计与分析/常见故障/数据重置）—— 少一章即红；
//   ② 手册页真的把正文渲染出来（标题、目录、代码块、表格），且搜索能筛。
// 另外单独钉住「安装后的激活」与「数据重置」两节的关键契约（端点、令牌、
// 保留谓词），避免日后改写手册时把可执行的操作细节删掉。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import HelpManualView from './HelpManualView.vue'
import { testI18n } from '../test-utils/i18n'
import { parseMarkdown } from '../lib/markdown'
import manualSource from '../../content/help-manual.zh-CN.md?raw'

/** 手册必须覆盖的十一个主题（顺序即目录顺序）。 */
const REQUIRED_CHAPTERS = [
  '1. 下载',
  '2. 安装',
  '3. 部署',
  '4. 供应商配置',
  '5. 凭据管理',
  '6. 租户管理',
  '7. 用户管理',
  '8. API Key 设置',
  '9. 客户端调用',
  '10. 统计与分析',
  '11. 常见故障处理',
  '12. 数据重置',
]

const chapters = parseMarkdown(manualSource)
  .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 2)
  .map((b) => b.text)

describe('帮助手册正文覆盖度', () => {
  it('包含约定的十一个主题章节', () => {
    for (const chapter of REQUIRED_CHAPTERS) {
      expect(chapters).toContain(chapter)
    }
  })

  it('章节顺序与目录顺序一致', () => {
    const positions = REQUIRED_CHAPTERS.map((c) => chapters.indexOf(c))
    expect(positions.every((p) => p >= 0)).toBe(true)
    const sorted = [...positions].sort((a, b) => a - b)
    expect(positions).toEqual(sorted)
  })

  it('每个主题章节下都有子章节（不是空壳标题）', () => {
    const blocks = parseMarkdown(manualSource)
    let current = ''
    const childCounts = new Map<string, number>()
    for (const block of blocks) {
      if (block.kind !== 'heading') continue
      if (block.level === 2) {
        current = block.text
        childCounts.set(current, 0)
      } else if (current) {
        childCounts.set(current, (childCounts.get(current) ?? 0) + 1)
      }
    }
    for (const chapter of REQUIRED_CHAPTERS) {
      expect(childCounts.get(chapter) ?? 0).toBeGreaterThan(0)
    }
  })

  it('正文含代码块、表格与可复制的调用示例', () => {
    const kinds = new Set(parseMarkdown(manualSource).map((b) => b.kind))
    expect(kinds.has('code')).toBe(true)
    expect(kinds.has('table')).toBe(true)
    expect(kinds.has('list')).toBe(true)
  })
})

/**
 * 新增两节的结构钉：post-install activation (2.6) 与 data reset (ch. 12)。
 *
 * ★ 断言一律用 ASCII 锚点（端点路径 / 标题前缀 / 计数），**不写中文字面量**：
 *   src/** 的硬编码中文计数是「只许降不许升」的棘轮门（hardcoded_cjk），
 *   新增中文字面量会把棘轮顶高 ⇒ 那是本仓不允许的「借测试夹带文案」。
 *   章节存在性用「前缀 + 计数」钉，操作契约用「端点路径 / 关键字」钉。
 */
const ACTIVATION_SECTION_PREFIX = '2.6'
const DATA_RESET_CHAPTER_PREFIX = '12.'
const DATA_RESET_MIN_SECTIONS = 7

/** 数据重置三步端点 + 令牌 + 保留谓词 + 审计留痕（全部 ASCII）。 */
const DATA_RESET_CONTRACT = [
  '/api/admin/data-lifecycle/data-init/catalog',
  '/api/admin/data-lifecycle/data-init/preview',
  '/api/admin/data-lifecycle/data-init/execute',
  'confirm_token',
  'token_expires_at',
  'target_rows',
  'rows_deleted',
  'is_system = true',
  'super_admin',
  'routing_audit_log',
  'data_init_reset',
]

/** 安装后激活：首启端点 + 命令行四种模式（全部 ASCII）。 */
const ACTIVATION_CONTRACT = [
  '/api/system/bootstrap/status',
  '/api/system/bootstrap/fingerprint',
  '/api/system/bootstrap/activate',
  '/api/system/bootstrap/activate-quick',
  '/api/system/bootstrap/register-center',
  '/api/system/bootstrap/import-offline',
  '--mode trial',
  '--license-key',
  'offline-request',
  'offline-import',
  'LLM_GATEWAY_LICENSE_ACTIVATED',
]

/**
 * 排障章节（11.4 请求失败 / 11.5 模型不可访问）的契约锚点。
 *
 * ★ 同样只用 ASCII：端点路径、读面视图名、诊断日志标记、CLI 工具名。
 * ★ 钉的是「可执行细节不许被改写掉」——这些是值班真正要敲的东西。
 */
const TROUBLESHOOT_CONTRACT = [
  // 请求级与候选级读面
  'request_logs_with_current_month',
  'candidate_failure_logs_with_current_month',
  'node_probe_runs',
  'failure_detail_code',
  'error_kind',
  'failure_stage',
  // 错误信封与响应头
  'X-Gateway-Last-Kind',
  'gateway_debug',
  'invalid_model',
  'modality_mismatch',
  'no_candidate',
  'model_not_found',
  'routing_client_canceled',
  'routing_circuit_open',
  'routing_schema_error',
  // 零候选归因日志与诊断端点
  '[candidate_diag] db_empty',
  'likely_cause',
  '/api/routing/resolve?model=',
  '/api/routing/health',
  '/api/logs/top-problems',
  '/api/admin/diagnostics/routing-blocked',
  // 现成排障命令与只读脚本
  'probe-cred',
  'verify-model-fetch',
  'check-credentials',
  'routing-test-client',
  'auto-testbench',
  'q-211-readonly-sql.sh',
  'api-211-admin-probe.py',
]

/**
 * 2026-11-17 真实网关 + 真实登录态逐项实测后校准的锚点。
 *
 * ★ 这一组是**实测出来的事实**：每一条都对应一次真实 HTTP 请求或一次真实
 *   数据库查询的结果，不是从代码推的。改写它们＝把已经验证过的排障依据改坏。
 * ★ 同样只用 ASCII（端点路径、错误码、字段名、状态串），不引入中文字面量。
 */
const TROUBLESHOOT_LIVE_CALIBRATION = [
  // 健康检查：全量入口是查询参数，不是 /healthz/full 这个路径
  '/healthz?full=true',
  // 认证层两种 401 的真实 code（authentication_error 是 type，不是 code）
  'missing_key',
  'authentication_required',
  // 预算与限流的真实 code
  'budget_exceeded',
  'rate_limit_exceeded',
  'key_throttled',
  'Retry-After',
  // 诊断端点的必带参数
  'provider_id=',
  // 限流写入是整组覆盖：三个旋钮缺一即 400
  'rate_limit_concurrent',
  'rate_limit_tpm',
  'dry-run=false',
  // 模型列表口径：默认由运营可配，未识别取值返 400
  'models.list_scope',
  'canonical_id',
  // 手工加绑定接口当前 500 的成因
  '42P08',
  // 强制改密闸与改密后的吊销纪元
  'password change required before accessing other APIs',
  'session revoked: password changed',
  '/api/auth/change-password',
  '/api/auth/token',
  // 启动迁移的真源目录与清单
  'embeddata/startup',
  'installed_startup_migrations.tsv',
]

/** 排障章节的小节前缀（11.2 / 11.4 / 11.5 是重点三节）。 */
const TROUBLESHOOT_SECTION_PREFIXES = ['11.2 ', '11.4 ', '11.5 ']

describe('troubleshooting sections', () => {
  const blocks = parseMarkdown(manualSource)
  const h3 = blocks
    .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 3)
    .map((b) => b.text)

  it('carries the symptom-lookup and the two focused sections', () => {
    for (const prefix of TROUBLESHOOT_SECTION_PREFIXES) {
      expect(h3.filter((t) => t.startsWith(prefix)).length).toBe(1)
    }
  })

  it('pins the executable troubleshooting details', () => {
    for (const needle of TROUBLESHOOT_CONTRACT) {
      expect(manualSource).toContain(needle)
    }
  })

  it('pins the facts calibrated against a live gateway', () => {
    for (const needle of TROUBLESHOOT_LIVE_CALIBRATION) {
      expect(manualSource).toContain(needle)
    }
  })

  it('does not present the unreadable health path as the full endpoint', () => {
    // `/healthz/full` 仍然被提及（作为「它不是全量入口」的警示），
    // 但全量入口的写法必须同时在场——否则读者只有警示、没有正确写法。
    expect(manualSource).toContain('/healthz?full=true')
    const fullPathMentions = manualSource.match(/healthz\/full/g) ?? []
    expect(fullPathMentions.length).toBeGreaterThan(0)
  })
})

/**
 * 结构守卫：手册不得使用**解析器渲染不出来**的嵌套构造。
 *
 * ★ 背景：`web/src/lib/markdown.ts` 的块模型是**平的**——一个块里不能嵌套
 *   另一个块。具体地：
 *   - 引用块把**连续**的 `>` 行压成**一段行内文本** ⇒ 引用块里的 ``` 围栏
 *     不会被当成代码块（围栏标记和代码会一起变成文字），引用块里的有序列表
 *     会变成一段以 "1. … 2. …" 开头的长句；
 *   - 表格识别用的是 `line.trim().startsWith('|')` ⇒ **缩进**的表格会被识别，
 *     但它会**脱离**前面的列表成为兄弟块，把有序列表**劈成两段**；视图用
 *     `<ol>` 自动编号 ⇒ 后一段从 1 重新开始，"第 3 步" 会显示成 "1."。
 * ★ 这两类构造在 2026-11-17 那轮实测校准里真实出现过（引用块内的围栏 +
 *   列表内的缩进表格），而**手测渲染才发现**：解析层的单测全绿、静态门禁
 *   全绿、文件里也"看着对"。
 * ★ 断言全用 ASCII（正则与结构），不引入中文字面量。
 */
describe('manual source stays inside the flat block model', () => {
  /** 逐行扫描，跳过代码围栏内部（代码块里的 `|` 不是表格）。 */
  function scanOutsideFences(source: string): { line: number; text: string }[] {
    const out: { line: number; text: string }[] = []
    let inFence = false
    source.split('\n').forEach((text, idx) => {
      if (text.trim().startsWith('```')) {
        inFence = !inFence
        return
      }
      if (!inFence) out.push({ line: idx + 1, text })
    })
    return out
  }

  it('has no fenced code block inside a block quote', () => {
    const offenders = scanOutsideFences(manualSource)
      .filter((l) => /^>\s*```/.test(l.text))
      .map((l) => l.line)
    expect(offenders, `fence inside a quote at lines: ${offenders.join(',')}`).toEqual([])
  })

  it('has no list item inside a block quote', () => {
    const offenders = scanOutsideFences(manualSource)
      .filter((l) => /^>\s*(\d+\.|[-*])\s/.test(l.text))
      .map((l) => l.line)
    expect(offenders, `list inside a quote at lines: ${offenders.join(',')}`).toEqual([])
  })

  it('has no indented table row (it would split the preceding list)', () => {
    const offenders = scanOutsideFences(manualSource)
      .filter((l) => /^\s+\|/.test(l.text))
      .map((l) => l.line)
    expect(offenders, `indented table rows at lines: ${offenders.join(',')}`).toEqual([])
  })

  it('numbers every ordered list segment 1..N (the view renders <ol>, which always numbers 1..N)', () => {
    // ★ 关键：视图用的是 `<ol>`（v-for 渲染 `<li>`，**没有 start 属性**）
    //   ⇒ 浏览器渲染出来的编号**恒为 1..N**，与源码里写的数字无关。
    //   ⇒ 源码写 "9." 的那一行，页面上会显示成 "3." ⇒ 源码在骗读者。
    //   ⇒ 判据：每一段有序列表的编号必须**从 1 起且连续**。
    // ★ 为什么"段内连续"也要查：视图把**连续**的列表项拍平成一个 <ol>
    //   （嵌套的有序子列表同样会被拍平），所以源码里的编号必须整段连续；
    //   否则"第 3 步"在页面上会变成别的数字。
    const lines = scanOutsideFences(manualSource).map((l) => l.text)
    const isListItem = (t: string) => /^\s*([-*+]|\d+\.)\s+/.test(t)
    const isOrdered = (t: string) => /^\s*\d+\.\s+/.test(t)
    const offenders: string[] = []
    let current: number[] = []
    const flush = () => {
      current.forEach((n, i) => {
        if (n !== i + 1) offenders.push(`item ${i + 1} written as ${n}`)
      })
      current = []
    }
    for (const text of lines) {
      if (isListItem(text)) {
        if (isOrdered(text)) {
          current.push(Number(/^(\d+)\./.exec(text.trim())![1]))
        } else {
          flush() // 有序 → 无序：断段
        }
        continue
      }
      flush() // 任何非列表行结束当前列表段
    }
    flush()
    expect(offenders, 'ordered list numbering must be 1..N within each segment').toEqual([])
  })
})

/**
 * 渲染层守卫：**源码声明的每个块，都必须真的渲染成对应的元素**。
 *
 * ★ 为什么源码层守卫不够：源码守卫管的是「写法别越界」，但决定用户看到
 *   什么的是**渲染后的 DOM**。这轮实测校准里，引用块里的围栏在源码里
 *   「看着对」，在 DOM 里却是**一段普通文字**（围栏标记和代码一起被压成
 *   行内文本）—— 静态门禁全绿、解析器单测全绿，只有把渲染结果数一遍
 *   才看得见。
 * ★ 口径（避免「同一个解析器自证」的空转）：源码侧的计数**不经过
 *   parseMarkdown**（自己扫源码），DOM 侧的计数来自挂载后的真实渲染
 *   ⇒ 两边独立。引用块的前缀在源码侧被剥掉（语义上它仍是表格/围栏/
 *   列表），在解析器侧却会被引用分支压平 ⇒ 差值正是要抓的缺陷。
 */
describe('manual renders every block the source declares', () => {
  /** 剥掉引用块前缀（"> "）——看的是「这段内容在语义上是什么」。 */
  function stripQuote(line: string): string {
    return line.replace(/^\s*(?:>\s*)+/, '')
  }

  /** 源码里（围栏外）的行。 */
  function sourceLinesOutsideFences(source: string): string[] {
    const out: string[] = []
    let inFence = false
    for (const raw of source.split('\n')) {
      if (/^\s*(?:>\s*)*```/.test(raw)) {
        inFence = !inFence
        continue
      }
      if (!inFence) out.push(raw)
    }
    return out
  }

  /** 源码里的围栏标记数（含嵌在引用块里的）⇒ 每个代码块两枚标记。 */
  function countFenceMarkers(source: string): number {
    return source.split('\n').filter((l) => /^\s*(?:>\s*)*```/.test(l)).length
  }

  /** 源码里的表格块数：表头行 + 下一行是分隔行 ⇒ 一张表。 */
  function countSourceTables(source: string): number {
    const lines = source.split('\n')
    let n = 0
    for (let i = 0; i < lines.length; i++) {
      if (!stripQuote(lines[i]).trim().startsWith('|')) continue
      const cells = stripQuote(lines[i + 1] ?? '')
        .trim()
        .replace(/^\|/, '')
        .replace(/\|$/, '')
        .split('|')
        .map((c) => c.trim())
      if (cells.length > 0 && cells.every((c) => /^:?-{2,}:?$/.test(c))) n++
    }
    return n
  }

  /** 源码里有序列表「段」的数量（连续的有序项算一段）。 */
  function countSourceOrderedRuns(source: string): number {
    let runs = 0
    let inRun = false
    for (const raw of sourceLinesOutsideFences(source)) {
      const isOrdered = /^\s*\d+\.\s+/.test(stripQuote(raw))
      if (isOrdered) {
        if (!inRun) runs++
        inRun = true
      } else {
        inRun = false
      }
    }
    return runs
  }

  it('renders one code block per fenced block in the source', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const expected = countFenceMarkers(manualSource) / 2
    expect(expected).toBeGreaterThan(0)
    expect(w.findAll('pre.manual-code')).toHaveLength(expected)
  })

  it('renders one table per table in the source', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const expected = countSourceTables(manualSource)
    expect(expected).toBeGreaterThan(0)
    expect(w.findAll('table.manual-table')).toHaveLength(expected)
  })

  it('renders one ordered list per ordered run in the source', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const expected = countSourceOrderedRuns(manualSource)
    expect(expected).toBeGreaterThan(0)
    expect(w.findAll('ol.manual-list-ordered')).toHaveLength(expected)
  })

  it('never flattens a fence into a quote', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const leaking = w
      .findAll('blockquote.manual-quote')
      .filter((q) => q.text().includes('```'))
      .map((q) => q.text().slice(0, 60))
    expect(leaking).toEqual([])
  })

  it('never nests a table or a code block inside a list or a quote', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const containers = [
      ...w.findAll('ol.manual-list-ordered'),
      ...w.findAll('ul.manual-list'),
      ...w.findAll('blockquote.manual-quote'),
    ]
    expect(containers.length).toBeGreaterThan(0)
    for (const c of containers) {
      expect(c.find('table.manual-table').exists()).toBe(false)
      expect(c.find('pre.manual-code').exists()).toBe(false)
    }
  })
})

describe('post-install activation and data reset sections', () => {
  const blocks = parseMarkdown(manualSource)
  const h2 = blocks
    .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 2)
    .map((b) => b.text)

  /** 第 12 章下的 h3 小节数（遇到下一个 h2 即停）。 */
  function sectionCountUnder(chapterPrefix: string): number {
    let inside = false
    let count = 0
    for (const block of blocks) {
      if (block.kind !== 'heading') continue
      if (block.level === 2) {
        inside = block.text.startsWith(chapterPrefix)
        continue
      }
      if (inside && block.level === 3) count++
    }
    return count
  }

  it('has one data-reset chapter and it is the last one', () => {
    expect(h2.filter((t) => t.startsWith(DATA_RESET_CHAPTER_PREFIX))).toHaveLength(1)
    expect(h2[h2.length - 1].startsWith(DATA_RESET_CHAPTER_PREFIX)).toBe(true)
    expect(h2).toHaveLength(REQUIRED_CHAPTERS.length)
  })

  it('data-reset chapter carries at least the pinned number of subsections', () => {
    expect(sectionCountUnder(DATA_RESET_CHAPTER_PREFIX)).toBeGreaterThanOrEqual(DATA_RESET_MIN_SECTIONS)
  })

  it('activation subsection exists under chapter 2', () => {
    const sections = blocks
      .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 3)
      .map((b) => b.text)
    expect(sections.filter((t) => t.startsWith(ACTIVATION_SECTION_PREFIX)).length).toBeGreaterThan(0)
  })

  it('pins the data-init endpoints, token and keep predicates', () => {
    for (const needle of DATA_RESET_CONTRACT) {
      expect(manualSource).toContain(needle)
    }
  })

  it('pins the bootstrap endpoints and CLI activation modes', () => {
    for (const needle of ACTIVATION_CONTRACT) {
      expect(manualSource).toContain(needle)
    }
  })
})

describe('HelpManualView', () => {
  it('渲染页头、目录与正文', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })

    expect(w.text()).toContain('帮助手册')
    expect(w.findAll('.manual-toc-link')).toHaveLength(REQUIRED_CHAPTERS.length)
    expect(w.findAll('.manual-h2').length).toBeGreaterThanOrEqual(REQUIRED_CHAPTERS.length)
    expect(w.findAll('pre.manual-code').length).toBeGreaterThan(0)
    expect(w.findAll('table.manual-table').length).toBeGreaterThan(0)
  })

  it('表格带语义表头（th scope=col）', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const headerCells = w.findAll('table.manual-table th')
    expect(headerCells.length).toBeGreaterThan(0)
    for (const cell of headerCells) {
      expect(cell.attributes('scope')).toBe('col')
    }
  })

  it('搜索按关键词筛正文，无匹配时给出空态', async () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })

    const input = w.find('input')
    expect(input.exists()).toBe(true)

    await input.setValue('count_tokens')
    const filteredHeadings = w.findAll('.manual-h2')
    expect(filteredHeadings.length).toBe(1)
    expect(filteredHeadings[0].text()).toBe('9. 客户端调用')

    await input.setValue('这个关键词一定搜不到任何内容')
    expect(w.find('.manual-empty').exists()).toBe(true)
    expect(w.text()).toContain('没有匹配的内容')
  })

  it('代码块带复制按钮', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const buttons = w.findAll('.manual-copy')
    expect(buttons.length).toBeGreaterThan(0)
    expect(buttons[0].text()).toBe('复制')
  })
})
