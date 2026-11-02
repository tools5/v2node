package core

import (
	"bytes"
	"encoding/base64"
	"testing"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/conf"
	"github.com/xtls/xray-core/app/proxyman"
	xcore "github.com/xtls/xray-core/core"
	xconf "github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
)

func TestShadowsocks2022PlaceholderAcceptedByCore(t *testing.T) {
	for _, tc := range []struct {
		cipher string
		size   int
	}{
		{"2022-blake3-aes-128-gcm", 16},
		{"2022-blake3-aes-256-gcm", 32},
	} {
		t.Run(tc.cipher, func(t *testing.T) {
			node := &panel.NodeInfo{Common: &panel.CommonNode{
				Cipher:    tc.cipher,
				ServerKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, tc.size)),
			}}
			in := &xconf.InboundDetourConfig{}
			if err := buildShadowsocks(node, in); err != nil {
				t.Fatal(err)
			}
			in.PortList = &xconf.PortList{Range: []xconf.PortRange{{From: 12345, To: 12345}}}
			built, err := in.Build()
			if err != nil {
				t.Fatal(err)
			}
			proxyConfig, err := built.ProxySettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			server := getCore(conf.New(), nil)
			t.Cleanup(func() { _ = server.Close() })
			// Key lengths are checked when the core creates the proxy, after
			// the JSON/protobuf configuration has already been accepted.
			if _, err := xcore.CreateObject(server, proxyConfig); err != nil {
				t.Fatalf("core rejected the generated placeholder user: %v", err)
			}
		})
	}
}

func TestDefaultOutboundResolvesIPv4ThenIPv6(t *testing.T) {
	out, err := buildDefaultOutbound()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := out.SenderSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	sender, ok := instance.(*proxyman.SenderConfig)
	if !ok {
		t.Fatalf("unexpected sender configuration: %T", instance)
	}
	if got := sender.GetStreamSettings().GetSocketSettings().GetDomainStrategy(); got != internet.DomainStrategy_USE_IP46 {
		t.Fatalf("default outbound domain strategy = %v, want USE_IP46", got)
	}
}
