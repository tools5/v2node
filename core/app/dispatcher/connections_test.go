package dispatcher

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/connections"
	"github.com/wyx2685/v2node/common/counter"
	"github.com/wyx2685/v2node/common/format"
	"github.com/wyx2685/v2node/limiter"
	xdispatcher "github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

func testContext(parent context.Context) context.Context {
	ctx := session.ContextWithInbound(parent, &session.Inbound{
		Tag: "node-1", User: &protocol.MemoryUser{Email: "user-1"},
		Source: net.TCPDestination(net.ParseAddress("2001:db8::1"), 54321),
	})
	return session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
}

func testPipeLink(t *testing.T, d *DefaultDispatcher, parent context.Context) (context.Context, *transport.Link, *transport.Link, *trackedConnection, *counter.TrafficStorage) {
	t.Helper()
	ur, uw := pipe.New()
	dr, dw := pipe.New()
	billing := &counter.TrafficStorage{}
	in := &transport.Link{Reader: dr, Writer: &xdispatcher.SizeStatWriter{
		Writer: uw, Counter: &counter.XrayTrafficCounter{V: &billing.UpCounter},
	}}
	out := &transport.Link{Reader: ur, Writer: &xdispatcher.SizeStatWriter{
		Writer: dw, Counter: &counter.XrayTrafficCounter{V: &billing.DownCounter},
	}}
	ctx, entry := d.trackConnection(testContext(parent), net.TCPDestination(net.ParseAddress("1.1.1.1"), 443), in, out)
	t.Cleanup(func() {
		common.Interrupt(in.Reader)
		common.Interrupt(in.Writer)
		common.Interrupt(out.Reader)
		common.Interrupt(out.Writer)
	})
	return ctx, in, out, entry, billing
}

func bytesBuffer(text string) buf.MultiBuffer {
	b := buf.New()
	b.WriteString(text)
	return buf.MultiBuffer{b}
}

