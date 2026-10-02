package settings

// KeyRequestLogsWriteEnabled is the S4 stop-write gate for the request_logs
// wide family (request_logs_hot main rows + request_logs_bodies_hot bodies).
// Default true = keep double-writing as today. Declared in spec_storage.go;
// this constant is the single Go-side spelling.
//
// Why a constant at all (2026-10-02): the key was spelled as a bare string
// literal in five places — admin/telemetry.go, cmd/gateway/lite_telemetry_sink.go,
// domains/hooks/observability/telemetry/client.go, and two reads added by the
// dual-read gate work. The existing comment on admin's requestLogsWriteEnabled()
// already names the failure mode this creates ("门内联两遍…就是分裂入口"),
// but the de-duplication it performed was package-local, so the other three
// sites stayed free to drift. A stop-write gate that is spelled five ways is a
// gate where one site can be flipped without the others.
//
// TestRequestLogsWriteEnabledKeyIsSpelledOnce pins the call sites: any new
// literal spelling of this key is a finding, not a style preference.
const KeyRequestLogsWriteEnabled = "storage.request_logs_write_enabled"

// RequestLogsWriteEnabled reports whether the request_logs wide family may
// still be written. Mirrors the S4 gate consulted by every ingest path.
//
// Safe to call before Global is initialised — GetPlatformBool falls back to
// the supplied default when Global is nil, so a validator wired early cannot
// panic and cannot silently read "false" out of an uninitialised store.
func RequestLogsWriteEnabled() bool {
	return GetPlatformBool(KeyRequestLogsWriteEnabled, true)
}
