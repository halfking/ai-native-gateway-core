package discovery

import "time"

// Event represents a plugin discovery event.
type Event struct {
	Type      EventType
	Manifest  *Manifest
	Timestamp time.Time
}

// EventType indicates the type of discovery event.
type EventType string

const (
	EventAdded    EventType = "added"
	EventModified EventType = "modified"
	EventDeleted  EventType = "deleted"
)

// Manifest represents a plugin manifest (manifest.yaml).
type Manifest struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
	Signature  string   `yaml:"signature"`
}

// Metadata contains plugin metadata.
type Metadata struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Author      string            `yaml:"author"`
	Homepage    string            `yaml:"homepage,omitempty"`
	License     string            `yaml:"license,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// Spec defines the plugin specification.
type Spec struct {
	Type            PluginType             `yaml:"type"`
	Executable      string                 `yaml:"executable,omitempty"`
	Args            []string               `yaml:"args,omitempty"`
	Env             map[string]string      `yaml:"env,omitempty"`
	WorkDir         string                 `yaml:"workDir,omitempty"`
	Remote          *RemoteConfig          `yaml:"remote,omitempty"`
	Dependencies    []Dependency           `yaml:"dependencies,omitempty"`
	ExtensionPoints []ExtensionPointDecl   `yaml:"extensionPoints"`
	Capabilities    Capabilities           `yaml:"capabilities"`
	Resources       ResourceLimits         `yaml:"resources"`
	Health          *HealthConfig          `yaml:"health,omitempty"`
	ConfigSchema    map[string]interface{} `yaml:"configSchema,omitempty"`
}

// PluginType indicates the plugin deployment type.
type PluginType string

const (
	TypeBuiltin  PluginType = "builtin"
	TypeExternal PluginType = "external"
	TypeRemote   PluginType = "remote"
)

// RemoteConfig configures remote plugin connection.
type RemoteConfig struct {
	URL     string     `yaml:"url"`
	TLS     *TLSConfig `yaml:"tls,omitempty"`
	Timeout string     `yaml:"timeout,omitempty"`
}

// TLSConfig configures TLS for remote plugins.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"certFile,omitempty"`
	KeyFile  string `yaml:"keyFile,omitempty"`
	CAFile   string `yaml:"caFile,omitempty"`
}

// Dependency declares a plugin dependency.
type Dependency struct {
	Name     string `yaml:"name"`
	Version  string `yaml:"version"`
	Optional bool   `yaml:"optional,omitempty"`
}

// ExtensionPointDecl declares an extension point provided by the plugin.
type ExtensionPointDecl struct {
	Name        string `yaml:"name"`
	Phase       string `yaml:"phase"`
	Priority    int    `yaml:"priority"`
	Interface   string `yaml:"interface"`
	Description string `yaml:"description,omitempty"`
	Async       bool   `yaml:"async,omitempty"`
}

// Capabilities declares what the plugin can access.
type Capabilities struct {
	CanAccessDatabase   bool     `yaml:"can_access_database,omitempty"`
	CanAccessRedis      bool     `yaml:"can_access_redis,omitempty"`
	CanAccessFilesystem bool     `yaml:"can_access_filesystem,omitempty"`
	CanAccessNetwork    bool     `yaml:"can_access_network,omitempty"`
	CanAccessUpstream   bool     `yaml:"can_access_upstream,omitempty"`
	CanReadRequest      bool     `yaml:"can_read_request,omitempty"`
	CanModifyRequest    bool     `yaml:"can_modify_request,omitempty"`
	CanReadResponse     bool     `yaml:"can_read_response,omitempty"`
	CanModifyResponse   bool     `yaml:"can_modify_response,omitempty"`
	AllowedAPIs         []string `yaml:"allowed_apis,omitempty"`
	SyscallWhitelist    []string `yaml:"syscall_whitelist,omitempty"`
}

// ResourceLimits defines resource constraints for the plugin.
type ResourceLimits struct {
	Memory       *ResourceQuantity `yaml:"memory,omitempty"`
	CPU          *ResourceQuantity `yaml:"cpu,omitempty"`
	Timeout      string            `yaml:"timeout,omitempty"`
	MaxOpenFiles int               `yaml:"max_open_files,omitempty"`
	MaxProcesses int               `yaml:"max_processes,omitempty"`
}

// ResourceQuantity represents a resource quantity with request/limit.
type ResourceQuantity struct {
	Request string `yaml:"request,omitempty"`
	Limit   string `yaml:"limit,omitempty"`
}

// HealthConfig defines health check configuration.
type HealthConfig struct {
	HTTP *HTTPHealthCheck `yaml:"http,omitempty"`
	GRPC *GRPCHealthCheck `yaml:"grpc,omitempty"`
}

// HTTPHealthCheck configures HTTP health checks.
type HTTPHealthCheck struct {
	Path             string `yaml:"path"`
	Port             int    `yaml:"port"`
	Scheme           string `yaml:"scheme,omitempty"`
	InitialDelay     string `yaml:"initial_delay,omitempty"`
	Period           string `yaml:"period,omitempty"`
	Timeout          string `yaml:"timeout,omitempty"`
	SuccessThreshold int    `yaml:"success_threshold,omitempty"`
	FailureThreshold int    `yaml:"failure_threshold,omitempty"`
}

// GRPCHealthCheck configures gRPC health checks.
type GRPCHealthCheck struct {
	Port    int    `yaml:"port"`
	Service string `yaml:"service,omitempty"`
}
