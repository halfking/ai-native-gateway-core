import { ref } from 'vue'
import { getDownloadCatalog, type DownloadCatalog } from '../api/public'

const catalogRef = ref<DownloadCatalog | null>(null)
const loadingRef = ref(false)
const loadedRef = ref(false)

export function usePublicCatalog() {
  async function ensureCatalog() {
    if (loadedRef.value || loadingRef.value) return catalogRef.value
    loadingRef.value = true
    try {
      catalogRef.value = await getDownloadCatalog()
      loadedRef.value = true
    } catch {
      catalogRef.value = null
    } finally {
      loadingRef.value = false
    }
    return catalogRef.value
  }

  return { catalog: catalogRef, loading: loadingRef, ensureCatalog }
}
