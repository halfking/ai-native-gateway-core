<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { store, clearAll, clearJwt, clearMustChangePasswordFlag, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, markAuthHydrated, setJwtToken, setUserInfo, authBearer } from './store'
import { logout as apiLogout } from './api/auth'
import { getAuthMe } from './api/admin'
import LoginModal from './components/LoginModal.vue'
import ChangePasswordDialog from './components/ChangePasswordDialog.vue'
import UserInfoDialog from './components/UserInfoDialog.vue'
import LanguageSelector from './components/LanguageSelector.vue'
import ThemeToggle from './components/ThemeToggle.vue'
import AppTopbar from './components/shell/AppTopbar.vue'
// 2026-10-03 审计 §9.73.7（③-4 裁决「接受冻结 + UI 标注」）：
// silently_frozen 读点没有任何错误信号，所以告示必须是全局的，
// 且取不到时也要显示（见 composable 的三态说明）。
import V1DataFrozenBanner from './components/shell/V1DataFrozenBanner.vue'
import { refreshV1DataHorizon } from './composables/useV1DataHorizon'
// 2026-09-13 方案 §4.4：移动端抽屉导航，与 AppTopbar 汉堡按钮共享开关状态
import AppNavDrawer from './components/ui/AppNavDrawer.vue'
import { useBreakpoint } from './composables/useBreakpoint'
// 2026-10-04 Hyper 移动端（docs/UI规范/00 §5.2 · H2）：compact 档的底栏与账户面板。
import AppBottomNav from './components/shell/AppBottomNav.vue'
import AppAccountSheet from './components/shell/AppAccountSheet.vue'
import { useWindowClass, getWindowClass } from './composables/useWindowClass'
import { installHyper, navigation, type NavigationScope } from './lib/shell/hyper'
import { navDrawerOpen } from './composables/useAppNav'
import { detectTheme, logoSrc } from './theme'
import { SITE_LOGO_SIZE, SITE_TITLE, SITE_TITLE_LINE_ONE, SITE_TITLE_LINE_TWO } from './config/brand'
import { useLoginModal } from './composables/useLoginModal'
import { onMaintainAvailabilityChange, probeMaintainAvailable } from './config/edition'
import { loadCredentialLabels, clearCredentialLabels } from './composables/useCredentialLabels'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { isCompact } = useWindowClass()
const accountSheetOpen = ref(false)
const shellHealth = ref<'ok' | 'down' | 'unknown'>('unknown')
const { showLoginModal, openLogin, closeLogin } = useLoginModal()
const showChangePassword = ref(false)
const showUserInfo = ref(false)
const passwordSuccessMessage = ref('')
const mustChangePassword = computed(() => !!store.jwtToken && !!store.userInfo?.must_change_password)

// 2026-07-09: isHydrating 防止页面在 auth probe 完成前误判为未登录。
// 与 store.authHydrated 配合：App.vue onMounted 触发 /api/auth/me，settle 后翻为 true。
const isHydrating = computed(() => !store.authHydrated)

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey || store.userInfo))
const brandLogo = ref(logoSrc(detectTheme()))
const logoObserver = typeof MutationObserver !== 'undefined'
  ? new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  : null
let stopMaintainAvailabilityWatch: (() => void) | null = null

