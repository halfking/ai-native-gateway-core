package dispatch

import "sync/atomic"

// modelQueue is the Tier-1 per-model FIFO buffer with a drainer goroutine
// (runModelDrainer) forwarding into the shared dispatchIn.
type modelQueue struct {
	name  string
	ch    chan *QueuedRequest
	depth atomic.Int64
}

// QueueSnapshot is a point-in-time view of one queue, for the admin endpoint.
type QueueSnapshot struct {
	Model      string `json:"model,omitempty"`
	Credential int    `json:"credential,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Depth      int64  `json:"depth"`
}

// Snapshot returns live Tier-1 and Tier-2 queue depths for the display API
// (GET /api/admin/dispatch/queues). This realizes the Tier-3 "display &
// statistics" layer alongside the Prometheus metrics.
func (p *Pipeline) Snapshot() (models, creds []QueueSnapshot) {
	p.modelMu.Lock()
	for _, mq := range p.models {
		models = append(models, QueueSnapshot{
			Model: mq.name, Depth: mq.depth.Load(),
		})
	}
	p.modelMu.Unlock()

	p.credMu.Lock()
	for _, cf := range p.forwarders {
		creds = append(creds, QueueSnapshot{
			Credential: cf.cred.CredentialID,
			Mode:       cf.cred.ConcurrencyMode,
			Depth:      cf.depth.Load(),
		})
	}
	p.credMu.Unlock()
	return models, creds
}
