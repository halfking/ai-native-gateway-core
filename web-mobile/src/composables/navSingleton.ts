// navSingleton.ts — NavigationContext 单例（模块级，App 生命周期共享）。

import { NavigationContext } from '../runtime/navigationContext'

let ctx: NavigationContext | null = null

export function navigationContextSingleton(): NavigationContext {
  if (!ctx) ctx = new NavigationContext()
  return ctx
}
