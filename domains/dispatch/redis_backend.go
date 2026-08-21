package dispatch

// Stage B production file — populated by commit B.2.
// Commit B.1 keeps this file as a stub so the Lua contract test pins the
// wire shape before any production code can cheat the contract.

// RedisEnforceBackend and RedisShadowBackend will live here.
// They implement GovernorBackend (Kind/Name/Open/Close/NotifyRevisions)
// plus the Stage B extension New(ctx, spec) (Governor, error) added in B.2.
