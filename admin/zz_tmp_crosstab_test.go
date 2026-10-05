package admin

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestZZTmpCrossTab(t *testing.T) {
	root := repoRootFromCaller(t)
	cross := map[string]map[string]int{}
	total := 0
	for file, c := range requestLogsStopWriteClassification {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		fam := sourceFamilyOf(string(raw), isSwitchConsumerFile(t, root, file))
		if cross[c.Effect] == nil {
			cross[c.Effect] = map[string]int{}
		}
		cross[c.Effect][fam]++
		total++
	}
	effects := make([]string, 0, len(cross))
	for e := range cross {
		effects = append(effects, e)
	}
	sort.Strings(effects)
	for _, e := range effects {
		fams := make([]string, 0, len(cross[e]))
		for f := range cross[e] {
			fams = append(fams, f)
		}
		sort.Strings(fams)
		sum := 0
		for _, f := range fams {
			sum += cross[e][f]
		}
		t.Logf("EFFECT %s total=%d", e, sum)
		for _, f := range fams {
			t.Logf("  FAM %s = %d", f, cross[e][f])
		}
	}
	t.Logf("GRANDTOTAL %d", total)
}
