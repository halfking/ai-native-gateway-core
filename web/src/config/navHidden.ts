/**
 * navHidden — 菜单隐藏清单（2026-11-23 owner 指令）
 *
 * ## 要隐藏什么
 *
 * owner 指令：「将『接入指南』与『运维中心』下的『赞助与捐赠』菜单隐藏」，
 * 随后澄清：**只隐藏「赞助与捐赠」这一个菜单项，不要把「接入指南」整组藏掉**。
 *
 * 「赞助与捐赠」在界面上挂在**两个不同来源**上：
 * ① 线上「运维中心」组是 **ai-native-maintain 远端下发**的菜单（前端
 *    `mergeRemoteOps` 会用远端组替换本地 `opsplatform` 组），其中有一项
 *    `path=/maintain/ops/donations → label=赞助与捐赠`
 *    （211 生产 `GET /maintain-api/menu/ops` 实测，2026-11-23）；
 * ② 本地真源 `appNav.ts` 的「接入指南」组（id=`guide`）里另有一项同名的
 *    `path=/maintain/donate → labelKey=nav.item.supportDonate`。
 * ⇒ 两个来源都要盖住，只处理一处，界面上仍会剩下一个「赞助与捐赠」。
 *
 * ★ **「接入指南」组本身保持可见**（接入示例 / 自动配置 / 帮助手册 / 自动更新），
 *   本清单只针对捐赠入口这一个菜单项。
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
 * ## 为什么只有路径清单，没有组清单
 *
 * 首版曾带一个「整组隐藏」开关（组 id 清单），owner 澄清后**已撤掉**：
 * 那条路径没有消费者了，而仓内对「单消费者布尔闸门」的既有结论是
 * 「不是资产，是负债」（2026-11-17「打开功能」轮退役 `NavItem.hidden`
 * 的同一条纪律，见 `appNav.ts` 的 /live2 注释与 `appNav.test.ts` 的反向锁钉测）。
 * ⇒ 隐藏策略只保留「路径」一把尺子；`NavItem.hidden` 字段也不复活
 *   （否则等于把那个已退役的闸门换个名字请回来）。
 *
 * ★ 注意口径：**隐藏清单是渲染层策略，不是真源删除**。真源里这两条路径仍在
 *   （`NAV_GROUPS` 仍含 donate 条目），口径门与 i18n 键覆盖不受影响。
 */

/**
 * 按路径隐藏：本地真源与远端运维菜单通用。
 *
 * ★ `/maintain/donate`（本地真源，「接入指南」组内）与 `/maintain/ops/donations`
 *   （远端运维中心下发）是**两个不同来源、同名文案**的入口，必须分别列出；
 *   只盖住其中一处，界面上仍会剩下一个「赞助与捐赠」。
 */
export const HIDDEN_NAV_PATHS: readonly string[] = [
  '/maintain/donate',        // 本地真源：「接入指南」组内的捐赠入口
  '/maintain/ops/donations', // 远端 ai-native-maintain 运维中心下发的捐赠入口
  // 2026-11-25 owner 指令：请求阶段树菜单暂时隐藏（菜单不见；路由/页面保留，
  // 直达 /live2 仍可用）。挂在同一份「路径级隐藏清单」上而不是复活
  // NavItem.hidden 布尔闸门 —— 那个单消费者闸门已于 2026-11-17 退役
  //（见 appNav.ts 的 /live2 注释：「没有第二消费者的布尔闸门不是资产，是负债」）。
  // 为什么藏：该页数据查询的效率/及时性/准确性审计尚未收口（2026-11-25 轮），
  // 结论落地前不再向用户宣传入口。/live 顶栏的「阶段树」按钮是**页面内**入口，
  // 不走菜单可见性链路 ⇒ 若也要藏需另改 LiveTopologyView（本轮未动，待 owner）。
  '/live2',                  // 请求阶段树（页面与路由保留，只藏菜单入口）

  // 2026-11-27 非管理员菜单重构轮（owner 指令）：「套餐与充值」菜单隐藏。
  // 隐藏 ≠ 删除：路由 /tenant/pricing 与 MaaSPricingView 页面保留，直链仍可
  // 访问（租户账户页内的充值入口不受影响）；只是菜单不再宣传充值入口。
  // 走本清单而不是从真源删除的理由同上：真源不动 ⇒ navCounts 口径门、
  // menu-config.json 构建产物零漂移。
  '/tenant/pricing',         // 套餐与充值（租户门户组，tenantOnly）
]

/** 该路径的菜单项是否隐藏（本地与远端同尺）。 */
export function isHiddenNavPath(path: string): boolean {
  return HIDDEN_NAV_PATHS.includes(path)
}
