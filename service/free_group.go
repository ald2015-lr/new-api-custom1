package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// BillingSourceFreeGroup marks requests in a free group: the user is not
// charged, while channel usage and logs still record the real cost.
const BillingSourceFreeGroup = "free_group"

// isFreeGroupRequest reports whether the request's using group (the group
// actually selected, for auto tokens) does not charge the user.
func isFreeGroupRequest(relayInfo *relaycommon.RelayInfo) bool {
	return relayInfo != nil && operation_setting.IsFreeGroup(relayInfo.UsingGroup)
}
