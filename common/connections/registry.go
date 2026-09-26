// Package connections keeps bounded, in-memory information about active proxy
// streams. It stores endpoints and byte counts, never payloads or request URLs.
package connections

import (
	"crypto/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultLimit = 10000

// Info is a snapshot of one TCP stream or logical UDP association. User is an
// internal lookup key, which may contain credentials and must not be serialized.
type Info struct {
	ID        string    `json:"id"`
	Inbound   string    `json:"inbound"`
	User      string    `json:"-"`
	Network   string    `json:"network"`
	Source    string    `json:"source"`
	Target    string    `json:"target"`
	Domain    string    `json:"domain,omitempty"`
	Outbound  string    `json:"outbound"`
	StartedAt time.Time `json:"started_at"`
	Upload    int64     `json:"upload"`
	Download  int64     `json:"download"`
}

// Filter matches both fields when supplied; an empty field is a wildcard.
type Filter struct {
	Inbound string
	User    string
}

func (f Filter) matches(inbound, user string) bool {
	return (f.Inbound == "" || f.Inbound == inbound) &&
		(f.User == "" || f.User == user)
}

// Stats describes registry capacity. Untracked counts active streams omitted
// because the limit was reached; callers must disclose incomplete snapshots.
// These capacity statistics are global, even when a snapshot uses a filter.
type Stats struct {
	Tracked   int   `json:"tracked"`
	Untracked int64 `json:"untracked"`
	Limit     int   `json:"limit"`
}

// Registry retains only live entries, up to its limit. Reaching the limit never
// rejects proxy traffic. Close functions are always called without its lock.
type Registry struct {
	mu        sync.RWMutex
	entries   map[string]*Entry
	limit     int
	prefix    string
	next      atomic.Uint64
	untracked int64
}

func NewRegistry(limit int) *Registry {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Registry{
		entries: make(map[string]*Entry),
		limit:   limit,
		prefix:  rand.Text(),
	}
}

// Entry owns lifetime byte counters independently of resettable billing stats.
// Its immutable scope (user/inbound) is set before it can be published.
type Entry struct {
	Upload   atomic.Int64
	Download atomic.Int64

	registry *Registry
	mu       sync.RWMutex
	info     Info
	close    func()
	// Protected by registry.mu, including for entries omitted at capacity.
	started   bool
	finished  bool
	untracked bool
}

// NewEntry prepares a stream without publishing it. Call Start only after its
// transport wrappers and close callback are ready for concurrent close requests.
func (r *Registry) NewEntry(info Info, closeStream func()) *Entry {
	info.ID = r.prefix + "-" + strconv.FormatUint(r.next.Add(1), 36)
	info.StartedAt = time.Now().UTC()
	info.Upload, info.Download = 0, 0
	return &Entry{registry: r, info: info, close: closeStream}
}

// Start publishes the entry if there is room. A finished entry cannot restart.
func (e *Entry) Start() {
	r := e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.started || e.finished {
		return
	}
	e.started = true
	if len(r.entries) >= r.limit {
		e.untracked = true
		r.untracked++
		return
	}
	r.entries[e.info.ID] = e
}

func (e *Entry) finishLocked() bool {
	if e.finished {
		return false
	}
	e.finished = true
	if e.started {
		if e.untracked {
			e.registry.untracked--
		} else {
			delete(e.registry.entries, e.info.ID)
		}
	}
	return true
}

// Finish forgets a completed stream without aborting its transport.
func (e *Entry) Finish() {
	e.registry.mu.Lock()
	e.finishLocked()
	e.registry.mu.Unlock()
}

// Close aborts a live stream at most once. Normal completion wins a race with a
// later close request, so stale references never close a completed transport.
func (e *Entry) Close() {
	e.registry.mu.Lock()
	closed := e.finishLocked()
	e.registry.mu.Unlock()
	if closed && e.close != nil {
		e.close()
	}
}

func (e *Entry) SetDomain(domain string) {
	e.mu.Lock()
	e.info.Domain = domain
	e.mu.Unlock()
}

func (e *Entry) SetOutbound(outbound string) {
	e.mu.Lock()
	e.info.Outbound = outbound
	e.mu.Unlock()
}

func (e *Entry) snapshot() Info {
	e.mu.RLock()
	info := e.info
	e.mu.RUnlock()
	info.Upload = e.Upload.Load()
	info.Download = e.Download.Load()
	return info
}

// Snapshot returns at most the configured capacity, newest streams first.
func (r *Registry) Snapshot(filter Filter) []Info {
	r.mu.RLock()
	result := make([]Info, 0)
	for _, entry := range r.entries {
		if filter.matches(entry.info.Inbound, entry.info.User) {
			result = append(result, entry.snapshot())
		}
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartedAt.Equal(result[j].StartedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartedAt.After(result[j].StartedAt)
	})
	return result
}

// Close matches an optional ID and scope. An empty ID closes the matching
// currently tracked streams, while allowing new connections immediately.
// Entries omitted at capacity cannot be selected; inspect Stats.Untracked.
func (r *Registry) Close(filter Filter, id string) int {
	r.mu.Lock()
	selected := make([]*Entry, 0)
	if id != "" {
		if entry, ok := r.entries[id]; ok && filter.matches(entry.info.Inbound, entry.info.User) && entry.finishLocked() {
			selected = append(selected, entry)
		}
	} else {
		for _, entry := range r.entries {
			if filter.matches(entry.info.Inbound, entry.info.User) && entry.finishLocked() {
				selected = append(selected, entry)
			}
		}
	}
	r.mu.Unlock()
	for _, entry := range selected {
		if entry.close != nil {
			entry.close()
		}
	}
	return len(selected)
}

func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Stats{Tracked: len(r.entries), Untracked: r.untracked, Limit: r.limit}
}
