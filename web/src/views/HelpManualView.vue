<script setup lang="ts">
// HelpManualView.vue — in-app help manual.
//
// Content lives in `web/content/help-manual.zh-CN.md` (a real file, not a
// string in this component) and is parsed by lib/markdown.ts into a block
// tree. The tree is rendered with ordinary Vue templates — the manual never
// becomes raw HTML, so it cannot inject markup.
//
// Why the body is a file instead of locale strings: the manual is a long
// document, and the repository's i18n gate treats hard-coded CJK inside
// `src/` as a ratcheted regression (count may only go down). Keeping the
// body next to the app as Markdown keeps the source of truth in one place
// while the UI chrome (title, search, buttons) stays fully translated.

import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElInput } from 'element-plus'
import { Search } from '@element-plus/icons-vue'
import PageHeader from '../components/ui/PageHeader.vue'
import ManualInline from '../components/help/ManualInline.vue'
import { buildToc, filterManualBlocks, parseMarkdown } from '../lib/markdown'
import manualSource from '../../content/help-manual.zh-CN.md?raw'

const { t } = useI18n()

const keyword = ref('')
const copiedIndex = ref<number | null>(null)

const allBlocks = parseMarkdown(manualSource)
const visibleBlocks = computed(() => filterManualBlocks(allBlocks, keyword.value))
const toc = computed(() => buildToc(visibleBlocks.value))
const hasResults = computed(() => visibleBlocks.value.length > 0)

function scrollToSection(id: string): void {
  const target = document.getElementById(id)
  if (!target) return
  target.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

async function copyCode(text: string, index: number): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    copiedIndex.value = index
    window.setTimeout(() => {
      if (copiedIndex.value === index) copiedIndex.value = null
    }, 2000)
  } catch {
    copiedIndex.value = null
  }
}
</script>

<template>
  <div class="manual-view">
    <PageHeader :title="t('helpManual.title')" :subtitle="t('helpManual.subtitle')" />

    <div class="manual-toolbar">
      <ElInput
        v-model="keyword"
        class="manual-search"
        :placeholder="t('helpManual.search')"
        :prefix-icon="Search"
        clearable
      />
      <p v-if="keyword && hasResults" class="manual-result-count">{{ t('helpManual.resultCount') }}</p>
    </div>

    <div class="manual-layout">
      <nav class="manual-toc" :aria-label="t('helpManual.toc')">
        <p class="manual-toc-title">{{ t('helpManual.toc') }}</p>
        <ul class="manual-toc-list">
          <li v-for="entry in toc" :key="entry.id">
            <button type="button" class="manual-toc-link" @click="scrollToSection(entry.id)">
              {{ entry.title }}
            </button>
            <ul v-if="entry.sections.length > 0" class="manual-toc-sublist">
              <li v-for="section in entry.sections" :key="section.id">
                <button type="button" class="manual-toc-sublink" @click="scrollToSection(section.id)">
                  {{ section.title }}
                </button>
              </li>
            </ul>
          </li>
        </ul>
      </nav>

      <article class="manual-body">
        <p v-if="!hasResults" class="manual-empty">{{ t('helpManual.emptyNoResult') }}</p>

        <template v-for="(block, index) in visibleBlocks" :key="index">
          <h1 v-if="block.kind === 'title'" class="manual-title">{{ block.text }}</h1>

          <h2
            v-else-if="block.kind === 'heading' && block.level === 2"
            :id="block.id"
            class="manual-h2"
          >
            {{ block.text }}
          </h2>

          <h3 v-else-if="block.kind === 'heading'" :id="block.id" class="manual-h3">
            {{ block.text }}
          </h3>

          <p v-else-if="block.kind === 'paragraph'" class="manual-p">
            <ManualInline :segments="block.segments" />
          </p>

          <ul v-else-if="block.kind === 'list' && !block.ordered" class="manual-list">
            <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
              <ManualInline :segments="item" />
            </li>
          </ul>

          <ol v-else-if="block.kind === 'list'" class="manual-list manual-list-ordered">
            <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
              <ManualInline :segments="item" />
            </li>
          </ol>

          <div v-else-if="block.kind === 'code'" class="manual-code-wrap">
            <div class="manual-code-head">
              <span class="manual-code-lang">{{ block.lang || 'text' }}</span>
              <button type="button" class="manual-copy" @click="copyCode(block.text, index)">
                {{ copiedIndex === index ? t('helpManual.copied') : t('helpManual.copy') }}
              </button>
            </div>
            <pre class="manual-code"><code>{{ block.text }}</code></pre>
          </div>

          <div v-else-if="block.kind === 'table'" class="manual-table-wrap">
            <table class="manual-table">
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

          <blockquote v-else-if="block.kind === 'quote'" class="manual-quote">
            <ManualInline :segments="block.segments" />
          </blockquote>
        </template>
      </article>
    </div>
  </div>