onMounted(async () => {
  // Hyper 运行时装配（docs/UI规范/00 §5.2）。
  //
  // hyperActive 只在「壳内或 compact」为真：**桌面必须传 false** ——
  // 桌面走浏览器原生历史与现状顶栏，Hyper 的 entry 链与 Esc 仲裁都不该介入，
  // 那是桌面零回归红线（installHyper 内部据此不挂路由适配器与持久化）。
  //
  // scope 用于导航历史隔离：换账号/换服务端会清空历史，避免看到上一个人的
  // 标题与筛选。此处暂用稳定的部署标识占位，接入真实 serverId 是 H6 的事。
  const scope: NavigationScope = {
    serverId: window.location.origin,
    accountId: String(store.userInfo?.id ?? 'anonymous'),
  }
  const install = () =>
    installHyper(router, {
      scope,
      // 每次按当前档重算：窗口从桌面缩到 compact（分屏 / 折叠展开 / 旋转）时
      // 要能补上 Hyper。installHyper 内部的 routerHookInstalled 守卫保证
      // 重复调用不会重复注册路由钩子。
      hyperActive: isCompact.value,
      getWindowClass: () => getWindowClass(),
      translate: (key) => t(key),
    }).catch(() => {
      // Hyper 是增强层，装配失败不得让控制台打不开
    })
  void install()
  // 已知限制（本轮不实现）：compact → 桌面时**不拆卸**路由适配器，
  // 只停止新的 Hyper 行为。拆卸需要重放/合并 entry 链，收益低于风险。
  // 影响仅是桌面下仍会记录导航条目（sessionStorage），不改变任何可见行为。
  watch(isCompact, () => { void install() })

  // 拉一次「v1 数据地平线」告示。失败**不**阻塞挂载：横幅自己会显示
  // 「无法确认状态」而不是消失 —— 未确认不等于正常。
  void refreshV1DataHorizon()
  brandLogo.value = logoSrc(detectTheme())
  logoObserver?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  // 2026-07-10: Auth hydration — probe /api/auth/me if JWT not already in localStorage.
  try {
    if (!store.jwtToken && !store.apiKey) {
      try {
        const me = await getAuthMe()
        const meAny = me as any
        if (meAny?.access_token) {
          setJwtToken(meAny.access_token)
        }
        setUserInfo(me)
      } catch {
        clearJwt()
      }
    }
  } finally {
    markAuthHydrated()
  }

  void probeMaintainAvailable()
  // 保留 maintain 可用性订阅入口以便未来 topbar 内 onUnmounted 正确清理。
  // 当前无回调（topbar 自取），不会泄漏 — onMaintainAvailabilityChange 在静态模块层仅保留全局 listener。
  stopMaintainAvailabilityWatch?.()
  stopMaintainAvailabilityWatch = onMaintainAvailabilityChange(() => { /* noop */ })
})

onUnmounted(() => {
  logoObserver?.disconnect()
  stopMaintainAvailabilityWatch?.()
  stopMaintainAvailabilityWatch = null
})

const versionInfo = ref<{
  version?: string
  git_sha?: string
  build_date?: string
  build_seq?: number
}>({})

async function loadVersion() {
  if (!isLoggedIn.value) return
  const token = authBearer()
  try {
    const resp = await fetch('/api/system/version', {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      credentials: 'same-origin',
    })
    // /api/system/version is a public display endpoint. A 401 here must not
    // wipe the session or bounce a request-detail deep link to the overview.
    if (resp.status === 401) return
    if (resp.ok) {
      versionInfo.value = await resp.json()
    }
  } catch {
    // ignore — version display is non-critical
  }
}

watch(
  isLoggedIn,
  (loggedIn) => {
    if (loggedIn) {
      closeLogin()
      loadVersion()
    }
  },
  { immediate: true },
)

watch(
  mustChangePassword,
  (required) => {
    if (required) {
      passwordSuccessMessage.value = ''
      showChangePassword.value = true
    }
  },
  { immediate: true },
)

// 2026-08-23 (凭据显示) : 全局加载凭据名称缓存。selfcheck/stream/节点矩阵
// 等视图通过 useCredentialLabels 共享这份缓存，避免子组件各自拉取。
// 登出时必须清空，否则下一位用户（尤其跨租户）会在 TTL 内看到上一
// 用户的凭据标签——这是跨租户信息泄露。
watch(isLoggedIn, (loggedIn) => {
  if (loggedIn) {
    void loadCredentialLabels()
  } else {
    clearCredentialLabels()
  }
}, { immediate: true })

