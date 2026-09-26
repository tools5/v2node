package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wyx2685/v2node/common/connectioncontrol"
	"github.com/wyx2685/v2node/conf"
)

func testConnectionTransport(t *testing.T, serverURL string) *ConnectionTransport {
	t.Helper()
	client, err := New(&conf.NodeConfig{APIHost: serverURL, NodeID: 6, Key: "private-node-token"})
	if err != nil {
		t.Fatal(err)
	}
	return NewConnectionTransport(client)
}

func TestConnectionWireContractAndProcessIdentity(t *testing.T) {
	var identity atomic.Value
	var posted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectionEndpoint || r.URL.Query().Get("node_id") != "6" || r.URL.Query().Get("node_type") != "v2node" || r.URL.Query().Get("token") != "private-node-token" {
			t.Errorf("incorrect node request: path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			instanceID := r.URL.Query().Get("instance_id")
			identity.Store(instanceID)
			if len(instanceID) != 32 {
				t.Error("missing process identity")
			}
			fmt.Fprint(w, `{"data":{"version":1,"commands":[{"id":"job1","action":"snapshot","user_id":7,"expires_at":100}],"poll_after":2}}`)
		case http.MethodPost:
			var result struct {
				connectioncontrol.Result
				InstanceID string `json:"instance_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			if result.InstanceID != identity.Load() || result.RequestID != "job1" || result.UserID != 7 {
				t.Errorf("wrong result scope: %+v", result)
			}
			posted.Store(true)
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	defer server.Close()
	transport := testConnectionTransport(t, server.URL)
	batch, err := transport.PollConnectionCommands(context.Background())
	if err != nil || batch.Version != 1 || len(batch.Commands) != 1 {
		t.Fatalf("poll: %+v %v", batch, err)
	}
	err = transport.SubmitConnectionResult(context.Background(), connectioncontrol.Result{RequestID: "job1", UserID: 7, Connections: []connectioncontrol.Connection{}})
	if err != nil || !posted.Load() {
		t.Fatalf("submit: %v", err)
	}
}

func TestMonitorWillNotFollowRedirectsWithNodeToken(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := testConnectionTransport(t, server.URL).PollConnectionCommands(context.Background())
	if err == nil || followed.Load() {
		t.Fatal("redirect was followed")
	}
	if strings.Contains(err.Error(), "private-node-token") {
		t.Fatal("token leaked through error")
	}
}

func TestMonitorDistinguishesUnsupportedAndExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusGone)
		}
	}))
	defer server.Close()
	transport := testConnectionTransport(t, server.URL)
	if _, err := transport.PollConnectionCommands(context.Background()); !errors.Is(err, connectioncontrol.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if err := transport.SubmitConnectionResult(context.Background(), connectioncontrol.Result{}); !errors.Is(err, connectioncontrol.ErrExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestMonitorBoundsAndValidatesResponses(t *testing.T) {
	for _, body := range []string{`<html>login</html>`, strings.Repeat(" ", (64<<10)+1), `{"data":{}} trailing`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
		_, err := testConnectionTransport(t, server.URL).PollConnectionCommands(context.Background())
		server.Close()
		if !errors.Is(err, connectioncontrol.ErrInvalid) {
			t.Fatalf("accepted invalid response: %v", err)
		}
	}
}

func TestMonitorCancelsInflightRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := testConnectionTransport(t, server.URL).PollConnectionCommands(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
