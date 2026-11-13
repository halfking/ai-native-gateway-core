//go:build !race

package sanitize

// raceEnabled 在非 race 构建下为 false（与 race_enabled.go 成对）。
const raceEnabled = false
