// RequestLogsView.searchContract.test.ts —— 「?q= 搜索框不许承诺它做不到的事」门。
//
// # 这道门守的是什么
//
// 2026-10-20 审计查「877 决策文档里那个恒为 0 的读数」时发现的一件事：
//
//   877 文档 §2.3 写「搜索框确实存在且接线正确 ⇒ 这是**需求为零**，不是**功能缺失**」。
//   那句话只核对了**客户端 → 服务端**这段接线，**没核对服务端 → 结果**：
//
//   · 后端 `?q=` 的谓词是 `rl.search_text ILIKE '%…%'`（admin/logs.go）；
//   · 而 `search_text` 由 domains/hooks/observability/telemetry/client.go 的
//     searchText() 拼成，成分**只有元数据**（模型 / 客户端配置 / 请求模式 /
//     会话·任务 ID / 密钥前缀 / 密钥属主 / 应用编码）—— **一条正文都没有**；
//   · 而这个框的 placeholder 当时写的是「搜索请求消息内容…」。
//
// ⇒ 用户照着提示去搜正文片段，**必然 0 命中**。
//   于是「带 ?q= 的请求数 = 0」这个读数，**分不清**「没人要」和
//   「功能承诺落空后没人要」—— 台账里那一行 0 因此**不能**当作需求为零的证据。
//
// 后端那一半由 admin/logs_q_search_contract_test.go 钉住（search_text
// 不许出现正文字段）；**前端这一半**由本门钉住：**界面不许承诺正文检索**。
//
// # 判据纪律：这道门自己也要能被打穿
//
// 变异实测见文件末：把 placeholder 改回「搜索请求消息内容…」→ 门红；
// 把 `q:` 接线删掉 → 门红。
//
// ★ 为什么不直接断言"title 里不能出现『正文』"：本门自己写的免责说明里
//   就含「不检索请求/响应正文」——那样断言会**打到自己**。
//   所以只对 **placeholder**（真正对用户许诺的那一行）禁词，
//   而对 title 要求它**必须把限制说清楚**（含「不检索」）。
//   这也正是本仓吃过的那类亏的同形：判据本身要能自证不自相矛盾。

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { dirname, join } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const VIEW = join(__dirname, 'RequestLogsView.vue')
const src = readFileSync(VIEW, 'utf8')

/** 取出 keyword 那个 <input> 整块（v-model="keyword" 所在的元素）。 */
function keywordInputBlock(): string {
  const marker = 'v-model="keyword"'
  const at = src.indexOf(marker)
  expect(at, 'RequestLogsView.vue 里找不到 v-model="keyword" ⇒ 搜索框被改名/删除，本门失效').toBeGreaterThan(-1)
  const start = src.lastIndexOf('<input', at)
  const end = src.indexOf('/>', at)
  expect(start, 'keyword input 的起始标签找不到').toBeGreaterThan(-1)
  expect(end, 'keyword input 的结束标签找不到').toBeGreaterThan(-1)
  return src.slice(start, end)
}

/** 只取属性值（placeholder="…" / title="…"），避免正文里出现同样的词造成误判。 */
function attr(block: string, name: string): string {
  const re = new RegExp(`${name}="([^"]*)"`)
  const m = re.exec(block)
  return m ? m[1] : ''
}

describe('请求日志 ?q= 搜索框的承诺边界', () => {
  it('placeholder 不许承诺检索请求/响应正文', () => {
    const block = keywordInputBlock()
    const placeholder = attr(block, 'placeholder')
    expect(placeholder, 'keyword input 没有 placeholder ⇒ 判据取不到值').not.toBe('')
    // 对用户许诺的是 placeholder 这一行：它必须只承诺元数据检索。
    for (const claim of ['消息内容', '消息片段', '正文', '请求内容', '响应内容', 'body']) {
      expect(
        placeholder.includes(claim),
        `placeholder="${placeholder}" 含有「${claim}」⇒ 它承诺了后端做不到的事。` +
          `后端 ?q= 走的是 search_text，而 search_text 只由元数据拼成。`,
      ).toBe(false)
    }
  })

  it('title 必须把限制说清楚（不检索正文）', () => {
    const block = keywordInputBlock()
    const title = attr(block, 'title')
    expect(title, 'keyword input 没有 title ⇒ 判据取不到值').not.toBe('')
    expect(
      title.includes('不检索'),
      `title="${title}" 没有说清"不检索请求/响应正文" ⇒ 光看 placeholder 的人会以为能搜正文。`,
    ).toBe(true)
  })

  it('?q= 仍然接线到 keyword（不许把功能悄悄摘掉）', () => {
    expect(
      /q:\s*keyword\.value\.trim\(\)\s*\|\|\s*undefined/.test(src),
      'RequestLogsView.vue 里找不到 `q: keyword.value.trim() || undefined` ⇒ ?q= 的发送侧被摘了。',
    ).toBe(true)
  })
})
