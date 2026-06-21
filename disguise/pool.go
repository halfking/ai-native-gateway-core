// Package disguise provides rotating pools of User-Agent and
// Accept-Language header values for outgoing HTTP requests.
//
// The goal is to defeat trivial server-side fingerprinting based on
// the User-Agent header alone. Pool entries are real browser strings
// collected from public sources; no impersonation of any specific
// user/device.
package disguise

import (
	"math/rand"
	"sync"
	"time"
)

// Pool is a thread-safe rotating pool of header values.
type Pool struct {
	mu            sync.RWMutex
	agents        []string
	languages     []string
	lastRotate    time.Time
	rotateEvery   time.Duration
}

// DefaultPool is the package-level pool used by main.go.
//
// Rotation interval: 30 minutes (up from 5 min).
//
// Why longer? With HeadersForSlot(slot) the same slot now returns the
// same UA every call, so the per-call random rotation in Headers() only
// matters for the no-slot fallback path. The MaybeRotate shuffle
// represents "this virtual device's user reinstalled their browser",
// which realistically takes weeks, not minutes. 30 min keeps the
// disguise pool "fresh" without churning the per-slot identity mid-session.
var DefaultPool = NewPool(30 * time.Minute)

// NewPool returns a pool seeded with the default user-agent and
// accept-language lists and the given rotation interval.
func NewPool(rotateInterval time.Duration) *Pool {
	return &Pool{
		agents:      append([]string(nil), defaultUserAgents...),
		languages:   append([]string(nil), defaultAcceptLanguages...),
		lastRotate:  time.Now(),
		rotateEvery: rotateInterval,
	}
}

// Headers returns a snapshot of "User-Agent" and "Accept-Language"
// values for use in an outbound HTTP request. Always returns a non-nil
// map with both keys present; an empty value signals "no pool
// available, do not send the header".
//
// This path picks randomly per call and is the right choice for
// stateless (no-slot) requests. For slot-bound requests, prefer
// HeadersForSlot so the same virtual device keeps a stable UA.
func (p *Pool) Headers() map[string]string {
	if p == nil {
		return map[string]string{
			"User-Agent":      "",
			"Accept-Language": "",
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]string{
		"User-Agent":      p.agents[rand.Intn(len(p.agents))],
		"Accept-Language": p.languages[rand.Intn(len(p.languages))],
	}
}

// HeadersForSlot returns deterministic UA/Accept-Language for a given
// slot index. Same slot → same headers, every call. Different slots →
// different UAs (the disguise goal: many virtual devices, each stable).
//
// This matches the "连接后保持稳定" (stable after connection) contract:
// a session that has acquired slot N keeps UA[N] for as long as it
// holds that slot. If the slot migrates (contention), the UA changes
// consistently with the new slot — the supplier sees one consistent
// "virtual device" per slot ownership window.
//
// Pass slot < 0 to fall back to the random Headers() path (for stateless
// requests with no acquired slot).
func (p *Pool) HeadersForSlot(slot int) map[string]string {
	if p == nil {
		return map[string]string{
			"User-Agent":      "",
			"Accept-Language": "",
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if slot < 0 {
		return map[string]string{
			"User-Agent":      p.agents[rand.Intn(len(p.agents))],
			"Accept-Language": p.languages[rand.Intn(len(p.languages))],
		}
	}
	return map[string]string{
		"User-Agent":      p.agents[slot%len(p.agents)],
		"Accept-Language": p.languages[slot%len(p.languages)],
	}
}

// MaybeRotate shuffles the underlying slices if enough time has
// passed since the last rotation.
func (p *Pool) MaybeRotate() {
	if p == nil {
		return
	}
	p.mu.RLock()
	elapsed := time.Since(p.lastRotate)
	p.mu.RUnlock()
	if elapsed < p.rotateEvery {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.lastRotate) < p.rotateEvery {
		return
	}
	rand.Shuffle(len(p.agents), func(i, j int) {
		p.agents[i], p.agents[j] = p.agents[j], p.agents[i]
	})
	rand.Shuffle(len(p.languages), func(i, j int) {
		p.languages[i], p.languages[j] = p.languages[j], p.languages[i]
	})
	p.lastRotate = time.Now()
}

// Stats returns a snapshot for the admin API.
func (p *Pool) Stats() map[string]interface{} {
	if p == nil {
		return map[string]interface{}{"enabled": false}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]interface{}{
		"enabled":         true,
		"agent_count":     len(p.agents),
		"language_count":  len(p.languages),
		"last_rotate":     p.lastRotate.Format(time.RFC3339),
		"rotate_interval": p.rotateEvery.String(),
	}
}

// defaultUserAgents — 50+ real browser UA strings, no impersonation.
// Source: open-source UA databases (useragents.me / whatismybrowser).
var defaultUserAgents = []string{
	// Chrome on Windows 125
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
	// Chrome on macOS 125
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	// Chrome on Linux 125
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	// Firefox on Windows 126
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:126.0) Gecko/20100101 Firefox/126.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:125.0) Gecko/20100101 Firefox/125.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:124.0) Gecko/20100101 Firefox/124.0",
	// Firefox on macOS
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:126.0) Gecko/20100101 Firefox/126.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:125.0) Gecko/20100101 Firefox/125.0",
	// Firefox on Linux
	"Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0",
	"Mozilla/5.0 (X11; Linux x86_64; rv:125.0) Gecko/20100101 Firefox/125.0",
	// Safari on macOS 17
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.3 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	// Edge on Windows
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
	// Chrome on Android (Pixel)
	"Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
	// Chrome on Android (Samsung)
	"Mozilla/5.0 (Linux; Android 14; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Mobile Safari/537.36",
	// Safari on iOS
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	// ChromeOS
	"Mozilla/5.0 (X11; CrOS x86_64 15633.58.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	// Vivaldi
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Vivaldi/6.7",
	// Opera
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 OPR/110.0.0.0",
	// Brave
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Brave/125",
	// Arc
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Arc/125",
	// Yandex
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 YaBrowser/24.1.0.0 Safari/537.36",
	// 360
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Safari/537.36 QIHU 360EE",
	// QQ Browser
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/102.0.5005.124 Safari/537.36 QQBrowser/13.0.6223.901",
	// Sogou
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.36 SE 2.X MetaSr 1.0",
	// Maxthon
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Maxthon/7.1.5.4000",
	// UC Browser
	"Mozilla/5.0 (Linux; U; Android 13; zh-CN; Mi 10 Build/QQ3A.200805.001) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/78.0.3904.108 UCBrowser/13.4.2.1106 Mobile Safari/537.36",
	// Samsung Internet
	"Mozilla/5.0 (Linux; Android 14; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/115.0.5790.138 Mobile Safari/537.36",
}

