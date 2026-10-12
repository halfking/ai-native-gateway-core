package sanitize

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLoosePlaceholder pins the mutation classes the tolerant stage
// absorbs, and — just as importantly — the ones it refuses.
func TestParseLoosePlaceholder(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		wantType  SensitiveType
		wantIndex int
		wantOK    bool
	}{
		// Canonical spellings keep working through the tolerant stage.
		{"exact", "{SENSITIVE:server_ip:1}", TypeServerIP, 1, true},
		{"exact multi word type", "{SENSITIVE:id_card:42}", TypeIDCard, 42, true},

		// Spacing: the model re-spaces the marker, or reflows it.
		{"space after colon", "{SENSITIVE: server_ip:1}", TypeServerIP, 1, true},
		{"space before colon", "{SENSITIVE : server_ip : 1}", TypeServerIP, 1, true},
		{"space inside braces", "{ SENSITIVE:server_ip:1 }", TypeServerIP, 1, true},
		{"newline reflow", "{SENSITIVE:\n\tserver_ip: \n 1}", TypeServerIP, 1, true},
		{"non breaking space", "{SENSITIVE:\u00a0server_ip:1}", TypeServerIP, 1, true},
		{"invisible zero width", "{SENSITIVE:\u200bserver_ip:\u200b1}", TypeServerIP, 1, true},

		// Case.
		{"lowercase marker", "{sensitive:server_ip:1}", TypeServerIP, 1, true},
		{"uppercase type", "{SENSITIVE:SERVER_IP:1}", TypeServerIP, 1, true},
		{"mixed case", "{SeNsItIvE:Server_Ip:1}", TypeServerIP, 1, true},

		// Index reformatting.
		{"zero padded index", "{SENSITIVE:secret:007}", TypeSecret, 7, true},
		{"large index", "{SENSITIVE:secret:123456}", TypeSecret, 123456, true},

		// Separator handling: a reflow out of "key: value" context doubles or
		// drops the colon. "-" is NOT a separator — see the negative-index case.
		{"hyphenated type", "{SENSITIVE:server-ip:1}", TypeServerIP, 1, true},
		{"doubled separator", "{SENSITIVE::secret::1}", TypeSecret, 1, true},
		{"missing separator", "{SENSITIVEsecret1}", TypeSecret, 1, true},

		// Full-width punctuation from a CJK input method.
		{"full width", "{ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１}", TypeSecret, 1, true},
		{"full width braces", "｛SENSITIVE：secret：1｝", TypeSecret, 1, true},

		{"hyphen separator is not a separator", "{SENSITIVE-secret-1}", "", 0, false},
		{"slash separator is not a separator", "{SENSITIVE/secret/1}", "", 0, false},
		{"square brackets", "[SENSITIVE:secret:1]", "", 0, false},

		// Not markers. These must never be rewritten: they are ordinary model
		// output, and repairing them would fabricate a value.
		{"plain json object", `{"a":1}`, "", 0, false},
		{"markdown bold", "**hello**", "", 0, false},
		// 2026-10-10: "missing index" moved from the negative table to its own
		// case below. It used to be asserted as not-a-marker on the grounds
		// that an index-less token cannot resolve to a real value and
		// "rewriting it would fabricate one". Measured on 154 that reasoning
		// hid a leak: minimax-m3 drops both the braces and the index and
		// enumerates the types it was shown — "（SENSITIVE:phone,
		// SENSITIVE:email, SENSITIVE:id_card）" — so 26 responses in 60
		// minutes carried an internal token to the client. An index-less
		// marker now parses with index -1: still marker syntax, never
		// resolvable, therefore masked rather than passed through. That keeps
		// the original no-fabrication guarantee intact — nothing is invented —
		// while closing the leak. See TestParseLoosePlaceholderIndexlessIsMasked.
		{"missing type", "{SENSITIVE::1}", "", 0, false},
		{"non numeric index", "{SENSITIVE:secret:abc}", "", 0, false},
		{"different marker word", "{SECRET:secret:1}", "", 0, false},
		{"template expression", "{user.name}", "", 0, false},
		{"nested braces", "{outer {SENSITIVE:secret:1} inner}", "", 0, false},
		{"type with hyphen only", "{SENSITIVE:--:1}", "", 0, false},
		{"negative index", "{SENSITIVE:secret:-1}", "", 0, false},
		{"trailing letters on index", "{SENSITIVE:secret:1a}", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, index, ok := parseLoosePlaceholder(tt.candidate)
			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.Equal(t, tt.wantType, typ)
			assert.Equal(t, tt.wantIndex, index)
		})
	}
}

