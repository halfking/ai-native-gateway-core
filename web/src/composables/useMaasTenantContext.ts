import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getTenant } from '../api'
import { getCurrentTenantId, isPlatformOpsView, isSuperAdmin } from '../store'

export type MaasBackLink = {
  to: { path: string; query?: Record<string, string> }
  label: string
}

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

  /** Parent route for admin ?tenant= views — tenant detail page. */
  const tenantDetailBack = computed<MaasBackLink | null>(() => {
    if (!isAdminTenantView.value || !tenantCode.value) return null
    const name = tenantDisplayName.value || tenantCode.value
    return {
      to: { path: `/tenants/${tenantCode.value}` },
      label: `返回 ${name}`,
    }
  })

  /** Default back link per MaaS page (admin tenant context vs normal tenant user). */
  function maasBackLink(page: 'models' | 'pricing' | 'usage' | 'account' | 'order'): MaasBackLink | null {
    if (isAdminTenantView.value) {
      return tenantDetailBack.value
    }
    const q = tenantFromQuery.value ? { tenant: tenantFromQuery.value } : undefined
    switch (page) {
      case 'models':
        return { to: { path: '/' }, label: '返回仪表盘' }
      case 'pricing':
        return { to: { path: '/maas/account', ...(q ? { query: q } : {}) }, label: '返回账户' }
      case 'usage':
        return { to: { path: '/maas/account', ...(q ? { query: q } : {}) }, label: '返回账户' }
      case 'account':
        return { to: { path: '/' }, label: '返回仪表盘' }
      case 'order':
        return { to: { path: '/maas/pricing', ...(q ? { query: q } : {}) }, label: '返回套餐' }
      default:
        return { to: { path: '/' }, label: '返回' }
    }
  }

  function tenantQuerySuffix(): Record<string, string> | undefined {
    if (isAdminTenantView.value && tenantCode.value) {
      return { tenant: tenantCode.value }
    }
    return undefined
  }

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
    tenantDetailBack,
    maasBackLink,
    tenantQuerySuffix,
    pageTitle,
  }
}
