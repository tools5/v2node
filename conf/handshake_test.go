package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandshakeTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       uint32
		invalid    bool
	}{
		{"omitted", `{}`, 4, false},
		{"slow_outbound", `{"HandshakeTimeout":15}`, 15, false},
		{"zero_retains_default", `{"HandshakeTimeout":0}`, 4, false},
		{"negative", `{"HandshakeTimeout":-1}`, 0, true},
		{"unbounded", `{"HandshakeTimeout":121}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c := New()
			err := c.LoadFromPath(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid handshake limit accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.HandshakeTimeoutSeconds(); got != tc.want {
				t.Fatalf("got %d seconds, want %d", got, tc.want)
			}
		})
	}
	if got := (&Conf{}).HandshakeTimeoutSeconds(); got != 4 {
		t.Fatalf("zero-value config changed to %d seconds", got)
	}
}
