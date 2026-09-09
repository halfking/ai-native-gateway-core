export default {
  title: '代理管理',
  tabs: { status: '狀態概覽', subscriptions: '訂閱管理', nodes: '節點管理' },
  status: { overview: '總覽', subscriptions: '活躍訂閱', nodes: '節點總數', dialable: '可撥號節點', healthy: '健康節點', unhealthy: '不健康節點', selectedNode: '目前選取節點', noNodeSelected: '未選取節點', swapState: '自動切流狀態', currentNode: '目前活躍節點', lastProbeAt: '最近主動探測', consecutiveFails: '連續失敗次數', probeInterval: '探測間隔', swapIdle: '暫無活躍節點', regions: '地區分布', policyTitle: '選擇策略' },
  actions: { batchHealthCheckAll: '批量探活全部節點', forceSwap: '強制切流到最佳節點' },
  regions: { empty: '暫無節點資料', region: '地區', total: '總數', dialable: '可撥號', active: '活躍', unhealthy: '不健康', banned: '已規避', avgLatency: '平均延遲', bestNode: '最佳節點' },
  policy: { lbStrategy: '負載平衡策略', affinity: '地區親和性', autoDisableThreshold: '自動停用閾值', autoDisableEnabled: '自動停用', autoRecoverEnabled: '自動恢復', swapIntervalMs: '切流間隔 (ms)', swapFailThreshold: '切流觸發連續失敗', edit: '編輯策略', editTitle: '編輯選擇策略', loading: '載入中…' },
  subscriptions: { create: '新建訂閱', createTitle: '新建代理訂閱', name: '訂閱名稱', url: '訂閱網址', urlHint: '必須為 http:// 或 https:// 開頭的 Clash 訂閱網址', nodeCount: '節點數', status: '狀態', lastFetch: '最後拉取', refresh: '重新整理', notes: '備註', bannedRegions: '規避地區', bannedRegionsHint: '以逗號分隔，例如 US、JP、HK；這些地區的節點會在出口選擇時被過濾。', batchHealthCheck: '批量探活', empty: '暫無訂閱' },
  nodes: { create: '新建節點', createTitle: '新建代理節點', subscription: '所屬訂閱', selectSubscription: '請選擇訂閱', protocolHint: '僅支援 HTTP/HTTPS/SOCKS5；trojan/vless 需先暴露為本機網橋', healthCheck: '探活', empty: '暫無節點' },
  node: { name: '節點名稱', protocol: '協議', server: '伺服器', port: '連接埠', username: '使用者名稱', password: '密碼', location: '位置', bannedRegions: '規避地區', dialable: '可撥號', status: '狀態', latency: '延遲', failures: '連續失敗', healthCheckUrl: '健康檢查網址' },
  confirmRefresh: '確定重新整理訂閱「{name}」嗎？', confirmDeleteSub: '確定刪除訂閱「{name}」及其所有節點嗎？', confirmHealthCheck: '確定對節點「{name}」執行健康檢查嗎？', confirmDeleteNode: '確定刪除節點「{name}」嗎？', confirmBatchAll: '確定對全部節點執行批量探活嗎？這會跳過智慧間隔，可能耗時數十秒。', confirmBatchSub: '確定對訂閱「{name}」下的所有節點執行批量探活嗎？',
  batchResult: { healthAll: '批量探活完成：成功 {ok} / 失敗 {failed} / 跳過 {skipped}', healthSub: '訂閱「{name}」批量探活：成功 {ok} / 失敗 {failed}', swap: '已強制切流到節點：{name}', policySaved: '策略已儲存並持久化' },
  error: { loadStatusFailed: '載入狀態失敗', loadSubsFailed: '載入訂閱清單失敗', loadNodesFailed: '載入節點清單失敗', nameUrlRequired: '訂閱名稱與網址不能為空', subscribeUrlMustHttp: '訂閱網址必須為 http:// 或 https:// 開頭', createSubFailed: '建立訂閱失敗', refreshSubFailed: '重新整理訂閱失敗', deleteSubFailed: '刪除訂閱失敗', nodeFieldsRequired: '節點名稱、伺服器、所屬訂閱不能為空', protocolNotDialable: '僅支援建立 HTTP/HTTPS/SOCKS5 節點，trojan/vless 需透過本機網橋暴露', createNodeFailed: '建立節點失敗', healthCheckFailed: '健康檢查失敗', deleteNodeFailed: '刪除節點失敗', batchHealthFailed: '批量探活失敗', swapFailed: '強制切流失敗', savePolicyFailed: '儲存策略失敗', saveSubBanFailed: '儲存訂閱規避地區失敗', saveNodeBanFailed: '儲存節點規避地區失敗' },
}
