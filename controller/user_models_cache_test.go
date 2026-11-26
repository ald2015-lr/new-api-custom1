package controller

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// /api/user/models lists the same models on both paths: from the memory
// channel cache without any abilities query while it is on, and with one
// query per group from the database while it is off.
func TestGetUserModelsReadsTheMemoryCacheWhenOn(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	t.Cleanup(func() {
		// Leave an empty channel cache behind for the next tests.
		common.MemoryCacheEnabled = true
		db.Exec("DELETE FROM abilities")
		model.InitChannelCache()
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
	require.NoError(t, db.Create(&model.User{
		Id:       1004,
		Username: "cached-model-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true},
		{Group: "default", Model: "claude-sonnet-4", ChannelId: 2, Enabled: true},
		{Group: "default", Model: "gpt-4o", ChannelId: 2, Enabled: true},
		{Group: "default", Model: "disabled-model", ChannelId: 3, Enabled: false},
		{Group: "vip", Model: "vip-model", ChannelId: 4, Enabled: true},
	}).Error)
	var abilityQueries atomic.Int32
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:count_ability_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			abilityQueries.Add(1)
		}
	}))
	userModels := func() []string {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/api/user/models?group=default", nil)
		context.Set("id", 1004)
		GetUserModels(context)
		return decodeUserModelsResponse(t, recorder)
	}

	common.MemoryCacheEnabled = false
	fromDatabase := userModels()
	assert.ElementsMatch(t, []string{"gpt-4o", "claude-sonnet-4"}, fromDatabase)
	assert.EqualValues(t, 1, abilityQueries.Load(), "one query for the group")

	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	abilityQueries.Store(0)
	fromCache := userModels()
	assert.ElementsMatch(t, fromDatabase, fromCache)
	assert.Zero(t, abilityQueries.Load(), "no abilities query while the memory cache is on")
}
