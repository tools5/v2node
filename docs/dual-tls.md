# One AnyTLS/VLESS TLS listener

This opt-in extension keeps one existing panel NodeID, TCP listening port,
domain, certificate and user list. It detects an authorized VLESS v0 UUID
after TLS termination; all other handshakes use the existing AnyTLS parser.
It supports ordinary VLESS TCP+TLS, including UDP/XUDP. Vision, Reality,
cleartext, WebSocket and other transports are not supported by this adapter.

Install this build once, then use the existing physical AnyTLS node's edit
form in the panel: enable **AnyTLS / VLESS 双协议** and save. No server JSON
edit, new Nodes entry or changed NodeID/port is required. The running node
detects the changed panel configuration on its normal polling interval and
reloads. That reload can interrupt existing connections.

The matching panel stores this preference in the existing node's TLS settings.
It advertises VLESS subscriptions only after a supporting node acknowledges
the exact configuration ETag after its listener and users successfully start.
The acknowledgement refreshes on authenticated config polls, including 304
responses, and expires after three polling intervals (at least 180 seconds).
An older binary cannot acknowledge this feature; enabling the checkbox alone
does not advertise an old listener as dual-capable. This confirms software
configuration loading, not a public-network or device connectivity test.

Compatibility: `"AnyTLSVLESS": true` in an existing `/etc/v2nb/config.json`
Nodes entry remains a fallback when the panel sends no preference. An explicit
panel true or false overrides that fallback. The previous
`ANYTLS_VLESS_VERIFIED_NODE_IDS` panel deployment gate remains a fallback for
nodes with no panel preference; it is unnecessary for the new switch.

`protocol=vless` projects authorized AnyTLS nodes to VLESS subscription records.
It preserves their ID, host, port, TLS settings, name, groups and shared child
metadata. Default/all and AnyTLS subscriptions are unchanged. Every VLESS
exporter uses the matching credential helper. Primary users retain their UUID;
shared-child HMAC passwords map to deterministic UUIDs, retaining child routes
and the original physical parent's billing, speed and device limits.

User steps after activation: select VLESS/ANYTLS on the website, import the
selected subscription, and refresh it in a supporting client. The selector
does not edit a client's existing subscription URL or switch an established
connection. Existing connections cannot change their wire protocol midstream.
The first node upgrade/restart can interrupt active connections.

Rollback: turn the panel switch off first, refresh affected
client subscriptions back to AnyTLS, then restore the previous node binary or
disable the legacy AnyTLSVLESS setting and restart. Keep the dual-capable node during the client
subscription transition so cached VLESS subscriptions keep connecting.

Validation: actual TLS loopback connections using Xray AnyTLS/VLESS clients;
TCP echo; VLESS XUDP; standard sing UoT over AnyTLS TCP; shared-child route
separation and canonical UID accounting; 12 concurrent mixed connections;
fragmented short VLESS header; AnyTLS auth hash beginning with zero; invalid
auth, TLS name checks and user revocation, including an authenticated AnyTLS
session. Test-only outbound rules allow loopback; production routing policy
is unchanged. The pinned Xray AnyTLS native UDP outbound parses server UoT
responses as requests, so AnyTLS UDP validation uses sing's standard encoding
over its TCP stream. This change does not modify that preexisting client bug.

The integration test loads its temporary TLS certificate once. The pinned
Xray certificate/OCSP reloader mutates the shared certificate slice without
synchronization, which independently triggers the race detector. The test
retains real TLS handshakes and concurrent protocol/accounting checks, but
does not validate certificate rotation. Production certificate reload/OCSP
settings and the pinned dependency are unchanged; that upstream race remains.
