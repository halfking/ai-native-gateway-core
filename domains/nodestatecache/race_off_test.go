//go:build !race

package nodestatecache

// raceEnabled 报告是否在 race detector 下运行（perf 门禁跳过用）。
const raceEnabled = false
