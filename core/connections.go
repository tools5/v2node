package core

import (
	"sort"
	"strings"

	"github.com/wyx2685/v2node/common/connectioncontrol"
	"github.com/wyx2685/v2node/common/connections"
)

// ExecuteConnectionCommand resolves a numeric panel UID only within this node.
// Neither another node's streams nor the UUID-bearing internal user tag escape
// this boundary.
func (vc *V2Core) ExecuteConnectionCommand(tag, inbound string, command connectioncontrol.Command) connectioncontrol.Result {
	result := connectioncontrol.Result{
		RequestID:   command.ID,
		UserID:      command.UserID,
		Connections: []connectioncontrol.Connection{},
	}
	if command.UserID <= 0 || (command.Action != "snapshot" && command.Action != "close") {
		result.Error = "invalid_command"
		return result
	}
	vc.access.Lock()
	defer vc.access.Unlock()
	if vc.dispatcher == nil {
		result.Error = "node_not_running"
		return result
	}
	vc.users.mapLock.RLock()
	var users []string
	for user, uid := range vc.users.uidMap {
		if uid == command.UserID && strings.HasPrefix(user, tag+"|") {
			users = append(users, user)
		}
	}
	vc.users.mapLock.RUnlock()
	if len(users) == 0 {
		result.Error = "user_not_on_node"
		return result
	}
	var live []connections.Info
	for _, user := range users {
		filter := connections.Filter{Inbound: tag, User: user}
		if command.Action == "close" {
			result.Closed += vc.dispatcher.CloseConnections(filter, command.ConnectionID)
		}
		live = append(live, vc.dispatcher.ListConnections(filter)...)
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].StartedAt.Equal(live[j].StartedAt) {
			return live[i].ID < live[j].ID
		}
		return live[i].StartedAt.After(live[j].StartedAt)
	})
	result.Total = len(live)
	result.Truncated = vc.dispatcher.ConnectionTrackingStats().Untracked > 0 || len(live) > connectioncontrol.MaxConnections
	if len(live) > connectioncontrol.MaxConnections {
		live = live[:connectioncontrol.MaxConnections]
	}
	for _, connection := range live {
		result.Connections = append(result.Connections, connectioncontrol.Connection{
			ID:          connection.ID,
			Inbound:     boundedConnectionText(inbound, 512),
			Network:     boundedConnectionText(connection.Network, 32),
			Source:      boundedConnectionText(connection.Source, 512),
			Destination: boundedConnectionText(connection.Target, 512),
			Domain:      boundedConnectionText(connection.Domain, 255),
			Outbound:    boundedConnectionText(connection.Outbound, 512),
			StartedAt:   connection.StartedAt.Unix(),
			Upload:      connection.Upload,
			Download:    connection.Download,
		})
	}
	return result
}

func boundedConnectionText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	characters := []rune(text)
	if len(characters) > limit {
		return string(characters[:limit])
	}
	return text
}
