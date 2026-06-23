package domain

// SecurityContext 封装安全检查领域的上下文。
type SecurityContext struct {
	ClientID       ClientIdentity
	APIKeyID       int
	APIKeyHash     string
	Authenticated  bool
	AuthMethod     string
	RateLimitOK    bool
	CircuitOpen    bool
	ConcurrentLmt  int
	IsolationLevel string
}

// ClientIdentity 客户端身份。
type ClientIdentity struct {
	Fingerprint ClientFingerprint
	RealIP      string
	UserAgent   string
}

// ClientFingerprint 客户端指纹。
type ClientFingerprint struct {
	IP        string
	UserAgent string
	APIKey    string
}
