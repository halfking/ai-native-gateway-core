// Package hello is the W2 demonstration in-process plugin adapter.
// It registers a Manifest-compatible settings struct; the process-plugin
// wire form lives under plugin-runtime/testdata/hello-plugin/.
//
// Boundary: plugins/* must not import another plugins/* package
// (see plugin-runtime/boundary_test.go). Core reaches plugins only via
// plugins/all (blank import from cmd/gateway).
package hello

import "github.com/kaixuan/llm-gateway-go/plugin-runtime"

// PluginID is the stable id used in plugins.yml and manifests.
const PluginID = "hello"

// Settings drives the admin form via SchemaOf (title_key → i18n slots).
type Settings struct {
	Greeting string `json:"greeting" title_key:"plugin.hello.greeting" description_key:"plugin.hello.greetingDesc"`
	Times    int    `json:"times" title_key:"plugin.hello.times" enum:"1,2,3" min:"1" max:"3"`
	Enabled  bool   `json:"enabled" title_key:"plugin.hello.enabled"`
}

// DefaultSettings is the Defaults layer before plugins.yml overlay.
func DefaultSettings() Settings {
	return Settings{Greeting: "hello", Times: 1, Enabled: true}
}

// Manifest returns a gateway Manifest fragment for the hello demonstration
// (no Runtime entrypoint — this adapter is settings/schema only; the
// process demo uses testdata/hello-plugin/plugin-manifest.json).
func Manifest() pluginruntime.Manifest {
	s, defs := pluginruntime.SchemaOf(DefaultSettings())
	return pluginruntime.Manifest{
		SchemaVersion:  2,
		PluginID:       PluginID,
		DisplayName:    "Hello Plugin",
		PluginVersion:  "0.1.0",
		Channel:        "dev",
		API:            "^1",
		Essential:      true,
		Defaults:       defs,
		SettingsSchema: s,
		GatewayCompatibility: pluginruntime.GatewayCompatibility{
			MinVersion:  "0.0.0",
			APIContract: pluginruntime.SupportedAPIContractV2,
		},
		Capabilities: []string{},
		Pages: []pluginruntime.Page{{
			Path: "settings",
			Type: "settings",
			Nav: &pluginruntime.Nav{
				Group:    "plugins",
				LabelKey: "plugin.hello.navSettings",
				Order:    900,
			},
		}},
		Web: pluginruntime.Web{
			Mount:    "iframe",
			BasePath: "/plugins/hello/",
			Entry:    "web/index.html",
		},
		ClientAssets: []string{"web/index.html", "web/i18n.js"},
		Provides:     []string{"demo.hello"},
	}
}