// 2026-09-13 方案 §4.4 P1c：访客壳 <768px 收纳为汉堡 + AppNavDrawer。
// guest-nav 桌面态（>=768）不动；断点用 isTablet(>=768) 而非 isMobile(<1024)。
const { isTablet } = useBreakpoint()

// 现有 guest-nav 的 6 个静态链接数组化（抽屉数据源）
const guestNavLinks = [
  { labelKey: 'landing.navDownload', href: '/customer/update-activate' },
  { labelKey: 'landing.navSetup', href: '/bootstrap' },
  { labelKey: 'landing.navActivate', href: '/customer/update-activate' },
  { labelKey: 'landing.navLicense', href: '/customer/update-activate' },
  { labelKey: 'landing.navAgreement', href: '/customer/update-activate' },
  { labelKey: 'landing.navSupport', href: '/customer/update-activate' },
]

watch(
  () => route.query.login,
  (login) => {
    if (login && !isLoggedIn.value) openLogin()
  },
  { immediate: true },
)

async function logout() {
  try { 
    await apiLogout() 
  } catch { 
    /* ignore */ 
  }
  clearAll()
  markAuthHydrated() // 2026-07-09: 登出后保持 hydrated=true，下一次 mount 才会重新探测
  // Maintain 未部署时留在 Gateway 登录页，不能停留在已清空认证态的受保护页面。
  if (typeof window !== 'undefined') {
    if (await probeMaintainAvailable()) {
      window.location.replace('/maintain/home')
    } else {
      await router.replace({ path: '/', query: { login: '1' } })
    }
  }
}

function openUserInfo() {
  showUserInfo.value = true
}

function openChangePassword() {
  passwordSuccessMessage.value = ''
  showChangePassword.value = true
}

async function handleChangePasswordSuccess() {
  clearMustChangePasswordFlag()
  showChangePassword.value = false
  // B4 (2026-09-22): the backend revokes every token issued before the
  // password change, so the current session is dead anyway — log out and
  // route to the login page instead of pretending to stay signed in.
  // (The success banner only renders in the logged-in shell, so it is
  // intentionally skipped here.)
  await logout()
}
</script>

