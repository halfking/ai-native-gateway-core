package autoupdate

import "time"

type Channel string

const (
	ChannelStable Channel = "stable"
	ChannelBeta   Channel = "beta"
	ChannelCanary Channel = "canary"
)

type Phase string

const (
	PhaseCanary Phase = "canary"
	PhaseBatch1 Phase = "batch_1"
	PhaseBatch2 Phase = "batch_2"
	PhaseBatch3 Phase = "batch_3"
	PhaseFull   Phase = "full"
)

type Release struct {
	ID          int64      `json:"id"`
	Version     string     `json:"version"`
	BuildSeq    int        `json:"build_seq"`
	Channel     Channel    `json:"channel"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Changelog   string     `json:"changelog"`
	ImageTag    string     `json:"image_tag"`
	ImageDigest string     `json:"image_digest,omitempty"`
	MinVersion  string     `json:"min_version,omitempty"`
	Mandatory   bool       `json:"mandatory"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type GrayReleaseRule struct {
	ID        int64     `json:"id"`
	ReleaseID int64     `json:"release_id"`
	Phase     Phase     `json:"phase"`
	Percent   int       `json:"percent"`
	Selectors []byte    `json:"selectors,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// GrayReleaseRuleView joins gray rule with release metadata for admin UI.
type GrayReleaseRuleView struct {
	GrayReleaseRule
	Version      string `json:"version"`
	ReleaseTitle string `json:"release_title,omitempty"`
}

type ReleaseStatus struct {
	// ReleaseID 可空：上报的 to_version 未必有对应的 releases 行——回滚上报
	// 的目标版本是「回滚到的旧版本」，天然可能不存在。迁移 809 解除该列的
	// NOT NULL（外键保留），RecordUpdateReport 在查不到 release 时写 NULL。
	//
	// 这里是 *int64 而不是 int64 + 0 哨兵：ReleaseStatus 同时是读模型和写命令
	// （UpdateInstanceStatus 收它），若读路径把 NULL 折成 0，一次
	// get→update 往返就会把 0 写回去并再次撞外键。0 在这张表上永远不是合法值。
	ReleaseID   *int64     `json:"release_id"`
	InstanceID  string     `json:"instance_id"`
	Status      string     `json:"status"`
	Version     string     `json:"version"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	RetryCount  int        `json:"retry_count"`

	// LogID 是 upgrade_logs.id（仅 GetUpgradeHistory 填充；来自
	// instance_release_status 的行恒为 0 —— 那张表没有 id 列，主键是 instance_id）。
	//
	// ★ 为什么要有它：提醒行与安装任务日志行**是同一行**（同一件事的两个阶段）。
	//   安装执行器要复用「检索器生成的那条待处理提醒行」并推进它的状态，
	//   就必须拿到这一行的主键；而 UpgradeUpgradeLog 的更新条件正是 id。
	//   查询本来就 SELECT 了 id，此前只是扫描进局部变量后丢弃了。
	LogID int64 `json:"log_id,omitempty"`
}

const (
	StatusPending   = "pending"
	StatusDownload  = "downloading"
	StatusReady     = "ready_to_restart"
	StatusUpgrading = "upgrading"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusRollback  = "rolled_back"
)

// IsActionableStatus 判断该状态是否仍代表「有新版待处理」。
//
// ★ 这条判据是提醒生命周期的核心：
//   - pending      ＝ 已发现新版、还没动手
//   - failed       ＝ 装失败（未换二进制），新版**仍然待处理**
//   - rolled_back  ＝ 装了但起不来、已还原成旧二进制 ⇒ 新版**仍然待处理**
//   - success      ＝ 装成了 ⇒ 不再是待办
//   - downloading / ready_to_restart / upgrading ＝ 正在处理中
//
// ⇒ 提醒列表 = {pending, failed, rolled_back}。
//
//	★ 若把 failed / rolled_back 排除在外，**回滚之后提醒就彻底消失了**：
//	  新版还在、节点还停在旧二进制上，却再也没有人知道该再升一次。
func IsActionableStatus(status string) bool {
	switch status {
	case StatusPending, StatusFailed, StatusRollback:
		return true
	default:
		return false
	}
}

type VersionInfo struct {
	Version   string `json:"version"`
	BuildSeq  int    `json:"build_seq"`
	Commit    string `json:"commit,omitempty"`
	BuiltAt   string `json:"built_at,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
}

type UpgradePlan struct {
	CurrentVersion string        `json:"current_version"`
	TargetVersion  string        `json:"target_version"`
	Steps          []UpgradeStep `json:"steps"`
	EstimatedSecs  int           `json:"estimated_secs"`
}

type UpgradeStep struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Optional    bool   `json:"optional"`
}

type UpdateReportData struct {
	InstanceID  string `json:"instance_id"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Status      string `json:"status"`
	DurationMS  int    `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}
