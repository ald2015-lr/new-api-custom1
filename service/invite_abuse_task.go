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

// inviteAbuseBanTickInterval is how quickly the master node notices that the
// invite abuse rule was turned on or its settings changed; the full sweep
// itself runs every model.InviteAbuseSweepInterval.
const inviteAbuseBanTickInterval = 1 * time.Minute

var inviteAbuseBanOnce sync.Once

// StartInviteAbuseBanTask runs the invite abuse rule's sweep on the master
// node: at startup and then on a one-minute tick that sweeps when one is due
// (see model.RunInviteAbuseSweepIfDue).
func StartInviteAbuseBanTask() {
	inviteAbuseBanOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("invite abuse ban task started: tick=%s", inviteAbuseBanTickInterval))
			ticker := time.NewTicker(inviteAbuseBanTickInterval)
			defer ticker.Stop()

			model.RunInviteAbuseSweepIfDue(time.Now())
			for now := range ticker.C {
				model.RunInviteAbuseSweepIfDue(now)
			}
		})
	})
}
