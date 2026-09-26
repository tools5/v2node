// Package connectioncontrol implements the panel's on-demand connection jobs.
package connectioncontrol

import (
	"context"
	"errors"
	"regexp"
	"time"
)

const (
	Version        = 1
	MaxCommands    = 16
	MaxConnections = 1000
	maxCachedJobs  = 256
)

var (
	ErrUnsupported = errors.New("panel does not support realtime connections")
	ErrExpired     = errors.New("connection request has expired")
	ErrInvalid     = errors.New("invalid connection response")
	jobIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type Command struct {
	ID           string `json:"id"`
	Action       string `json:"action"`
	UserID       int    `json:"user_id"`
	ConnectionID string `json:"connection_id,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

type Batch struct {
	Version   int       `json:"version"`
	Commands  []Command `json:"commands"`
	PollAfter int       `json:"poll_after"`
}

// Connection intentionally excludes the internal user tag and credentials.
type Connection struct {
	ID          string `json:"id"`
	Inbound     string `json:"inbound"`
	Network     string `json:"network"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Domain      string `json:"domain"`
	Outbound    string `json:"outbound"`
	StartedAt   int64  `json:"started_at"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

type Result struct {
	RequestID   string       `json:"request_id"`
	UserID      int          `json:"user_id"`
	Connections []Connection `json:"connections"`
	Total       int          `json:"total"`
	Truncated   bool         `json:"truncated"`
	Closed      int          `json:"closed"`
	Error       string       `json:"error,omitempty"`
}

type Transport interface {
	PollConnectionCommands(context.Context) (Batch, error)
	SubmitConnectionResult(context.Context, Result) error
}

type cachedResult struct {
	command Command
	result  Result
	expires time.Time
}

// Worker is used by one controller goroutine. Saving the result before posting
// it makes a retried close job safe even if its first acknowledgement was lost.
type Worker struct {
	transport Transport
	execute   func(Command) Result
	now       func() time.Time
	completed map[string]cachedResult
}

func NewWorker(transport Transport, execute func(Command) Result) *Worker {
	return &Worker{
		transport: transport,
		execute:   execute,
		now:       time.Now,
		completed: make(map[string]cachedResult),
	}
}

// Step performs one bounded poll and processes jobs serially. Expired and
// malformed jobs never invoke the connection closer.
func (w *Worker) Step(ctx context.Context) (time.Duration, error) {
	now := w.now()
	for id, entry := range w.completed {
		if !now.Before(entry.expires) {
			delete(w.completed, id)
		}
	}
	batch, err := w.transport.PollConnectionCommands(ctx)
	if err != nil {
		return 0, err
	}
	if batch.Version != Version {
		return 0, ErrUnsupported
	}
	if len(batch.Commands) > MaxCommands {
		return 0, ErrInvalid
	}
	for _, command := range batch.Commands {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !jobIDPattern.MatchString(command.ID) || command.UserID <= 0 {
			return 0, ErrInvalid
		}
		result := Result{RequestID: command.ID, UserID: command.UserID, Connections: []Connection{}}
		if cached, ok := w.completed[command.ID]; ok {
			if cached.command != command {
				return 0, ErrInvalid
			}
			result = cached.result
		} else {
			// Never evict a live idempotency record to run another close job.
			if len(w.completed) >= maxCachedJobs {
				return 5 * time.Second, errors.New("connection job cache is full")
			}
			switch {
			case command.ExpiresAt <= w.now().Unix():
				result.Error = "request_expired"
			case command.ExpiresAt > w.now().Add(2*time.Minute).Unix():
				result.Error = "invalid_expiry"
			case command.Action != "snapshot" && command.Action != "close":
				result.Error = "unsupported_action"
			case command.ConnectionID != "" && !jobIDPattern.MatchString(command.ConnectionID):
				result.Error = "invalid_connection_id"
			default:
				result = w.execute(command)
				result.RequestID = command.ID
				result.UserID = command.UserID
				if result.Connections == nil {
					result.Connections = []Connection{}
				}
			}
			w.completed[command.ID] = cachedResult{command, result, w.now().Add(2 * time.Minute)}
		}
		if err := w.transport.SubmitConnectionResult(ctx, result); err != nil {
			if !errors.Is(err, ErrExpired) {
				return 0, err
			}
		} else if command.Action == "snapshot" {
			// A repeated read is harmless. Do not retain browsing destinations
			// from successfully acknowledged snapshots for the replay window.
			delete(w.completed, command.ID)
		} else {
			// The panel has acknowledged this close. Keep its id/count to
			// prevent a replay, but release the potentially large snapshot.
			cached := w.completed[command.ID]
			cached.result.Connections = []Connection{}
			w.completed[command.ID] = cached
		}
	}
	return time.Duration(max(1, min(batch.PollAfter, 10))) * time.Second, nil
}

// Run isolates the optional monitoring channel from proxy/controller reloads.
// Old panels are retried infrequently and transport failures never stop traffic.
func (w *Worker) Run(ctx context.Context, reportError func(error)) {
	delay := time.Duration(0)
	var lastError time.Time
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		next, err := w.Step(requestCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrUnsupported):
			delay = 5 * time.Minute
		case err != nil:
			delay = 10 * time.Second
			if reportError != nil && (lastError.IsZero() || time.Since(lastError) >= time.Minute) {
				reportError(err)
				lastError = time.Now()
			}
		default:
			delay = next
		}
	}
}
