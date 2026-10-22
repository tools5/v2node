package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wyx2685/v2node/conf"
)

func TestPanelDualTLSPriorityAndAppliedAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		local, want   bool
	}{
		{"legacy-on", "", true, true},
		{"legacy-off", "", false, false},
		{"panel-on", `,"anytls_vless":true`, false, true},
		{"panel-off-overrides-local", `,"anytls_vless":false`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var applied atomic.Value
			applied.Store("")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.Header.Get("X-V2nb-Features"), "anytls-child-routing-v1") ||
					!strings.Contains(r.Header.Get("X-V2nb-Features"), "anytls-vless-panel-v1") {
					t.Error("missing feature advertisement")
				}
				applied.Store(r.Header.Get("X-V2nb-Config-Applied"))
				w.Header().Set("ETag", `"revision-25"`)
				if r.Header.Get("If-None-Match") != "" {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"protocol":"anytls","tls":1,"network":"tcp","server_port":443,"tls_settings":{"cert_mode":"file"},"base_config":{"pull_interval":60,"push_interval":60}%s}`, tc.setting)
			}))
			defer server.Close()
			client, err := New(&conf.NodeConfig{APIHost: server.URL, NodeID: 25, AnyTLSVLESS: tc.local})
			if err != nil {
				t.Fatal(err)
			}
			node, err := client.GetNodeInfo(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if node.Common.DualTLS != tc.want || node.ConfigRevision != "revision-25" || applied.Load() != "" {
				t.Fatalf("configuration precedence or premature ack: %+v, ack=%q", node, applied.Load())
			}
			// Downloading/parsing is not sufficient to advertise an active listener.
			if _, err := client.GetNodeInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			if applied.Load() != "" {
				t.Fatal("unloaded config acknowledged")
			}
			client.MarkNodeApplied(node)
			if _, err := client.GetNodeInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantAck := ""
			if tc.want {
				wantAck = "revision-25"
			}
			if applied.Load() != wantAck {
				t.Fatalf("applied ack=%q, want %q", applied.Load(), wantAck)
			}
			client.MarkNodeApplied(nil)
			if _, err := client.GetNodeInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			if applied.Load() != "" {
				t.Fatal("stopped listener still acknowledged")
			}
		})
	}
}
