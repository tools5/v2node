package node

import (
	panel "github.com/wyx2685/v2node/api/v2board"
	"testing"
)

func TestChangingChildAuthorizationReplacesOldCredentials(t *testing.T) {
	old := []panel.UserInfo{{Id: 1, Uuid: "u", SharedChildIDs: []int{26}}}
	next := []panel.UserInfo{{Id: 1, Uuid: "u", SharedChildIDs: []int{27}}}
	deleted, added, modified := compareUserList(old, next)
	if len(deleted) != 1 || len(added) != 1 || len(modified) != 0 || deleted[0].SharedChildIDs[0] != 26 || added[0].SharedChildIDs[0] != 27 {
		t.Fatal("stale child credentials were retained")
	}
}
