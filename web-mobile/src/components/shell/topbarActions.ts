import { inject, onBeforeUnmount, shallowRef, type InjectionKey, type ShallowRef } from 'vue'
import type { IconName } from '@/components/common/AppIcon.vue'

export interface TopbarAction {
  key: string
  label: string | (() => string)
  icon?: IconName
  onClick: () => void | Promise<void>
  disabled?: boolean | (() => boolean)
}

export interface TopbarActionRegistry {
  actions: ShallowRef<TopbarAction[]>
  register(owner: symbol, action: TopbarAction): void
  clear(owner: symbol): void
}

export const TOPBAR_ACTIONS: InjectionKey<TopbarActionRegistry> = Symbol('topbar-actions')

export function createTopbarActionRegistry(): TopbarActionRegistry {
  const actions = shallowRef<TopbarAction[]>([])
  const registered = new Map<symbol, TopbarAction>()
  const publish = () => {
    actions.value = [...registered.values()]
  }

  return {
    actions,
    register(owner, action) {
      registered.set(owner, action)
      publish()
    },
    clear(owner) {
      if (!registered.delete(owner)) return
      publish()
    },
  }
}

export function useTopbarAction(action: TopbarAction): void {
  const registry = inject(TOPBAR_ACTIONS, null)
  if (!registry) return

  const owner = Symbol(action.key)
  registry.register(owner, action)
  onBeforeUnmount(() => registry.clear(owner))
}
