package dispatcher

import (
	"context"
	"strings"

	"github.com/wyx2685/v2node/common/format"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	routing_session "github.com/xtls/xray-core/features/routing/session"
)

type sharedChildContext struct {
	routing.Context
	tag string
}

func (c sharedChildContext) GetInboundTag() string { return c.tag }

// Only the router sees a virtual inbound. Limits, traffic, and connection
// control continue to belong to the real parent listener and original UID.
func sharedAnyTLSRoutingContext(ctx context.Context) routing.Context {
	original := routing_session.AsRoutingContext(ctx)
	in := session.InboundFromContext(ctx)
	if in == nil || in.User == nil {
		return original
	}
	canonical, id := format.SplitSharedChildUser(in.User.Email)
	if id == 0 || !strings.HasPrefix(canonical, in.Tag+"|") {
		return original
	}
	return sharedChildContext{Context: original, tag: format.SharedChildTag(in.Tag, id)}
}
