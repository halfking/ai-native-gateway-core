// selfUpdateView.ts — 自更新管理页（SelfUpdateView）文案。
//
// ★ 措辞纪律（与页面代码同源）：
//   「检查失败 / 未配置远端源 / 还没查过」这三类**结论不可得**的情形，
//   一律说「状态未知」并给出原因，**绝不说「已是最新」**。
//   本仓血案见 web/src/views/lifecycle/upgradeLatestVsUnknown.test.ts。
export default {
  pageTitle: '自更新与升级',
  pageSubtitle: '系统自动检索最新版本、生成提醒，由管理员确认后安装升级',

  statusCardTitle: '当前版本状态',
  statusCardHint: '展示本节点正在运行的版本，以及最近一次远端版本检索的结论',
  currentVersion: '当前版本',
  latestVersion: '最新版本',
  buildSequence: '构建序号',
  lastCheckedAt: '最近检查时间',
  checkInterval: '检查间隔',
  checkIntervalHours: '每 {hours} 小时自动检查一次',
  checkIntervalMinutes: '每 {minutes} 分钟自动检查一次',
  checkIntervalSeconds: '每 {seconds} 秒自动检查一次',
  checkNow: '立即检查更新',
  checking: '检查中',

  stateNotConfiguredLabel: '未配置远端版本源',
  stateNotConfiguredDetail:
    '本节点没有配置远端版本源，自动检索与提醒未启用，暂时无法判断是否存在新版本。',
  stateUnknownLabel: '版本状态未知',
  stateUnknownDetail:
    '本次版本检查未能得到结论：{reason}。在检查成功之前，无法判断是否存在新版本。',
  stateNeverCheckedDetail:
    '本节点已配置远端版本源，但还没有执行过版本检查，暂时无法判断是否存在新版本。',
  stateUpToDateLabel: '当前已是最新版本',
  stateUpToDateDetail: '远端检索确认没有可安装的新版本。',
  stateAvailableLabel: '发现新版本',
  stateAvailableDetail: '远端检索确认存在新版本 {version}，可以安装升级。',

  bannerTitle: '发现新版本',
  bannerMandatory: '强制更新',
  bannerOptional: '可选更新',
  bannerNotesTitle: '版本说明',
  bannerNoNotes: '该版本没有附带发布说明。',
  installNow: '立即升级',

  confirmInstallTitle: '确认升级到 {version}',
  confirmInstallBody:
    '升级会下载新版本二进制、校验后替换当前二进制，并重启服务。服务重启期间请求会短暂中断。确认继续？',
  confirmInstallButton: '确认升级',
  installStartFailed: '安装任务未能启动：{reason}',

  installCardTitle: '安装进度',
  installCardHint: '展示最近一次安装任务的状态与执行步骤',
  installCardEmpty: '本节点还没有安装任务记录。',
  jobVersion: '目标版本',
  jobMode: '安装方式',
  jobModeUnknown: '未知安装方式',
  jobStatus: '任务状态',
  jobStatusPending: '等待开始',
  jobStatusDownloading: '正在下载制品',
  jobStatusReadyToRestart: '等待重启服务',
  jobStatusUpgrading: '正在替换二进制',
  jobStatusSuccess: '升级成功',
  jobStatusFailed: '升级失败',
  jobStatusRolledBack: '已回滚到旧版本',
  jobStatusUnknown: '未知状态',
  jobStartedAt: '开始时间',
  jobStepsTitle: '执行步骤',
  jobStepsEmpty: '任务尚未产生执行步骤。',
  jobErrorTitle: '失败原因',
  jobSuccessNotice: '升级完成，新版本已生效。',
  jobRestartScheduled: '升级已完成，服务将在稍后重启。',
  modeArtifact: '下载制品安装',
  modeSource: '本机源码编译安装',

  notificationsTitle: '升级提醒',
  notificationsHint: '展示检索到新版本时生成的提醒记录',
  notificationsEmpty: '当前没有待处理的升级提醒。',
  notificationsLoadFailed: '读取提醒列表失败：{reason}',
  notificationVersion: '版本',
  notificationStatus: '状态',
  notificationStartedAt: '提醒时间',
  notificationCompletedAt: '处理时间',
  notificationRetryCount: '重试次数',
  notificationPending: '待处理',
  notificationHandled: '已处理',

  sourceCardTitle: '从本地源码编译升级',
  sourceCardHint: '用本机源码编译出二进制并安装，构建过程不会改动当前工作树',
  sourceRefLabel: '源码 ref',
  sourceRefPlaceholder: '留空表示使用当前工作树',
  sourceBuildNow: '从源码编译并升级',
  confirmSourceBuildTitle: '确认从源码编译并升级',
  confirmSourceBuildBody:
    '编译会拉起独立工作树、构建二进制并替换当前版本，随后重启服务。构建期间会占用本机 CPU 与磁盘。确认继续？',
  confirmSourceBuildButton: '确认编译升级',
  sourceBuildNoVersion: '无法确定要安装的版本号，请先完成一次版本检查。',
  sourceBuildFailed: '源码编译任务未能启动：{reason}',
}
