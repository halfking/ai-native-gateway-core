// hyper.ts —— Hyper 壳层文案（底栏 / 账户面板 / 列表状态 / 专注 / 桌面专属提示）。
// 2026-10-04 随 Hyper 运行时新增（docs/UI规范/00 §5.4，H2/H3）。
export default {
  bottomNav: {
    ariaLabel: '主导航',
    more: '更多',
  },
  account: {
    title: '账户',
    language: '语言',
    theme: '主题',
    help: '帮助',
    health: '服务健康',
    healthy: '可达',
    unhealthy: '不可达',
    unknown: '状态未知',
    logout: '退出登录',
    adminEntry: '管理入口',
    close: '关闭',
  },
  dataView: {
    table: '表格',
    cards: '卡片',
  },
  list: {
    refreshing: '正在刷新…',
    loadMore: '加载更多',
    loadingMore: '加载中…',
    retry: '重试',
    allLoaded: '已全部加载 {count} 条',
    loadFailed: '加载失败，请重试',
  },
}