// TestParseLoosePlaceholderIndexlessIsMasked carries the case that used to sit
// in the negative table. An index-less marker must now be recognised as marker
// syntax (so it cannot leak) while remaining unresolvable (so nothing is
// fabricated): index -1 can never match a mapping row, which makes every
// mask-mode caller redact it.
func TestParseLoosePlaceholderIndexlessIsMasked(t *testing.T) {
	forms := []string{
		"{SENSITIVE:secret}",
		"SENSITIVE:phone",
		"SENSITIVE:id_card",
		"{SENSITIVE: server_ip }",
	}
	for _, candidate := range forms {
		t.Run(candidate, func(t *testing.T) {
			typ, index, ok := parseLoosePlaceholder(candidate)
			require.True(t, ok, "an index-less marker is still marker syntax")
			assert.NotEmpty(t, typ, "the type is still parsed and can be reported")
			assert.Equal(t, -1, index, "no index means unresolvable, never index 0 or 1")

			sm := SanitizeMap{
				"{SENSITIVE:secret:1}": "should-not-resolve",
				"{SENSITIVE:phone:1}":  "should-not-resolve",
			}
			_, resolved := sm.resolverFor()(typ, index)
			assert.False(t, resolved, "an index-less marker must never resolve to a value")
		})
	}
}

// TestParseLoosePlaceholderCorruptIndex keeps a corrupt marker inside the
// marker syntax so mask-mode callers still redact it, while making sure it can
// never resolve to a mapping row.
func TestParseLoosePlaceholderCorruptIndex(t *testing.T) {
	typ, index, ok := parseLoosePlaceholder("{SENSITIVE:secret:99999999999999999999}")
	require.True(t, ok, "a corrupt index is still marker syntax")
	assert.Equal(t, TypeSecret, typ)
	assert.Equal(t, -1, index)

	sm := SanitizeMap{"{SENSITIVE:secret:99999999999999999999}": "should-not-resolve"}
	_, resolved := sm.resolverFor()(typ, index)
	assert.False(t, resolved, "a corrupt index must not resolve")
}

// TestRestoreOutputOrMaskToleratedMutations is the core regression: every
// mutation class a model produces must still restore the original value, which
// is the whole point of the tolerant stage.
func TestRestoreOutputOrMaskToleratedMutations(t *testing.T) {
	const secret = "s3cr3t-p@ssw0rd"
	sm := SanitizeMap{"{SENSITIVE:secret:1}": secret}
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	for _, mutated := range []string{
		"{SENSITIVE:secret:1}",
		"{SENSITIVE: secret:1}",
		"{SENSITIVE:secret : 1}",
		"{ SENSITIVE : secret : 1 }",
		"{SENSITIVE:\n secret:\n 1\n}",
		"{sensitive:secret:1}",
		"{SENSITIVE:SECRET:1}",
		"{SENSITIVE:secret:01}",
		"{SENSITIVE::secret::1}",
		"{SENSITIVEsecret1}",
		"｛ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１｝",
	} {
		t.Run(mutated, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), "token is "+mutated+" done", sm)
			require.NoError(t, err)
			assert.Equal(t, "token is "+secret+" done", got)
		})
	}
}

// TestRestoreOutputOrMaskDoesNotFabricate pins the security contract: a
// normalized marker resolves only through a real mapping row. Nothing here may
// produce the secret, whether the shape is well-formed or corrupt.
func TestRestoreOutputOrMaskDoesNotFabricate(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": "s3cr3t-p@ssw0rd"}

	// Well-formed markers that name nothing this session issued must be masked:
	// an internal token must not reach the client in any spelling.
	masked := []string{
		"{SENSITIVE:secret:2}",       // never issued
		"{SENSITIVE: server_ip : 1}", // real type, but not this session's
		"{SENSITIVE: ip:1}",          // semantic rename is not normalization
		"{SENSITIVE:SECRETS:1}",      // different type
	}
	for _, forged := range masked {
		t.Run("mask/"+forged, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), forged, sm)
			require.NoError(t, err)
			// Each forged span has its own type, so the marker is typed. Assert
			// the shape and the absence of the secret rather than one literal.
			assert.Regexp(t, `^\[REDACTED(:[a-z_]+)?\]$`, got)
			assert.NotContains(t, got, "s3cr3t")
		})
	}

	// Corrupt markers are not marker syntax at all. They must not resolve to a
	// neighbouring row; leaving the text alone is what the exact stage has
	// always done, and the residual probes catch the literal at the interceptor
	// boundary. What matters here is that no secret escapes.
	corrupt := []string{
		"{SENSITIVE:secret:1a}",
		"{SENSITIVE:secret:-1}",
		"{SENSITIVE:secret}",
	}
	for _, bad := range corrupt {
		t.Run("corrupt/"+bad, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), bad, sm)
			require.NoError(t, err)
			assert.NotContains(t, got, "s3cr3t")
		})
	}

	// Ordinary brace output must survive untouched.
	const passthrough = `{"note": "use {a} and {b} and {a.b.c} in the config"}`
	got, err := s.RestoreOutputOrMask(context.Background(), passthrough, sm)
	require.NoError(t, err)
	assert.Equal(t, passthrough, got)
}

