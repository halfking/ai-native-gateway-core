<script setup lang="ts">
// PageHelpDrawer.vue — 页面右上角「帮助」图标 + 抽屉（2026-11-20）。
//
// 需求（owner）：每个页面右上角一个帮助图标，点开一个帮助页，讲这一页
// 「数据处理模式 / 查询或统计口径」，以业务能懂的语言对齐数据、自查偏差。
//
// 结构：
// · 图标由 AppTopbar 渲染（依赖当前 route）；本组件只负责抽屉本体。
// · 内容 = web/content/page-help/<id>.<locale>.md（8 语种齐全，2026-11-20 L1 轮
//   补齐），经 lib/markdown.ts 解析成结构化块树后用 Vue 模板渲染（复用
//   HelpManualView 的管线，绝不产出 raw HTML ⇒ 无注入面）。
// · 语种解析：当前 locale 命中即用；缺译文 ⇒ 回退中文正文（宁可看中文，
//   不给空抽屉）——与 chrome（i18n 8 语种，永远有）解耦。

import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElDrawer, ElIcon } from 'element-plus'
import { QuestionFilled, Close } from '@element-plus/icons-vue'
import ManualInline from './ManualInline.vue'
import { parseMarkdown, inlineText, type ManualBlock } from '../../lib/markdown'
import { resolvePageHelp, resolvePageHelpById } from '../../config/pageHelpRegistry'

const props = defineProps<{
  /** 当前路由 path（AppTopbar 传入，含 query 亦可；内部自行剥离） */
  routePath: string
  /**
   * 2026-11-23 统计标准轮：**页面内嵌**帮助按钮用的显式内容 id。
   *
   * 页面内嵌的帮助入口不在路由上（例如「看板」页签里的按钮打开
   * 「看板与会话统计的统计标准」），路由解析用不上 ⇒ 给 contentId 时
   * 按内容 id 取条目（标题键同样出自注册表，页面不自带文案）。
   * 不给时保持原行为：按 routePath 解析。
   */
  contentId?: string
  /**
   * 2026-11-23：触发按钮文案。给了就渲染「图标 + 文字」形态（页面内嵌用，
   * 让用户知道点的是「统计标准」而不是又一个问号）；不给则保持右上角
   * 纯图标形态（AppTopbar 用）。
   */
  label?: string
}>()

const open = ref(false)

const { t, locale } = useI18n()

const entry = computed(() =>
  props.contentId ? resolvePageHelpById(props.contentId) : resolvePageHelp(props.routePath),
)


// 编译期全量加载帮助正文（Vite ?raw；构建期确定，无运行时网络请求）。
// ★ 路径按本文件位置解析：src/components/help/ → ../../../content/page-help/
//   （glob 不匹配时 Vite 静默返回空对象 ⇒ 路径写错表现为「全站空抽屉」而非
//   构建红 ⇒ 有 PageHelpDrawer.test.ts 钉住正文可渲染，改层级必须同步改这里）。
const modules = import.meta.glob('../../../content/page-help/*.md', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

/** 从模块路径提取内容键：<id>.<locale>.md → "<id>.<locale>"（id 与语种整体作键，
 *  取值时按当前 locale 优先、zh-CN 兜底逐级降级） */
function keyOf(path: string): string {
  const base = path.split('/').pop() ?? ''
  return base.replace(/\.md$/, '')
}

const contentByKey = new Map<string, string>()
for (const [path, raw] of Object.entries(modules)) {
  contentByKey.set(keyOf(path), raw)
}

/** 当前抽屉正文：locale 命中即用；无译文回退中文（不空抽屉） */
const content = computed<string | undefined>(() => {
  const id = entry.value?.id
  if (!id) return undefined
  return contentByKey.get(`${id}.${locale.value}`) ?? contentByKey.get(`${id}.zh-CN`)
})

const hasContent = computed(() => entry.value !== null && content.value !== undefined)

const blocks = computed<ManualBlock[]>(() => {
  const raw = content.value
  return raw ? parseMarkdown(raw) : []
})

// 抽屉标题：语言键（titleKey 的 zh-CN 值就是页面名，比正文 H1 更稳定可对拍）
const drawerTitle = computed(() => {
  if (!entry.value) return t('pageHelp.fallbackTitle')
  return t(entry.value.titleKey)
})

// 关键词过滤：命中段落的块保留（标题/段落/列表/表格任一文本命中）
const keyword = ref('')
const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  if (!kw) return blocks.value
  return blocks.value.filter((block) => {
    switch (block.kind) {
      case 'title':
        return block.text.toLowerCase().includes(kw)
      case 'heading':
        return block.text.toLowerCase().includes(kw)
      case 'paragraph':
      case 'quote':
        return inlineText(block.segments).toLowerCase().includes(kw)
      case 'list':
        return block.items.some((item) => inlineText(item).toLowerCase().includes(kw))
      case 'table':
        return (
          block.headers.some((cell) => inlineText(cell).toLowerCase().includes(kw)) ||
          block.rows.some((row) => row.some((cell) => inlineText(cell).toLowerCase().includes(kw)))
        )
      case 'code':
        return block.text.toLowerCase().includes(kw)
    }
  })
})

