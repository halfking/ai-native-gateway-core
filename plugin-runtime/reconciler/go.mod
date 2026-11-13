module github.com/kaixuan/llm-gateway-go/plugin-runtime/reconciler

go 1.27.2

replace github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor => ../supervisor

replace github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery => ../discovery

require (
	github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery v0.0.0
	github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor v0.0.0-00010101000000-000000000000
)

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	golang.org/x/sys v0.13.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
