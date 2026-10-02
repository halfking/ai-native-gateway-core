// Package sanitize - threetier_activation.go (2026-09-30, 修订审计三十二轮
// §四A A-G1)
//
// Blank import that pulls the three-tier provenance verifier into the
// gateway binary. threetier's init() (threetier/register.go) registers its
// verifier into the compression package; compression cannot import threetier
// directly (threetier already imports compression), so an init-time
// registration from a package linked into every gateway process is the
// cycle-safe seam. This package qualifies: cmd/gateway/main.go imports
// security/sanitize for the input-sanitization middleware, hence this file
// guarantees the check is wired in production. The verifier itself is
// observability-only — it logs and counts, never blocks a request.
package sanitize

import (
	_ "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/threetier"
)
