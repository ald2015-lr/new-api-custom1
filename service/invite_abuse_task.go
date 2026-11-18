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

// StartInviteAbuseBanTask runs the invite abuse rule's sweep on the master
// node: at startup, on a one-minute tick that sweeps when one is due, and at
// once when the rule is turned on or its settings change on this node,
// whether saved here or synced from another node (see
// model.RunInviteAbuseSweepIfDue).
func StartInviteAbuseBanTask() {
	inviteAbuseBanOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("invite abuse ban task started: tick=%s", inviteAbuseBanTickInterval))
			ticker := time.NewTicker(inviteAbuseBanTickInterval)
			defer ticker.Stop()

			// Loading the saved options at startup may have woken the task
			// already; the startup sweep covers them.
			select {
			case <-model.InviteAbuseSweepWake():
			default:
			}
			model.RunInviteAbuseSweepIfDue(time.Now())
			for {
				select {
				case now := <-ticker.C:
					model.RunInviteAbuseSweepIfDue(now)
				case <-model.InviteAbuseSweepWake():
					model.RunInviteAbuseSweepIfDue(time.Now())
				}
			}
		})
	})
}