const hasFilteredResults = computed(() => filtered.value.length > 0)

// 切页时收起抽屉并清空搜索（防止上一页的过滤词残留下页）
watch(
  () => [props.routePath, props.contentId],
  () => {
    open.value = false
    keyword.value = ''
  },
)

// 切语种时正文跟随 locale computed 自动重解析；搜索词不残留（关键词是
// 当前语种文本的子串 ⇒ 语种变了旧词必然失配 ⇒ 一并清空）。
watch(
  () => locale.value,
  () => {
    keyword.value = ''
  },
)
</script>

<template>
  <button
    type="button"
    class="page-help-trigger"
    :class="{ 'page-help-trigger--labeled': !!label }"
    :aria-label="t('pageHelp.open')"
    :title="t('pageHelp.open')"
    data-testid="page-help-trigger"
    @click.stop="open = true"
  >
    <el-icon :size="16"><QuestionFilled /></el-icon>
    <span v-if="label" class="page-help-trigger__label">{{ label }}</span>
  </button>

  <ElDrawer
    v-model="open"
    :title="drawerTitle"
    direction="rtl"
    size="520px"
    class="page-help-drawer"
  >
    <template #header>
      <div class="page-help-header">
        <el-icon :size="18" class="page-help-header-icon"><QuestionFilled /></el-icon>
        <span class="page-help-header-title">{{ drawerTitle }}</span>
        <button type="button" class="page-help-close" :aria-label="t('pageHelp.close')" @click="open = false">
          <el-icon :size="16"><Close /></el-icon>
        </button>
      </div>
    </template>

    <div class="page-help-body">
      <p class="page-help-intro">{{ t('pageHelp.intro') }}</p>

      <div class="page-help-toolbar">
        <input
          v-model="keyword"
          type="text"
          class="page-help-search"
          :placeholder="t('pageHelp.search')"
        />
      </div>

      <div v-if="!entry || !hasContent" class="page-help-empty">
        <p>{{ t('pageHelp.emptyNoContent') }}</p>
      </div>

      <template v-else>
        <p v-if="!hasFilteredResults" class="page-help-empty">{{ t('pageHelp.emptyNoResult') }}</p>

        <template v-for="(block, index) in filtered" :key="index">
          <h3 v-if="block.kind === 'title'" class="page-help-title">{{ block.text }}</h3>

          <h4
            v-else-if="block.kind === 'heading' && block.level === 2"
            class="page-help-h2"
          >
            {{ block.text }}
          </h4>

          <h5 v-else-if="block.kind === 'heading'" class="page-help-h3">
            {{ block.text }}
          </h5>

          <p v-else-if="block.kind === 'paragraph'" class="page-help-p">
            <ManualInline :segments="block.segments" />
          </p>

          <ul v-else-if="block.kind === 'list' && !block.ordered" class="page-help-list">
            <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
              <ManualInline :segments="item" />
            </li>
          </ul>

          <ol v-else-if="block.kind === 'list'" class="page-help-list page-help-list-ordered">
            <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
              <ManualInline :segments="item" />
            </li>
          </ol>

          <div v-else-if="block.kind === 'table'" class="page-help-table-wrap">
            <table class="page-help-table">
              <thead>
                <tr>
                  <th v-for="(cell, cellIndex) in block.headers" :key="cellIndex" scope="col">
                    <ManualInline :segments="cell" />
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, rowIndex) in block.rows" :key="rowIndex">
                  <td v-for="(cell, cellIndex) in row" :key="cellIndex">
                    <ManualInline :segments="cell" />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <blockquote v-else-if="block.kind === 'quote'" class="page-help-quote">
            <ManualInline :segments="block.segments" />
          </blockquote>
        </template>
      </template>
    </div>
  </ElDrawer>
