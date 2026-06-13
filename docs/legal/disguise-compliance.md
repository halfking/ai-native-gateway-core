# Traffic Disguise — Legal & Compliance Notice

## Scope

llm-gateway-go includes optional traffic disguise features designed to
defeat trivial server-side fingerprinting based on the User-Agent and
TLS ClientHello headers. These features are:

1. **User-Agent / Accept-Language rotation** (`disguise/` package)
2. **TLS ClientHello fingerprint rotation** (`tlsfp/` package, utls)

Both are disabled by default and gated by the
`LLM_GATEWAY_ENABLE_DISGUISE` env var.

## What is NOT changed

* Request body / JSON payloads (only header fields are touched)
* Authentication (Bearer tokens, API keys)
* Source IP address
* TLS protocol version or cipher suites (only the order of extensions
  in the ClientHello is varied; the negotiated parameters comply with
  the TLS spec).

## Compliance considerations

### Provider terms of service

Some upstream API providers explicitly require accurate
`User-Agent` headers, or prohibit "automated access" of any kind
(Anthropic ToS §A.2, OpenAI ToS §3, Google Cloud ToS "probing/spoofing"
clause). Operators MUST review the ToS of every provider they
front-end with this gateway and may need to disable disguise for
non-whitelisted providers.

### CFAA / EU CSAM / Chinese Cybersecurity Law

These laws are concerned with substantive misuse of the upstream API
itself (e.g., generating illegal content). They do not regulate the
specific TLS handshake order or User-Agent string. However, deliberate
obfuscation intended to bypass access controls may be considered
evidence of bad faith in a US/EU civil dispute.

### Export controls (US EAR)

utls is a TLS library, not a cryptographic primitive under EAR Part 772.
No export license is required.

### Local legal counsel

The operators of this gateway MUST consult their own legal counsel
before enabling disguise for any provider not on the documented
whitelist in `docs/legal/provider-whitelist.md`.

## Operational safeguards

* Disguise is **off by default**; flipping it on is a conscious choice.
* Disguise is **per-process**: a single `LLM_GATEWAY_ENABLE_DISGUISE=true`
  does not globally apply to every llm-gateway-go instance.
* All admin API endpoints exposing disguise stats are protected by the
  bearer-token middleware (`adminMiddleware`).
* Every tlsfp/disguise rotation is logged at DEBUG level with the
  profile name; operators can audit which profiles were used.

## References

* utls library — https://github.com/refraction-networking/utls
* MDN: User-Agent header — https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/User-Agent
* Salesforce blog: TLS fingerprinting — https://www.salesforce.com/blog/2019/03/tls-fingerprinting.html

## Change log

* 2026-06-14: Initial notice (v1.0). Disguise is opt-in.
