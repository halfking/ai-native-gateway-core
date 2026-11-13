//go:build race

package sanitize

// raceEnabled 在 -race 构建下为 true（与 race_disabled.go 成对）。
// 用途：与绝对阈值（如 allocs 门）冲突的门在 race 下跳过——race 插桩
// 会把分配计数放大数倍，绝对阈值在插桩下失真（2026-11-13 12h 审计轮）。
const raceEnabled = true
