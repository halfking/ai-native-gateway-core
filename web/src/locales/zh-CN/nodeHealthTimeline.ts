// nodeHealthTimeline.ts — T9 节点恢复时间线视图文案
export default {
  title: '节点恢复时间线',
  subtitle: '节点凭据过去一段时间内的关键恢复事件',
  loading: '正在加载恢复时间线...',
  error: '恢复时间线加载失败',
  empty: '该节点暂无恢复事件记录',
  observationDegraded: '观测已降级',
  reason: '原因 {code}',
  duration: '耗时 {ms}',
  events: {
    failed: '失败',
    probing: '探针中',
    recovered: '已恢复',
    degraded: '已降级',
    reconnected: '已重连',
    quarantined: '已隔离',
  },
}