// sessionAnalytics.ts — セッション分析画面の文案。
export default {
  title: 'セッション分析',
  nav: {
    analyticsCenter: 'セッション分析センター',
    overview: '概要',
    userRanking: 'ユーザーランキング',
    userDetail: 'ユーザー詳細',
    agentRanking: 'エージェントランキング',
    modelRanking: 'モデルランキング',
  },
  hint: {
    modelTrend: '使用量トレンドで同じ期間のこのモデルのリクエスト/トークン/コスト曲線を見る',
  },

  range: {
    label: '集計期間',
    start: '開始日',
    end: '終了日',
    preset7: '直近 7 日間',
    preset30: '直近 30 日間',
  },
  action: {
    refresh: 'データを更新',
    retry: '再読み込み',
    reset: '絞り込みを解除',
    back: '人員ランキングへ戻る',
    viewDetail: '詳細を表示',
  },
  metric: {
    totalSessions: 'セッション総数',
    avgTokens: '平均トークン数',
    avgDuration: '平均セッション時間',
    avgTurns: '平均やり取り回数',
    totalCost: '総コスト',
    activeUsers: '活動中の利用者',
    activeProjects: '進行中のプロジェクト',
    activeTasks: '進行中のタスク',
    successRate: '成功の割合',
    cacheRate: 'キャッシュヒット率',
    sessionCount: 'セッション数',
    taskCount: 'タスク数',
    rank: '現在の順位',
    rankChange: '順位の変動',
    internalCost: '内部コスト（USD）',
    externalCost: '外部コスト（クレジット）',
  },
  growth: '前期間比 {value}%',
  trend: {
    sessions: 'セッションの推移',
    cost: 'コストの推移',
    tokens: 'トークンの推移',
    tasks: 'タスクの推移',
    rank: '順位の変動',
  },
  section: {
    topModels: '利用頻度の高いモデル',
    topUsers: '利用の多い利用者',
    topProjects: '利用の多いプロジェクト',
    projects: 'プロジェクト一覧',
    tasks: 'タスク一覧',
  },
  col: {
    rank: '順位',
    owner: '利用者',
    email: 'メールアドレス',
    project: 'プロジェクト',
    task: 'タスク',
    agent: 'エージェント',
    model: 'モデル',
    provider: 'プロバイダー',
    sessions: 'セッション数',
    projects: 'プロジェクト数',
    tasks: 'タスク数',
    tokens: 'トークン数',
    cost: 'コスト',
    requests: 'リクエスト数',
    users: '利用人数',
    successRate: '成功の割合',
    cacheRate: 'キャッシュヒット率',
    avgDuration: '平均時間',
    lastActive: '最終利用日時',
    latestTask: '最新のタスク',
    lastUsed: '最終利用日',
  },
  filter: {
    project: 'プロジェクトで絞り込み',
    sortBy: '並べ替えの基準',
    order: '並べ替えの方向',
    apply: 'フィルタを適用',
  },
  sort: {
    sessions: 'セッション数',
    tokens: 'トークン数',
    cost: 'コスト',
    lastActive: '最終利用日時',
    users: '利用人数',
    requests: 'リクエスト数',
    successRate: '成功の割合',
    cacheRate: 'キャッシュヒット率',
    lastUsed: '最終利用日',
  },
  order: {
    asc: '昇順',
    desc: '降順',
  },
  unit: {
    seconds: '{value}秒',
    milliseconds: '{value}ミリ秒',
    percent: '{value} %',
    usd: '{value} USD',
    credits: '{value}クレジット',
  },
  costNote:
    'コストの列は内部コスト（USD）と外部クレジットの合計です。単位が異なるため、相対比較にだけ使えて、同じ金額として扱うことはできません。',
  state: {
    loading: 'セッション分析データを読み込んでいます',
    staleTitle: '集計データを更新中です',
    staleReason:
      '集計ビューのデータが鮮度予算を超えたため、サーバーは古い数値を返すことを拒否しました。しばらくしてから再試行してください。',
  },
  empty: {
    noResultOverview:
      'この期間にはまだセッションデータがありません。別の集計期間でお試しください。',
    noResultUserRanking:
      'この期間にはまだセッションを行う人がいません。期間を変えるか、プロジェクトフィルターを解除してください。',
    filteredUserRanking:
      '現在の絞り込み条件に一致する人のセッション記録はありません。プロジェクトフィルターを解除すると全員を確認できます。',
    noResultUserDetail:
      'このユーザーは選択した期間にセッション記録がありません。別の集計期間でお試しください。',
    noResultUserProjects:
      'このユーザーは選択した期間にプロジェクト記録がありません。別の集計期間でお試しください。',
    noResultProjectTasks:
      'このプロジェクトは選択した期間にタスク記録がありません。別の集計期間でお試しください。',
    noResultAgentRanking:
      'この期間にはまだエージェントが一度も使われていません。別の集計期間でお試しください。',
    noResultModelRanking:
      'この期間にはまだモデルが一度も呼び出されていません。別の集計期間でお試しください。',
  },
  error: {
    overviewFailed: 'セッション総覧の読み込みに失敗しました。再試行してください。',
    userRankingFailed: 'ユーザーランキングの読み込みに失敗しました。再試行してください。',
    userDetailFailed: 'ユーザー詳細の読み込みに失敗しました。再試行してください。',
    userProjectsFailed: 'プロジェクト一覧の読み込みに失敗しました。再試行してください。',
    projectTasksFailed: 'タスク一覧の読み込みに失敗しました。再試行してください。',
    agentRankingFailed: 'エージェントランキングの読み込みに失敗しました。再試行してください。',
    modelRankingFailed: 'モデルランキングの読み込みに失敗しました。再試行してください。',
  },
}
