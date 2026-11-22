/**
 * navHidden — 菜单隐藏清单（2026-11-23 owner 指令）
 *
 * ## 要隐藏什么
 *
 * owner 指令：「将『接入指南』与『运维中心』下的『赞助与捐赠』菜单隐藏」。
 *
 * 线上实测（2026-11-23，211 生产 `GET /maintain-api/menu/ops`）：「运维中心」
 * 组是 **ai-native-maintain 远端下发**的菜单（前端 `mergeRemoteOps` 会用远端
 * 组替换本地 `opsplatform` 组），其中有一项
 * `path=/maintain/ops/donations → label=赞助与捐赠`。
 * 本地真源 `appNav.ts` 的「接入指南」组（id=`guide`）里另有一项同名的
 * `path=/maintain/donate → labelKey=nav.item.supportDonate`。
 * ⇒ **同名入口在界面上出现两次**，两个来源都要盖住。
 *
 * ## 为什么是「清单」而不是「从真源删除」
 *
 * ① **隐藏 ≠ 删除**：页面、路由、外链全部保留（直链仍可访问）。
 *    删真源会让 `/help`（帮助手册）、`/examples`（接入示例）变成无入口孤儿页面，
 *    而 owner 要的是「菜单里别出现」，不是「功能下线」。
 * ② **口径门不受影响**：`navCounts`（ITEMS 68 / PATHS 67 / GROUPS 8 / FOLDED 54）
 *    按**真源**计数，与 `opsPlatform`/`piAnalysis` 这类渲染层可见性闸门同一口径
 *    —— 真源不动 ⇒ 三份简易模式方案文档、台账生成器（gen-simple-mode-ledger.mjs）、
 *    `web/public/menu-config.json` 构建产物**全部零改动**，不会产生
 *    「门禁绿着但文档数字错了」的漂移。
 * ③ **按路径隐藏才能盖住两处**：远端运维菜单的条目由后端 JSON 构造
 *    （没有本地 NavItem 可挂标记）⇒ 只有「路径」这一把尺子能同时管住
 *    本地真源与远端下发两条链路。
 *
 * ## 为什么不是给 NavItem 加 `hidden` 字段
 *
 * 2026-11-17「打开功能」轮已把 `NavItem.hidden` 这个闸门**整体退役**（见
 * `appNav.ts` 的 /live2 注释与 `appNav.test.ts` 的反向锁钉测）：单消费者的
 * 布尔闸门会把「一次决定」伪装成「一个机制」。本次不再复活那个字段 ——
 * 隐藏策略集中在本清单里，一眼看得见「藏了什么、为什么藏」，也便于日后
 * 增删而不是在几十个条目上撒标记。
 *
 * ★ 注意口径：**隐藏清单是渲染层策略，不是真源删除**。真源里这些条目仍在
 *   （`NAV_GROUPS` 仍含 guide 组与两项捐赠入口），口径门与 i18n 键覆盖不受影响。
 */

/** 整组隐藏：组标题与其下全部条目一起从菜单消失。 */
export const HIDDEN_NAV_GROUP_IDS: readonly string[] = [
  // 「接入指南」组（labelKey=nav.group.guide）—— owner 2026-11-23 指令隐藏。
  'guide',
]

/**
 * 按路径隐藏：本地真源与远端运维菜单通用。
 *
 * ★ `/maintain/donate`（本地真源，guide 组内）与 `/maintain/ops/donations`
 *   （远端运维中心下发）是**两个不同来源、同名文案**的入口，必须分别列出；
 *   只盖住其中一处，界面上仍会剩下一个「赞助与捐赠」。
 */
export const HIDDEN_NAV_PATHS: readonly string[] = [
  '/maintain/donate',        // 本地真源：「接入指南」组内的捐赠入口
  '/maintain/ops/donations', // 远端 ai-native-maintain 运维中心下发的捐赠入口
]

/** 该分组是否整组隐藏。 */
export function isHiddenNavGroup(groupId: string): boolean {
  return HIDDEN_NAV_GROUP_IDS.includes(groupId)
}

/** 该路径的菜单项是否隐藏（本地与远端同尺）。 */
export function isHiddenNavPath(path: string): boolean {
  return HIDDEN_NAV_PATHS.includes(path)
}
