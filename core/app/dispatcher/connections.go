package dispatcher

import (
	"context"
	stdnet "net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wyx2685/v2node/common/connections"
	"github.com/wyx2685/v2node/common/counter"
	xdispatcher "github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport"
)

func (d *DefaultDispatcher) connectionRegistry() *connections.Registry {
	d.connectionsOnce.Do(func() {
		d.connections = connections.NewRegistry(connections.DefaultLimit)
	})
	return d.connections
}

func (d *DefaultDispatcher) ListConnections(filter connections.Filter) []connections.Info {
	return d.connectionRegistry().Snapshot(filter)
}

func (d *DefaultDispatcher) CloseConnections(filter connections.Filter, id string) int {
	return d.connectionRegistry().Close(filter, id)
}

func (d *DefaultDispatcher) ConnectionTrackingStats() connections.Stats {
	return d.connectionRegistry().Stats()
}

func connectionEndpoint(destination net.Destination) string {
	if destination.Address == nil {
		return ""
	}
	address := destination.Address.String()
	if destination.Address.Family().IsIP() {
		address = destination.Address.IP().String()
	}
	return stdnet.JoinHostPort(address, destination.Port.String())
}

// connectionCounter leaves the outer Xray SizeStatWriter recognizable to its
// splice path. Billing resets only affect the aggregate; lifetime counts remain.
// Xray reports bytes copied by kernel splice only after that copy returns.
type connectionCounter struct {
	aggregate stats.Counter
	lifetime  *atomic.Int64
}

func (c *connectionCounter) Value() int64          { return c.aggregate.Value() }
func (c *connectionCounter) Set(value int64) int64 { return c.aggregate.Set(value) }
func (c *connectionCounter) Add(value int64) int64 {
	c.lifetime.Add(value)
	return c.aggregate.Add(value)
}

func addConnectionCounter(writer buf.Writer, lifetime *atomic.Int64) buf.Writer {
	if statWriter, ok := writer.(*xdispatcher.SizeStatWriter); ok {
		statWriter.Counter = &connectionCounter{aggregate: statWriter.Counter, lifetime: lifetime}
		return statWriter
	}
	return &xdispatcher.SizeStatWriter{
		Counter: &counter.XrayTrafficCounter{V: lifetime},
		Writer:  writer,
	}
}

type trackedConnection struct {
	*connections.Entry
	cancel           context.CancelFunc
	mu               sync.Mutex
	ended            bool
	dispatchReturned bool
	downlinkClosed   bool
	stop             func() bool
}

func (t *trackedConnection) finish() {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended = true
	if t.stop != nil {
		t.stop()
	}
	t.mu.Unlock()
	t.Entry.Finish()
	t.cancel()
}

func (t *trackedConnection) watch(ctx context.Context) {
	stop := context.AfterFunc(ctx, t.Entry.Close)
	t.mu.Lock()
	if t.ended {
		stop()
	} else {
		t.stop = stop
	}
	t.mu.Unlock()
}

func (t *trackedConnection) dispatchDone() {
	t.mu.Lock()
	t.dispatchReturned = true
	finished := t.downlinkClosed
	t.mu.Unlock()
	if finished {
		t.finish()
	}
}

func (t *trackedConnection) closeDownlink() {
	t.mu.Lock()
	t.downlinkClosed = true
	finished := t.dispatchReturned
	t.mu.Unlock()
	if finished {
		t.finish()
	}
}

// A normal downstream close can precede upload completion. Wait for Dispatch to
// return as well; asynchronous mux does the reverse (returns before closing).
type trackedWriter struct {
	writer  buf.Writer
	tracker *trackedConnection
}

func (w *trackedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	err := w.writer.WriteMultiBuffer(mb)
	if err != nil {
		w.tracker.finish()
	}
	return err
}

func (w *trackedWriter) Close() error {
	defer w.tracker.closeDownlink()
	return common.Close(w.writer)
}

func (w *trackedWriter) Interrupt() {
	defer w.tracker.finish()
	common.Interrupt(w.writer)
}

func (d *DefaultDispatcher) trackConnection(ctx context.Context, destination net.Destination, inbound, outbound *transport.Link) (context.Context, *trackedConnection) {
	in := session.InboundFromContext(ctx)
	if in == nil || in.User == nil || in.User.Email == "" {
		return ctx, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	tracker := &trackedConnection{cancel: cancel}
	// Capture endpoints before the handler can mutate its link (e.g. UDP endpoint
	// wrappers). Cancellation belongs to this stream, never the parent mux.
	uploadReader, downloadWriter := outbound.Reader, outbound.Writer
	var inboundReader buf.Reader
	var inboundWriter buf.Writer
	if inbound != nil {
		inboundReader, inboundWriter = inbound.Reader, inbound.Writer
	}
	info := connections.Info{
		Inbound: in.Tag,
		User:    in.User.Email,
		Network: strings.ToLower(destination.Network.String()),
		Source:  connectionEndpoint(in.Source),
		Target:  connectionEndpoint(destination),
	}
	if destination.Address.Family().IsDomain() {
		info.Domain = destination.Address.Domain()
	}
	tracker.Entry = d.connectionRegistry().NewEntry(info, func() {
		tracker.finish()
		common.Interrupt(uploadReader)
		common.Interrupt(downloadWriter)
		common.Interrupt(inboundReader)
		common.Interrupt(inboundWriter)
	})
	if inbound != nil {
		inbound.Writer = addConnectionCounter(inbound.Writer, &tracker.Upload)
	} else if reader, ok := outbound.Reader.(*CounterReader); ok {
		reader.ConnectionCounter = &tracker.Upload
	}
	outbound.Writer = addConnectionCounter(outbound.Writer, &tracker.Download)
	statWriter := outbound.Writer.(*xdispatcher.SizeStatWriter)
	statWriter.Writer = &trackedWriter{writer: statWriter.Writer, tracker: tracker}
	tracker.Entry.Start()
	tracker.watch(ctx)
	return ctx, tracker
}