<template>
  <!--
    2026-07-21: 三态渲染 — hydrating / logged-in / guest.
    - isHydrating=true 时显示加载中
    - 已登录态：顶部水平菜单（AppTopbar）+ 业务页（RouterView，业务页内部保持原貌）
    - 未登录态：保持原 LifecycleShell / LandingView 体系
    - 老板："其它页面风格保持不变" → 业务页 (DashboardView 等) 不动，只换外壳
  -->
  <div v-if="isHydrating" class="auth-loading">
    <div class="auth-loading-spinner" />
    <div class="auth-loading-text">{{ t('login.checking') || '正在检测登录状态…' }}</div>
  </div>
  <div v-else-if="isLoggedIn" class="app-layout app-layout--topbar">
    <AppTopbar
      :version-info="versionInfo"
      @user-info="openUserInfo"
      @change-password="openChangePassword"
      @logout="logout"
    />
    <V1DataFrozenBanner />
    <main class="main-content">
      <div v-if="passwordSuccessMessage" class="main-banner">
        <div class="alert alert-success header-alert">{{ passwordSuccessMessage }}</div>
      </div>
      <section class="main-body" :class="{ 'main-body--fill': route.meta.fillViewport }">
        <!--
          路由转场（docs/UI规范/12 §7）：compact 顶级目的地切换 fade 160ms；
          medium+ 用 page-none —— 该名字刻意没有任何 CSS 规则，Transition 无匹配
          样式时瞬时完成 ⇒ 桌面 DOM 与行为零变化。:key 同理只在 compact 绑定
          route.path（桌面 key=undefined 维持既有「同组件不重挂」行为）。
        -->
        <RouterView v-slot="{ Component }">
          <Transition :name="isCompact ? 'page-fade' : 'page-none'" mode="out-in">
            <component :is="Component" :key="isCompact ? route.path : undefined" />
          </Transition>
        </RouterView>
      </section>
    </main>
    <!-- 移动导航抽屉挂载点（Teleport 到 body；遮罩 z-index 对齐既有弹层约定） -->
    <AppNavDrawer />

    <!--
      Hyper compact 壳（docs/UI规范/02 §3–§5）。
      AppBottomNav 内部 v-if="isCompact"，medium 及以上不渲染 —— 桌面 DOM 零变化。
      它的「更多」复用既有 navDrawerOpen，与 AppTopbar 汉堡同语义。
    -->
    <AppBottomNav
      @navigate="(item) => router.push(item.path)"
      @more="navDrawerOpen = true"
    />

    <!--
      账户面板：compact 上的系统设置唯一入口。
      桌面不渲染它 —— 桌面继续用 AppTopbar 的专业下拉（规范 02 §5 末句）。
    -->
    <AppAccountSheet
      v-if="isCompact"
      v-model="accountSheetOpen"
      :health="shellHealth"
      @logout="logout"
      @help="router.push('/examples')"
      @open-admin="router.push('/admin/users')"
    />
  </div>
  <div v-else class="guest-layout">
    <header class="guest-header">
      <a href="/" class="guest-brand" :aria-label="SITE_TITLE">
        <img
          :src="brandLogo"
          :width="SITE_LOGO_SIZE"
          :height="SITE_LOGO_SIZE"
          alt="开轩启圭"
          class="guest-brand-img"
        />
        <span class="guest-brand-text">
          <span>{{ SITE_TITLE_LINE_ONE }}</span>
          <span>{{ SITE_TITLE_LINE_TWO }}</span>
        </span>
      </a>
      <button
        v-if="!isTablet"
        type="button"
        class="btn btn-ghost btn-sm guest-hamburger"
        :aria-label="t('landing.guestNavAria', '产品导航')"
        aria-haspopup="dialog"
        @click="navDrawerOpen = true"
      >☰</button>
      <nav v-else class="guest-nav" :aria-label="t('landing.guestNavAria') || '产品导航'">
        <a href="/customer/update-activate">{{ t('landing.navDownload') }}</a>
        <a href="/bootstrap">{{ t('landing.navSetup') || '安装激活' }}</a>
        <a href="/customer/update-activate">{{ t('landing.navActivate') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navLicense') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navAgreement') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navSupport') }}</a>
      </nav>
      <div class="guest-header-right">
        <ThemeToggle />
        <LanguageSelector />
        <button type="button" class="btn btn-primary btn-sm guest-login-btn" @click="openLogin">{{ t('login.submit') }}</button>
      </div>
    </header>
    <main class="guest-main">
      <RouterView />
    </main>
    <AppNavDrawer :guest-links="guestNavLinks" />
    <LoginModal v-model="showLoginModal" />
  </div>
  <ChangePasswordDialog v-model="showChangePassword" :forced="mustChangePassword" @success="handleChangePasswordSuccess" />
  <UserInfoDialog v-model="showUserInfo" />
</template>

<style scoped>
/*
 * compact 路由转场（docs/UI规范/12 §7，2026-10-05）。
 * 只动 opacity（GPU 合成，禁 layout 属性）；160ms 与 web-mobile 的 page-fade 对齐。
 * scoped 样式能命中子组件根节点（Vue 给子根挂父 scope 属性），Transition 类
 * 恰好作用在子组件根上，所以这里匹配。page-none 刻意不定义 —— 见模板注释。
 * prefers-reduced-motion 下转场取消（12 §4 契约）。
 */
.page-fade-enter-active,
.page-fade-leave-active {
  transition: opacity 160ms ease;
}
.page-fade-enter-from,
.page-fade-leave-to {
  opacity: 0;
}
@media (prefers-reduced-motion: reduce) {
  .page-fade-enter-active,
  .page-fade-leave-active {
    transition: none;
  }
}

/* 2026-07-09: 首次进入时的 auth 探测加载中状态 */
.auth-loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100vh;
  height: 100dvh;
  background: var(--bg-card);
  color: var(--text-secondary);
  font-size: 13px;
}
.auth-loading-spinner {
  width: 32px;
  height: 32px;
  border: 3px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: auth-spin 0.8s linear infinite;
  margin-bottom: 14px;
}
.auth-loading-text {
  letter-spacing: 0.05em;
}
@keyframes auth-spin {
  to { transform: rotate(360deg); }
}

