package settings

// KeyRequestLogsWriteEnabled is the S4 stop-write gate for the _request_logs
// wide family (_request_logs_hot main rows + _request_logs_bodies_hot bodies).
// Default false = "not double-writing" is a code fact, not a per-deployment
// configuration fact (2026-10-20, audit D0). Declared in spec_storage.go;
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

// RequestLogsWriteEnabled reports whether the _request_logs wide family may
// still be written. Mirrors the S4 gate consulted by every ingest path.
//
// Safe to call before Global is initialised — GetPlatformBool falls back to
// the supplied default when Global is nil, so a validator wired early cannot
// panic and cannot silently read "false" out of an uninitialised store.
//
// The fallback is **false** (2026-10-20, audit D0). It used to be true, and
// that fallback was the load-bearing half of the defect: a store that is not
// initialised yet (or a deployment that never configures the key) read as
// "keep double-writing", so "the wide family is retired" was true only in one
// environment's settings_kv, not in the code. false is the conservative
// direction here because the stop-write side is the *intended* end state and
// the write side is the legacy one.
//
// The mirror image of this change is that "false" now means "v1 is frozen"
// rather than "cannot be evaluated": cmd/gateway's S4 cutover gate
// (s4GateVerdictOf) reports S4GateVoid with reason v1_writes_disabled for a
// false value. That verdict is still void — there is no v1 side left to
// compare against — but it is now void *by configuration*, which is a
// different operator message than "the gate could not be evaluated".
func RequestLogsWriteEnabled() bool {
	return GetPlatformBool(KeyRequestLogsWriteEnabled, false)
}