// defaultAcceptLanguages — common real-world language preferences.
var defaultAcceptLanguages = []string{
	"en-US,en;q=0.9",
	"en-US,en;q=0.9,fr;q=0.8",
	"en-US,en;q=0.9,de;q=0.8",
	"en-US,en;q=0.9,es;q=0.8",
	"en-US,en;q=0.9,ja;q=0.8",
	"en-US,en;q=0.9,zh-CN;q=0.8",
	"en-US,en;q=0.9,ko;q=0.8",
	"en-US,en;q=0.9,pt;q=0.8",
	"en-US,en;q=0.9,ru;q=0.8",
	"en-US,en;q=0.9,ar;q=0.8",
	"en-GB,en;q=0.9",
	"en-GB,en;q=0.9,fr;q=0.8",
	"en-GB,en;q=0.9,de;q=0.8",
	"zh-CN,zh;q=0.9,en;q=0.8",
	"zh-CN,zh;q=0.9,en;q=0.8,ja;q=0.7",
	"zh-TW,zh;q=0.9,en;q=0.8",
	"zh-HK,zh;q=0.9,en;q=0.8",
	"ja,en-US;q=0.9,en;q=0.8",
	"ja,en;q=0.9",
	"ko-KR,ko;q=0.9,en-US;q=0.8,en;q=0.7",
	"de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7",
	"fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7",
	"es-ES,es;q=0.9,en-US;q=0.8,en;q=0.7",
	"es-MX,es;q=0.9,en-US;q=0.8,en;q=0.7",
	"pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7",
	"pt-PT,pt;q=0.9,en-US;q=0.8,en;q=0.7",
	"ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
	"ar-SA,ar;q=0.9,en-US;q=0.8,en;q=0.7",
	"hi-IN,hi;q=0.9,en-US;q=0.8,en;q=0.7",
	"th-TH,th;q=0.9,en-US;q=0.8,en;q=0.7",
	"vi-VN,vi;q=0.9,en-US;q=0.8,en;q=0.7",
	"tr-TR,tr;q=0.9,en-US;q=0.8,en;q=0.7",
	"it-IT,it;q=0.9,en-US;q=0.8,en;q=0.7",
	"nl-NL,nl;q=0.9,en-US;q=0.8,en;q=0.7",
	"pl-PL,pl;q=0.9,en-US;q=0.8,en;q=0.7",
}
