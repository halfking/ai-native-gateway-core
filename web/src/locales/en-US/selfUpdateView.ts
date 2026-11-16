// selfUpdateView.ts — Self-update management page (SelfUpdateView).
//
// Wording rule (same source as the page code): when the release conclusion
// cannot be obtained (check failed, no remote source configured, never checked)
// the page says the status is unknown and gives the reason. It never claims
// the node is up to date in that case.
export default {
  pageTitle: 'Self-update and upgrade',
  pageSubtitle:
    'The node looks for new releases on its own, raises a reminder, and installs the upgrade once an administrator confirms it',

  statusCardTitle: 'Current release status',
  statusCardHint:
    'Shows the release this node is running and the outcome of the most recent remote release check',
  currentVersion: 'Current version',
  latestVersion: 'Latest version',
  buildSequence: 'Build sequence',
  lastCheckedAt: 'Last checked',
  checkInterval: 'Check interval',
  checkIntervalHours: 'Checks automatically every {hours} hours',
  checkIntervalMinutes: 'Checks automatically every {minutes} minutes',
  checkIntervalSeconds: 'Checks automatically every {seconds} seconds',
  checkNow: 'Check for updates now',
  checking: 'Checking',

  stateNotConfiguredLabel: 'No remote release source configured',
  stateNotConfiguredDetail:
    'This node has no remote release source configured, so automatic checks and reminders are off. Whether a newer release exists cannot be determined.',
  stateUnknownLabel: 'Release status unknown',
  stateUnknownDetail:
    'The release check did not return a conclusion: {reason}. Whether a newer release exists cannot be determined until a check succeeds.',
  stateNeverCheckedDetail:
    'A remote release source is configured on this node, but no release check has run yet, so whether a newer release exists cannot be determined.',
  stateUpToDateLabel: 'Already up to date',
  stateUpToDateDetail: 'The remote check confirmed that no newer release is available.',
  stateAvailableLabel: 'A newer release is available',
  stateAvailableDetail:
    'The remote check confirmed that release {version} is available and can be installed.',

  bannerTitle: 'A newer release is available',
  bannerMandatory: 'Mandatory update',
  bannerOptional: 'Optional update',
  bannerNotesTitle: 'Release notes',
  bannerNoNotes: 'This release came without release notes.',
  installNow: 'Upgrade now',

  confirmInstallTitle: 'Confirm upgrade to {version}',
  confirmInstallBody:
    'The upgrade downloads the new binary, verifies it, replaces the current binary, and restarts the service. Requests are interrupted briefly while the service restarts. Continue?',
  confirmInstallButton: 'Confirm upgrade',
  installStartFailed: 'The install task could not be started: {reason}',

  installCardTitle: 'Install progress',
  installCardHint: 'Shows the state and the executed steps of the most recent install task',
  installCardEmpty: 'This node has no install task recorded yet.',
  jobVersion: 'Target version',
  jobMode: 'Install method',
  jobModeUnknown: 'Unknown install method',
  jobStatus: 'Task status',
  jobStatusPending: 'Waiting to start',
  jobStatusDownloading: 'Downloading the artifact',
  jobStatusReadyToRestart: 'Waiting for the service restart',
  jobStatusUpgrading: 'Replacing the binary',
  jobStatusSuccess: 'Upgrade succeeded',
  jobStatusFailed: 'Upgrade failed',
  jobStatusRolledBack: 'Rolled back to the previous version',
  jobStatusUnknown: 'Unknown state',
  jobStartedAt: 'Started at',
  jobStepsTitle: 'Executed steps',
  jobStepsEmpty: 'The task has not produced any step yet.',
  jobErrorTitle: 'Failure reason',
  jobSuccessNotice: 'The upgrade finished and the new version is in effect.',
  jobRestartScheduled: 'The upgrade finished and the service will restart shortly.',
  modeArtifact: 'Download and install the artifact',
  modeSource: 'Build from local source and install',

  notificationsTitle: 'Upgrade reminders',
  notificationsHint: 'Shows the reminder records created when a new release was found',
  notificationsEmpty: 'There is no upgrade reminder waiting for action.',
  notificationsLoadFailed: 'The reminder list could not be read: {reason}',
  notificationVersion: 'Release',
  notificationStatus: 'State',
  notificationStartedAt: 'Raised at',
  notificationCompletedAt: 'Handled at',
  notificationRetryCount: 'Retry count',
  notificationPending: 'Waiting for action',
  notificationHandled: 'Handled',

  sourceCardTitle: 'Build and upgrade from local source',
  sourceCardHint:
    'Builds the binary from the source on this machine and installs it. The build leaves the current work tree untouched.',
  sourceRefLabel: 'Source ref',
  sourceRefPlaceholder: 'Leave empty to use the current work tree',
  sourceBuildNow: 'Build from source and upgrade',
  confirmSourceBuildTitle: 'Confirm building from source and upgrading',
  confirmSourceBuildBody:
    'The build checks out a separate work tree, builds the binary, replaces the current version, and then restarts the service. It uses CPU and disk on this machine. Continue?',
  confirmSourceBuildButton: 'Confirm build and upgrade',
  sourceBuildNoVersion: 'The version to install is not known yet. Run a release check first.',
  sourceBuildFailed: 'The source build task could not be started: {reason}',
}
