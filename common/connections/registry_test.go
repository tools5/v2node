package connections

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func startEntry(r *Registry, info Info, closeStream func()) *Entry {
	entry := r.NewEntry(info, closeStream)
	entry.Start()
	return entry
}

func TestScopedCloseAllowsReconnectAndRunsWithoutLock(t *testing.T) {
	r := NewRegistry(10)
	var closed atomic.Int64
	var replacement *Entry
	first := startEntry(r, Info{Inbound: "node-1", User: "user-1"}, func() {
		closed.Add(1)
		// A close callback can reenter the registry, as transport cleanup does.
		r.Stats()
		replacement = startEntry(r, Info{Inbound: "node-1", User: "user-1"}, nil)
	})
	startEntry(r, Info{Inbound: "node-1", User: "user-2"}, nil)
	startEntry(r, Info{Inbound: "node-2", User: "user-1"}, nil)
	if n := r.Close(Filter{Inbound: "node-2", User: "user-1"}, first.info.ID); n != 0 {
		t.Fatalf("closed an ID outside its node: %d", n)
	}
	if n := r.Close(Filter{Inbound: "node-1", User: "user-1"}, ""); n != 1 {
		t.Fatalf("closed %d streams, want 1", n)
	}
	if closed.Load() != 1 || len(r.Snapshot(Filter{})) != 3 {
		t.Fatal("close affected other users/nodes or rejected a new connection")
	}
	if replacement == nil || replacement.info.ID == first.info.ID {
		t.Fatal("reconnected stream reused a stale ID")
	}
	if r.Close(Filter{}, first.info.ID) != 0 || closed.Load() != 1 {
		t.Fatal("repeated stale close was not idempotent")
	}
}

func TestCapacityIsVisibleAndReleasedOnCompletion(t *testing.T) {
	r := NewRegistry(1)
	first := startEntry(r, Info{User: "one"}, nil)
	second := startEntry(r, Info{User: "two"}, nil)
	if got := r.Stats(); got.Tracked != 1 || got.Untracked != 1 || got.Limit != 1 {
		t.Fatalf("unexpected capacity stats: %+v", got)
	}
	first.Finish()
	startEntry(r, Info{User: "three"}, nil)
	second.Finish()
	second.Finish()
	if got := r.Stats(); got.Tracked != 1 || got.Untracked != 0 {
		t.Fatalf("completion did not release capacity exactly once: %+v", got)
	}
	pending := r.NewEntry(Info{User: "pending"}, nil)
	pending.Finish()
	pending.Start()
	if got := r.Stats(); got.Tracked != 1 || got.Untracked != 0 {
		t.Fatalf("already completed stream was registered: %+v", got)
	}
}

func TestSnapshotMetadataAndCredentials(t *testing.T) {
	r := NewRegistry(10)
	e := startEntry(r, Info{
		Inbound: "node-1", User: "secret-uuid", Network: "udp",
		Source: "[2001:db8::1]:54321", Target: "1.1.1.1:443",
	}, nil)
	e.SetDomain("example.com")
	e.SetOutbound("direct")
	e.Upload.Add(10)
	e.Download.Add(20)
	items := r.Snapshot(Filter{Inbound: "node-1", User: "secret-uuid"})
	if len(items) != 1 {
		t.Fatalf("snapshot has %d entries", len(items))
	}
	got := items[0]
	if got.ID == "" || got.StartedAt.IsZero() || got.Source != "[2001:db8::1]:54321" ||
		got.Target != "1.1.1.1:443" || got.Network != "udp" || got.Domain != "example.com" ||
		got.Outbound != "direct" || got.Upload != 10 || got.Download != 20 {
		t.Fatalf("incorrect snapshot: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "secret-uuid") {
		t.Fatalf("internal user tag leaked into JSON: %s, %v", encoded, err)
	}
	got.Domain = "mutated.example"
	if r.Snapshot(Filter{})[0].Domain != "example.com" {
		t.Fatal("snapshot aliases mutable entry metadata")
	}
	otherProcess := NewRegistry(10)
	other := otherProcess.NewEntry(Info{}, nil)
	if other.info.ID == e.info.ID {
		t.Fatal("IDs reused between registries")
	}
}

func TestConcurrentSnapshotsCloseAndCompletion(t *testing.T) {
	r := NewRegistry(100)
	const count = 50
	entries := make([]*Entry, count)
	var closed atomic.Int64
	for i := range entries {
		entries[i] = startEntry(r, Info{Inbound: "node-1", User: "user"}, func() { closed.Add(1) })
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			for _, e := range entries {
				e.SetDomain("example.com")
				e.SetOutbound("direct")
				e.Upload.Add(1)
				e.Download.Add(2)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			r.Snapshot(Filter{User: "user"})
			r.Stats()
		}
	}()
	go func() {
		defer wg.Done()
		for _, e := range entries {
			r.Close(Filter{User: "user"}, e.info.ID)
			e.Close()
			e.Finish()
		}
	}()
	wg.Wait()
	if got := closed.Load(); got != count {
		t.Fatalf("close callback ran %d times, want %d", got, count)
	}
	if got := r.Stats(); got.Tracked != 0 || got.Untracked != 0 {
		t.Fatalf("completed streams retained: %+v", got)
	}
}
