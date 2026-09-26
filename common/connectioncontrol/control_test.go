package connectioncontrol

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeTransport struct {
	batch     Batch
	pollErr   error
	submitErr error
	results   []Result
}

func (f *fakeTransport) PollConnectionCommands(context.Context) (Batch, error) {
	return f.batch, f.pollErr
}
func (f *fakeTransport) SubmitConnectionResult(_ context.Context, result Result) error {
	f.results = append(f.results, result)
	return f.submitErr
}

func newTestWorker(t *testing.T, action string) (*Worker, *fakeTransport, *int) {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	transport := &fakeTransport{batch: Batch{Version: Version, PollAfter: 2, Commands: []Command{{ID: "request-1", Action: action, UserID: 6, ExpiresAt: now.Add(20 * time.Second).Unix()}}}}
	calls := new(int)
	worker := NewWorker(transport, func(command Command) Result {
		*calls++
		return Result{Closed: 3}
	})
	worker.now = func() time.Time { return now }
	return worker, transport, calls
}

func TestLostAcknowledgementDoesNotCloseReconnectedSessions(t *testing.T) {
	worker, transport, calls := newTestWorker(t, "close")
	transport.submitErr = errors.New("lost response")
	if _, err := worker.Step(context.Background()); err == nil {
		t.Fatal("expected failed acknowledgement")
	}
	transport.submitErr = nil
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || len(transport.results) != 2 || transport.results[1].Closed != 3 {
		t.Fatalf("close ran %d times, results: %+v", *calls, transport.results)
	}
	if transport.results[1].Connections == nil {
		t.Fatal("empty result must serialize as []")
	}
}

func TestExpiredCloseNeverExecutes(t *testing.T) {
	worker, transport, calls := newTestWorker(t, "close")
	transport.batch.Commands[0].ExpiresAt = worker.now().Add(-time.Second).Unix()
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || transport.results[0].Error != "request_expired" {
		t.Fatalf("expired close executed: %+v", transport.results)
	}
}

func TestAcknowledgedSnapshotIsNotRetained(t *testing.T) {
	worker, _, _ := newTestWorker(t, "snapshot")
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(worker.completed) != 0 {
		t.Fatal("successful snapshot retained unnecessary connection details")
	}
}

func TestCommandIDCannotBeReusedForAnotherUser(t *testing.T) {
	worker, transport, calls := newTestWorker(t, "close")
	if _, err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport.batch.Commands[0].UserID = 7
	if _, err := worker.Step(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if *calls != 1 {
		t.Fatal("replayed id closed another user's sessions")
	}
}

func TestInvalidJobsCannotInvokeCloser(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Command)
	}{
		{"all_users", func(c *Command) { c.UserID = 0 }},
		{"unsupported_action", func(c *Command) { c.Action = "disable_user" }},
		{"invalid_connection_id", func(c *Command) { c.ConnectionID = "a/b" }},
		{"unbounded_expiry", func(c *Command) { c.ExpiresAt = time.Now().Add(time.Hour).Unix() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, transport, calls := newTestWorker(t, "close")
			test.change(&transport.batch.Commands[0])
			_, _ = worker.Step(context.Background())
			if *calls != 0 {
				t.Fatal("invalid job invoked closer")
			}
		})
	}
}

func TestUnsupportedPanelsAndOversizedBatch(t *testing.T) {
	worker, transport, calls := newTestWorker(t, "snapshot")
	transport.batch.Version = 0
	if _, err := worker.Step(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	transport.batch.Version = Version
	transport.batch.Commands = make([]Command, MaxCommands+1)
	if _, err := worker.Step(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if *calls != 0 {
		t.Fatal("unexpected execution")
	}
}

func TestCacheCapacityDoesNotEvictRecentCloseResults(t *testing.T) {
	worker, transport, calls := newTestWorker(t, "close")
	for i := 0; i < maxCachedJobs; i++ {
		worker.completed[string(rune(i+1000))] = cachedResult{expires: worker.now().Add(time.Minute)}
	}
	if _, err := worker.Step(context.Background()); err == nil {
		t.Fatal("expected full cache to defer job")
	}
	if *calls != 0 {
		t.Fatal("executed job without idempotency capacity")
	}
	if len(transport.results) != 0 {
		t.Fatal("fabricated completion for deferred job")
	}
}

func TestCanceledWorkerStopsEvenDuringUnsupportedBackoff(t *testing.T) {
	worker, transport, _ := newTestWorker(t, "snapshot")
	transport.pollErr = ErrUnsupported
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx, nil); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker failed to cancel promptly")
	}
}
