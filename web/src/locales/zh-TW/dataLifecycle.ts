export default {
  refresh: "重新整理",
  loading: "載入中…",
  storage: {
    migrateFailedWithReason: '遷移沒能完成：{reason}。請重試',
  },
  /**
   * 大欄位管理（BlobManager）文案。
   * ★ 空態（2026-10-27 第 14 增量 · 空狀態 5 分）：原先用通用鍵
   *   `common.feedback.noData`（「暫時沒有內容可顯示」）。
   *   這一頁**沒有時間篩選器** ⇒ 不寫時間範圍（不編造頁面不知道的事實）。
   */
  blob: {
    emptyNoResult: '還沒有大欄位記錄。',
  },
  storageOverview: {
    summary: {
      databaseTitle: "PostgreSQL 資料庫",
      databaseSubtitle: "總占用（表 + 索引 + TOAST 之和）",
      databaseMetric: "資料庫",
      tablesMetric: "表",
      indexesMetric: "索引",
      toastMetric: "TOAST",
      diskTitle: "本機磁碟",
      diskUsedOf: "已用 / {total}",
      path: "路徑",
      used: "已用",
      free: "剩餘",
      columnarTitle: "列存儲存（citus_columnar）",
      enabled: "已啟用",
      notInstalled: "未安裝",
      columnarTableCount: "表數（含分區）",
      columnarTableCountMeta: "使用 columnar 存取方法的表",
      columnarTotalColumns: "欄位總數",
      columnarTotalColumnsMeta: "所有列存表的欄位總和",
      columnarSize: "列存佔用",
      columnarSizeMeta: "壓縮後（含 chunk 中繼資料）",
      columnarDbShare: "DB 佔比",
      columnarDbShareMeta: "列存 / Database 總和",
      columnarNotInstalledFallback: "citus_columnar 擴充未安裝",
      localLogsTitle: "本機日誌目錄",
      notFound: "不存在",
      fileCount: "檔案數",
      size: "大小",
      oldest: "最舊",
      newest: "最新",
      localLogsMissingHint: "未找到日誌目錄，gzip 備份/輪轉不會寫入。",
      warningsTitle: "提示"
    },
    tablesTitle: "資料庫表 Top {n}",
    refresh: "重新整理",
    refreshLoading: "載入中…",
    columns: {
      tableName: "表名",
      rows: "列數",
      totalSize: "總大小",
      indexes: "索引",
      toast: "TOAST",
      percent: "佔比",
      partition: "分區",
      actions: "操作"
    },
    partitionBadge: "分區",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table} — 不鎖表，清理 dead tuples",
      vacuumFullTitle: "VACUUM FULL {table} — 鎖表，回收磁碟給 OS",
      reindexTitle: "REINDEX TABLE {table} — 鎖表，回收索引 bloat"
    },
    modal: {
      vacuumTitle: "🧹 確認執行 VACUUM",
      vacuumFullTitle: "🗜 確認執行 VACUUM FULL",
      reindexTitle: "🔧 確認執行 REINDEX",
      intro: "將對 {table} 執行 {op}。",
      currentSize: "當前大小：",
      rowCount: "列數：",
      partition: "分區：",
      partitionYes: "是（⚠️ 鎖表影響所有子表）",
      partitionNo: "否",
      flow: "操作流程",
      impact: "造成的影響",
      disk: "對磁碟空間的影響",
      time: "估算操作時間",
      cancel: "取消",
      confirm: "確認執行 {op}",
      success: "{op} {table} 完成 — 釋放 {size} ({pct}%)",
      failed: "{op} 失敗：{msg}"
    },
    rowResult: {
      running: "{op} 執行中…",
      success: "{op}: 釋放 {size} ({pct}%, {ms}ms)",
      failed: "{op} 失敗：{msg}"
    },
    op: {
      vacuum: {
        flow: "掃描表中的 dead tuples，將其佔用的空間標記為可重用（不立即歸還作業系統）。同時更新統計資訊，讓查詢規劃器選擇更優的執行計畫。",
        impact: "不鎖表，不阻塞正常的 SELECT / INSERT / UPDATE / DELETE 讀寫操作。執行時會佔用少量額外 CPU 和 IO。",
        disk: "不會立即釋放磁碟空間給作業系統，僅在表內複用。可降低後續 INSERT 觸發的磁碟擴展次數，但不會改變 pg_database_size 的數值。",
        time: "通常數秒至數分鐘。耗時 ≈ 表大小 / 循序掃描 IO 吞吐（普通 SSD 約 200 MB/s）。"
      },
      vacuumFull: {
        flow: "為表獲取 ACCESS EXCLUSIVE 鎖後，將整張表資料重寫到新的磁碟檔案中，同時壓縮所有 bloat。完成後用新檔案替換舊檔案並釋放舊檔案佔用的磁碟空間。",
        impact: "⚠️ 持有 ACCESS EXCLUSIVE 鎖期間完全禁止對該表的所有讀寫，會阻塞所有依賴該表的業務請求。建議在凌晨低峰期執行。",
        disk: "執行期間磁碟佔用會先翻倍（保留舊檔案 + 寫入新檔案），結束後釋放舊檔案空間。最大可回收 bloat 一般等於表 + TOAST + 索引的總和。",
        time: "耗時 ≈ (表大小 + 索引大小) / 循序寫入 IO 吞吐（普通 SSD 約 200 MB/s）。例如 10 GB 表約需 1–3 分鐘，100 GB 表約需 15–30 分鐘。"
      },
      reindex: {
        flow: "為該表的所有索引獲取鎖後，依序重建每一個索引（B-tree 等可重建索引類型）。完成後用新索引檔案替換舊索引檔案。",
        impact: "⚠️ 會持有鎖表，時間與索引大小線性相關。執行期間對該表的 DML（INSERT/UPDATE/DELETE）會被阻塞。建議在低峰期執行。",
        disk: "執行期間索引佔用空間會先翻倍（舊索引 + 新索引並存），結束後釋放舊索引空間。可回收索引 bloat 並提升後續查詢效率。",
        time: "耗時 ≈ 索引總大小 / 循序寫入 IO 吞吐（普通 SSD 約 200 MB/s）。本表索引合計約 {size}，預計數秒至數分鐘。"
      }
    }
  },
  hotPartition: {
    hotTableTitle: "Hot 表資料遷移",
    hotTableDesc: "將 hot 表中的資料遷移到月度分區表。遷移後需要執行 VACUUM FULL 才能真正釋放磁碟空間。",
    retention: {
      all: "立即遷移全部",
      "1day": "保留 1 天",
      "3day": "保留 3 天",
      "7day": "保留 7 天",
      "30day": "保留 30 天"
    },
    startMigrate: "開始遷移",
    migrating: "遷移中...",
    migrationProgress: "已遷移 {migrated} 列（{batches} 批次，{duration}s）",
    promoteConfirm: "確認遷移 {label} 中超過 {hours} 的資料？",
    promoteAll: "全部",
    days: "{n} 天",
    partitionTitle: "月度分區管理",
    partitionDesc: "刪除舊的月度分區以釋放磁碟空間。",
    partitionWarning: "操作不可恢復，請謹慎。",
    tables: {
      title: "分區表列表",
      refresh: "重新整理",
      columns: {
        name: "表名",
        description: "說明",
        totalSize: "總大小",
        rows: "總列數",
        partitions: "分區數",
        archivable: "可清理",
        actions: "操作"
      },
      viewPartitions: "查看/管理分區",
      noTables: "暫無分區表"
    },
    partitionList: {
      title: "{table} 的分區",
      columns: {
        name: "分區名",
        storageType: "儲存類型",
        size: "大小",
        rows: "列數",
        month: "月份",
        action: "操作"
      },
      storage: {
        columnar: "列存",
        heap: "堆表",
        archive: "已歸檔"
      },
      delete: "刪除",
      deleting: "刪除中...",
      empty: "該表暫無分區資料"
    },
    deleteModal: {
      title: "確認刪除分區",
      warning: "此操作不可恢復！",
      fields: {
        name: "分區名",
        size: "大小",
        rows: "列數",
        month: "月份"
      },
      inputLabel: "請輸入分區名以確認：",
      cancel: "取消",
      confirm: "確認刪除",
      deleting: "刪除中...",
    },
    hotTableNames: {
      requestLogs: "請求記錄",
      credentialModelIndex: "憑證模型索引",
      usageLedger: "用量帳本",
      routingDecisionLog: "路由決策記錄"
    },
    labels: {
      rows: "行數",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'hot 表統計沒能載入出來：{reason}。請重試',
    startMigrationFailedWithReason: '遷移任務沒能啟動：{reason}。請重試',
    submitDeleteFailedWithReason: '刪除任務沒能提交：{reason}。請重試',
  },
  preview: "預覽清理",
  hotData: "熱資料 (0-7天)",
  warmData: "溫資料 (7-30天)",
  coldData: "冷資料 (30-90天)",
  expiredData: "過期資料 (>90天)",
  missingDate: "請選擇起始日期和結束日期",
  previewFailed: '預覽沒能完成，請重試',
  needPreview: "請先預覽影響",
  executeNotImpl: "清理功能將在下一階段實作。請使用命令列指令碼執行清理操作。"
,
  pages: {
    filesystemMaintenance: '檔案系統維護',
    storageConfig: '儲存設定',
    logManagement: '日誌管理',
    usageCost: '用量成本',
    usageEnhanced: '用量增強分析',
  },

  usageEnhanced: {
    refresh: '重新整理',
    loading: '載入中…',
    empty: '這段時間沒有用量記錄',
    cards: {
      totalCost: '總成本',
      totalRequests: '總請求數',
      periodDelta: '同期比較 Δ 成本',
      cacheHitRate: '快取命中率',
    },
    trend: {
      title: '成本趨勢（最近 {days} 天）',
    },
    hotKeys: {
      title: 'Top API keys（依 token 消耗排序）',
      colId: 'API 金鑰 ID',
      colPrefix: 'Key 前綴',
      colApplication: '應用程式',
      colRequests: '請求數',
      colTokens: 'Token 數',
      colCost: '總成本',
    },
    degraded: {
      title: '部分指標不可用：',
      hint: '資料庫結構落後於目前版本，以下數字是「無法計算」的佔位值，不是真實量測值。',
    },
    errors: {
      load: '用量增強資料沒能載入出來，請重試',
    },
    sources: {
      summary: '用量總覽',
      costTrend: '成本趨勢',
      periodCompare: '週期比較',
      cacheEconomics: '快取經濟學',
      hotKeys: '熱門 API Key',
    },
    footer: '最近重新整理：{time}',
  },

  usageCost: {
    refresh: '重新整理',
    loading: '載入中…',
    compare: {
      title: '成本對比',
      currentPeriod: '本期：',
      previousPeriod: '對比：',
      currentLabel: '本期 ({period})',
      previousLabel: '對比期 ({period})',
      change: '變化',
      significant: '顯著',
      meta: '{requests} 次請求：{models} 個模型',
    },
    attribution: {
      title: '成本歸因',
      groupBy: '分組維度：',
      model: '依模型',
      provider: '依提供商',
      intent: '按意圖',
      shareChart: '成本佔比',
      detailChart: '成本明細（輸入 vs 輸出）',
      otherNote: '其他 {count} 項合計: {cost}',
      otherLabel: '其他',
      colModel: '模型',
      colProvider: '供應商',
      colIntent: '意圖',
      colRequests: '請求數',
      colTotalCost: '總成本',
      colInputCost: '輸入成本',
      colOutputCost: '輸出成本',
      colShare: '佔比',
      colLatency: '平均延遲',
    },
    charts: {
      inputCost: '輸入成本',
      outputCost: '輸出成本',
    },
    cache: {
      title: '快取經濟學',
      hitRate: '快取命中率',
      cacheSaved: '快取節省',
      cacheSavedHint: '相對無快取',
      compressionSaved: '壓縮節省',
      compressionCount: '{n} 次壓縮',
      savingsRate: '綜合節省率',
      totalSaved: '總節省 {cost}',
      actualSpend: '實際支出',
      effectiveRatio: '占無優化成本 {pct}',
      totalRequests: '總請求數',
    },
    errors: {
      costTrend: '成本趨勢沒能載入出來，請重試',
      periodCompare: '週期對比沒能載入出來，請重試',
      cacheEconomics: '快取成本沒能載入出來，請重試',
    },
    degraded: {
      title: '部分指標不可用：',
      hint: '資料庫結構落後於目前版本，以下數字是「無法計算」的佔位值，不是真實測量值（不等於這段期間沒有資料）。請聯絡管理員執行資料聚合遷移。',
    },
  },
  confirmDeleteCleanup: '確認刪除 {rows} 行資料？\n預計釋放空間: {size}\n\n此操作不可逆！',
  confirmArchiveCleanup: '確認封存 {rows} 行資料？\n預計釋放空間: {size}\n\n此操作不可逆！',
  confirmAttachmentNullify: '確認執行？\n將置 NULL {n} 個 elements\n累計 {size}\n（不會刪除檔案系統實體檔案）',
  confirmBlobCleanup: '確認清理？\n影響 {rows} 行\n釋放約 {size}\n（僅置 NULL request_body / outbound_body，保留元資料）',
  confirmReplay: '確認回放 {filename}？',
  confirmReplayArchive: '確認回放並封存 {filename}？',
  confirmFileCleanup: '確認刪除 {days} 天前的檔案？\n原因：{reason}',
  confirmLogCleanup: '確認刪除 {days} 天前的日誌檔案？此操作不可恢復。',
  confirmDirMigrate: '即將修改附件儲存目錄，系統會把現有檔案複製到新目錄，校驗完成後刪除舊目錄。\n\n· 複製期間下載/寫入不受影響（仍走舊目錄）\n· 完成後自動切換到新目錄\n· 檔案較多時可能耗時較長，進度會在下方顯示\n\n確認修改目錄並遷移？',
  dirMigrateCancelled: '已取消（目錄未修改）',
}