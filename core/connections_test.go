package core

import (
	"testing"

	"github.com/wyx2685/v2node/common/connectioncontrol"
	"github.com/wyx2685/v2node/core/app/dispatcher"
)

func TestConnectionCommandsRequireUserMembershipOnThisNode(t *testing.T) {
	vc := &V2Core{dispatcher: &dispatcher.DefaultDispatcher{}, users: &UserMap{uidMap: map[string]int{"node-1|private-uuid": 6, "node-2|other-uuid": 7}}}
	for _, action := range []string{"snapshot", "close"} {
		result := vc.ExecuteConnectionCommand("node-1", "anytls", connectioncontrol.Command{ID: "test", Action: action, UserID: 7})
		if result.Error != "user_not_on_node" || result.Closed != 0 {
			t.Fatalf("cross-node command accepted: %+v", result)
		}
		result = vc.ExecuteConnectionCommand("node-1", "anytls", connectioncontrol.Command{ID: "test", Action: action, UserID: 6})
		if result.Error != "" || result.Connections == nil {
			t.Fatalf("valid empty snapshot: %+v", result)
		}
	}
}

func TestConnectionCommandsRejectInvalidOrStoppedCore(t *testing.T) {
	vc := &V2Core{}
	result := vc.ExecuteConnectionCommand("node", "anytls", connectioncontrol.Command{Action: "close", UserID: 0})
	if result.Error != "invalid_command" {
		t.Fatalf("got %+v", result)
	}
	result = vc.ExecuteConnectionCommand("node", "anytls", connectioncontrol.Command{Action: "close", UserID: 1})
	if result.Error != "node_not_running" {
		t.Fatalf("got %+v", result)
	}
}
