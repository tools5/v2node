package limiter

import (
	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/format"
	"testing"
)

func TestSharedChildSharesSpeedBucketAndDeviceSet(t *testing.T) {
	Init()
	tag, uuid := "parent-25", "11111111-1111-4111-8111-111111111111"
	user := panel.UserInfo{Id: 1, Uuid: uuid, SpeedLimit: 1, DeviceLimit: 1, SharedChildIDs: []int{26}}
	l := AddLimiter("anytls", tag, []panel.UserInfo{user}, map[int]int{1: 0})
	t.Cleanup(func() { DeleteLimiter(tag) })
	main, reject := l.CheckLimit(format.UserTag(tag, uuid), "192.0.2.1", true)
	if reject || main == nil {
		t.Fatal("parent rejected")
	}
	child, reject := l.CheckLimit(format.SharedChildUserTag(tag, uuid, 26), "192.0.2.1", true)
	if reject || child != main {
		t.Fatal("child received an independent speed bucket")
	}
	l.AliveList[1] = 1
	if _, reject = l.CheckLimit(format.SharedChildUserTag(tag, uuid, 26), "192.0.2.2", true); !reject {
		t.Fatal("child credential bypassed the original device limit")
	}
	online, err := l.GetOnlineDevice()
	if err != nil || len(*online) != 1 || (*online)[0].UID != 1 || (*online)[0].IP != "192.0.2.1" {
		t.Fatalf("duplicate or incorrectly attributed online devices: %+v %v", online, err)
	}
	if _, reject = l.CheckLimit(format.SharedChildUserTag(tag, uuid, 999), "192.0.2.1", true); !reject {
		t.Fatal("unknown child session accepted")
	}
	revoked := user
	revoked.SharedChildIDs = nil
	l.UpdateUser(tag, []panel.UserInfo{revoked}, []panel.UserInfo{user}, nil)
	if _, reject = l.CheckLimit(format.SharedChildUserTag(tag, uuid, 26), "192.0.2.1", true); !reject {
		t.Fatal("revoked child reused an authenticated session")
	}
	if _, reject = l.CheckLimit(format.UserTag(tag, uuid), "192.0.2.1", true); reject {
		t.Fatal("child revocation rejected parent")
	}
}