</template>

<style scoped>
.manual-view {
  padding: 0;
  color: var(--text-primary);
  font-size: var(--text-md);
  line-height: 1.7;
}

.manual-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-bottom: var(--space-5);
  flex-wrap: wrap;
}

.manual-search {
  max-width: 360px;
  width: 100%;
}

.manual-result-count {
  margin: 0;
  font-size: var(--text-xs);
  color: var(--muted);
}

.manual-layout {
  display: grid;
  grid-template-columns: 240px minmax(0, 1fr);
  gap: var(--space-6);
  align-items: start;
}

.manual-toc {
  position: sticky;
  top: var(--space-4);
  max-height: calc(100vh - var(--space-8));
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: var(--radius-card);
  background: var(--surface);
  padding: var(--space-4);
}

.manual-toc-title {
  margin: 0 0 var(--space-3);
  font-size: var(--text-xs);
  font-weight: var(--font-semibold);
  color: var(--muted);
  text-transform: none;
}

.manual-toc-list,
.manual-toc-sublist {
  list-style: none;
  margin: 0;
  padding: 0;
}

.manual-toc-sublist {
  margin: var(--space-1) 0 var(--space-3) var(--space-3);
}

.manual-toc-link,
.manual-toc-sublink {
  display: block;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  padding: var(--space-1) 0;
  cursor: pointer;
  font-family: inherit;
  font-size: var(--text-sm);
  color: var(--text-secondary);
  line-height: 1.5;
}

.manual-toc-link {
  font-weight: var(--font-semibold);
  color: var(--text-primary);
}

.manual-toc-link:hover,
.manual-toc-sublink:hover {
  color: var(--accent);
}

.manual-toc-sublink {
  font-size: var(--text-xs);
}

.manual-body {
  min-width: 0;
}

.manual-title {
  margin: 0 0 var(--space-4);
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
  color: var(--text-primary);
}

.manual-h2 {
  margin: var(--space-6) 0 var(--space-3);
  padding-top: var(--space-4);
  border-top: 1px solid var(--border-muted);
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
  color: var(--text-primary);
  scroll-margin-top: var(--space-4);
}

.manual-h3 {
  margin: var(--space-5) 0 var(--space-2);
  font-size: var(--text-lg);
  font-weight: var(--font-semibold);
  color: var(--text-primary);
  scroll-margin-top: var(--space-4);
}

.manual-p {
  margin: 0 0 var(--space-3);
}

.manual-list {
  margin: 0 0 var(--space-4);
  padding-left: var(--space-5);
}

.manual-list li {
  margin-bottom: var(--space-2);
}

.manual-list-ordered {
  padding-left: var(--space-5);
}

.manual-quote {
  margin: 0 0 var(--space-4);
  padding: var(--space-3) var(--space-4);
  /* 不用侧边条：≥2px 的侧边条属于 AI 化视觉痕迹（design-ai-smell 的 V2 规则）。
     引用块改用「软填充 + 整框 1px 描边」表达层级。 */
  background: var(--surface-soft);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text-secondary);
}

.manual-code-wrap {
  margin: 0 0 var(--space-4);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.manual-code-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-soft);
  border-bottom: 1px solid var(--border);
}

.manual-code-lang {
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--muted);
}

.manual-copy {
  background: none;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 2px var(--space-2);
  cursor: pointer;
  font-family: inherit;
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.manual-copy:hover {
  color: var(--accent);
  border-color: var(--accent);
}

.manual-code {
  margin: 0;
  padding: var(--space-3);
  background: var(--surface-soft);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: 1.6;
  overflow-x: auto;
  white-space: pre;
}

.manual-table-wrap {
  margin: 0 0 var(--space-4);
  overflow-x: auto;
}

.manual-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-sm);
}

.manual-table th,
.manual-table td {
  border: 1px solid var(--border);
  padding: var(--space-2) var(--space-3);
  text-align: left;
  vertical-align: top;
}

.manual-table th {
  background: var(--surface-soft);
  font-weight: var(--font-semibold);
  color: var(--text-primary);
}

.manual-empty {
  margin: 0;
  padding: var(--space-5);
  text-align: center;
  color: var(--muted);
  font-size: var(--text-sm);
}

/* Single column below the desktop breakpoint; the TOC stays reachable above
   the body instead of disappearing. */
@media (max-width: 1024px) {
  .manual-layout {
    grid-template-columns: minmax(0, 1fr);
    gap: var(--space-5);
  }

  .manual-toc {
    position: static;
    max-height: 240px;
  }
}

@media (max-width: 768px) {
  .manual-search {
    max-width: none;
  }

  .manual-h2 {
    font-size: var(--text-lg);
  }
}
</style>
