package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupGroupAccessTest(t *testing.T, rules string) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousQuotaPerUnit := common.QuotaPerUnit
	previousRedis := common.RedisEnabled
	previousRules := operation_setting.GroupAccessRulesJSON()
	previousCountRedemption := operation_setting.GroupAccessCountsRedemption()
	previousGroupRatios := ratio_setting.GroupRatio2JSONString()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Redemption{}))
	model.DB = db
	common.QuotaPerUnit = 500000
	common.RedisEnabled = false
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"svip":2,"vvip":3}`))
	require.NoError(t, operation_setting.ValidateGroupAccessOption(operation_setting.GroupAccessRulesOptionKey, rules))
	operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessRulesOptionKey, rules)
	operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessCountRedemptionOptionKey, "true")

	t.Cleanup(func() {
		model.DB = previousDB
		common.QuotaPerUnit = previousQuotaPerUnit
		common.RedisEnabled = previousRedis
		operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessRulesOptionKey, previousRules)
		operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessCountRedemptionOptionKey, strconv.FormatBool(previousCountRedemption))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatios))
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return db
}

func seedGroupAccessTopUp(t *testing.T, db *gorm.DB, userId int, amount int64, paymentMethod string, status string) {
	t.Helper()
	require.NoError(t, db.Create(&model.TopUp{
		UserId:        userId,
		Amount:        amount,
		TradeNo:       fmt.Sprintf("group-access-%d-%d-%s-%s-%d", userId, amount, paymentMethod, status, common.GetTimestamp()) + common.GetRandomString(6),
		PaymentMethod: paymentMethod,
		Status:        status,
	}).Error)
	model.InvalidateUserTopupTotalCache(userId)
}

func newGroupAccessContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return ctx
}

func TestCheckGroupAccess(t *testing.T) {
	require.NoError(t, i18n.Init())
	db := setupGroupAccessTest(t, `[
		{"group":"svip","min_topup":50,"users":[{"id":3,"username":"listed"}]},
		{"group":"vvip","min_topup":0,"users":[{"id":3,"username":"listed"}]}
	]`)
	// User 2 reaches exactly 50: pending, failed and subscription mirror rows do not count.
	seedGroupAccessTopUp(t, db, 2, 30, "alipay", common.TopUpStatusSuccess)
	seedGroupAccessTopUp(t, db, 2, 20, model.PaymentMethodStripe, common.TopUpStatusSuccess)
	seedGroupAccessTopUp(t, db, 2, 100, "alipay", common.TopUpStatusPending)
	seedGroupAccessTopUp(t, db, 2, 100, "alipay", common.TopUpStatusFailed)
	seedGroupAccessTopUp(t, db, 2, 0, model.PaymentMethodStripe, common.TopUpStatusSuccess)
	seedGroupAccessTopUp(t, db, 4, 49, "wxpay", common.TopUpStatusSuccess)
	seedGroupAccessTopUp(t, db, 5, 60, "alipay", common.TopUpStatusSuccess)
	// Creem stores raw quota: 12,500,000 / 500,000 = 25 units.
	seedGroupAccessTopUp(t, db, 6, 25, "alipay", common.TopUpStatusSuccess)
	seedGroupAccessTopUp(t, db, 6, 12_500_000, model.PaymentMethodCreem, common.TopUpStatusSuccess)
	// A used redemption code counts even after it was deleted.
	redemption := &model.Redemption{Key: "group-access-redeemed-code-0007", Status: common.RedemptionCodeStatusUsed, Quota: 25_000_000, UsedUserId: 7}
	require.NoError(t, db.Create(redemption).Error)
	require.NoError(t, db.Delete(redemption).Error)
	require.NoError(t, db.Create(&model.Redemption{Key: "group-access-unused-code-00007", Status: common.RedemptionCodeStatusEnabled, Quota: 25_000_000, UsedUserId: 7}).Error)
	model.InvalidateUserTopupTotalCache(7)

	tests := []struct {
		name            string
		subject         GroupAccessSubject
		group           string
		countRedemption bool
		wantDenied      bool
		wantWhitelist   bool
		wantCurrent     string
	}{
		{name: "ungated group", subject: GroupAccessSubject{UserId: 1, Group: "default"}, group: "default"},
		{name: "admin exempt", subject: GroupAccessSubject{UserId: 1, Role: common.RoleAdminUser, Group: "default"}, group: "vvip"},
		{name: "own group exempt", subject: GroupAccessSubject{UserId: 1, Group: "vvip"}, group: "vvip"},
		{name: "whitelisted", subject: GroupAccessSubject{UserId: 3, Group: "default"}, group: "svip"},
		{name: "no top-up", subject: GroupAccessSubject{UserId: 1, Group: "default"}, group: "svip", wantDenied: true, wantCurrent: "0"},
		{name: "anonymous", subject: GroupAccessSubject{Group: ""}, group: "svip", wantDenied: true, wantCurrent: "0"},
		{name: "below threshold", subject: GroupAccessSubject{UserId: 4, Group: "default"}, group: "svip", wantDenied: true, wantCurrent: "49"},
		{name: "at threshold", subject: GroupAccessSubject{UserId: 2, Group: "default"}, group: "svip"},
		{name: "above threshold", subject: GroupAccessSubject{UserId: 5, Group: "default"}, group: "svip"},
		{name: "creem quota converted", subject: GroupAccessSubject{UserId: 6, Group: "default"}, group: "svip"},
		{name: "whitelist only denies top-up", subject: GroupAccessSubject{UserId: 5, Group: "default"}, group: "vvip", wantDenied: true, wantWhitelist: true},
		{name: "whitelist only allows listed", subject: GroupAccessSubject{UserId: 3, Group: "default"}, group: "vvip"},
		{name: "redemption counted", subject: GroupAccessSubject{UserId: 7, Group: "default"}, group: "svip", countRedemption: true},
		{name: "redemption not counted", subject: GroupAccessSubject{UserId: 7, Group: "default"}, group: "svip", wantDenied: true, wantCurrent: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessCountRedemptionOptionKey, strconv.FormatBool(tt.countRedemption))

			denial, err := CheckGroupAccess(newGroupAccessContext(), tt.subject, tt.group)

			require.NoError(t, err)
			if !tt.wantDenied {
				assert.Nil(t, denial)
				return
			}
			require.NotNil(t, denial)
			assert.Equal(t, tt.wantWhitelist, denial.WhitelistOnly)
			if !tt.wantWhitelist {
				assert.Equal(t, tt.wantCurrent, denial.CurrentTopup.String())
			}
		})
	}

	operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessCountRedemptionOptionKey, "true")
	ctx := newGroupAccessContext()
	denial, err := CheckGroupAccess(ctx, GroupAccessSubject{UserId: 4, Group: "default"}, "svip")
	require.NoError(t, err)
	require.NotNil(t, denial)
	assert.Equal(t, "Group svip requires a cumulative top-up of at least 50 (current: 49)", denial.Message(ctx))

	locked := GetLockedUsableGroups(ctx, GroupAccessSubject{UserId: 4, Group: "default"}, map[string]string{"default": "Default", "svip": "SVIP", "vvip": "VVIP", "auto": "Auto"})
	assert.Equal(t, map[string]LockedGroup{
		"svip": {Desc: "SVIP", Ratio: 2, MinTopup: 50, CurrentTopup: 49},
		"vvip": {Desc: "VVIP", Ratio: 3, MinTopup: 0, CurrentTopup: 49, WhitelistOnly: true},
	}, locked)
}

func TestNormalizeGroupAccessRulesInputResolvesWhitelistUsers(t *testing.T) {
	db := setupGroupAccessTest(t, `[]`)
	alice := &model.User{Username: "alice", Password: "password-placeholder", AffCode: "group-access-alice"}
	bob := &model.User{Username: "Bob", Password: "password-placeholder", AffCode: "group-access-bob"}
	require.NoError(t, db.Create(alice).Error)
	require.NoError(t, db.Create(bob).Error)

	stored, err := model.NormalizeGroupAccessRulesInput(fmt.Sprintf(
		`[{"group":" svip ","min_topup":50,"users":[{"id":%d},{"username":"alice"},{"id":%d,"username":"renamed"}]}]`,
		bob.Id, alice.Id,
	))
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(
		`[{"group":"svip","min_topup":50,"users":[{"id":%d,"username":"alice"},{"id":%d,"username":"Bob"}]}]`,
		alice.Id, bob.Id,
	), stored)
	require.NoError(t, operation_setting.ValidateGroupAccessOption(operation_setting.GroupAccessRulesOptionKey, stored))

	_, err = model.NormalizeGroupAccessRulesInput(`[{"group":"svip","min_topup":50,"users":[{"username":"ghost"},{"id":9999}]}]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
	assert.Contains(t, err.Error(), "9999")
}

func TestLoadGroupAccessRulesKeepsPreviousSnapshotOnInvalidValue(t *testing.T) {
	valid := `[{"group":"svip","min_topup":10,"users":[]}]`
	setupGroupAccessTest(t, valid)

	operation_setting.LoadGroupAccessOption(operation_setting.GroupAccessRulesOptionKey, `[{"group":"svip","min_topup":-1}]`)

	assert.Equal(t, valid, operation_setting.GroupAccessRulesJSON())
	assert.Equal(t, map[string]operation_setting.GroupAccessRequirement{"svip": {MinTopup: 10}}, operation_setting.GetGroupAccessRequirements())
}
