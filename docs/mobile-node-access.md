# Mobile gateway node access

The native client keeps a local registry of gateway nodes. A super-admin can
show a short-lived signed QR descriptor from **Account → Gateway node**. The
mobile client scans it, displays the node name, endpoint, and signing-key
fingerprint for confirmation, then stores the selected public or NetBird
transport. The descriptor expires after five minutes and contains no gateway
or NetBird credentials.

## Public node

Configure the signing key and public origin on the gateway process:

```text
LLMGW_NODE_ID=prod-east-1
LLMGW_NODE_NAME=Production East
LLMGW_PUBLIC_ORIGIN=https://gateway.example.com
LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY=<base64 Ed25519 seed or private key>
```

Generate a random 32-byte seed with your secret-management tooling and store it
in the server secret store. The matching public key is included in each signed
QR payload. The operator must obtain the full SHA-256 public-key fingerprint independently
from an authenticated administrator before first pairing. The admin account
page shows this fingerprint separately from the QR; it can also be derived
from the authenticated descriptor response using the client repository
`scripts/node-descriptor-fingerprint.mjs`. A self-signed QR alone is not a
trust anchor. Repeat pairing pins the full fingerprint and rejects key changes.

## Private node through NetBird

Install and enroll the gateway as a NetBird peer. The operator's phone must
also have the NetBird client installed and logged in. Use a dedicated target
group containing only this gateway peer and a source group containing authorized mobile
operator peers. Set:

```text
LLMGW_NODE_ID=private-edge-1
LLMGW_NODE_NAME=Private Edge
LLMGW_NETBIRD_ORIGIN=https://gateway.private.example.com
NETBIRD_GATEWAY_PEER_ID=<NetBird peer ID for this gateway>
NETBIRD_API_URL=https://api.netbird.io
NETBIRD_API_TOKEN=<NetBird management API token>
NETBIRD_GATEWAY_GROUP_ID=<dedicated gateway group ID>
NETBIRD_MOBILE_ADMIN_GROUP_ID=<authorized mobile operators group ID>
LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY=<base64 Ed25519 seed or private key>
```

For a self-hosted NetBird management server, set `NETBIRD_API_URL` to its HTTPS
origin. Keep the API token and descriptor private key on the gateway server;
neither is sent to the app.

Before signing a descriptor with a private origin, the gateway uses the NetBird
management API to validate the peer, both groups, and any same-name policy
before writing. It creates the narrow policy first, then adds the peer;
matching policies are reused without changing posture checks. That policy allows TCP/443 from the configured
mobile-operator group to the gateway group. It does not change other groups or
policies. The selected policy name defaults to a node-specific
`llmgw-mobile-access-<node-id>`; set `NETBIRD_MOBILE_POLICY_NAME` only when a
unique managed name is needed. A same-name policy without the
`managed-by=llmgw-mobile-node` marker is left untouched and causes QR creation
to fail closed.

Each node must own a dedicated destination group with no other peers or
network resources. All replicas managing that node must share the gateway
database: a PostgreSQL session advisory lock serializes the full reconciliation
sequence. Sharing a group between independent gateway databases is unsupported.
The private HTTPS origin must use port 443. The
mobile-operator group must contain only authorized operator peers. This makes
the generated allow rule's scope explicit and reviewable in the NetBird
management console. Use `LLMGW_PUBLIC_ORIGIN` as well when a node supports both
public and private access; the client offers both transports.

If the signing key, endpoint, peer, group, or API configuration is incomplete,
QR provisioning returns an unavailable response. Failed validation performs no
writes. If creating a policy succeeds but membership fails, no QR is returned;
retrying reuses the policy and finishes membership. The API refuses redirects
and bounds response size and total reconciliation time.

Logout uses the current gateway HTTPS logout endpoint and an origin-scoped
local tombstone when offline. Remote pages receive no native bridge; logout
does not erase the local node directory, signing-key pins, or other gateways’
website data. An explicit successful login clears the tombstone.
