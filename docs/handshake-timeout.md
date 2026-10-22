# Handshake timeout

`HandshakeTimeout` sets the Xray level-0 handshake timeout in seconds. This includes
waiting for a SOCKS outbound to resolve and connect to its destination. The default
remains four seconds. A slow VPN outbound can need more time even when its tunnel
and data path are healthy.

For example, merge this field into the existing server configuration:

```json
{
  "HandshakeTimeout": 15
}
```

The setting applies to the core on that server, including inbound and outbound
handshakes. Other connection idle and traffic policies retain their existing values.
Zero uses the historical four-second default; negative values and values above
120 seconds are rejected. Restart the service after changing this setting.
