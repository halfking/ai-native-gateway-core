import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { logout } from '@/api/auth'
vi.mock('@/api/auth', () => ({ logout: vi.fn(), login: vi.fn(), fetchMe: vi.fn() }))
vi.mock('@/hyper', () => ({ Hyper: { clearPersistence: vi.fn() } }))
beforeEach(() => { setActivePinia(createPinia()); localStorage.clear(); sessionStorage.clear(); vi.resetAllMocks() })
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); sessionStorage.clear() })
it('logs out through the current origin without calling a container-wide native erase', async () => {
  const erase = vi.fn()
  vi.stubGlobal('Capacitor', { Plugins: { AuthSession: { signOut: erase } } })
  vi.mocked(logout).mockResolvedValue(undefined)
  localStorage.setItem('llmgw:shell.directory:v2', 'nodes-and-pins')
  const auth = useAuthStore()
  auth.userInfo = { id: 1, tenant_id: 'default', username: 'admin', display_name: 'Admin', role: 'super_admin', email: '', enabled: true }
  await auth.logout()
  expect(logout).toHaveBeenCalledWith({ signal: expect.any(AbortSignal) })
  expect(erase).not.toHaveBeenCalled()
  expect(localStorage.getItem('llmgw:shell.directory:v2')).toBe('nodes-and-pins')
  expect(auth.isAuthenticated).toBe(false)
})
