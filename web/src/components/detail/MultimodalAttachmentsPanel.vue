<script setup lang="ts">
// MultimodalAttachmentsPanel — preview image / audio / video from body + attachments.
import { computed, ref, watch } from 'vue'
import { surfaceError } from '../../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import {
  getRequestAttachments,
  type AttachmentInfo,
} from '../../api/logs'
import {
  collectMultimodalMedia,
  type MediaKind,
  type MultimodalMediaItem,
} from './multimodalHelpers'

const props = defineProps<{
  requestId: string | null
  requestBody: unknown
  /** Attachments already on log detail (may be empty until fetched). */
  attachments?: AttachmentInfo[] | null
}>()

const loading = ref(false)
const error = ref('')
const fetched = ref<AttachmentInfo[] | null>(null)

const { t } = useI18n()

watch(
  () => props.requestId,
  async (id) => {
    fetched.value = null
    error.value = ''
    if (!id) return
    // Prefer props.attachments when present; otherwise fetch list endpoint.
    if (props.attachments?.length) return
    loading.value = true
    try {
      const res = await getRequestAttachments(id)
      fetched.value = res.attachments || []
    } catch (e: unknown) {
      error.value = surfaceError(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const media = computed((): MultimodalMediaItem[] =>
  collectMultimodalMedia(props.requestBody, props.attachments?.length ? props.attachments : fetched.value),
)

const filter = ref<'all' | MediaKind>('all')
const visible = computed(() =>
  filter.value === 'all' ? media.value : media.value.filter((m) => m.kind === filter.value),
)

const counts = computed(() => {
  const c = { image: 0, audio: 0, video: 0, file: 0 }
  for (const m of media.value) c[m.kind]++
  return c
})

/**
 * 媒体类型的中文标签。
 *
 * ★ 为什么不用模板串 `t(\`…kindLabel.${kind}\`)`：
 *   本仓的 i18n 扫描器**只认字面量键** ⇒ 模板串里的键不会进引用集
 *   ⇒ 那 4 个键会变成「有值但没人知道被谁用」的死键面。
 *   ⇒ 一律写字面量。
 */
const kindLabel = computed<Record<MediaKind, string>>(() => ({
  image: t('requestDetail.attachments.kindLabel.image'),
  audio: t('requestDetail.attachments.kindLabel.audio'),
  video: t('requestDetail.attachments.kindLabel.video'),
  file: t('requestDetail.attachments.kindLabel.file'),
}))
</script>

<template>
  <div class="mm-panel" data-testid="multimodal-attachments">
    <div v-if="loading" class="muted">{{ t('requestDetail.attachments.loading') }}</div>
    <p v-if="error" class="err">{{ error }}</p>

    <div class="toolbar">
      <span class="summary">
        {{ t('requestDetail.attachments.total', { count: media.length }) }}
        {{ t('requestDetail.attachments.byType.image', { n: counts.image }) }}
        {{ t('requestDetail.attachments.byType.audio', { n: counts.audio }) }}
        {{ t('requestDetail.attachments.byType.video', { n: counts.video }) }}
        {{ t('requestDetail.attachments.byType.file', { n: counts.file }) }}
      </span>
      <div class="filters">
        <button
          v-for="f in (['all', 'image', 'audio', 'video', 'file'] as const)"
          :key="f"
          type="button"
          class="btn btn-sm"
          :class="{ 'btn-primary': filter === f }"
          @click="filter = f"
        >{{ f === 'all' ? t('requestDetail.attachments.allFilter') : kindLabel[f] }}</button>
      </div>
    </div>

    <div v-if="!visible.length" class="muted">
      {{ t('requestDetail.attachments.empty') }}
    </div>

    <ul v-else class="grid">
      <li v-for="(m, i) in visible" :key="`${m.source}-${i}-${m.url.slice(0, 32)}`" class="card">
        <div class="meta">
          <span class="kind">{{ kindLabel[m.kind] }}</span>
          <span class="src">{{ m.source }}</span>
          <span v-if="m.messageIndex >= 0" class="idx">msg#{{ m.messageIndex }}</span>
          <span v-if="m.label" class="lbl">{{ m.label }}</span>
        </div>
        <div class="preview">
          <img v-if="m.kind === 'image' && m.url" :src="m.url" :alt="m.label || 'image'" loading="lazy" />
          <audio v-else-if="m.kind === 'audio' && m.url" :src="m.url" controls preload="metadata" />
          <video v-else-if="m.kind === 'video' && m.url" :src="m.url" controls preload="metadata" />
          <a v-else-if="m.url" :href="m.url" target="_blank" rel="noopener">{{ t('requestDetail.attachments.openOrDownload') }}</a>
          <span v-else class="muted">{{ t('requestDetail.attachments.noPreviewUrl') }}</span>
        </div>
        <p v-if="m.detail || m.mime" class="detail">{{ m.mime }} · {{ m.detail }}</p>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.mm-panel { font-size: 13px; }
.toolbar {
  display: flex; flex-wrap: wrap; gap: 8px; align-items: center;
  margin-bottom: 12px; justify-content: space-between;
}
.summary { font-size: 12px; color: var(--muted); }
.filters { display: flex; gap: 4px; flex-wrap: wrap; }
.grid {
  list-style: none; margin: 0; padding: 0;
  display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px;
}
.card {
  border: 1px solid var(--border); border-radius: 8px; padding: 8px;
  background: var(--bg-card, var(--card));
}
.meta { display: flex; flex-wrap: wrap; gap: 4px; font-size: 11px; margin-bottom: 8px; }
.kind {
  /* ★ 徽标内容已本地化（kindLabel）⇒ 去掉 uppercase：
     中文界面里 uppercase 无意义（DESIGN.md §4 规则 5），
     而这枚徽标原本显示的是裸英文枚举值（image/audio/video/file）。 */
  font-weight: 600;
  padding: 1px 4px; border-radius: 4px; background: var(--bg-subtle);
}
.src, .idx, .lbl { color: var(--muted); }
.preview {
  min-height: 80px; display: flex; align-items: center; justify-content: center;
  background: var(--bg-subtle); border-radius: 6px; overflow: hidden;
}
.preview img, .preview video {
  max-width: 100%; max-height: 200px; object-fit: contain; display: block;
}
.preview audio { width: 100%; }
.detail { margin: 8px 0 0; font-size: 11px; color: var(--muted); word-break: break-all; }
.muted { color: var(--muted); font-size: 12px; }
.err { color: var(--danger); font-size: 12px; }
</style>
