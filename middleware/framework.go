package middleware

import "net/http"

type Middleware interface {
	Name() string
	Wrap(next http.Handler) http.Handler
}

type BypassRule struct {
	ExactPaths   []string
	PathPrefixes []string
	Methods      []string
}

func (br *BypassRule) Match(r *http.Request) bool {
	if br == nil {
		return false
	}
	if len(br.ExactPaths) > 0 {
		for _, p := range br.ExactPaths {
			if r.URL.Path == p {
				return true
			}
		}
	}
	if len(br.PathPrefixes) > 0 {
		for _, p := range br.PathPrefixes {
			if len(r.URL.Path) >= len(p) && r.URL.Path[:len(p)] == p {
				return true
			}
		}
	}
	return false
}

func (br *BypassRule) MatchMethod(r *http.Request) bool {
	if br == nil || len(br.Methods) == 0 {
		return true
	}
	for _, m := range br.Methods {
		if r.Method == m {
			return true
		}
	}
	return false
}

func (br *BypassRule) ShouldSkip(r *http.Request) bool {
	if br == nil {
		return false
	}
	return br.Match(r) || !br.MatchMethod(r)
}

type BaseMiddleware struct {
	name   string
	bypass BypassRule
}

func (m *BaseMiddleware) Name() string { return m.name }

func (m *BaseMiddleware) ShouldBypass(r *http.Request) bool {
	return m.bypass.ShouldSkip(r)
}

type Chain []Middleware

func (c Chain) Then(handler http.Handler) http.Handler {
	for i := len(c) - 1; i >= 0; i-- {
		handler = c[i].Wrap(handler)
	}
	return handler
}

type Builder struct {
	chain Chain
}

func NewBuilder() *Builder { return &Builder{} }

func (b *Builder) Add(m Middleware) *Builder {
	b.chain = append(b.chain, m)
	return b
}

func (b *Builder) Build() Chain {
	return b.chain
}