// TestRestoreOutputKeepsUnknownMarkers keeps RestoreOutput's original contract
// on the tolerant stage: never guess, never error.
func TestRestoreOutputKeepsUnknownMarkers(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": "s3cr3t"}

	got, err := s.RestoreOutput(context.Background(), "a {SENSITIVE: secret : 9} b {SENSITIVE:secret:1} c", sm)
	require.NoError(t, err)
	assert.Equal(t, "a {SENSITIVE: secret : 9} b s3cr3t c", got)
}

// TestPlaceholderCandidate pins the cheap gate the call sites use before paying
// for a restore pass.
func TestPlaceholderCandidate(t *testing.T) {
	assert.True(t, placeholderCandidate("x {SENSITIVE:secret:1} y"))
	assert.True(t, placeholderCandidate("x {sensitive : secret : 1} y"))
	assert.False(t, placeholderCandidate("x {a} y"))
	assert.False(t, placeholderCandidate("no braces here"))
	assert.False(t, placeholderCandidate(""))
}

// TestReservedMarkerResidual keeps the fail-closed probes aligned with the
// tolerant stage, so a re-spelled marker cannot slip past them.
func TestReservedMarkerResidual(t *testing.T) {
	assert.True(t, placeholderCandidate("a {SENSITIVE:secret:1} b"))
	assert.True(t, placeholderCandidate("a {sensitive: secret : 1} b"))
	assert.True(t, placeholderCandidate("a {ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１} b"))
	assert.False(t, placeholderCandidate("a {b} c {d.e} f"))
}

// TestValidLoosePlaceholderPrefix covers the streamed split: a delta routinely
// cuts a marker after the brace or halfway through the marker word, and the
// bytes after the split are not a marker until the next delta lands.
func TestValidLoosePlaceholderPrefix(t *testing.T) {
	held := []string{
		"{",
		"{s", "{S", "{se", "{sen", "{sens", "{sensi", "{sensit", "{sensiti",
		"{sensitiv", "{sensitive",
		"{SENSITIVE:", "{SENSITIVE: ", "{sensitive:server_ip",
		"{SENSITIVE:server_ip:", "{SENSITIVE: server_ip : ", "{SENSITIVE:server_ip:1",
		"｛SENSITIVE：secret：",
	}
	for _, candidate := range held {
		t.Run("hold/"+candidate, func(t *testing.T) {
			assert.True(t, validLoosePlaceholderPrefix(candidate), "must hold back")
		})
	}

	released := []string{
		"",
		"plain text",
		"{a",
		"{select",
		"{SENSITIVE:server_ip:1}", // complete — the restore pass owns it
		"{SENSITIVE:server_ip:1x",
		"{server_ip:1}",
		`{"key":`,
	}
	for _, candidate := range released {
		t.Run("release/"+candidate, func(t *testing.T) {
			assert.False(t, validLoosePlaceholderPrefix(candidate), "must not hold back")
		})
	}
}

// TestReservedMarkerStarted decides whether withheld bytes may be released
// verbatim or must be redacted.
func TestReservedMarkerStarted(t *testing.T) {
	assert.True(t, reservedMarkerStarted("{SENSITIVE:"))
	assert.True(t, reservedMarkerStarted("{sensitive"))
	assert.True(t, reservedMarkerStarted("{SENSITIVE:secret:"))
	assert.False(t, reservedMarkerStarted("{"), "a bare brace is ordinary output")
	assert.False(t, reservedMarkerStarted("plain"))
}

// TestReserveMutatedPlaceholders closes the input-side collision hole: a
// re-spelled marker echoed back by the client must not alias a credential the
// gateway allocates later in the same turn. The exact spelling is the exact
// pass's job and is skipped here so the two passes cannot double-count.
func TestReserveMutatedPlaceholders(t *testing.T) {
	mutated := map[SensitiveType]int{}
	reserveMutatedPlaceholders("client echoed {SENSITIVE: server_ip : 9} back", mutated)
	assert.Equal(t, 9, mutated[TypeServerIP])

	multi := map[SensitiveType]int{}
	reserveMutatedPlaceholders("a {sensitive: secret : 4} b {SENSITIVE: id_card : 12} c", multi)
	assert.Equal(t, 4, multi[TypeSecret])
	assert.Equal(t, 12, multi[TypeIDCard])

	neverLower := map[SensitiveType]int{}
	reserveMutatedPlaceholders("client echoed {SENSITIVE: server_ip : 2} back", neverLower)
	reserveMutatedPlaceholders("client echoed {SENSITIVE: server_ip : 9} back", neverLower)
	assert.Equal(t, 9, neverLower[TypeServerIP], "the high-water mark only moves forward")

	exact := map[SensitiveType]int{}
	reserveMutatedPlaceholders("client echoed {SENSITIVE:server_ip:9} back", exact)
	assert.Empty(t, exact, "the exact spelling is reserved by the exact pass")

	none := map[SensitiveType]int{}
	reserveMutatedPlaceholders("client echoed {a:9} back", none)
	assert.Empty(t, none)
}

