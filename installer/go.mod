module github.com/kaixuan/llm-gateway-go/installer

// Full major.minor.patch: golang.org/toolchain publishes go1.22.0, not go1.22,
// so a bare `go 1.22` makes GOTOOLCHAIN=auto resolve a module version that does
// not exist and offline-package builds fail with "toolchain not available".
go 1.27.1

require (
	github.com/spf13/cobra v1.8.0
	golang.org/x/term v0.21.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/sys v0.21.0 // indirect
)
