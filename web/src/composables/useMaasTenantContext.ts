import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getTenant } from '../api'
import { getCurrentTenantId, isPlatformOpsView, isSuperAdmin } from '../store'

/** Admin viewing a specific tenant's MaaS data via ?tenant=code on /maas/* routes. */
export function useMaasTenantContext() {
  const route = useRoute()

  const tenantFromQuery = computed(() => {
    const t = route.query.tenant
    return typeof t === 'string' && t.trim() ? t.trim() : null
  })

  const isAdminTenantView = computed(
    () => isSuperAdmin() && isPlatformOpsView() && !!tenantFromQuery.value,
  )

  const tenantCode = computed(() =>
    isAdminTenantView.value ? tenantFromQuery.value! : getCurrentTenantId(),
  )

  const tenantDisplayName = ref<string | null>(null)

  watch(
    tenantCode,
    async (code) => {
      if (isAdminTenantView.value && code) {
        try {
          const t = await getTenant(code)
          tenantDisplayName.value = t.name || code
        } catch {
          tenantDisplayName.value = code
        }
      } else {
        tenantDisplayName.value = null
      }
    },
    { immediate: true },
  )

  const tenantLabel = computed(() => {
    if (isAdminTenantView.value) {
      return tenantDisplayName.value
        ? `租户: ${tenantDisplayName.value} (${tenantCode.value})`
        : `租户: ${tenantCode.value}`
    }
    const id = getCurrentTenantId()
    if (isDefaultTenantLocal()) return '默认租户'
    return `租户: ${id}`
  })

  function isDefaultTenantLocal(): boolean {
    return getCurrentTenantId() === 'default'
  }

  function pageTitle(base: string): string {
    if (isAdminTenantView.value) {
      const name = tenantDisplayName.value || tenantCode.value
      return `${name} · ${base}`
    }
    return base
  }

  return {
    tenantFromQuery,
    isAdminTenantView,
    tenantCode,
    tenantDisplayName,
    tenantLabel,
    pageTitle,
  }
}
