package core

import (
	"testing"
	"time"

	"github.com/wyx2685/v2node/conf"
	"github.com/xtls/xray-core/features/policy"
)

func TestCoreAppliesConfiguredHandshakeTimeout(t *testing.T) {
	for _, seconds := range []int{0, 15} {
		c := conf.New()
		c.HandshakeTimeout = seconds
		server := getCore(c, nil)
		manager := server.GetFeature(policy.ManagerType()).(policy.Manager)
		want := 4 * time.Second
		if seconds != 0 {
			want = time.Duration(seconds) * time.Second
		}
		if got := manager.ForLevel(0).Timeouts.Handshake; got != want {
			t.Errorf("core handshake timeout = %v, want %v", got, want)
		}
		_ = server.Close()
	}
}
