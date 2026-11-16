// HelpManualView.test.ts — 帮助手册页的装配与覆盖度钉测。
//
// 这道门守两件事：
//   ① 手册必须覆盖约定的主题章节（下载/安装/部署/供应商/凭据/租户/用户/
//      API Key/客户端调用/常见故障/数据重置）—— 少一章即红；
//   ② 手册页真的把正文渲染出来（标题、目录、代码块、表格），且搜索能筛。
// 另外单独钉住「安装后的激活」与「数据重置」两节的关键契约（端点、令牌、
// 保留谓词），避免日后改写手册时把可执行的操作细节删掉。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import HelpManualView from './HelpManualView.vue'
import { testI18n } from '../test-utils/i18n'
import { parseMarkdown } from '../lib/markdown'
import manualSource from '../../content/help-manual.zh-CN.md?raw'

/** 手册必须覆盖的十个主题（顺序即目录顺序）。 */
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
  '10. 常见故障处理',
  '11. 数据重置',
]

const chapters = parseMarkdown(manualSource)
  .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 2)
  .map((b) => b.text)

describe('帮助手册正文覆盖度', () => {
  it('包含约定的十个主题章节', () => {
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
 * 新增两节的结构钉：post-install activation (2.6) 与 data reset (ch. 11)。
 *
 * ★ 断言一律用 ASCII 锚点（端点路径 / 标题前缀 / 计数），**不写中文字面量**：
 *   src/** 的硬编码中文计数是「只许降不许升」的棘轮门（hardcoded_cjk），
 *   新增中文字面量会把棘轮顶高 ⇒ 那是本仓不允许的「借测试夹带文案」。
 *   章节存在性用「前缀 + 计数」钉，操作契约用「端点路径 / 关键字」钉。
 */
const ACTIVATION_SECTION_PREFIX = '2.6'
const DATA_RESET_CHAPTER_PREFIX = '11.'
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
 * 排障章节（10.4 请求失败 / 10.5 模型不可访问）的契约锚点。
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

/** 排障章节的小节前缀（10.2 / 10.4 / 10.5 是重点三节）。 */
const TROUBLESHOOT_SECTION_PREFIXES = ['10.2 ', '10.4 ', '10.5 ']

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
})

describe('post-install activation and data reset sections', () => {
  const blocks = parseMarkdown(manualSource)
  const h2 = blocks
    .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 2)
    .map((b) => b.text)

  /** 第 11 章下的 h3 小节数（遇到下一个 h2 即停）。 */
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
