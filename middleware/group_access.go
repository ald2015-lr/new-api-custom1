package middleware

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// abortIfGroupAccessDenied enforces the recharge gate of an explicitly
// selected group and reports whether the request was aborted.
func abortIfGroupAccessDenied(c *gin.Context, subject service.GroupAccessSubject, group string) bool {
	denial, err := service.CheckGroupAccess(c, subject, group)
	if denial == nil {
		return false
	}
	if err != nil {
		common.SysLog(fmt.Sprintf("group access check failed for user %d group %s: %v", subject.UserId, group, err))
		abortWithOpenAiMessage(c, http.StatusInternalServerError, common.TranslateMessage(c, i18n.MsgDatabaseError))
		return true
	}
	abortWithOpenAiMessage(c, http.StatusForbidden, denial.Message(c), types.ErrorCodeAccessDenied)
	return true
}
