# Shared AnyTLS child routes

This extension lets compatible V2node AnyTLS children use one parent's TCP/TLS listener and distinct outbound routes. It requires the matching panel extension and this v2nb build.

Enable selected physical parent IDs in the panel environment, for example `SHARED_ANYTLS_PARENT_IDS=25`. Leave it unset until all selected physical backends have been upgraded. Reload the panel configuration cache and workers after changing the environment. The node advertises `X-V2nb-Features: anytls-child-routing-v1`; older nodes receive the original API payloads.

Eligible children must share the parent's user groups, billing rate, listening IP/port, protocol, transport, TLS configuration and network configuration. The physical parent must be a root AnyTLS TCP node with ordinary TLS. Child DNS rules that modify the global DNS configuration are excluded. Other protocols, distinct rates, and distinct permissions require separate backends.

The parent retains the original UUID password. A child's subscription password is lowercase hex HMAC-SHA256, key = original UUID string, message = `v2nb:anytls-child:v1:<child ID>`. This is an authentication alias for the existing user, never a second account. Updated subscriptions are required; older subscriptions continue to authenticate as the parent.

The panel returns child IDs with the original UID/limits and child routes with the physical configuration. The core adds authentication aliases and virtual routing tags without extra sockets. Child outbound tags are scoped to the virtual inbound. Limits, traffic reports and online devices aggregate under the original physical parent and UID. Revocation is checked for each dispatched stream, including streams opened on previously authenticated multiplexed connections.

Child route changes alter the parent configuration ETag and trigger the usual node reload. Connection reporting resolves aliases to the original UID. Traffic is billed at the parent's rate, which is why equal rates are required.

For rollback, first unset the panel opt-in, rebuild the configuration cache and reload workers, then restore the previous node binary. Clients must refresh subscriptions to restore original child passwords. Keep the enhanced backend during that subscription transition if uninterrupted use of cached child passwords is required.
