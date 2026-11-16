// selfUpdateView.ts — セルフアップデート管理画面（SelfUpdateView）の文言。
//
// 文言の原則（画面コードと同じ根拠）：チェック失敗 / リモートリリース元未設定 /
// 未チェック の 3 つは「結論が得られない」状態なので、必ず「状態不明」と理由を出します。
// この状態で「すでに最新」と表示することはありません。
export default {
  pageTitle: 'セルフアップデートとアップグレード',
  pageSubtitle:
    'ノードが自動で新しいリリースを調べ、リマインダーを出し、管理者が確認してからアップグレードを適用します',

  statusCardTitle: '現在のリリース状態',
  statusCardHint: 'このノードが実行中のリリースと、直近のリモートリリース検査の結果を表示します',
  currentVersion: '現在のバージョン',
  latestVersion: '最新のバージョン',
  buildSequence: 'ビルドシークエンス',
  lastCheckedAt: '最終チェック時刻',
  checkInterval: 'チェック間隔',
  checkIntervalHours: '{hours} 時間ごとに自動チェックします',
  checkIntervalMinutes: '{minutes} 分ごとに自動チェックします',
  checkIntervalSeconds: '{seconds} 秒ごとに自動チェックします',
  checkNow: '今すぐ更新を確認',
  checking: '確認中',

  stateNotConfiguredLabel: 'リモートリリース元が未設定',
  stateNotConfiguredDetail:
    'このノードはリモートリリース元が未設定のため、自動チェックとリマインダーは無効です。新しいリリースがあるかどうかは判断できません。',
  stateUnknownLabel: 'リリース状態が不明',
  stateUnknownDetail:
    'リリースチェックで結論が得られませんでした：{reason}。チェックが成功するまで、新しいリリースがあるかどうかは判断できません。',
  stateNeverCheckedDetail:
    'このノードはリモートリリース元が設定されていますが、リリースチェックはまだ一度も実行されていません。新しいリリースがあるかどうかは判断できません。',
  stateUpToDateLabel: 'すでに最新バージョンです',
  stateUpToDateDetail: 'リモートチェックで、導入できる新しいリリースがないことを確認しました。',
  stateAvailableLabel: '新しいリリースがあります',
  stateAvailableDetail: 'リモートチェックで、リリース {version} が利用できることを確認しました。',

  bannerTitle: '新しいリリースがあります',
  bannerMandatory: '必須アップデート',
  bannerOptional: '任意アップデート',
  bannerNotesTitle: 'リリースノート',
  bannerNoNotes: 'このリリースにはリリースノートが添付されていません。',
  installNow: '今すぐアップグレード',

  confirmInstallTitle: '{version} へのアップグレードを確認',
  confirmInstallBody:
    'アップグレードは新しいバイナリをダウンロードし、検証してから現在のバイナリを置き換え、サービスを再起動します。サービスの再起動中はリクエストが短時間中断します。続行しますか？',
  confirmInstallButton: 'アップグレードする',
  installStartFailed: 'インストールタスクを開始できませんでした：{reason}',

  installCardTitle: 'インストールの進捗',
  installCardHint: '直近のインストールタスクの状態と実行した手順を表示します',
  installCardEmpty: 'このノードにはまだインストールタスクの記録がありません。',
  jobVersion: '対象バージョン',
  jobMode: 'インストール方法',
  jobModeUnknown: '不明なインストール方法',
  jobStatus: 'タスク状態',
  jobStatusPending: '開始待ち',
  jobStatusDownloading: '成果物をダウンロード中',
  jobStatusReadyToRestart: 'サービス再起動待ち',
  jobStatusUpgrading: 'バイナリを置き換え中',
  jobStatusSuccess: 'アップグレード成功',
  jobStatusFailed: 'アップグレード失敗',
  jobStatusRolledBack: '以前のバージョンに戻しました',
  jobStatusUnknown: '不明な状態',
  jobStartedAt: '開始時刻',
  jobStepsTitle: '実行した手順',
  jobStepsEmpty: 'タスクはまだ手順を生成していません。',
  jobErrorTitle: '失敗の理由',
  jobSuccessNotice: 'アップグレードが完了し、新しいバージョンが有効になりました。',
  jobRestartScheduled: 'アップグレードが完了しました。まもなくサービスを再起動します。',
  modeArtifact: '成果物をダウンロードしてインストール',
  modeSource: 'ローカルのソースからビルドしてインストール',

  notificationsTitle: 'アップグレードのリマインダー',
  notificationsHint: '新しいリリースを検出したときに作成されたリマインダーの記録を表示します',
  notificationsEmpty: '対応待ちのアップグレードリマインダーはありません。',
  notificationsLoadFailed: 'リマインダー一覧を読み取れませんでした：{reason}',
  notificationVersion: 'リリース',
  notificationStatus: '状態',
  notificationStartedAt: '通知時刻',
  notificationCompletedAt: '対応時刻',
  notificationRetryCount: '再試行回数',
  notificationPending: '対応待ち',
  notificationHandled: '対応済み',

  sourceCardTitle: 'ローカルソースからビルドしてアップグレード',
  sourceCardHint:
    'このマシン上のソースからバイナリをビルドしてインストールします。ビルドは現在のワークツリーを変更しません。',
  sourceRefLabel: 'ソース ref',
  sourceRefPlaceholder: '空欄なら現在のワークツリーを使用します',
  sourceBuildNow: 'ソースからビルドしてアップグレード',
  confirmSourceBuildTitle: 'ソースからのビルドとアップグレードを確認',
  confirmSourceBuildBody:
    'ビルドは別のワークツリーを取り出してバイナリを作成し、現在のバージョンを置き換えてからサービスを再起動します。このマシンの CPU とディスクを使用します。続行しますか？',
  confirmSourceBuildButton: 'ビルドしてアップグレード',
  sourceBuildNoVersion: '導入するバージョンをまだ特定できません。先にリリースチェックを実行してください。',
  sourceBuildFailed: 'ソースビルドタスクを開始できませんでした：{reason}',
}
