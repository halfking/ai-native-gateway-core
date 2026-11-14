module github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor

go 1.27.4

require github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery v0.0.0

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	golang.org/x/sys v0.13.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery => ../discovery
