// sessionTimeline.ts — SessionTurnsTimeline.vue 文字 (OBS-FE5 輪次時間線)
// 涵蓋：延遲空值兜底 / 重新整理 / 錯誤 / 空態 / 載入更多 / 已全部載入 / 網路錯誤
export default {
  latencyUnknown: '未知',
  refresh: '重新整理',
  refreshing: '重新整理中…',
  retry: '重試',
  empty: '此工作階段暫無輪次記錄',
  loading: '載入中…',
  loadMore: '載入更多輪次',
  allLoaded: '共 {n} 輪次，已全部載入',
  errors: {
    network: '網路錯誤，請檢查連線後重試',
  },
}
