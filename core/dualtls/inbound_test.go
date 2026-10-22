package dualtls

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/wyx2685/v2node/common/format"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/uuid"
	"testing"
)

func TestCredentialIdentityAndClassification(t *testing.T) {
	password := "11111111-1111-4111-8111-111111111111"
	id := CredentialUUID(password)
	if id.String() != password {
		t.Fatal("existing UUID changed")
	}
	shared := CredentialUUID(format.SharedChildPassword(password, 26))
	if shared.String() != "828d7268-10ff-5166-93d9-2f5ce715f98c" {
		t.Fatal("Go/PHP shared child credential vector differs")
	}
	child := CredentialUUID("06bf6f1ed036ed1517485121636a042ef3ad9d4c95d1da4f0638e763aee0fdd72")
	if child == id || child != CredentialUUID("06bf6f1ed036ed1517485121636a042ef3ad9d4c95d1da4f0638e763aee0fdd72") {
		t.Fatal("unstable child identity")
	}
	h := &Handler{ids: map[uuid.UUID]string{id: "u"}}
	if !h.isVLESS(vlessPrefix(id)) {
		t.Fatal("authorized VLESS not recognized")
	}
	if h.isVLESS(vlessPrefix(child)) {
		t.Fatal("unknown VLESS recognized")
	}
	// AnyTLS really can start with VLESS version 0. Exercise an actual hash.
	for n := 0; ; n++ {
		hash := sha256.Sum256([]byte(fmt.Sprintf("anytls-password-%d", n)))
		if hash[0] != 0 {
			continue
		}
		var prefix [17]byte
		copy(prefix[:], hash[:17])
		if h.isVLESS(prefix) {
			t.Fatal("AnyTLS first byte misclassified")
		}
		break
	}
	ctx := session.ContextWithContent(context.Background(), &session.Content{Attributes: map[string]string{"anytls": "notls", "keep": "yes"}})
	clean := session.ContentFromContext(tlsContext(ctx))
	if clean.Attribute("anytls") != "" || clean.Attribute("keep") != "yes" {
		t.Fatal("routing attributes lost")
	}
	if session.ContentFromContext(ctx).Attribute("anytls") != "notls" {
		t.Fatal("shared content mutated")
	}
}
