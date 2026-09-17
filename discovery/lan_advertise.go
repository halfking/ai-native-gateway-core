// Package discovery includes LAN service advertisement via mDNS for local network discovery.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

// LANAdvertiser manages mDNS service advertisement for the LLM gateway.
// It broadcasts the gateway's availability on the local network so clients
// can discover it without manual configuration.
type LANAdvertiser struct {
	server   *mdns.Server
	port     int
	name     string
	version  string
	apis     []string
	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	hostname string
}

// NewLANAdvertiser creates a new LAN advertiser instance.
// name is the service instance name (e.g., "llm-gateway-office")
// port is the HTTP port the gateway listens on
// version is the gateway version string
// apis is the list of supported API types (e.g., ["openai", "anthropic", "gemini"])
func NewLANAdvertiser(name string, port int, version string, apis []string) *LANAdvertiser {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "llm-gateway"
	}
	
	return &LANAdvertiser{
		port:     port,
		name:     name,
		version:  version,
		apis:     apis,
		stopCh:   make(chan struct{}),
		hostname: hostname,
	}
}

// Start begins broadcasting the mDNS service on the local network.
// The service will be advertised as "_llm-gateway._tcp.local."
func (a *LANAdvertiser) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return fmt.Errorf("LAN advertiser already running")
	}

	// Build TXT records with gateway metadata
	txt := []string{
		"version=" + a.version,
		"api=" + strings.Join(a.apis, ","),
		"proto=http",
	}

	// Get the local IP addresses
	ips, err := getLocalIPs()
	if err != nil {
		slog.Warn("failed to get local IPs for mDNS", "error", err)
	}

	// Create mDNS service instance
	service, err := mdns.NewMDNSService(
		a.name,              // Instance name
		"_llm-gateway._tcp", // Service type
		"",                  // Domain (empty = .local)
		"",                  // Host name (empty = auto)
		a.port,              // Port
		ips,                 // IPs to advertise
		txt,                 // TXT records
	)
	if err != nil {
		return fmt.Errorf("failed to create mDNS service: %w", err)
	}

	// Start the mDNS server
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return fmt.Errorf("failed to start mDNS server: %w", err)
	}

	a.server = server
	a.stopCh = make(chan struct{}) // Reset stop channel for restart support
	a.running = true

	slog.Info("LAN advertiser started",
		"service", a.name,
		"type", "_llm-gateway._tcp.local.",
		"port", a.port,
		"version", a.version,
		"apis", a.apis,
		"hostname", a.hostname,
		"ips", ips,
	)

	// Monitor context cancellation
	go func() {
		select {
		case <-ctx.Done():
			a.Stop()
		case <-a.stopCh:
			return
		}
	}()

	return nil
}

// Stop stops the mDNS service advertisement.
func (a *LANAdvertiser) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.running {
		return
	}

	if a.server != nil {
		if err := a.server.Shutdown(); err != nil {
			slog.Error("failed to shutdown mDNS server", "error", err)
		}
		a.server = nil
	}

	// Close stop channel only if it's open
	select {
	case <-a.stopCh:
		// Already closed
	default:
		close(a.stopCh)
	}
	
	a.running = false

	slog.Info("LAN advertiser stopped", "service", a.name)
}

// IsRunning returns whether the advertiser is currently running.
func (a *LANAdvertiser) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// Status returns the current advertiser status.
func (a *LANAdvertiser) Status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	return map[string]any{
		"running":  a.running,
		"name":     a.name,
		"type":     "_llm-gateway._tcp.local.",
		"port":     a.port,
		"version":  a.version,
		"apis":     a.apis,
		"hostname": a.hostname,
	}
}

// getLocalIPs returns the list of non-loopback IPv4 addresses for this host.
func getLocalIPs() ([]net.IP, error) {
	var ips []net.IP

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		// Skip down or loopback interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			// Only include IPv4 addresses
			if ip != nil && ip.To4() != nil {
				ips = append(ips, ip)
			}
		}
	}

	return ips, nil
}

// DiscoverGateways scans the local network for advertised LLM gateway instances.
// timeout controls how long to wait for responses.
func DiscoverGateways(ctx context.Context, timeout time.Duration) ([]*GatewayInfo, error) {
	entriesCh := make(chan *mdns.ServiceEntry, 16)
	var gateways []*GatewayInfo
	var mu sync.Mutex

	// Collect entries in background
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for entry := range entriesCh {
			mu.Lock()
			gw := parseServiceEntry(entry)
			if gw != nil {
				gateways = append(gateways, gw)
			}
			mu.Unlock()
		}
	}()

	params := &mdns.QueryParam{
		Service:             "_llm-gateway._tcp",
		Domain:              "local",
		Timeout:             timeout,
		Entries:             entriesCh,
		WantUnicastResponse: true,
	}

	if err := mdns.Query(params); err != nil {
		close(entriesCh)
		wg.Wait()
		return nil, fmt.Errorf("mDNS query failed: %w", err)
	}

	close(entriesCh)

	// Wait for collector goroutine to finish
	wg.Wait()

	return gateways, nil
}

// GatewayInfo contains information about a discovered gateway.
type GatewayInfo struct {
	Name      string
	Host      string
	Port      int
	Address   string // Full address (http://host:port)
	Version   string
	APIs      []string
	TXTRecord map[string]string
}

// parseServiceEntry converts an mDNS service entry to GatewayInfo.
func parseServiceEntry(entry *mdns.ServiceEntry) *GatewayInfo {
	if entry == nil {
		return nil
	}

	// Parse TXT records
	txtMap := make(map[string]string)
	for _, txt := range entry.InfoFields {
		parts := strings.SplitN(txt, "=", 2)
		if len(parts) == 2 {
			txtMap[parts[0]] = parts[1]
		}
	}

	// Extract version and APIs
	version := txtMap["version"]
	apisStr := txtMap["api"]
	var apis []string
	if apisStr != "" {
		apis = strings.Split(apisStr, ",")
	}

	// Prefer IPv4 addresses
	host := entry.AddrV4.String()
	if host == "" || host == "<nil>" {
		host = entry.Host
	}

	return &GatewayInfo{
		Name:      entry.Name,
		Host:      host,
		Port:      entry.Port,
		Address:   fmt.Sprintf("http://%s:%d", host, entry.Port),
		Version:   version,
		APIs:      apis,
		TXTRecord: txtMap,
	}
}