// TestLooksLikeMarkerSpan keeps the degenerate-marker class narrow. A span only
// fails closed when it really has marker shape; ordinary output that merely
// contains the word inside braces must survive, or a model explaining a
// "{sensitive_value} field" would get its prose masked.
func TestLooksLikeMarkerSpan(t *testing.T) {
	// Degenerate echoes: marker shape, broken index.
	for _, candidate := range []string{
		"{SENSITIVE:secret:X}",
		"{sensitive : secret : a}",
		"{SENSITIVE:server_ip:1a}",
		"{SENSITIVE::}",
	} {
		t.Run("marker/"+candidate, func(t *testing.T) {
			assert.True(t, looksLikeMarkerSpan(candidate))
		})
	}

	// Prose and code. None of these is a token and none may be touched.
	for _, candidate := range []string{
		"{sensitive_value}",
		"{sensitive data}",
		"{SENSITIVE}",
		`{"sensitive": true}`,
		"{select * from sensitive}",
		"{user.name}",
		"${SENSITIVE_ENV}",
	} {
		t.Run("prose/"+candidate, func(t *testing.T) {
			assert.False(t, looksLikeMarkerSpan(candidate))
		})
	}
}

// TestTruncatedMarkerStillFailsClosed covers the span that never got its closing
// brace. It is a leaked internal token like any other, and it can never be shown
// to belong to a peer hop, so it is not exempt.
func TestTruncatedMarkerStillFailsClosed(t *testing.T) {
	ctx := WithForeignMarkers(context.Background(), foreignMarkerSet{"{SENSITIVE:secret:1}": {}})

	assert.True(t, placeholderCandidate("the value is {SENSITIVE:secret:1"),
		"a truncated marker must stay visible to the residual probe")
	assert.True(t, containsNonForeignMarker(ctx, []byte("the value is {SENSITIVE:secret:1")),
		"a truncated marker is not a peer marker")
	assert.True(t, containsNonForeignMarker(ctx, []byte("the value is {SENSITIVE:secret:1} ok {SENSITIVE:secret:2")),
		"a truncated span may not ride along beside an exempt one")
	assert.False(t, containsNonForeignMarker(ctx, []byte("the value is {SENSITIVE:secret:1} done {SENSITIVE:secret:1}")),
		"a complete peer marker beside prose is still exempt")

	// The tolerance must not swallow ordinary prose that ends in braces.
	assert.False(t, placeholderCandidate("see {sensitive_value"))
}

// TestPeerPassthroughWithoutOwnMap is the regression behind the two-hop round
// trip: an upstream hop normally holds no mapping of its own, so the restore
// stage runs with a nil resolver. The peer exemption used to be gated behind a
// non-nil resolver, which masked exactly the markers the passthrough exists to
// carry home.
func TestPeerPassthroughWithoutOwnMap(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	// The inbound set holds canonical spellings — that is what the peer's own
	// mints normalize to, and it is what makes the exemption forge-proof.
	ctx := WithForeignMarkers(context.Background(), foreignMarkerSet{
		"{SENSITIVE:internal_ip:1}": {},
		"{SENSITIVE:secret:1}":      {},
	})

	got, err := s.RestoreOutputOrMask(ctx,
		"host {SENSITIVE:internal_ip:1} pwd {sensitive:secret:01} forged {SENSITIVE:secret:9}", SanitizeMap{})
	require.NoError(t, err)
	assert.Equal(t, "host {SENSITIVE:internal_ip:1} pwd {sensitive:secret:01} forged "+RedactionMarker(TypeSecret), got,
		"peer markers come back verbatim; anything else is still masked")

	// The same holds for the non-masking mode, which must not invent a value.
	kept, err := s.RestoreOutput(ctx, "host {SENSITIVE:internal_ip:1} forged {SENSITIVE:secret:9}", SanitizeMap{})
	require.NoError(t, err)
	assert.Equal(t, "host {SENSITIVE:internal_ip:1} forged {SENSITIVE:secret:9}", kept)
}
