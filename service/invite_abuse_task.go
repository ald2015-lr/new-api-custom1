package service

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

// inviteAbuseBanTickInterval is how often the master node checks whether a
// sweep is due and continues a pass that ran out of time; the full sweep
// itself runs every model.InviteAbuseSweepInterval.
const inviteAbuseBanTickInterval = 1 * time.Minute

var inviteAbuseBanOnce sync.Once

// StartInviteAbuseBanTask runs the invite abuse rule's task on the master
// node with a one-minute tick (see model.RunInviteAbuseSweepTask).
func StartInviteAbuseBanTask() {
	inviteAbuseBanOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("invite abuse ban task started: tick=%s", inviteAbuseBanTickInterval))
			ticker := time.NewTicker(inviteAbuseBanTickInterval)
			defer ticker.Stop()
			model.RunInviteAbuseSweepTask(ticker.C, nil)
		})
	})
}
