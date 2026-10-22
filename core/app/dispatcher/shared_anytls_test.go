package dispatcher

import (
	"context"
	"github.com/wyx2685/v2node/common/format"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"testing"
)

func TestSharedChildChangesOnlyRoutingIdentity(t *testing.T) {
	tag, uuid := "parent-25", "original-uuid"
	for _, child := range []int{0, 26, 27} {
		email, want := format.UserTag(tag, uuid), tag
		if child > 0 {
			email, want = format.SharedChildUserTag(tag, uuid, child), format.SharedChildTag(tag, child)
		}
		in := &session.Inbound{Tag: tag, User: &protocol.MemoryUser{Email: email}}
		ctx := session.ContextWithInbound(context.Background(), in)
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
		if got := sharedAnyTLSRoutingContext(ctx).GetInboundTag(); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
		if in.Tag != tag || in.User.Email != email {
			t.Fatal("routing mutated the real listener/account context")
		}
	}
	in := &session.Inbound{Tag: tag, User: &protocol.MemoryUser{Email: format.SharedChildUserTag("another-parent", uuid, 26)}}
	ctx := session.ContextWithInbound(context.Background(), in)
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
	if sharedAnyTLSRoutingContext(ctx).GetInboundTag() != tag {
		t.Fatal("cross-listener alias accepted")
	}
}
