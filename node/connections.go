package node

import (
	"context"

	log "github.com/sirupsen/logrus"
	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/connectioncontrol"
)

func (c *Controller) startConnectionMonitor() {
	if c.conf.RealtimeConnections != nil && !*c.conf.RealtimeConnections {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.connectionMonitorCancel = cancel
	c.connectionMonitorDone = make(chan struct{})
	worker := connectioncontrol.NewWorker(panel.NewConnectionTransport(c.apiClient), func(command connectioncontrol.Command) connectioncontrol.Result {
		return c.server.ExecuteConnectionCommand(c.tag, c.info.Type, command)
	})
	go func() {
		defer close(c.connectionMonitorDone)
		worker.Run(ctx, func(err error) {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).Warn("Realtime connection monitor request failed; retrying")
		})
	}()
}

func (c *Controller) stopConnectionMonitor() {
	if c.connectionMonitorCancel != nil {
		c.connectionMonitorCancel()
		<-c.connectionMonitorDone
		c.connectionMonitorCancel = nil
	}
}
