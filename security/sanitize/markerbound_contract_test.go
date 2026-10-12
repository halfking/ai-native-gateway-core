package sanitize

import (
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/markerbound"
)

// TestMarkerBoundaryContractMatchesSanitizeGrammars pins the markerbound span
// grammar (internal/markerbound — a deliberate copy, see its package comment)
// to this package's markerSpans. compression/ and transformation/ truncate
// text at markerbound's cut boundaries; if this package grows a new tolerated
// marker spelling and the copy is not updated, a truncation boundary can split
// the new spelling in half and the fragment rides upstream. This test turns
// that drift into a red gate here, next to the grammar it mirrors.
func TestMarkerBoundaryContractMatchesSanitizeGrammars(t *testing.T) {
	forms := []string{
		"{SENSITIVE:phone:1}",
		"{SENSITIVE: server_ip : 1}",
		"{SENSITIVE:\n server_ip : 1\n}",
		"{SENSITIVE:server-ip:1}",
		"{SENSITIVE:server_ip:01}",
		"{sensitive:secret:1}",
		"{ＳＥＮＳＩＴＩＶＥ：secret：１}",
		"{ sensitive : id_card : 07 }",
		"{SENSITIVE:secret:X}",
		"{SENSITIVE::}",
		"SENSITIVE:phone:1",
		"sensitive:email:2",
		"{SENSITIVE:phon",
		"｛ＳＥＮＳＩＴＩＶＥ：secret：１",
		"SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card",
		"{SENSITIVEsecret1}",
		"{hello world}",
		"sensitive data",
		"the sensitive: 12345 field",
		"{SENSITIVE:phone:999999999999999999999}",
		"{SENSITIVE:phone:-1}",
		"",
	}
	contexts := []struct{ prefix, suffix string }{
		{"", ""},
		{"plain text ", " more"},
		{"中文前缀", "中文后缀"},
		{"{\"key\":\"", "\"}"},
		{"[", "]"},
		{"tool_call(fn): ", "\nnext"},
	}
	var corpus []string
	for _, form := range forms {
		for _, ctx := range contexts {
			corpus = append(corpus, ctx.prefix+form+ctx.suffix)
		}
	}
	corpus = append(corpus,
		"a{SENSITIVE:phone:1}b{SENSITIVE:email:2}c",
		"（SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）",
		"prefix {SENSITIVE:phone:1}{SENSITIVE:phone:1} suffix",
		"reply {sensitive: server_ip : 1} and {SENSITIVE:phone:1} end",
	)

	for _, text := range corpus {
		local := markerSpans(text)
		sort.Strings(local)
		remoteSpans := markerbound.Spans(text)
		remote := make([]string, 0, len(remoteSpans))
		for _, span := range remoteSpans {
			remote = append(remote, text[span[0]:span[1]])
		}
		sort.Strings(remote)
		if strings.Join(local, "\x00") != strings.Join(remote, "\x00") {
			t.Errorf("span sets diverge for %q:\n sanitize:  %q\n markerbound: %q", text, local, remote)
		}
	}
}
