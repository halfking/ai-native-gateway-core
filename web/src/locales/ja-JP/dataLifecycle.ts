export default {
  refresh: "更新",
  loading: "読み込み中…",
  storage: {
    migrateFailedWithReason: '移行を完了できませんでした：{reason}。再試行してください。',
  },
  /**
   * 大フィールド管理（BlobManager）の文言。
   * ★ 空状態（2026-10-27 第 14 インクリメント · 空状態 5 分）：以前は汎用キー
   *   `common.feedback.noData`（表示できる内容はまだありません）を使っていた。
   *   このページに**時間フィルターは無い** ⇒ 時間は書かない（ページが知らない事実を作らない）。
   */
  blob: {
    emptyNoResult: '大きなフィールドの記録はまだありません。',
  },
  storageOverview: {
    summary: {
      databaseTitle: "PostgreSQL データベース",
      databaseSubtitle: "総フットプリント（テーブル + インデックス + TOAST）",
      databaseMetric: "データベース",
      tablesMetric: "テーブル",
      indexesMetric: "インデックス",
      toastMetric: "TOAST",
      diskTitle: "ローカルディスク",
      diskUsedOf: "使用済み / {total}",
      path: "パス",
      used: "使用済み",
      free: "空き",
      columnarTitle: "カラムナストレージ（citus_columnar）",
      enabled: "有効",
      notInstalled: "未インストール",
      columnarTableCount: "テーブル数（パーティション含む）",
      columnarTableCountMeta: "columnar アクセスメソッドを使うテーブル",
      columnarTotalColumns: "総カラム数",
      columnarTotalColumnsMeta: "すべてのカラムナ表のカラム合計",
      columnarSize: "カラムナ使用量",
      columnarSizeMeta: "圧縮後（chunk メタデータ含む）",
      columnarDbShare: "DB 比率",
      columnarDbShareMeta: "カラムナ / Database 合計",
      columnarNotInstalledFallback: "citus_columnar 拡張がインストールされていません",
      localLogsTitle: "ローカルログディレクトリ",
      notFound: "見つかりません",
      fileCount: "ファイル数",
      size: "サイズ",
      oldest: "最古",
      newest: "最新",
      localLogsMissingHint: "ログディレクトリが見つからないため、gzip バックアップ/ローテーションは書き込まれません。",
      warningsTitle: "注意"
    },
    tablesTitle: "データベーステーブル Top {n}",
    refresh: "更新",
    refreshLoading: "読み込み中…",
    columns: {
      tableName: "テーブル",
      rows: "行数",
      totalSize: "合計サイズ",
      indexes: "インデックス",
      toast: "TOAST",
      percent: "割合",
      partition: "パーティション",
      actions: "操作"
    },
    partitionBadge: "パーティション",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table} — ロックなし、dead tuples を再利用",
      vacuumFullTitle: "VACUUM FULL {table} — ロックあり、ディスクを OS に返却",
      reindexTitle: "REINDEX TABLE {table} — ロックあり、インデックスを再構築"
    },
    modal: {
      vacuumTitle: "🧹 VACUUM の実行確認",
      vacuumFullTitle: "🗜 VACUUM FULL の実行確認",
      reindexTitle: "🔧 REINDEX の実行確認",
      intro: "{table} に対して {op} を実行します。",
      currentSize: "現在のサイズ：",
      rowCount: "行数：",
      partition: "パーティション：",
      partitionYes: "はい（⚠️ ロックはすべての子パーティションに影響）",
      partitionNo: "いいえ",
      flow: "操作フロー",
      impact: "実行時の影響",
      disk: "ディスク容量への影響",
      time: "推定所要時間",
      cancel: "キャンセル",
      confirm: "{op} を実行",
      success: "{op} {table} 完了 — {size} を解放 ({pct}%)",
      failed: "{op} 失敗：{msg}"
    },
    rowResult: {
      running: "{op} 実行中…",
      success: "{op}: {size} を解放 ({pct}%, {ms}ms)",
      failed: "{op} 失敗：{msg}"
    },
    op: {
      vacuum: {
        flow: "テーブル内の dead tuples を走査し、その領域を再利用可能としてマークします（OS にはすぐ返却しません）。同時に統計情報も更新され、クエリプランナが最適な実行計画を選択しやすくなります。",
        impact: "テーブルをロックせず、SELECT / INSERT / UPDATE / DELETE を継続できます。実行中は CPU と IO を少量余分に消費します。",
        disk: "OS にすぐにはディスクを返却せず、テーブル内で再利用します。以降の INSERT によるファイル拡張は減りますが、pg_database_size はすぐには縮小しません。",
        time: "通常は数秒〜数分。所要時間 ≈ テーブルサイズ / シーケンシャル読み取り IO（一般的な SSD で約 200 MB/s）。"
      },
      vacuumFull: {
        flow: "ACCESS EXCLUSIVE ロックを取得し、テーブル全体を新しいディスクファイルに書き直します。同時にすべての bloat を圧縮し、完了後に新ファイルと差し替えて旧ファイルを解放します。",
        impact: "⚠️ ACCESS EXCLUSIVE ロック保持中は、そのテーブルへの読み書きがすべてブロックされます。トラフィックが少ない時間帯に実行してください。",
        disk: "実行中はディスク使用量が約 2 倍になります（旧ファイル + 新ファイル）。完了後に旧ファイルが解放されます。回収できる bloat は最大でテーブル + TOAST + インデックスの合計サイズまでです。",
        time: "所要時間 ≈ (テーブル + インデックス) サイズ / シーケンシャル書き込み IO（一般的な SSD で約 200 MB/s）。例：10 GB テーブルで 1〜3 分、100 GB で 15〜30 分。"
      },
      reindex: {
        flow: "テーブルのロックを取得し、サポートされるアクセスメソッド（B-tree 等）のすべてのインデックスを順次再構築します。完了後に新インデックスが旧インデックスと差し替えられます。",
        impact: "⚠️ 再構築中は DML（INSERT / UPDATE / DELETE）がロックされます。所要時間はインデックスサイズに比例するため、低トラフィック時間帯の実行を推奨します。",
        disk: "実行中はインデックス使用量が約 2 倍になります（旧 + 新が共存）。完了後に旧インデックスが解放されます。インデックス bloat を解消し、後続の読み取り性能を改善できます。",
        time: "所要時間 ≈ インデックス合計サイズ / シーケンシャル書き込み IO（一般的な SSD で約 200 MB/s）。このテーブルのインデックスは約 {size} で、数秒〜数分の見込みです。"
      }
    }
  },
  hotPartition: {
    hotTableTitle: "Hot 表データ移行",
    hotTableDesc: "hot 表のデータを月次パーティション表へ移行します。移行後に VACUUM FULL を実行して初めて OS にディスクが返却されます。",
    retention: {
      all: "今すぐ全件移行",
      "1day": "最新 1 日を保持",
      "3day": "最新 3 日を保持",
      "7day": "最新 7 日を保持",
      "30day": "最新 30 日を保持"
    },
    startMigrate: "移行を開始",
    migrating: "移行中...",
    migrationProgress: "{migrated} 行移行済み（{batches} バッチ、{duration}s）",
    promoteConfirm: "{label} のうち {hours} より古いデータを移行しますか？",
    promoteAll: "すべて",
    days: "{n} 日",
    partitionTitle: "月次パーティション管理",
    partitionDesc: "古い月次パーティションを削除してディスクを解放します。",
    partitionWarning: "元に戻せない操作です。慎重に実行してください。",
    tables: {
      title: "パーティション表一覧",
      refresh: "更新",
      columns: {
        name: "テーブル",
        description: "説明",
        totalSize: "合計サイズ",
        rows: "総行数",
        partitions: "パーティション数",
        archivable: "整理可能",
        actions: "操作"
      },
      viewPartitions: "パーティションを表示 / 管理",
      noTables: "パーティション表がありません"
    },
    partitionList: {
      title: "{table} のパーティション",
      columns: {
        name: "パーティション名",
        storageType: "ストレージ",
        size: "サイズ",
        rows: "行数",
        month: "月",
        action: "操作"
      },
      storage: {
        columnar: "カラムナ",
        heap: "ヒープ",
        archive: "アーカイブ済み"
      },
      delete: "削除",
      deleting: "削除中...",
      empty: "このテーブルにはパーティションがありません"
    },
    deleteModal: {
      title: "パーティション削除の確認",
      warning: "この操作は元に戻せません！",
      fields: {
        name: "パーティション",
        size: "サイズ",
        rows: "行数",
        month: "月"
      },
      inputLabel: "確認のためパーティション名を入力してください：",
      cancel: "キャンセル",
      confirm: "削除を実行",
      deleting: "削除中...",
    },
    hotTableNames: {
      requestLogs: "リクエストログ",
      credentialModelIndex: "認証情報モデルインデックス",
      usageLedger: "使用量台帳",
      routingDecisionLog: "ルーティング判断ログ"
    },
    labels: {
      rows: "行数",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'hot 表の統計を読み込めませんでした：{reason}。再試行してください。',
    startMigrationFailedWithReason: '移行タスクを開始できませんでした：{reason}。再試行してください。',
    submitDeleteFailedWithReason: '削除タスクを送信できませんでした：{reason}。再試行してください。',
  },
  preview: "クリーンアップをプレビュー",
  hotData: "ホットデータ (0-7日)",
  warmData: "ウォームデータ (7-30日)",
  coldData: "コールドデータ (30-90日)",
  expiredData: "期限切れデータ (>90日)",
  missingDate: "開始日と終了日を選択してください",
  previewFailed: "プレビューに失敗しました",
  needPreview: "先に影響を確認してください",
  executeNotImpl: "クリーンアップ機能は次のフェーズで実装予定です。コマンドラインスクリプトでクリーンアップ操作を実行してください。"
,
  pages: {
    filesystemMaintenance: 'ファイルシステムメンテナンス',
    storageConfig: 'ストレージ設定',
    logManagement: 'ログ管理',
    usageCost: '利用コスト',
    usageEnhanced: '利用率詳細分析',
  },

  usageEnhanced: {
    refresh: '更新',
    loading: '読み込み中…',
    empty: 'この期間の使用量記録はまだありません。',
    cards: {
      totalCost: '総コスト',
      totalRequests: '総リクエスト数',
      periodDelta: '期間比較 Δ コスト',
      cacheHitRate: 'キャッシュヒット率',
    },
    trend: {
      title: 'コスト推移（直近 {days} 日間）',
    },
    hotKeys: {
      title: '上位 API キー（トークン消費順）',
      colId: 'API キー ID',
      colPrefix: 'プレフィックス',
      colApplication: 'アプリケーション',
      colRequests: 'リクエスト数',
      colTokens: 'トークン数',
      colCost: '総コスト',
    },
    degraded: {
      title: '一部の指標は利用できません: ',
      hint: 'データベーススキーマがこのビルドより古くなっています。以下の数値は「計算できない」を示すプレースホルダであり、実測値ではありません。',
    },
    errors: {
      load: '利用率詳細データの読み込みに失敗しました',
    },
    sources: {
      summary: '利用率サマリー',
      costTrend: 'コスト推移',
      periodCompare: '期間比較',
      cacheEconomics: 'キャッシュ経済性',
      hotKeys: 'ホット API キー',
    },
    footer: '最終更新: {time}',
  },

  usageCost: {
    refresh: '更新',
    loading: '読み込み中…',
    compare: {
      title: 'コスト比較',
      currentPeriod: '当期:',
      previousPeriod: '比較:',
      currentLabel: '当期 ({period})',
      previousLabel: '比較期 ({period})',
      change: '変化',
      significant: '有意',
      meta: '{requests} リクエスト：{models} モデル',
    },
    attribution: {
      title: 'コスト帰属',
      groupBy: 'グループ化:',
      model: 'モデル別',
      provider: 'プロバイダ別',
      intent: 'インテント別',
      shareChart: 'コスト比率',
      detailChart: 'コスト内訳（入力 vs 出力）',
      otherNote: 'その他 {count} 件合計: {cost}',
      otherLabel: 'その他',
      colModel: 'モデル',
      colProvider: 'プロバイダ',
      colIntent: 'インテント',
      colRequests: 'リクエスト数',
      colTotalCost: '総コスト',
      colInputCost: '入力コスト',
      colOutputCost: '出力コスト',
      colShare: '比率',
      colLatency: '平均レイテンシ',
    },
    charts: {
      inputCost: '入力コスト',
      outputCost: '出力コスト',
    },
    cache: {
      title: 'キャッシュ経済',
      hitRate: 'キャッシュヒット率',
      cacheSaved: 'キャッシュ節約',
      cacheSavedHint: 'キャッシュなし比',
      compressionSaved: '圧縮節約',
      compressionCount: '{n} 回圧縮',
      savingsRate: '総合節約率',
      totalSaved: '総節約 {cost}',
      actualSpend: '実支出',
      effectiveRatio: '未最適化コストの {pct}',
      totalRequests: '総リクエスト数',
    },
    errors: {
      costTrend: 'コスト推移の読み込みに失敗',
      periodCompare: '期間比較の読み込みに失敗',
      cacheEconomics: 'キャッシュ経済の読み込みに失敗',
    },
    degraded: {
      title: '一部の指標を利用できません：',
      hint: 'データベースのスキーマが現在のビルドより古いため、以下の数値は「計算できなかった」というプレースホルダーであり、実測値ではありません（この期間にデータがなかったこととは異なります）。管理者にデータ集約マイグレーションの実行を依頼してください。',
    },
  },
  confirmDeleteCleanup: '{rows} 行のデータを削除しますか？\n解放見込み容量: {size}\n\nこの操作は元に戻せません！',
  confirmArchiveCleanup: '{rows} 行のデータをアーカイブしますか？\n解放見込み容量: {size}\n\nこの操作は元に戻せません！',
  confirmAttachmentNullify: '実行しますか？\n{n} 件の elements を NULL に設定します\n合計 {size}\n（ファイルシステム上の実体ファイルは削除されません）',
  confirmBlobCleanup: 'クリーンアップしますか？\n{rows} 行に影響\n約 {size} 解放\n（request_body / outbound_body のみ NULL 化、メタデータは保持）',
  confirmReplay: '{filename} をリプレイしますか？',
  confirmReplayArchive: '{filename} をリプレイしてアーカイブしますか？',
  confirmFileCleanup: '{days} 日より前のファイルを削除しますか？\n理由：{reason}',
  confirmLogCleanup: '{days} 日より前のログファイルを削除しますか？この操作は元に戻せません。',
  confirmDirMigrate: '添付ファイルの保存ディレクトリを変更します。既存ファイルは新しいディレクトリへコピーされ、検証完了後に旧ディレクトリは削除されます。\n\n・コピー中もダウンロード/書き込みには影響しません（旧ディレクトリを使用）\n・完了後、自動的に新ディレクトリへ切替\n・ファイル数が多い場合は時間がかかります。進捗は下に表示されます\n\nディレクトリを変更して移行しますか？',
  dirMigrateCancelled: 'キャンセルされました（ディレクトリは未変更）',
}