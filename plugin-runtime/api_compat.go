package pluginruntime

import (
	"strconv"
	"strings"
)

// HostAPI is the host pluginrt API version plugins' caret ranges are checked against.
// Kept at major 1 so existing process plugins (empty API → "^1") stay compatible.
const HostAPI = "1.0"

// Compatible reports whether a plugin written for caret range want runs on HostAPI.
// An empty range is "^1". Same major, and a minor no newer than the host's.
func Compatible(want string) bool {
	if want == "" {
		want = "^1"
	}
	want, ok := strings.CutPrefix(want, "^")
	if !ok {
		return false
	}
	wMaj, wMin, ok1 := parseAPIVersion(want)
	hMaj, hMin, ok2 := parseAPIVersion(HostAPI)
	return ok1 && ok2 && wMaj == hMaj && wMin <= hMin
}

func parseAPIVersion(v string) (int, int, bool) {
	maj, min, _ := strings.Cut(v, ".")
	a, err := strconv.Atoi(maj)
	if err != nil {
		return 0, 0, false
	}
	if min == "" {
		return a, 0, true
	}
	min, _, _ = strings.Cut(min, ".")
	b, err := strconv.Atoi(min)
	return a, b, err == nil
}
