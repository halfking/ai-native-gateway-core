// sessionTimeline.ts — SessionTurnsTimeline.vue 文案 (OBS-FE5 轮次时间线)
// 涵盖：延迟空值兜底 / 刷新 / 错误态 / 空态 / 加载更多 / 已全部加载 / 网络错误
export default {
  latencyUnknown: '未知',
  refresh: '刷新',
  refreshing: '刷新中…',
  retry: '重试',
  empty: '该会话暂无轮次记录',
  loading: '加载中…',
  loadMore: '加载更多轮次',
  allLoaded: '共 {n} 轮，已全部加载',
  errors: {
    network: '网络错误，请检查连接后重试',
  },
}