</template>

<style scoped>
.page-help-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  transition: color 0.15s ease, background-color 0.15s ease;
}
.page-help-trigger:hover,
.page-help-trigger:focus-visible {
  color: var(--accent);
  background: var(--surface-soft);
}

/* 页面内嵌形态（2026-11-23）：图标 + 文字的胶囊按钮——右上角纯问号形态
   在页面里语义不清（用户不知道点的是「统计标准」还是别的帮助）。 */
.page-help-trigger--labeled {
  width: auto;
  height: auto;
  gap: 4px;
  padding: 3px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
}

.page-help-trigger--labeled:hover,
.page-help-trigger--labeled:focus-visible {
  border-color: var(--accent);
  color: var(--accent);
}

.page-help-trigger__label {
  line-height: 1.4;
}

.page-help-header {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
}
.page-help-header-icon {
  color: var(--accent);
  flex: 0 0 auto;
}
.page-help-header-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1 1 auto;
  min-width: 0;
}
.page-help-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  padding: 0;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  flex: 0 0 auto;
}
.page-help-close:hover {
  color: var(--text-primary);
  background: var(--surface-soft);
}

.page-help-body {
  padding: 0 4px 16px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-primary);
}

.page-help-intro {
  margin: 0 0 10px;
  padding: 8px 10px;
  border-radius: 8px;
  background: var(--surface-soft);
  border: 1px solid var(--border-muted);
  font-size: 12px;
  color: var(--muted);
}

.page-help-toolbar {
  margin-bottom: 12px;
}
.page-help-search {
  width: 100%;
  height: 30px;
  padding: 0 10px;
  font-size: 12px;
  color: var(--text-primary);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 6px;
  outline: none;
}
.page-help-search:focus {
  border-color: var(--accent);
}

.page-help-empty {
  margin: 16px 0;
  font-size: 13px;
  color: var(--muted);
}

.page-help-title {
  margin: 4px 0 12px;
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
}
.page-help-h2 {
  margin: 18px 0 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-muted);
  padding-bottom: 4px;
}
.page-help-h3 {
  margin: 14px 0 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}
.page-help-p {
  margin: 6px 0;
}
.page-help-list {
  margin: 6px 0;
  padding-left: 20px;
}
.page-help-list-ordered {
  list-style: decimal;
}
.page-help-quote {
  margin: 8px 0;
  padding: 6px 10px;
  border-left: 3px solid var(--accent);
  background: var(--surface-soft);
  border-radius: 0 6px 6px 0;
  color: var(--muted);
}
.page-help-table-wrap {
  margin: 8px 0;
  overflow-x: auto;
}
.page-help-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.page-help-table th,
.page-help-table td {
  border: 1px solid var(--border-muted);
  padding: 5px 8px;
  text-align: left;
  vertical-align: top;
}
.page-help-table th {
  background: var(--surface-soft);
  font-weight: 600;
  white-space: nowrap;
}
</style>

<style>
/* 抽屉全局 chrome：scoped 到不了 ElDrawer 内部 header，用全局类补齐 */
.page-help-drawer .el-drawer__header {
  margin-bottom: 12px;
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--border-muted);
}
.page-help-drawer .el-drawer__body {
  padding: 12px 16px;
}
</style>
