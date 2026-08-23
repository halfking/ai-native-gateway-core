import { computed, ref } from 'vue'
import { getCredentialMonitorSummary } from '../api/credential-monitor'

const LABEL_TTL_MS = 60_000

const labels = ref<Map<number, string>>(new Map())
const revision = ref(0)
let loadedAt = 0
let pending: Promise<void> | null = null

function normalizeLabel(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function credentialIdFromSyntheticModel(modelName: string): number | null {
  if (!modelName.startsWith('cred-')) return null
  const raw = modelName.slice('cred-'.length)
  if (!/^\d+$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

export function credentialLabelForId(id: number | null | undefined): string | null {
  if (id == null || !Number.isFinite(id) || id <= 0) return null
  return labels.value.get(id) || null
}

export function credentialDisplayName(
  id: number | null | undefined,
  prefix = '凭据',
): string {
  if (id == null || !Number.isFinite(id) || id <= 0) return '—'
  const label = credentialLabelForId(id)
  if (label) return label
  return `${prefix} #${id}`
}

export function displaySyntheticCredentialModel(modelName: string): string {
  const id = credentialIdFromSyntheticModel(modelName)
  return id == null ? modelName : credentialDisplayName(id)
}

export function loadCredentialLabels(force = false): Promise<void> {
  const fresh = !force && loadedAt > 0 && Date.now() - loadedAt < LABEL_TTL_MS
  if (fresh) return Promise.resolve()
  if (pending) return pending

  pending = getCredentialMonitorSummary({ mode: 'core' })
    .then((response) => {
      const next = new Map<number, string>()
      for (const credential of response.credentials ?? []) {
        const id = Number(credential.id)
        const label = normalizeLabel(credential.label)
        if (Number.isSafeInteger(id) && id > 0 && label) next.set(id, label)
      }
      labels.value = next
      loadedAt = Date.now()
      revision.value++
    })
    .catch(() => {
      // A label lookup is best-effort; callers retain the numeric fallback.
    })
    .finally(() => {
      pending = null
    })

  return pending
}

export function clearCredentialLabels(): void {
  labels.value = new Map()
  loadedAt = 0
  revision.value++
}

export function useCredentialLabels() {
  const labelRevision = computed(() => revision.value)
  return {
    labels,
    labelRevision,
    credentialLabelForId,
    credentialDisplayName,
    credentialIdFromSyntheticModel,
    displaySyntheticCredentialModel,
    loadCredentialLabels,
  }
}