.app-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
}

/* 2026-07-21: topbar 模式下，.app-layout 改为纵向 flex（topbar 在上 + main-content 在下）。
 * overflow 必须 visible，否则绝对定位二级下拉会被裁切成「窄条内滚动」。
 * 滚动约束下放到 .main-content / .main-body。 */
.app-layout--topbar {
  flex-direction: column;
  overflow: visible;
}
.app-layout--topbar .main-content {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  border-inline-start: 0;
  overflow: hidden;
}

.header-alert {
  margin: 0;
  padding: 6px 10px;
  font-size: 12px;
}

/* 2026-09-12: 删除 2026-07-21 topbar 迁移后遗留的整套 sidebar 死样式
 * (.sidebar / .sidebar-* / .nav-* / .toggle-icon / .user-name / .version-tag 等约 350 行，
 * 模板已无对应元素；连带清理 640px 媒体查询中的 .main-header 系列)。 */

.main-content {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.main-banner {
  padding: 8px 24px 0;
}

.main-body {
  flex: 1;
  overflow-y: auto;
  padding: 24px;
}

.guest-layout {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  min-height: 100dvh;
  width: 100%;
  background: var(--bg);
}

.guest-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px clamp(20px, 4vw, 48px);
  border-bottom: 1px solid var(--border);
  background: var(--sidebar);
  position: sticky;
  top: 0;
  z-index: 10;
  width: 100%;
  box-sizing: border-box;
}

.guest-nav {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 14px;
  flex: 1;
  min-width: 0;
  margin-inline-start: 12px;
}

/* 2026-09-13 P1c：访客壳汉堡按钮（仅 <768 由 v-if 渲染），触摸目标 44px */
.guest-hamburger {
  flex-shrink: 0;
  min-width: 44px;
  min-height: 44px;
  padding: 8px 12px;
  font-size: 16px;
  line-height: 1;
}

.guest-nav a {
  color: var(--muted);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
  transition: color 0.15s ease;
}

.guest-nav a:hover {
  color: var(--accent-h);
}

.guest-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text);
  text-decoration: none;
  cursor: pointer;
}

.guest-brand-img {
  display: block;
  width: 60px;
  height: 60px;
  object-fit: contain;
  border-radius: 12px;
  flex-shrink: 0;
}

.guest-brand-text {
  display: inline-flex;
  flex-direction: column;
  gap: 1px;
  font-size: 13px;
  font-weight: 700;
  line-height: 1.2;
  letter-spacing: 0;
  color: var(--text);
  max-width: min(52vw, 420px);
}

.guest-header-right {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  margin-left: auto;
}

.guest-login-btn {
  flex-shrink: 0;
}

.guest-main {
  flex: 1;
  overflow-y: auto;
  width: 100%;
}

@media (max-width: 640px) {
  .main-body {
    padding: 12px;
  }

  .guest-header {
    padding: 12px 16px;
    flex-wrap: wrap;
  }

  .guest-nav {
    order: 3;
    width: 100%;
    margin: 4px 0 0;
    gap: 8px 12px;
  }
}

/* fillViewport 路由（用量趋势等）铺满主区，不再套 24px 内边距。 */
.main-body.main-body--fill {
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
</style>
