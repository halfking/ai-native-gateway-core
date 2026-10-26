/**
 * Site-wide branding — shared with Maintain shell via the same title string.
 *
 * ★ 2026-10-27（第 10 增量 · 应用外壳 i18n 化）：
 *   这里原本导出的是**硬编码中文常量**（`SITE_TITLE = 'AI Native 组织核心网关'`），
 *   而它出现在 5 个外壳组件里（App / AppTopbar / LoginModal / LandingView /
 *   UpdateActivateView）⇒ 换语种时这 5 处仍然是中文，且**语言包里没有任何一份
 *   可以改的文案**——它是绕过了 i18n 体系的第二份真值。
 *   ⇒ 值统一搬到语言包（`locales/<lang>/app.ts` → `app.brand.*`），语言包成为唯一真值。
 *
 * ★★ 为什么这里**不**导出 `SITE_TITLE_KEY` 常量（而是让调用点写字面量）：
 *   i18n 引用集扫描器（`src/i18n/scanner.ts`）只认**字面量**键 ——
 *   `t(SITE_TITLE_KEY)` 里的键对它**不可见** ⇒ 该键会被算成「未引用」⇒
 *   它就**逃出了「用户可见简体残留 = 0」那道硬闸的视野**（实测：本轮用常量写的
 *   `app.brand.siteTitle` / `app.brand.siteTitleLineTwo` 两个键确实没进引用集）。
 *   ⇒ **键必须写字面量**（`t('app.brand.siteTitle')`），值才归语言包管。
 *   这是第 9 增量的同一条教训：**「只认字面量」是量具的形状，不是缺陷**；
 *   要么让代码迁就量具，要么扩展量具——两者都要求先承认量具有边界。
 */

/** 品牌专名（AI Native）——跨语种不译，因此留在代码里而不是语言包。 */
export const SITE_TITLE_LINE_ONE = 'AI Native'

export const SITE_LOGO_SIZE = 60
