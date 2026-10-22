# One AnyTLS/VLESS TLS listener

This opt-in extension keeps one existing panel NodeID, TCP listening port,
domain, certificate and user list. It detects an authorized VLESS v0 UUID
after TLS termination; all other handshakes use the existing AnyTLS parser.
It supports ordinary VLESS TCP+TLS, including UDP/XUDP. Vision, Reality,
cleartext, WebSocket and other transports are not supported by this adapter.

Add `"AnyTLSVLESS": true` to the existing entry in `/etc/v2nb/config.json`
after installing this build. Do not add a Nodes entry or change NodeID/ports.
Restart the node once to load the opt-in. Invalid node/transport combinations
fail at startup. With the flag omitted, the original AnyTLS handler is retained.

The matching panel patch uses `ANYTLS_VLESS_VERIFIED_NODE_IDS`, a comma-separated
list of physical IDs that the operator has upgraded and successfully tested.
Leave it unset until then. Rebuild the panel configuration cache/reload its
workers using the normal deployment procedure after changing this setting.
This is an explicit operator deployment gate; it is not automatic runtime
capability detection and does not by itself prove a listener was upgraded.

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

Rollback: remove physical IDs from the panel opt-in first, refresh affected
client subscriptions back to AnyTLS, then restore the previous node binary or
disable AnyTLSVLESS and restart. Keep the dual-capable node during the client
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
