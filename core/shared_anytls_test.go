package core

import (
	"encoding/json"
	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/format"
	"github.com/xtls/xray-core/proxy/anytls"
	"testing"
)

func TestSharedAnyTLSRegistersDistinctPasswordsForOneAccount(t *testing.T) {
	user := panel.UserInfo{Id: 7, Uuid: "11111111-1111-4111-8111-111111111111", SharedChildIDs: []int{26, 27}}
	users := buildAnyTLSUsers("parent-25", []panel.UserInfo{user})
	if len(users) != 3 {
		t.Fatalf("wrong auth entries: %d", len(users))
	}
	for i, u := range users {
		instance, err := u.Account.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		password := instance.(*anytls.Account).Password
		if i == 0 {
			if password != user.Uuid || u.Email != format.UserTag("parent-25", user.Uuid) {
				t.Fatal("legacy authentication changed")
			}
		} else {
			id := user.SharedChildIDs[i-1]
			if password != format.SharedChildPassword(user.Uuid, id) || u.Email != format.SharedChildUserTag("parent-25", user.Uuid, id) {
				t.Fatal("child credential not bound to child identity")
			}
		}
	}
}

func TestSharedAnyTLSOutboundsWithSameNameRemainIndependent(t *testing.T) {
	parentValue := `{"tag":"same","protocol":"freedom"}`
	childValue := `{"tag":"same","protocol":"blackhole"}`
	root := &panel.NodeInfo{Id: 25, Type: "anytls", Tag: "parent-25", Common: &panel.CommonNode{
		Routes:         []panel.Route{{Id: 1, Action: "default_out", ActionValue: &parentValue}},
		SharedChildren: []panel.SharedAnyTLSChild{{Id: 26, Routes: []panel.Route{{Id: 2, Action: "default_out", ActionValue: &childValue}}}},
	}}
	infos, err := sharedAnyTLSRouteInfos([]*panel.NodeInfo{root})
	if err != nil {
		t.Fatal(err)
	}
	var child map[string]interface{}
	if err := json.Unmarshal([]byte(*infos[1].Common.Routes[0].ActionValue), &child); err != nil {
		t.Fatal(err)
	}
	childTag := format.SharedChildTag(root.Tag, 26) + "|out:same"
	if child["tag"] != childTag || *root.Common.Routes[0].ActionValue != parentValue || *root.Common.SharedChildren[0].Routes[0].ActionValue != childValue {
		t.Fatal("scoping mutated the original routes or reused the parent's outbound")
	}
	_, outbounds, _, err := GetCustomConfig([]*panel.NodeInfo{root})
	if err != nil || !hasOutboundWithTag(outbounds, "same") || !hasOutboundWithTag(outbounds, childTag) {
		t.Fatalf("separate outbounds not built: %v", err)
	}
	root.Common.SharedChildren[0].Routes[0].Action = "dns"
	if _, err := sharedAnyTLSRouteInfos([]*panel.NodeInfo{root}); err == nil {
		t.Fatal("child global DNS configuration accepted")
	}
}

func TestSharedAnyTLSVirtualRoutesPreservePhysicalListener(t *testing.T) {
	root := &panel.NodeInfo{Id: 25, Type: "anytls", Tag: "parent-25", Common: &panel.CommonNode{ServerPort: 50000,
		SharedChildren: []panel.SharedAnyTLSChild{{Id: 26, Routes: []panel.Route{{Id: 3, Action: "default_out"}}}}}}
	infos, err := sharedAnyTLSRouteInfos([]*panel.NodeInfo{root})
	if err != nil || len(infos) != 2 {
		t.Fatalf("expansion failed: %v", err)
	}
	if infos[0] != root || infos[1].Tag != format.SharedChildTag(root.Tag, 26) || len(infos[1].Common.Routes) != 1 {
		t.Fatal("wrong route identity")
	}
	if len(root.Common.Routes) != 0 || root.Common.ServerPort != 50000 || len(root.Common.SharedChildren) != 1 {
		t.Fatal("parent config mutated")
	}
	params := &AddUsersParams{Tag: root.Tag, NodeInfo: root, Users: []panel.UserInfo{{Uuid: "u", SharedChildIDs: []int{26}}}}
	if err := validateSharedChildUsers(params); err != nil {
		t.Fatal(err)
	}
	params.Users[0].SharedChildIDs = []int{999}
	if validateSharedChildUsers(params) == nil {
		t.Fatal("unknown child authorization accepted")
	}
	params.Users[0].SharedChildIDs = []int{26, 26}
	if validateSharedChildUsers(params) == nil {
		t.Fatal("duplicate auth identity accepted")
	}
}
