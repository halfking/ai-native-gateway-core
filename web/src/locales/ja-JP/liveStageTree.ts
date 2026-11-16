// liveStageTree.ts — リクエスト段階ツリー（live2）文言（2026-11-16）
export default {
  title: 'リクエスト段階ツリー',
  empty: '現在の絞り込み条件では対象期間内にリクエストがありません',
  stage: {
    auth: '認証/レート制限',
    sanitize: 'マスキング',
    approval: '承認',
    injection: 'インジェクション検知',
    route: 'ルーティング',
    compress: '圧縮',
    firstByte: '初回バイト',
    restore: 'マスキング復元',
    provider: 'プロバイダ/トークンプール',
    meter: '計量',
    reply: '応答',
  },
  state: {
    pending: '未到達',
    active: '進行中',
    done: '完了',
    skipped: '該当なし',
    failed: '失敗',
    warned: '警告あり',
    unmeasured: '未計測',
  },
  value: {
    sanitize: '{n} 件',
    route: '候補 {n} 件',
    meter: '{n} トークン',
    injection: 'スコア {n}（{d}）',
    compress: {
      yes: '圧縮済み',
      no: '圧縮なし',
    },
  },
  decision: {
    pass: '通過',
    need_approval: '承認待ち',
    block: '拒否',
    safe: '無害',
    warn: '検知（警告のみ）',
    unknown: '判定なし',
  },
  return: {
    lane: '応答は戻り経路でクライアントに戻ります',
    label: 'クライアントへ',
  },
  band: {
    status: {
      success: '正常終了',
      in_progress: '進行中',
      failure: '失敗',
    },
    toolsRestore: 'tools 復元 {n}',
    toolsRestored: '復元済み',
    finalModel: '最終モデル',
    upstreamModel: '上流モデル',
    finalModelTip: {
      action: '取得元: ルーティングで選ばれたノードイベント（解決後の標準モデル名）。',
      canonical: '取得元: リクエストに記録された標準モデル名。',
      upstreamRaw: '取得元: 上流へ実際に送信したモデル文字列。',
      frame: '取得元: リクエストのモデル項目。',
      none: '未計測: 判定できるモデル取得元がありません。',
    },
    modelEntry: {
      auto: 'AUTO 選択 {model}',
      direct: 'リクエスト {model}',
    },
    chain: {
      auto: 'auto タスク種別 {task} → 一致 {model}',
      alias: '{from} を {model} に解決',
      switch: 'モデル切替 {from} → {to}',
    },
    cache: {
      hit: 'キャッシュ命中 {pct}%',
      na: 'キャッシュ命中率 —',
      anomaly: 'キャッシュ基準の不整合',
      tip: {
        na: '上流がキャッシュヒット数を報告していないため、0% ではありません。',
        unknown: '上流プロトコルが未判定のため、分母を特定できません。',
        anomaly: '命中率が 100% を超えたため、2 つの基準が混在しています。',
        parallel: '基準: キャッシュ読み取りは入力と並立 ⇒ 分母 = 入力 + キャッシュ読み取り。',
        subset: '基準: キャッシュ読み取りは入力に含まれる ⇒ 分母 = 入力。',
      },
    },
  },
  credibility: {
    verified: '信頼できる',
    suspected: '希釈の疑い',
    diluted: '希釈あり',
    unknown: '判定不能',
    unmeasured: '信頼度は未計測',
    fingerprintMissing: '取得なし',
    measuredAtMissing: '時刻不明',
    tip: {
      unmeasured: 'この資格情報と上流モデルの組合せに信頼度の計測記録はありません。',
      detail:
        '判定 {verdict}｜識別の一貫性 {identity}｜能力 {capability}｜安定性 {consistency}｜基準 {reference}｜システムフィンガープリント {fingerprint}｜計測時刻 {measuredAt}',
    },
  },
  stats: {
    open: '統計を全画面で開く',
    readout: '{total} 件 · 成功率 {rate}% · 進行中 {inFlight}',
    title: 'リアルタイム統計',
    hint:
      '統計の基準はリアルタイムトポロジページと同じです: 直近 5 分と直近 1 時間はデータベース直読み、当日は日次集計（最大 1 時間の遅延）です。',
    close: '閉じる',
  },
  idle: {
    wait: '{ms} ミリ秒新しいリクエストがありません',
  },
}
