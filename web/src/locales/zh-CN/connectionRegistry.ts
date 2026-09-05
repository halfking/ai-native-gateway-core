// connectionRegistry.ts — T9 连接注册台视图文案
export default {
  title: '连接注册台',
  subtitle: '流式客户端 SSE 连接注册表（进程内 live + 近期 closed 审计）',
  apiDegraded: '连接注册表 API 拉取失败',
  liveCount: '在线 {count} / 容量 {capacity}',
  historyNote: '关闭记录仅保留本进程近期审计（不持久化）',
  search: '查询连接',
  openJourney: '查看旅程',
  searchPlaceholder: '输入 request_id 查询连接状态…',
  filterPlaceholder: '筛选列表（request_id / 协议 / 客户端）…',
  sections: {
    live: '在线连接',
    closed: '近期关闭',
  },
  columns: {
    requestId: '请求 ID',
    protocol: '协议',
    client: '客户端',
    frames: '帧数',
    lastFrame: '最后帧',
    closeReason: '关闭原因',
    registeredAt: '注册时间',
  },
  empty: {
    live: '暂无在线流式连接',
    closed: '暂无近期关闭记录',
  },
  // 节点恢复时间线（NodeHealthTimelineView 仍使用）
  viewTimeline: '查看节点恢复时间线',
  recoverAt: '预计恢复 {time}（{delta}）',
  detail: {
    lastError: '最近错误',
    lastErrorAt: '最近错误时间',
    recoverAt: '预计恢复时间',
  },
  state: {
    connected: '已建连',
    connecting: '建连中',
    disconnected: '已断开',
  },
  inFlight: '在途 {count}',
}