func drain(t *testing.T, reader buf.Reader) {
	t.Helper()
	mb, err := reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTrackingCountersSurviveBillingResetsAndHalfClose(t *testing.T) {
	d := &DefaultDispatcher{}
	ctx, in, out, entry, billing := testPipeLink(t, d, context.Background())
	if _, ok := out.Reader.(*pipe.Reader); !ok {
		t.Fatal("tracking changed the raw reader required by mux/XUDP")
	}
	up := in.Writer.(*xdispatcher.SizeStatWriter)
	down := out.Writer.(*xdispatcher.SizeStatWriter)
	if err := up.WriteMultiBuffer(bytesBuffer("hello")); err != nil {
		t.Fatal(err)
	}
	drain(t, out.Reader)
	if err := down.WriteMultiBuffer(bytesBuffer("world")); err != nil {
		t.Fatal(err)
	}
	drain(t, in.Reader)
	// This is also the Counter.Add route used by Xray's raw splice copy.
	down.Counter.Add(17)
	up.Counter.Set(0)
	billing.DownCounter.Swap(0)
	if err := up.WriteMultiBuffer(bytesBuffer("!")); err != nil {
		t.Fatal(err)
	}
	drain(t, out.Reader)
	entry.SetDomain("example.com")
	entry.SetOutbound("direct")
	got := d.ListConnections(connections.Filter{Inbound: "node-1", User: "user-1"})[0]
	if got.Upload != 6 || got.Download != 22 || billing.UpCounter.Load() != 1 || billing.DownCounter.Load() != 0 {
		t.Fatalf("lifetime and billing counters diverged incorrectly: %+v, billing %d/%d", got, billing.UpCounter.Load(), billing.DownCounter.Load())
	}
	if got.Source != "[2001:db8::1]:54321" || got.Target != "1.1.1.1:443" || got.Domain != "example.com" || got.Outbound != "direct" || got.Network != "tcp" {
		t.Fatalf("incorrect endpoints: %+v", got)
	}
	if err := common.Close(in.Writer); err != nil {
		t.Fatal(err)
	}
	if len(d.ListConnections(connections.Filter{})) != 1 || ctx.Err() != nil {
		t.Fatal("upload half-close prematurely removed/canceled response stream")
	}
	if err := out.Writer.WriteMultiBuffer(bytesBuffer("tail")); err != nil {
		t.Fatal(err)
	}
	drain(t, in.Reader)
	common.Close(out.Writer)
	entry.dispatchDone()
	if len(d.ListConnections(connections.Filter{})) != 0 {
		t.Fatal("completed connection retained")
	}
}

func TestTargetedCloseCancelsOnlyItsStream(t *testing.T) {
	d := &DefaultDispatcher{}
	parent := context.Background()
	firstCtx, first, _, _, _ := testPipeLink(t, d, parent)
	id := d.ListConnections(connections.Filter{})[0].ID
	secondCtx, second, secondOut, _, _ := testPipeLink(t, d, parent)
	if count := d.CloseConnections(connections.Filter{Inbound: "node-1", User: "user-1"}, id); count != 1 {
		t.Fatalf("closed %d streams", count)
	}
	if firstCtx.Err() == nil || secondCtx.Err() != nil || parent.Err() != nil {
		t.Fatal("targeted close did not isolate the selected stream")
	}
	if err := first.Writer.WriteMultiBuffer(bytesBuffer("closed")); err == nil {
		t.Fatal("closed stream still accepted uploads")
	}
	if err := second.Writer.WriteMultiBuffer(bytesBuffer("open")); err != nil {
		t.Fatalf("neighbor stream was closed: %v", err)
	}
	drain(t, secondOut.Reader)
	testPipeLink(t, d, parent)
	if len(d.ListConnections(connections.Filter{})) != 2 {
		t.Fatal("closing a stream blocked reconnects")
	}
}

type testOutboundHandler struct {
	outbound.Handler
	dispatch func(context.Context, *transport.Link)
}

func (*testOutboundHandler) Tag() string { return "test-direct" }
func (h *testOutboundHandler) Dispatch(ctx context.Context, link *transport.Link) {
	if h.dispatch != nil {
		h.dispatch(ctx, link)
	}
}

type testOutboundManager struct {
	outbound.Manager
	handler outbound.Handler
}

func (m *testOutboundManager) GetDefaultHandler() outbound.Handler { return m.handler }
func (*testOutboundManager) GetHandler(string) outbound.Handler    { return nil }

func TestAsyncOutboundRemainsTrackedUntilLinkCloses(t *testing.T) {
	d := &DefaultDispatcher{ohm: &testOutboundManager{handler: &testOutboundHandler{}}}
	ctx, _, out, entry, _ := testPipeLink(t, d, context.Background())
	d.routedDispatch(ctx, out, net.TCPDestination(net.ParseAddress("1.1.1.1"), 443), entry)
	items := d.ListConnections(connections.Filter{})
	if len(items) != 1 || items[0].Outbound != "test-direct" {
		t.Fatalf("async mux-style dispatch was removed too early: %+v", items)
	}
	common.Close(out.Writer)
	if len(d.ListConnections(connections.Filter{})) != 0 {
		t.Fatal("async session completion did not remove record")
	}
}

func TestDownstreamHalfCloseAllowsRemainingUpload(t *testing.T) {
	d := &DefaultDispatcher{}
	ctx, in, out, entry, _ := testPipeLink(t, d, context.Background())
	d.ohm = &testOutboundManager{handler: &testOutboundHandler{dispatch: func(ctx context.Context, link *transport.Link) {
		// Freedom closes its output when the response finishes, while task.Run
		// can still be waiting for the client's remaining upload.
		common.Close(link.Writer)
		if ctx.Err() != nil || len(d.ListConnections(connections.Filter{})) != 1 {
			t.Fatal("response EOF canceled or removed the ongoing upload")
		}
		if err := in.Writer.WriteMultiBuffer(bytesBuffer("remaining upload")); err != nil {
			t.Fatalf("upload after response half-close failed: %v", err)
		}
		drain(t, link.Reader)
	}}}
	d.routedDispatch(ctx, out, net.TCPDestination(net.ParseAddress("1.1.1.1"), 443), entry)
	if len(d.ListConnections(connections.Filter{})) != 0 {
		t.Fatal("completed two-way stream retained")
	}
}

func TestRoutingFailureAndCancellationRemoveConnections(t *testing.T) {
	t.Run("missing route", func(t *testing.T) {
		d := &DefaultDispatcher{ohm: &testOutboundManager{}}
		ctx, _, out, entry, _ := testPipeLink(t, d, context.Background())
		d.routedDispatch(ctx, out, net.TCPDestination(net.ParseAddress("1.1.1.1"), 443), entry)
		if len(d.ListConnections(connections.Filter{})) != 0 {
			t.Fatal("failed route retained")
		}
	})
	t.Run("context canceled", func(t *testing.T) {
		d := &DefaultDispatcher{}
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, _, out, _, _ := testPipeLink(t, d, parent)
		cancel()
		deadline := time.After(time.Second)
		for d.ConnectionTrackingStats().Tracked != 0 {
			select {
			case <-deadline:
				t.Fatal("canceled context retained a live record")
			default:
				time.Sleep(time.Millisecond)
			}
		}
		mb, err := out.Reader.ReadMultiBuffer()
		buf.ReleaseMulti(mb)
		if err == nil {
			t.Fatal("context cancellation did not interrupt the stream")
		}
	})
}

type partialTimeoutReader struct {
	timeout     time.Duration
	interrupted bool
}

func (*partialTimeoutReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return bytesBuffer("tail"), io.EOF
}

func (r *partialTimeoutReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	r.timeout = timeout
	return bytesBuffer("data"), io.EOF
}

func (r *partialTimeoutReader) Interrupt() { r.interrupted = true }

func TestDirectLinkReaderPreservesTimeoutPartialDataAndCounters(t *testing.T) {
	raw := &partialTimeoutReader{}
	var billing, lifetime atomic.Int64
	r := &CounterReader{Reader: raw, Counter: &billing, ConnectionCounter: &lifetime}
	mb, err := r.ReadMultiBufferTimeout(50 * time.Millisecond)
	if !errors.Is(err, io.EOF) || mb.Len() != 4 || raw.timeout != 50*time.Millisecond {
		t.Fatalf("timeout/partial read changed: %v, %d, %v", err, mb.Len(), raw.timeout)
	}
	buf.ReleaseMulti(mb)
	billing.Store(0)
	mb, err = r.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if !errors.Is(err, io.EOF) || billing.Load() != 4 || lifetime.Load() != 8 {
		t.Fatalf("partial reads not counted independently: %v, %d, %d", err, billing.Load(), lifetime.Load())
	}
	r.Interrupt()
	if !raw.interrupted {
		t.Fatal("reader interruption was swallowed")
	}
}

func TestDispatchLinkTracksSniffedDomainAndCountsCachedUploadOnce(t *testing.T) {
	const nodeTag = "direct-node"
	const uuid = "test-user-uuid"
	const request = "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	limiter.Init()
	limiter.AddLimiter("vless", nodeTag, []panel.UserInfo{{Id: 1, Uuid: uuid}}, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(nodeTag) })
	d := &DefaultDispatcher{}
	d.ohm = &testOutboundManager{handler: &testOutboundHandler{dispatch: func(ctx context.Context, link *transport.Link) {
		items := d.ListConnections(connections.Filter{Inbound: nodeTag})
		if len(items) != 1 || items[0].Domain != "example.com" || items[0].Target != "1.1.1.1:443" || items[0].Outbound != "test-direct" {
			t.Fatalf("direct link routing metadata missing: %+v", items)
		}
		mb, err := link.Reader.ReadMultiBuffer()
		length := mb.Len()
		buf.ReleaseMulti(mb)
		if err != nil || length != int32(len(request)) {
			t.Fatalf("sniffed payload changed: length %d, err %v", length, err)
		}
		if err := link.Writer.WriteMultiBuffer(bytesBuffer("response")); err != nil {
			t.Fatal(err)
		}
		items = d.ListConnections(connections.Filter{Inbound: nodeTag})
		if len(items) != 1 || items[0].Upload != int64(len(request)) || items[0].Download != 8 {
			t.Fatalf("direct link byte counts incorrect: %+v", items)
		}
		common.Close(link.Writer)
	}}}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{
		Tag:    nodeTag,
		User:   &protocol.MemoryUser{Email: format.UserTag(nodeTag, uuid)},
		Source: net.TCPDestination(net.ParseAddress("192.0.2.1"), 50000),
	})
	ctx = session.ContextWithContent(ctx, &session.Content{SniffingRequest: session.SniffingRequest{
		Enabled: true, RouteOnly: true, OverrideDestinationForProtocol: []string{"http"},
	}})
	ctx = context.WithValue(ctx, xcore.XrayKey(1), new(xcore.Instance))
	err := d.DispatchLink(ctx, net.TCPDestination(net.ParseAddress("1.1.1.1"), 443), &transport.Link{
		Reader: buf.NewReader(strings.NewReader(request)), Writer: buf.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ListConnections(connections.Filter{})) != 0 {
		t.Fatal("direct link completion retained its record")
	}
}
