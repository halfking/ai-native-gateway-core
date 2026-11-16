// useWorkMode.ts — 工作模式（简易 / 专家）状态层（2026-11-17）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html
//          docs/03-design/02-feature-design/simple-mode-v2-magpie-comparison.md
//
// ★ 它只决定**界面投影**：哪些导航露出、首页渲染哪个页面。
//   它**不改**后端数据、不改接口语义 ⇒ 切模式不丢数据、不重置设置。
//
// ★ 解析口径（唯一一处实现，别处不要另写一份）：
//     1. 本机选择（localStorage `llmgw_work_mode`）—— 用户自己切的，只影响本机
//     2. 平台默认（settings 体系 `gateway.mode`）—— super_admin 在设置页改，影响所有人
//     3. 兜底 = expert（= 现状）
//   ⇒ **既有用户升级后界面一行不变**（默认 expert）。
//
// ★ 为什么本机选择在前：平台设置是 ScopePlatform，一个管理员改了会立刻改变所有人的
//   界面；而「我想先用简易模式看看」是**个人偏好**，不该由平台替他决定。
//   两者的关系是「本机覆盖 > 平台默认」，与浏览器语言偏好（locale）的处理同款。
//
// ★ 白名单降级：平台设置项是 TypeString + Options（与 security.mode 同款），
//   **服务端不校验取值域** ⇒ 读侧必须自己降级：不是 'simple' 一律当 'expert'。
//   后端侧的同款约定记在 settings/spec_gateway_mode_test.go。

import { computed, ref } from 'vue'
import { getSetting } from '../api/settings'
import { usePersistedValue } from './usePersistedValue'

export type WorkMode = 'simple' | 'expert'

/** 平台设置项 key（与 settings/spec_gateway.go 的注册项一致） */
export const WORK_MODE_SETTING_KEY = 'gateway.mode'

/** 本机选择（localStorage） */
export const WORK_MODE_STORAGE_KEY = 'llmgw_work_mode'

/**
 * 白名单降级：只有 'simple' 才是简易模式，其余一律按 expert（现状）处理。
 * 平台设置不校验取值域（TypeString + Options）⇒ 脏值不能变成界面故障。
 */
export function coerceWorkMode(value: unknown): WorkMode {
  return value === 'simple' ? 'simple' : 'expert'
}

// —— 模块级单例：工作模式是全站状态，不能每个组件各存一份 —— //
const mode = ref<WorkMode>('expert')
const platformDefault = ref<WorkMode | null>(null)
const platformLoaded = ref(false)

let localHandle: ReturnType<typeof usePersistedValue<WorkMode | null>> | null = null
let initialized = false

function ensureInit(): void {
  if (initialized) return
  initialized = true
  localHandle = usePersistedValue<WorkMode | null>(
    WORK_MODE_STORAGE_KEY,
    () => null,
    {
      immediate: true,
      serialize: (v) => v ?? '',
      deserialize: (raw) => (raw === 'simple' || raw === 'expert' ? raw : undefined),
    },
  )
  // 首帧同步取值 ⇒ 切模式不闪烁（localStorage 是同步的）。
  mode.value = localHandle.value.value ?? 'expert'
}

/**
 * 仅供测试：复位模块级单例（模式 / 平台默认 / 本机选择）。
 * 与 useAppNav 的 `_resetUseAppNavForTests` 同款：模块级单例在同一个
 * 测试文件里会跨用例存活，不复位就会出现「上一个用例的选择泄漏到下一个」。
 */
export function _resetWorkModeForTests(): void {
  try {
    localStorage.removeItem(WORK_MODE_STORAGE_KEY)
  } catch {
    /* ignore */
  }
  mode.value = 'expert'
  platformDefault.value = null
  platformLoaded.value = false
  localHandle = null
  initialized = false
}

export function useWorkMode() {
  ensureInit()

  const isSimple = computed(() => mode.value === 'simple')
  const isExpert = computed(() => mode.value === 'expert')
  /** 模式来源：本机选择优先，其次平台默认 */
  const source = computed<'local' | 'platform' | 'default'>(() => {
    if (localHandle?.value.value) return 'local'
    if (platformDefault.value) return 'platform'
    return 'default'
  })

  /**
   * 拉取平台默认（`gateway.mode`）。
   * 失败不阻塞界面：取不到就退回「本机选择 / 兜底 expert」。
   */
  async function refreshPlatformDefault(): Promise<void> {
    try {
      const resp = await getSetting(WORK_MODE_SETTING_KEY)
      platformDefault.value = coerceWorkMode(resp.value)
    } catch {
      platformDefault.value = null
    } finally {
      platformLoaded.value = true
      // 没有本机选择时，平台默认才生效。
      if (!localHandle?.value.value) {
        mode.value = platformDefault.value ?? 'expert'
      }
    }
  }

  /** 切模式：只改本机选择，不改平台默认（不替别人做决定）。 */
  function setMode(next: WorkMode): void {
    const v = coerceWorkMode(next)
    mode.value = v
    if (localHandle) localHandle.value.value = v
  }

  /**
   * 放弃本机选择，回到平台默认。
   * ★ 用 remove() 而不是「写 null」：usePersistedValue 的 flush 遇到
   *   snapshot === null 会**跳过写入**（把 null 当成「没有值」），
   *   于是 localStorage 里会留着旧值。remove() 才是真正删键。
   */
  function resetToPlatformDefault(): void {
    if (localHandle) localHandle.remove()
    mode.value = platformDefault.value ?? 'expert'
  }

  return {
    mode,
    isSimple,
    isExpert,
    source,
    platformDefault,
    platformLoaded,
    setMode,
    resetToPlatformDefault,
    refreshPlatformDefault,
  }
}
