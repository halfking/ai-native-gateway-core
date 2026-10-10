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
QR payload. The native client pins its fingerprint after the first scan and
rejects a different key for a node already registered on that device.

## Private node through NetBird

Install and enroll the gateway as a NetBird peer. The operator's phone must
also have the NetBird client installed and logged in. Use a dedicated target
group containing gateway peers and a source group containing authorized mobile
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
management API to add its peer to the configured gateway group and create or
refresh a dedicated policy. That policy allows TCP/443 from the configured
mobile-operator group to the gateway group. It does not change other groups or
policies. The selected policy name defaults to a node-specific
`llmgw-mobile-access-<node-id>`; set `NETBIRD_MOBILE_POLICY_NAME` only when a
unique managed name is needed. A same-name policy without the
`managed-by=llmgw-mobile-node` marker is left untouched and causes QR creation
to fail closed.

The configured gateway group must be dedicated to gateway peers, and the
mobile-operator group must contain only authorized operator peers. This makes
the generated allow rule's scope explicit and reviewable in the NetBird
management console. Use `LLMGW_PUBLIC_ORIGIN` as well when a node supports both
public and private access; the client offers both transports.

If the signing key, endpoint, peer, group, or API configuration is incomplete,
QR provisioning returns an unavailable response and no access rule is created.
