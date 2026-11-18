package model

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// useInviteTestDatabase points DB at an empty database of dialect whose
// tables carry prefix, so the queries under test are shown to be dialect- and
// table-prefix-safe; LOG_DB stays the package's SQLite database. MySQL and
// PostgreSQL run when TEST_MYSQL_DSN / TEST_POSTGRES_DSN are set; PostgreSQL
// uses the simple protocol like the server does.
func useInviteTestDatabase(t *testing.T, dialect string, prefix string, models ...any) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(":memory:")
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN not configured")
		}
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	}
	db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s", dialect, version)
	// Tables a failed earlier run left behind.
	require.NoError(t, db.Migrator().DropTable(models...))
	require.NoError(t, db.AutoMigrate(models...))
	previousDB, previousType := DB, common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseType(dialect))
	initCol()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		initCol()
		assert.NoError(t, db.Migrator().DropTable(models...))
		assert.NoError(t, sqlDB.Close())
	})
	return db
}

var inviteAbuseTestModels = []any{&User{}, &UserSession{}, &TopUp{}, &Redemption{}, &SubscriptionOrder{}, &InviteReward{}, &InviteAbuseBan{}}

func resetInviteAbuseTables(t *testing.T) {
	t.Helper()
	for _, table := range inviteAbuseTestModels {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(table).Error)
	}
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	require.NoError(t, LOG_DB.Exec("DELETE FROM audit_logs").Error)
}

// useInviteAbuseSettings pins the rule's options (paid inviters exempt, the
// default), the settings a sign-up reads and the sweep's schedule state, and
// restores them afterwards.
func useInviteAbuseSettings(t *testing.T, enabled bool, threshold int, minRedeemQuota int) {
	t.Helper()
	payment := operation_setting.GetPaymentSetting()
	savedConfirmed := payment.ComplianceConfirmed
	savedEnabled, savedThreshold, savedMin := common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota
	savedExemptPaid := common.InviteAbuseBanExemptPaidInviter
	savedNewUser, savedOptions := common.QuotaForNewUser, common.OptionMap
	t.Cleanup(func() {
		payment.ComplianceConfirmed = savedConfirmed
		common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota = savedEnabled, savedThreshold, savedMin
		common.InviteAbuseBanExemptPaidInviter = savedExemptPaid
		common.QuotaForNewUser, common.OptionMap = savedNewUser, savedOptions
		inviteAbuseSweep = inviteAbuseSweepState{}
	})
	payment.ComplianceConfirmed = false
	common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota = enabled, threshold, minRedeemQuota
	common.InviteAbuseBanExemptPaidInviter = true
	common.QuotaForNewUser, common.OptionMap = 0, map[string]string{}
	inviteAbuseSweep = inviteAbuseSweepState{}
}

func createInviteAbuseTestUser(t *testing.T, name string, inviterId int, role int) User {
	t.Helper()
	user := User{
		Username: name, DisplayName: name, Role: role, Status: common.UserStatusEnabled,
		Group: "default", AffCode: name, InviterId: inviterId, AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

// createInviteAbuseTestInviter creates an inviter and its invited users
// <name>-1 ... <name>-<invitees>.
func createInviteAbuseTestInviter(t *testing.T, name string, role int, invitees int) (User, []User) {
	t.Helper()
	inviter := createInviteAbuseTestUser(t, name, 0, role)
	users := make([]User, 0, invitees)
	for i := range invitees {
		users = append(users, createInviteAbuseTestUser(t, fmt.Sprintf("%s-%d", name, i+1), inviter.Id, common.RoleCommonUser))
	}
	return inviter, users
}

func releaseInviteAbuseTestUser(t *testing.T, user User) {
	t.Helper()
	require.NoError(t, DB.Create(&InviteAbuseBan{
		UserId: user.Id, InviterId: user.Id, Kind: InviteAbuseKindInviter, InviteCount: 1,
		CreatedAt: 1, ReleasedAt: 2, ReleasedBy: 1,
	}).Error)
}

func inviteAbuseUserState(t *testing.T, id int) User {
	t.Helper()
	var user User
	require.NoError(t, DB.Unscoped().Select("id", "status", "auth_version").Where("id = ?", id).Take(&user).Error)
	return user
}

func disabledInviteAbuseTestUsers(t *testing.T) []int {
	t.Helper()
	ids := []int{}
	require.NoError(t, DB.Unscoped().Model(&User{}).Where("status = ?", common.UserStatusDisabled).Order("id").Pluck("id", &ids).Error)
	return ids
}

func inviteAbuseTestUserIds(users ...User) []int {
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	return ids
}

// onInviteAbuseBan calls fn each time the rule has disabled an account and
// recorded its ban, so a test can change something in the middle of a pass.
func onInviteAbuseBan(t *testing.T, fn func(ban InviteAbuseBan)) {
	t.Helper()
	const name = "test:on-invite-abuse-ban"
	require.NoError(t, DB.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(name, func(tx *gorm.DB) {
		// Records a test creates for released users are not bans.
		if ban, ok := tx.Statement.Dest.(*InviteAbuseBan); ok && tx.Error == nil && tx.RowsAffected == 1 && ban.ReleasedAt == 0 {
			fn(*ban)
		}
	}))
	t.Cleanup(func() { assert.NoError(t, DB.Callback().Create().Remove(name)) })
}

func TestInviteAbuseDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			useInviteTestDatabase(t, dialect, "invite_abuse_test_", inviteAbuseTestModels...)
			t.Run("migration", testInviteAbuseMigration)
			t.Run("qualifying recharge", testInviteAbuseQualifyingRecharge)
			t.Run("paid inviter", testInviteAbusePaidInviter)
			t.Run("threshold", testInviteAbuseThreshold)
			t.Run("administrators and exempt users", testInviteAbuseAdministratorsAndExemptUsers)
			t.Run("ban effects and idempotency", testInviteAbuseBanEffects)
			t.Run("release", testInviteAbuseRelease)
			t.Run("sign-up", testInviteAbuseSignUp)
			t.Run("sign-up keeps a concurrent ban", testInviteAbuseSignUpKeepsConcurrentBan)
			t.Run("sweep schedule", testInviteAbuseSweepSchedule)
			t.Run("settings change stops a pass", testInviteAbuseSettingsChangeStopsPass)
			t.Run("verdict taken again before disabling", testInviteAbuseVerdictAtDisableTime)
			t.Run("sweep batches and budget", testInviteAbuseSweepBatches)
			t.Run("preview", testInviteAbusePreview)
			t.Run("preview of an invite chain", testInviteAbusePreviewChain)
			t.Run("preview truncation", testInviteAbusePreviewTruncation)
		})
	}
}

func testInviteAbuseMigration(t *testing.T) {
	resetInviteAbuseTables(t)
	for range 2 {
		require.NoError(t, DB.AutoMigrate(&InviteAbuseBan{}))
	}
	assert.True(t, DB.Migrator().HasIndex(&InviteAbuseBan{}, "UserId"))
	assert.True(t, DB.Migrator().HasIndex(&InviteAbuseBan{}, "InviterId"))
	ban := InviteAbuseBan{UserId: 7, InviterId: 7, Kind: InviteAbuseKindInviter, InviteCount: 5, CreatedAt: 1}
	require.NoError(t, DB.Create(&ban).Error)
	duplicate := ban
	duplicate.Id = 0
	assert.Error(t, DB.Create(&duplicate).Error, "one record per user")
	var stored InviteAbuseBan
	require.NoError(t, DB.First(&stored, ban.Id).Error)
	assert.Zero(t, stored.ReleasedAt)
	assert.Zero(t, stored.ReleasedBy)
}

// A recharge of any invited user clears the inviter. The sign-up check
// (single inviter) and the grouped query of the sweep and the preview must
// agree on every kind of money-in.
func testInviteAbuseQualifyingRecharge(t *testing.T) {
	const minRedeem = 500_000
	for _, tc := range []struct {
		name     string
		abusive  bool
		recharge func(t *testing.T, invitee User)
	}{
		{name: "no recharge", abusive: true, recharge: func(*testing.T, User) {}},
		{name: "online top-up", recharge: func(t *testing.T, invitee User) {
			seedTestTopUp(t, TopUp{UserId: invitee.Id, Amount: 2, Money: 14, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay})
		}},
		{name: "pending top-up", abusive: true, recharge: func(t *testing.T, invitee User) {
			seedTestTopUp(t, TopUp{UserId: invitee.Id, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending})
		}},
		{name: "subscription paid through a gateway", recharge: func(t *testing.T, invitee User) {
			seedTestTopUp(t, TopUp{UserId: invitee.Id, Amount: 0, Money: 9.9, PaymentMethod: PaymentMethodStripe})
		}},
		{name: "order completed by an administrator", recharge: func(t *testing.T, invitee User) {
			order := TopUp{
				UserId: invitee.Id, Amount: 2, Money: 14, TradeNo: "invite-abuse-manual-order", PaymentMethod: "alipay",
				PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp(),
			}
			require.NoError(t, DB.Create(&order).Error)
			require.NoError(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1"))
		}},
		{name: "subscription paid from the balance", abusive: true, recharge: func(t *testing.T, invitee User) {
			now := common.GetTimestamp()
			require.NoError(t, DB.Create(&SubscriptionOrder{
				UserId: invitee.Id, PlanId: 1, Money: 9.9, TradeNo: "invite-abuse-balance-order", PaymentMethod: PaymentMethodBalance,
				PaymentProvider: PaymentProviderBalance, Status: common.TopUpStatusSuccess, CreateTime: now, CompleteTime: now,
			}).Error)
		}},
		{name: "redeemed less than the minimum", abusive: true, recharge: func(t *testing.T, invitee User) {
			seedUsedRedemption(t, invitee.Id, minRedeem-1)
		}},
		{name: "redeemed the minimum with a code deleted after use", recharge: func(t *testing.T, invitee User) {
			seedUsedRedemption(t, invitee.Id, minRedeem-100)
			deleted := seedUsedRedemption(t, invitee.Id, 100)
			require.NoError(t, DB.Delete(&deleted).Error)
		}},
		{name: "invited user deleted after paying", recharge: func(t *testing.T, invitee User) {
			seedTestTopUp(t, TopUp{UserId: invitee.Id, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay})
			require.NoError(t, DB.Delete(&invitee).Error)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, true, 3, minRedeem)
			inviter, invitees := createInviteAbuseTestInviter(t, "inviter", common.RoleCommonUser, 4)
			tc.recharge(t, invitees[0])

			preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
			require.NoError(t, err)
			disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
			require.NoError(t, err)
			assert.True(t, finished)
			if !tc.abusive {
				assert.Zero(t, preview.AbusiveInviters)
				assert.Zero(t, disabled)
				assert.Empty(t, disabledInviteAbuseTestUsers(t))
				return
			}
			assert.Equal(t, 1, preview.AbusiveInviters)
			assert.Equal(t, 5, disabled)
			assert.Equal(t, inviteAbuseTestUserIds(append([]User{inviter}, invitees...)...), disabledInviteAbuseTestUsers(t))
		})
	}
}

// An inviter who recharged itself, by the same definition as for invited
// users, is not abusive while paid inviters are exempt: the preview, the
// sweep and the sign-up check agree. Without the exemption it is.
func testInviteAbusePaidInviter(t *testing.T) {
	const minRedeem = 500_000
	for _, tc := range []struct {
		name     string
		paid     bool
		recharge func(t *testing.T, inviter User)
	}{
		{name: "no recharge", recharge: func(*testing.T, User) {}},
		{name: "online top-up", paid: true, recharge: func(t *testing.T, inviter User) {
			seedTestTopUp(t, TopUp{UserId: inviter.Id, Amount: 2, Money: 14, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay})
		}},
		{name: "pending top-up", recharge: func(t *testing.T, inviter User) {
			seedTestTopUp(t, TopUp{UserId: inviter.Id, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending})
		}},
		{name: "subscription paid through a gateway", paid: true, recharge: func(t *testing.T, inviter User) {
			seedTestTopUp(t, TopUp{UserId: inviter.Id, Amount: 0, Money: 9.9, PaymentMethod: PaymentMethodStripe})
		}},
		{name: "redeemed less than the minimum", recharge: func(t *testing.T, inviter User) {
			seedUsedRedemption(t, inviter.Id, minRedeem-1)
		}},
		{name: "redeemed the minimum with a code deleted after use", paid: true, recharge: func(t *testing.T, inviter User) {
			seedUsedRedemption(t, inviter.Id, minRedeem-100)
			deleted := seedUsedRedemption(t, inviter.Id, 100)
			require.NoError(t, DB.Delete(&deleted).Error)
		}},
	} {
		for _, exempt := range []bool{true, false} {
			if !exempt && !tc.paid {
				continue // the qualifying recharge test covers inviters who did not pay
			}
			t.Run(fmt.Sprintf("%s, paid inviters exempt: %v", tc.name, exempt), func(t *testing.T) {
				resetInviteAbuseTables(t)
				useInviteAbuseSettings(t, true, 2, minRedeem)
				common.InviteAbuseBanExemptPaidInviter = exempt
				spared := exempt && tc.paid

				swept, sweptInvitees := createInviteAbuseTestInviter(t, "swept", common.RoleCommonUser, 2)
				tc.recharge(t, swept)
				preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
				require.NoError(t, err)
				assert.Equal(t, exempt, preview.ExemptPaidInviter)
				_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
				require.NoError(t, err)
				require.True(t, complete)
				if spared {
					assert.Zero(t, preview.AbusiveInviters)
					assert.Empty(t, disabledInviteAbuseTestUsers(t))
				} else {
					assert.Equal(t, 1, preview.AbusiveInviters)
					assert.Equal(t, inviteAbuseTestUserIds(append([]User{swept}, sweptInvitees...)...), disabledInviteAbuseTestUsers(t))
				}

				// A sign-up that brings the inviter to the threshold.
				inviter, invitees := createInviteAbuseTestInviter(t, "signup", common.RoleCommonUser, 1)
				tc.recharge(t, inviter)
				newcomer := User{Username: "signup-2", DisplayName: "signup-2", Role: common.RoleCommonUser, InviterId: inviter.Id}
				require.NoError(t, newcomer.Insert(inviter.Id))
				want := common.UserStatusDisabled
				if spared {
					want = common.UserStatusEnabled
				}
				for _, user := range []User{inviter, invitees[0], newcomer} {
					assert.Equal(t, want, inviteAbuseUserState(t, user.Id).Status, user.Username)
				}
			})
		}
	}
}

func testInviteAbuseThreshold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold int
		invitees  int
		deleted   int
		minRedeem int
		redeemed  int
		abusive   bool
	}{
		{name: "one short of the threshold", threshold: 3, invitees: 2, minRedeem: 500_000},
		{name: "exactly the threshold", threshold: 3, invitees: 3, minRedeem: 500_000, abusive: true},
		// A farm cannot stay under the threshold by deleting its invited
		// accounts; the deleted ones are counted but cannot be disabled.
		{name: "deleted invited users are counted", threshold: 3, invitees: 3, deleted: 1, minRedeem: 500_000, abusive: true},
		{name: "threshold of one", threshold: 1, invitees: 1, minRedeem: 500_000, abusive: true},
		{name: "redeemed exactly the minimum", threshold: 2, invitees: 2, minRedeem: 500_000, redeemed: 500_000},
		{name: "minimum of zero takes any redemption", threshold: 2, invitees: 2, minRedeem: 0, redeemed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, true, tc.threshold, tc.minRedeem)
			inviter, invitees := createInviteAbuseTestInviter(t, "inviter", common.RoleCommonUser, tc.invitees)
			for _, invitee := range invitees[:tc.deleted] {
				require.NoError(t, DB.Delete(&invitee).Error)
			}
			if tc.redeemed > 0 {
				seedUsedRedemption(t, invitees[len(invitees)-1].Id, tc.redeemed)
			}

			preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
			require.NoError(t, err)
			disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
			require.NoError(t, err)
			assert.True(t, finished)
			if !tc.abusive {
				assert.Zero(t, preview.AbusiveInviters)
				assert.Zero(t, disabled)
				return
			}
			assert.Equal(t, 1, preview.AbusiveInviters)
			require.Len(t, preview.Items, 1)
			assert.EqualValues(t, tc.invitees, preview.Items[0].InviteCount)
			assert.Equal(t, 1+tc.invitees-tc.deleted, disabled)
		})
	}
}

func testInviteAbuseAdministratorsAndExemptUsers(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, true, 2, 500_000)
	admin, _ := createInviteAbuseTestInviter(t, "admin", common.RoleAdminUser, 2)
	root, _ := createInviteAbuseTestInviter(t, "root", common.RoleRootUser, 2)
	exempt, _ := createInviteAbuseTestInviter(t, "exempt", common.RoleCommonUser, 2)
	releaseInviteAbuseTestUser(t, exempt)
	inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 2)
	adminInvitee := createInviteAbuseTestUser(t, "farmer-admin", inviter.Id, common.RoleAdminUser)
	releaseInviteAbuseTestUser(t, invitees[0])

	for _, skipped := range []User{admin, root, exempt} {
		disabled, finished, err := enforceInviteAbuseRuleOnInviter(skipped.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		assert.True(t, finished)
		assert.Zero(t, disabled, skipped.Username)
	}
	_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, complete)

	// Only the plain inviter and its plain, not exempt invited user.
	assert.Equal(t, inviteAbuseTestUserIds(inviter, invitees[1]), disabledInviteAbuseTestUsers(t))
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseUserState(t, adminInvitee.Id).Status)
	changed, err := DisableUserBySystem(admin.Id, inviteAbuseRevokeReason)
	require.NoError(t, err)
	assert.False(t, changed, "administrators are never disabled by the system")
}

func testInviteAbuseBanEffects(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, true, 2, 500_000)
	inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 2)
	now := common.GetTimestamp()
	require.NoError(t, DB.Create(&UserSession{
		SID: "invite-abuse-session", UserID: invitees[0].Id, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive,
		RefreshHash: "invite-abuse-refresh", LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)
	banned := append([]User{inviter}, invitees...)

	disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, 3, disabled)
	for _, user := range banned {
		state := inviteAbuseUserState(t, user.Id)
		assert.Equal(t, common.UserStatusDisabled, state.Status, user.Username)
		assert.EqualValues(t, 2, state.AuthVersion, user.Username)
	}
	var session UserSession
	require.NoError(t, DB.First(&session, "sid = ?", "invite-abuse-session").Error)
	assert.Equal(t, UserSessionStatusRevoked, session.Status)
	assert.Equal(t, inviteAbuseRevokeReason, session.RevokedReason)

	var bans []InviteAbuseBan
	require.NoError(t, DB.Order("user_id").Find(&bans).Error)
	require.Len(t, bans, 3)
	for i, ban := range bans {
		wantKind := InviteAbuseKindInvitee
		if i == 0 {
			wantKind = InviteAbuseKindInviter
		}
		assert.Equal(t, banned[i].Id, ban.UserId)
		assert.Equal(t, wantKind, ban.Kind)
		assert.Equal(t, inviter.Id, ban.InviterId)
		assert.Equal(t, 2, ban.InviteCount)
		assert.Positive(t, ban.CreatedAt)
		assert.Zero(t, ban.ReleasedAt)
	}
	assert.EqualValues(t, 1, countSystemLogs(t, inviter.Id, "invited users and none of them has recharged."))
	for _, invitee := range invitees {
		assert.EqualValues(t, 1, countSystemLogs(t, invitee.Id, "invited users have no recharge."))
	}
	var audits []AuditLog
	require.NoError(t, LOG_DB.Where("action = ?", inviteAbuseAuditAction).Order("user_id").Find(&audits).Error)
	require.Len(t, audits, 3)
	assert.Equal(t, inviter.Id, audits[0].UserId)
	assert.Equal(t, AuditCategoryOperation, audits[0].Category)
	assert.Equal(t, common.RoleAdminUser, audits[0].ActorRole)
	require.NotNil(t, audits[0].Other.AdminInfo)
	assert.Equal(t, "system", audits[0].Other.AdminInfo.AdminUsername)
	for i, audit := range audits {
		require.NotNil(t, audit.Other.Op)
		params, err := common.Marshal(audit.Other.Op.Params)
		require.NoError(t, err)
		kind := InviteAbuseKindInvitee
		if i == 0 {
			kind = InviteAbuseKindInviter
		}
		assert.JSONEq(t, fmt.Sprintf(`{"id":%d,"username":%q,"kind":%q,"inviter_id":%d,"invite_count":2}`, banned[i].Id, banned[i].Username, kind, inviter.Id), string(params))
	}

	// Running the rule again, by a sign-up or a sweep, changes nothing.
	again, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Zero(t, again)
	_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, complete)
	for _, user := range banned {
		assert.EqualValues(t, 2, inviteAbuseUserState(t, user.Id).AuthVersion, user.Username)
	}
	var records, auditCount int64
	require.NoError(t, DB.Model(&InviteAbuseBan{}).Count(&records).Error)
	require.NoError(t, LOG_DB.Model(&AuditLog{}).Where("action = ?", inviteAbuseAuditAction).Count(&auditCount).Error)
	assert.EqualValues(t, 3, records)
	assert.EqualValues(t, 3, auditCount)
	assert.EqualValues(t, 1, countSystemLogs(t, inviter.Id, "invited users and none of them has recharged."))
}

// An account an administrator enables again is exempt: as an inviter it is
// never checked again and as an invited user it is never disabled again.
func testInviteAbuseRelease(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, true, 2, 500_000)
	inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 2)
	_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, complete)
	require.Len(t, disabledInviteAbuseTestUsers(t), 3)

	for _, user := range []User{inviter, invitees[0]} {
		released, err := ReleaseInviteAbuseBan(user.Id, 42)
		require.NoError(t, err)
		assert.True(t, released)
		released, err = ReleaseInviteAbuseBan(user.Id, 42)
		require.NoError(t, err)
		assert.False(t, released, "a record is released once")
		require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusEnabled).Error)
	}
	var ban InviteAbuseBan
	require.NoError(t, DB.Where("user_id = ?", inviter.Id).Take(&ban).Error)
	assert.Positive(t, ban.ReleasedAt)
	assert.Equal(t, 42, ban.ReleasedBy)

	newcomer := createInviteAbuseTestUser(t, "farmer-3", inviter.Id, common.RoleCommonUser)
	_, complete, err = sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Equal(t, inviteAbuseTestUserIds(invitees[1]), disabledInviteAbuseTestUsers(t))
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseUserState(t, newcomer.Id).Status, "a released inviter is not checked")

	other, otherInvitees := createInviteAbuseTestInviter(t, "other", common.RoleCommonUser, 1)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", invitees[0].Id).Update("inviter_id", other.Id).Error)
	disabled, finished, err := enforceInviteAbuseRuleOnInviter(other.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, finished)
	assert.Equal(t, 2, disabled, "the inviter and its invited user that is not exempt")
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseUserState(t, invitees[0].Id).Status)
	assert.Equal(t, common.UserStatusDisabled, inviteAbuseUserState(t, otherInvitees[0].Id).Status)
}

// A sign-up that makes its inviter abusive disables the inviter and every
// invited user at once, the new one included; the caller's copy of the new
// user carries the disabled status the OAuth login checks.
func testInviteAbuseSignUp(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		invitees int
		oauth    bool
		disabled bool
	}{
		{name: "password sign-up reaching the threshold", enabled: true, invitees: 2, disabled: true},
		{name: "OAuth sign-up reaching the threshold", enabled: true, invitees: 2, oauth: true, disabled: true},
		{name: "sign-up below the threshold", enabled: true, invitees: 1, oauth: true},
		{name: "rule turned off", invitees: 2, oauth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, tc.enabled, 3, 500_000)
			inviter, invitees := createInviteAbuseTestInviter(t, "inviter", common.RoleCommonUser, tc.invitees)
			user := User{Username: "newcomer", DisplayName: "newcomer", Role: common.RoleCommonUser, InviterId: inviter.Id}
			if tc.oauth {
				user.Status = common.UserStatusEnabled
				require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return user.InsertWithTx(tx, inviter.Id) }))
				user.FinalizeOAuthUserCreation(inviter.Id)
			} else {
				require.NoError(t, user.Insert(inviter.Id))
			}

			want := common.UserStatusEnabled
			if tc.disabled {
				want = common.UserStatusDisabled
			}
			stored := inviteAbuseUserState(t, user.Id)
			assert.Equal(t, want, stored.Status)
			assert.Equal(t, want, inviteAbuseUserState(t, inviter.Id).Status)
			for _, invitee := range invitees {
				assert.Equal(t, want, inviteAbuseUserState(t, invitee.Id).Status)
			}
			if tc.oauth || tc.disabled {
				assert.Equal(t, want, user.Status)
			}
			if tc.disabled {
				assert.Equal(t, stored.AuthVersion, user.AuthVersion)
				var ban InviteAbuseBan
				require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&ban).Error)
				assert.Equal(t, InviteAbuseKindInvitee, ban.Kind)
				assert.Equal(t, 3, ban.InviteCount)
			}
		})
	}
}

// Another sign-up's check or a sweep may disable a new user right after the
// sign-up read it to write its sidebar config. That write touches only the
// setting column: the account stays disabled, only the ban moved its
// authentication version, and a cached copy gets the new setting with the
// disabled status.
func testInviteAbuseSignUpKeepsConcurrentBan(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(fmt.Sprintf("OAuth sign-up: %v", oauth), func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, false, 2, 500_000)
			useUserCacheMiniRedis(t)
			newcomer := User{Username: "newcomer", DisplayName: "newcomer", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
			require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return newcomer.InsertWithTx(tx, 0) }))
			_, err := GetUserCache(newcomer.Id)
			require.NoError(t, err)
			banned := false
			const name = "test:ban-during-sign-up"
			require.NoError(t, DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				// The first read of a user is the sign-up's read of the new user.
				if banned || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "User" {
					return
				}
				banned = true
				changed, err := DisableUserBySystem(newcomer.Id, inviteAbuseRevokeReason)
				require.NoError(t, err)
				require.True(t, changed)
			}))
			t.Cleanup(func() { assert.NoError(t, DB.Callback().Query().Remove(name)) })

			if oauth {
				newcomer.FinalizeOAuthUserCreation(0)
			} else {
				newcomer.FinishInsert(0)
			}
			require.True(t, banned)
			stored := inviteAbuseUserState(t, newcomer.Id)
			assert.Equal(t, common.UserStatusDisabled, stored.Status, "the ban is kept")
			assert.EqualValues(t, 2, stored.AuthVersion, "only the ban moved the authentication version")
			var setting User
			require.NoError(t, DB.Select("setting").Where("id = ?", newcomer.Id).Take(&setting).Error)
			assert.NotEmpty(t, setting.GetSetting().SidebarModules, "the sidebar config is written")
			cached, err := GetUserCache(newcomer.Id)
			require.NoError(t, err)
			assert.Equal(t, common.UserStatusDisabled, cached.Status)
			assert.EqualValues(t, 2, cached.AuthVersion)
			assert.NotEmpty(t, cached.GetSetting().SidebarModules)
		})
	}
}

// The master node's tick sweeps right after the rule is turned on and after a
// setting changed, and otherwise once per InviteAbuseSweepInterval.
func testInviteAbuseSweepSchedule(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, false, 2, 500_000)
	status := func(user User) int { return inviteAbuseUserState(t, user.Id).Status }
	start := time.Now()

	first, _ := createInviteAbuseTestInviter(t, "first", common.RoleCommonUser, 2)
	RunInviteAbuseSweepIfDue(start)
	assert.Equal(t, common.UserStatusEnabled, status(first), "the rule is off")

	require.NoError(t, updateOptionMap("InviteAbuseBanEnabled", "true"))
	RunInviteAbuseSweepIfDue(start.Add(time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(first), "turning the rule on sweeps at once")

	second, _ := createInviteAbuseTestInviter(t, "second", common.RoleCommonUser, 2)
	RunInviteAbuseSweepIfDue(start.Add(2 * time.Minute))
	assert.Equal(t, common.UserStatusEnabled, status(second), "no sweep within the interval")

	require.NoError(t, updateOptionMap("InviteAbuseBanThreshold", "1"))
	RunInviteAbuseSweepIfDue(start.Add(3 * time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(second), "a new threshold sweeps at once")

	third, _ := createInviteAbuseTestInviter(t, "third", common.RoleCommonUser, 1)
	RunInviteAbuseSweepIfDue(start.Add(4 * time.Minute))
	assert.Equal(t, common.UserStatusEnabled, status(third))
	require.NoError(t, updateOptionMap("InviteAbuseBanMinRedeemQuota", "1000"))
	RunInviteAbuseSweepIfDue(start.Add(5 * time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(third), "a new minimum redeemed quota sweeps at once")

	fourth, _ := createInviteAbuseTestInviter(t, "fourth", common.RoleCommonUser, 1)
	RunInviteAbuseSweepIfDue(start.Add(5*time.Minute + InviteAbuseSweepInterval - time.Second))
	assert.Equal(t, common.UserStatusEnabled, status(fourth))
	RunInviteAbuseSweepIfDue(start.Add(5*time.Minute + InviteAbuseSweepInterval))
	assert.Equal(t, common.UserStatusDisabled, status(fourth), "the next periodic sweep")

	// The periodic option sync applies the saved values again: no change.
	later := start.Add(5*time.Minute + InviteAbuseSweepInterval)
	for key, value := range map[string]string{
		"InviteAbuseBanEnabled": "true", "InviteAbuseBanThreshold": "1",
		"InviteAbuseBanMinRedeemQuota": "1000", "InviteAbuseBanExemptPaidInviter": "true",
	} {
		require.NoError(t, updateOptionMap(key, value))
	}
	synced, _ := createInviteAbuseTestInviter(t, "synced", common.RoleCommonUser, 1)
	RunInviteAbuseSweepIfDue(later.Add(time.Minute))
	assert.Equal(t, common.UserStatusEnabled, status(synced), "applying the same settings again does not sweep")

	require.NoError(t, updateOptionMap("InviteAbuseBanEnabled", "false"))
	fifth, _ := createInviteAbuseTestInviter(t, "fifth", common.RoleCommonUser, 1)
	RunInviteAbuseSweepIfDue(later.Add(2 * time.Minute))
	assert.Equal(t, common.UserStatusEnabled, status(fifth))
	require.NoError(t, updateOptionMap("InviteAbuseBanEnabled", "true"))
	RunInviteAbuseSweepIfDue(later.Add(3 * time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(fifth), "turning the rule on again sweeps at once")

	// Off and on again between two ticks: no tick saw the rule off, and a
	// farm that reached the threshold meanwhile (no sign-up check ran) is
	// swept by the next tick all the same.
	require.NoError(t, updateOptionMap("InviteAbuseBanEnabled", "false"))
	sixth, _ := createInviteAbuseTestInviter(t, "sixth", common.RoleCommonUser, 1)
	require.NoError(t, updateOptionMap("InviteAbuseBanEnabled", "true"))
	RunInviteAbuseSweepIfDue(later.Add(4 * time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(sixth), "turning the rule off and on between two ticks sweeps at once")

	patron, _ := createInviteAbuseTestInviter(t, "patron", common.RoleCommonUser, 1)
	seedTestTopUp(t, TopUp{UserId: patron.Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay})
	RunInviteAbuseSweepIfDue(later.Add(5 * time.Minute))
	assert.Equal(t, common.UserStatusEnabled, status(patron))
	require.NoError(t, updateOptionMap("InviteAbuseBanExemptPaidInviter", "false"))
	RunInviteAbuseSweepIfDue(later.Add(6 * time.Minute))
	assert.Equal(t, common.UserStatusDisabled, status(patron), "no longer exempting paid inviters sweeps at once")
}

// Saving an option of the rule, or the periodic option sync applying one,
// wakes the master node's sweep only for a real change that calls for a new
// pass: the rule turned on, or one of its settings changed while it is on.
func TestInviteAbuseRuleChangesWakeTheSweep(t *testing.T) {
	useInviteAbuseSettings(t, false, 5, 500_000)
	woken := func() bool {
		select {
		case <-InviteAbuseSweepWake():
			return true
		default:
			return false
		}
	}
	woken() // a wake-up an earlier test left behind
	for _, step := range []struct {
		name, key, value string
		wakes            bool
	}{
		{"threshold changed while off", "InviteAbuseBanThreshold", "6", false},
		{"still off", "InviteAbuseBanEnabled", "false", false},
		{"turned on", "InviteAbuseBanEnabled", "true", true},
		{"still on", "InviteAbuseBanEnabled", "true", false},
		{"same threshold", "InviteAbuseBanThreshold", "6", false},
		{"new threshold", "InviteAbuseBanThreshold", "7", true},
		{"same minimum", "InviteAbuseBanMinRedeemQuota", "500000", false},
		{"new minimum", "InviteAbuseBanMinRedeemQuota", "1000", true},
		{"same exemption", "InviteAbuseBanExemptPaidInviter", "true", false},
		{"no exemption", "InviteAbuseBanExemptPaidInviter", "false", true},
		{"turned off", "InviteAbuseBanEnabled", "false", false},
	} {
		require.NoError(t, updateOptionMap(step.key, step.value))
		assert.Equal(t, step.wakes, woken(), step.name)
	}
}

// An administrator who turns the rule off or raises the threshold while a
// pass runs stops it at once: nothing more is disabled under the old
// settings, and the next tick does nothing while the rule is off or starts a
// new pass with the new settings.
func testInviteAbuseSettingsChangeStopsPass(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{name: "rule turned off", key: "InviteAbuseBanEnabled", value: "false"},
		{name: "threshold raised", key: "InviteAbuseBanThreshold", value: "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, true, 1, 500_000)
			first, _ := createInviteAbuseTestInviter(t, "farm0", common.RoleCommonUser, 1)
			for i := 1; i < 5; i++ {
				createInviteAbuseTestInviter(t, fmt.Sprintf("farm%d", i), common.RoleCommonUser, 1)
			}
			changed := false
			onInviteAbuseBan(t, func(InviteAbuseBan) {
				if !changed {
					changed = true
					require.NoError(t, updateOptionMap(tc.key, tc.value)) // what saving the option does
				}
			})

			start := time.Now()
			RunInviteAbuseSweepIfDue(start)
			require.True(t, changed)
			assert.Equal(t, inviteAbuseTestUserIds(first), disabledInviteAbuseTestUsers(t), "only the ban made before the change")
			RunInviteAbuseSweepIfDue(start.Add(time.Minute))
			assert.Equal(t, inviteAbuseTestUserIds(first), disabledInviteAbuseTestUsers(t), "the next tick does not finish the old pass")
		})
	}

	// A pass that stops after it got through an inviter does not go on from
	// there with the new settings: the next tick starts over, so an inviter
	// before the stop is checked with them too.
	t.Run("threshold lowered after the first inviter", func(t *testing.T) {
		resetInviteAbuseTables(t)
		useInviteAbuseSettings(t, true, 2, 500_000)
		small, smallInvitees := createInviteAbuseTestInviter(t, "small", common.RoleCommonUser, 1)
		farm0, farm0Invitees := createInviteAbuseTestInviter(t, "farm0", common.RoleCommonUser, 2)
		farm1, farm1Invitees := createInviteAbuseTestInviter(t, "farm1", common.RoleCommonUser, 2)
		onInviteAbuseBan(t, func(ban InviteAbuseBan) {
			if ban.UserId == farm1.Id {
				require.NoError(t, updateOptionMap("InviteAbuseBanThreshold", "1"))
			}
		})

		start := time.Now()
		RunInviteAbuseSweepIfDue(start)
		stopped := append(append([]User{farm0}, farm0Invitees...), farm1)
		assert.Equal(t, inviteAbuseTestUserIds(stopped...), disabledInviteAbuseTestUsers(t), "the pass stops in the second inviter")
		RunInviteAbuseSweepIfDue(start.Add(time.Minute))
		all := append(append(append([]User{small}, smallInvitees...), stopped...), farm1Invitees...)
		assert.Equal(t, inviteAbuseTestUserIds(all...), disabledInviteAbuseTestUsers(t), "the inviter before the stop is disabled with the new threshold")
	})
}

// A sweep's candidate batch only pre-selects inviters. A recharge that comes
// in while the pass is busy with an earlier inviter of the batch — by an
// invited user, or by the inviter while paid inviters are exempt — clears
// the later farm, and an account an administrator released after the pass
// loaded the invited users is not disabled again. While one inviter's
// accounts are disabled, a recharge clears it when the verdict is taken
// again and a release of the inviter keeps its remaining accounts enabled.
func testInviteAbuseVerdictAtDisableTime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		payer func(inviter User, invitees []User) User
	}{
		{name: "an invited user", payer: func(_ User, invitees []User) User { return invitees[0] }},
		{name: "the inviter", payer: func(inviter User, _ []User) User { return inviter }},
	} {
		t.Run(tc.name+" pays while the inviter's accounts are disabled", func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, true, 3, 500_000)
			savedInterval := inviteAbuseRecheckInterval
			inviteAbuseRecheckInterval = 0 // the verdict is taken again before every account
			t.Cleanup(func() { inviteAbuseRecheckInterval = savedInterval })
			inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 3)
			payer := tc.payer(inviter, invitees)
			paid := false
			onInviteAbuseBan(t, func(ban InviteAbuseBan) {
				if !paid && ban.UserId == inviter.Id {
					paid = true // the inviter is disabled; its invited users are next
					seedTestTopUp(t, TopUp{UserId: payer.Id, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay})
				}
			})

			disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
			require.NoError(t, err)
			require.True(t, paid)
			assert.True(t, finished, "the inviter is no longer abusive")
			assert.Equal(t, 1, disabled)
			assert.Equal(t, inviteAbuseTestUserIds(inviter), disabledInviteAbuseTestUsers(t), "its invited users stay enabled")
		})

		t.Run(tc.name+" pays during the pass", func(t *testing.T) {
			resetInviteAbuseTables(t)
			useInviteAbuseSettings(t, true, 2, 500_000)
			first, firstInvitees := createInviteAbuseTestInviter(t, "first", common.RoleCommonUser, 2)
			second, secondInvitees := createInviteAbuseTestInviter(t, "second", common.RoleCommonUser, 2)
			payer := tc.payer(second, secondInvitees)
			paid := false
			onInviteAbuseBan(t, func(InviteAbuseBan) {
				if !paid {
					paid = true // the pass has just disabled the first inviter
					seedTestTopUp(t, TopUp{UserId: payer.Id, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay})
				}
			})

			_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
			require.NoError(t, err)
			require.True(t, complete)
			require.True(t, paid)
			assert.Equal(t, inviteAbuseTestUserIds(append([]User{first}, firstInvitees...)...), disabledInviteAbuseTestUsers(t),
				"the second inviter and its invited users stay enabled")
		})
	}

	t.Run("an administrator releases an invited user during the pass", func(t *testing.T) {
		resetInviteAbuseTables(t)
		useInviteAbuseSettings(t, true, 2, 500_000)
		inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 3)
		released := false
		onInviteAbuseBan(t, func(InviteAbuseBan) {
			if !released {
				// The inviter is disabled and its invited users are loaded;
				// the oldest one, disabled last, is enabled again meanwhile.
				released = true
				releaseInviteAbuseTestUser(t, invitees[0])
			}
		})

		disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		assert.True(t, finished)
		require.True(t, released)
		assert.Equal(t, 3, disabled)
		assert.Equal(t, inviteAbuseTestUserIds(inviter, invitees[1], invitees[2]), disabledInviteAbuseTestUsers(t))
		assert.Zero(t, countSystemLogs(t, invitees[0].Id, "invited users have no recharge."))
	})

	t.Run("an administrator releases the inviter while its invited users are disabled", func(t *testing.T) {
		resetInviteAbuseTables(t)
		useInviteAbuseSettings(t, true, 3, 500_000)
		inviter, invitees := createInviteAbuseTestInviter(t, "farmer", common.RoleCommonUser, 3)
		released := false
		onInviteAbuseBan(t, func(ban InviteAbuseBan) {
			if !released && ban.UserId == inviter.Id {
				released = true // what ManageUser "enable" does: release the record, enable the account
				ok, err := ReleaseInviteAbuseBan(inviter.Id, 1)
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, DB.Model(&User{}).Where("id = ?", inviter.Id).Update("status", common.UserStatusEnabled).Error)
			}
		})

		disabled, finished, err := enforceInviteAbuseRuleOnInviter(inviter.Id, CurrentInviteAbuseSettings(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.True(t, released)
		assert.True(t, finished)
		assert.Equal(t, 1, disabled, "only the inviter, before its release")
		assert.Empty(t, disabledInviteAbuseTestUsers(t), "the invited users of an exempt inviter stay enabled")
		for _, invitee := range invitees {
			assert.Zero(t, countSystemLogs(t, invitee.Id, "invited users have no recharge."), invitee.Username)
		}
	})
}

// A pass reads the candidates in batches and stops at its time budget, or in
// the middle of an inviter, without losing its place: it continues with the
// inviter it did not finish.
func testInviteAbuseSweepBatches(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, true, 1, 500_000)
	savedBatch := inviteAbuseBatchSize
	inviteAbuseBatchSize = 2
	t.Cleanup(func() { inviteAbuseBatchSize = savedBatch })
	var all []User
	for i := range 5 {
		inviter, invitees := createInviteAbuseTestInviter(t, fmt.Sprintf("batch%d", i), common.RoleCommonUser, 1)
		all = append(all, inviter, invitees[0])
	}
	settings := CurrentInviteAbuseSettings()

	cursor, complete, err := sweepInviteAbuse(settings, 0, time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, complete)
	assert.Zero(t, cursor)
	assert.Empty(t, disabledInviteAbuseTestUsers(t), "the budget was spent before the first inviter")

	// The third inviter's pass stops right after the inviter itself.
	onInviteAbuseBan(t, func(ban InviteAbuseBan) {
		if ban.UserId == all[4].Id {
			common.InviteAbuseBanThreshold = 2
		}
	})
	cursor, complete, err = sweepInviteAbuse(settings, cursor, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, complete)
	assert.Equal(t, all[2].Id, cursor, "the pass continues with the third inviter")
	assert.Equal(t, inviteAbuseTestUserIds(all[:5]...), disabledInviteAbuseTestUsers(t))

	common.InviteAbuseBanThreshold = 1
	cursor, complete, err = sweepInviteAbuse(settings, cursor, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Equal(t, inviteAbuseTestUserIds(all...), disabledInviteAbuseTestUsers(t))

	preview, err := PreviewInviteAbuse(settings)
	require.NoError(t, err)
	assert.Equal(t, 5, preview.AbusiveInviters)
	assert.Zero(t, preview.InvitersToDisable)
	assert.Zero(t, preview.InviteesToDisable)
}

func testInviteAbusePreview(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, false, 2, 500_000)
	// a: enabled, invited one user of each kind plus a deleted one, who is
	// counted but cannot be disabled.
	a, aInvitees := createInviteAbuseTestInviter(t, "a", common.RoleCommonUser, 2)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", aInvitees[1].Id).Update("status", common.UserStatusDisabled).Error)
	createInviteAbuseTestUser(t, "a-admin", a.Id, common.RoleAdminUser)
	releaseInviteAbuseTestUser(t, createInviteAbuseTestUser(t, "a-exempt", a.Id, common.RoleCommonUser))
	aDeleted := createInviteAbuseTestUser(t, "a-deleted", a.Id, common.RoleCommonUser)
	require.NoError(t, DB.Delete(&aDeleted).Error)
	// b: disabled already, two enabled invited users.
	b, _ := createInviteAbuseTestInviter(t, "b", common.RoleCommonUser, 2)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", b.Id).Update("status", common.UserStatusDisabled).Error)
	// Not abusive or not checked.
	_, paid := createInviteAbuseTestInviter(t, "paid", common.RoleCommonUser, 3)
	seedTestTopUp(t, TopUp{UserId: paid[2].Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay})
	patron, _ := createInviteAbuseTestInviter(t, "patron", common.RoleCommonUser, 2)
	seedTestTopUp(t, TopUp{UserId: patron.Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay})
	createInviteAbuseTestInviter(t, "admin", common.RoleAdminUser, 2)
	exempt, _ := createInviteAbuseTestInviter(t, "exempt", common.RoleCommonUser, 2)
	releaseInviteAbuseTestUser(t, exempt)
	deleted, _ := createInviteAbuseTestInviter(t, "deleted", common.RoleCommonUser, 2)
	require.NoError(t, DB.Delete(&deleted).Error)
	createInviteAbuseTestInviter(t, "few", common.RoleCommonUser, 1)

	preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
	require.NoError(t, err)
	assert.Equal(t, &InviteAbusePreview{
		Threshold: 2, MinRedeemQuota: 500_000, ExemptPaidInviter: true, AbusiveInviters: 2, InvitersToDisable: 1, InviteesToDisable: 3,
		Items: []InviteAbusePreviewItem{
			{Id: a.Id, Username: "a", DisplayName: "a", Status: common.UserStatusEnabled, InviteCount: 5, InviteesToDisable: 1},
			{Id: b.Id, Username: "b", DisplayName: "b", Status: common.UserStatusDisabled, InviteCount: 2, InviteesToDisable: 2},
		},
	}, preview)

	unexempted, err := PreviewInviteAbuse(InviteAbuseSettings{Threshold: 2, MinRedeemQuota: 500_000})
	require.NoError(t, err)
	assert.Equal(t, &InviteAbusePreview{
		Threshold: 2, MinRedeemQuota: 500_000, AbusiveInviters: 3, InvitersToDisable: 2, InviteesToDisable: 5,
		Items: []InviteAbusePreviewItem{
			{Id: a.Id, Username: "a", DisplayName: "a", Status: common.UserStatusEnabled, InviteCount: 5, InviteesToDisable: 1},
			{Id: patron.Id, Username: "patron", DisplayName: "patron", Status: common.UserStatusEnabled, InviteCount: 2, InviteesToDisable: 2},
			{Id: b.Id, Username: "b", DisplayName: "b", Status: common.UserStatusDisabled, InviteCount: 2, InviteesToDisable: 2},
		},
	}, unexempted, "without the exemption the inviter who paid is abusive like the others")

	stricter, err := PreviewInviteAbuse(InviteAbuseSettings{Threshold: 6, MinRedeemQuota: 500_000})
	require.NoError(t, err)
	assert.Zero(t, stricter.AbusiveInviters)
	assert.Empty(t, stricter.Items)
	assert.NotNil(t, stricter.Items, "an empty list, not null")

	// The preview counts exactly what a sweep disables once the rule is on.
	common.InviteAbuseBanEnabled = true
	before := len(disabledInviteAbuseTestUsers(t))
	_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, complete)
	assert.Len(t, disabledInviteAbuseTestUsers(t), before+preview.InvitersToDisable+int(preview.InviteesToDisable))
	after, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
	require.NoError(t, err)
	assert.Equal(t, 2, after.AbusiveInviters)
	assert.Zero(t, after.InvitersToDisable)
	assert.Zero(t, after.InviteesToDisable)
}

// a invites b and c, and b, an abusive inviter itself, invites d and e. The
// totals count b once, as an inviter, so they add up to what a sweep
// disables; each item keeps its own inviter's numbers.
func testInviteAbusePreviewChain(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, true, 2, 500_000)
	a := createInviteAbuseTestUser(t, "a", 0, common.RoleCommonUser)
	b := createInviteAbuseTestUser(t, "b", a.Id, common.RoleCommonUser)
	createInviteAbuseTestUser(t, "c", a.Id, common.RoleCommonUser)
	createInviteAbuseTestUser(t, "d", b.Id, common.RoleCommonUser)
	createInviteAbuseTestUser(t, "e", b.Id, common.RoleCommonUser)

	preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
	require.NoError(t, err)
	assert.Equal(t, 2, preview.AbusiveInviters)
	assert.Equal(t, 2, preview.InvitersToDisable)
	assert.EqualValues(t, 3, preview.InviteesToDisable)
	require.Len(t, preview.Items, 2)
	for _, item := range preview.Items {
		assert.EqualValues(t, 2, item.InviteesToDisable, item.Username)
	}

	_, complete, err := sweepInviteAbuse(CurrentInviteAbuseSettings(), 0, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, complete)
	assert.Len(t, disabledInviteAbuseTestUsers(t), 5)
	assert.Len(t, disabledInviteAbuseTestUsers(t), preview.InvitersToDisable+int(preview.InviteesToDisable))
}

func testInviteAbusePreviewTruncation(t *testing.T) {
	resetInviteAbuseTables(t)
	useInviteAbuseSettings(t, false, 1, 500_000)
	inviters := make([]User, 0, inviteAbusePreviewItems+1)
	for i := range inviteAbusePreviewItems + 1 {
		name := fmt.Sprintf("many%03d", i)
		inviters = append(inviters, User{Username: name, DisplayName: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: name, AuthVersion: 1})
	}
	require.NoError(t, DB.CreateInBatches(&inviters, 50).Error)
	invitees := make([]User, 0, len(inviters)+1)
	for _, inviter := range inviters {
		name := inviter.Username + "-1"
		invitees = append(invitees, User{Username: name, DisplayName: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: name, InviterId: inviter.Id, AuthVersion: 1})
	}
	top := inviters[0]
	invitees = append(invitees, User{Username: "many-top-2", DisplayName: "many-top-2", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "many-top-2", InviterId: top.Id, AuthVersion: 1})
	require.NoError(t, DB.CreateInBatches(&invitees, 50).Error)

	preview, err := PreviewInviteAbuse(CurrentInviteAbuseSettings())
	require.NoError(t, err)
	assert.Equal(t, inviteAbusePreviewItems+1, preview.AbusiveInviters)
	assert.True(t, preview.Truncated)
	require.Len(t, preview.Items, inviteAbusePreviewItems)
	assert.Equal(t, top.Id, preview.Items[0].Id, "most invited users first")
	assert.EqualValues(t, 2, preview.Items[0].InviteCount)
	assert.Equal(t, inviters[len(inviters)-1].Id, preview.Items[1].Id, "then the newest inviter")
	assert.Greater(t, preview.Items[1].Id, preview.Items[2].Id)
}

// Disabling ends cached access when Redis is on: the user hash is republished
// with the disabled status and the new authentication version, cached API
// tokens are dropped and sessions are revoked.
func TestDisableUserBySystemEndsCachedAccess(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	user := User{Username: "system-disabled", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "system-disabled", AuthVersion: 1}
	require.NoError(t, DB.Create(&user).Error)
	token := Token{UserId: user.Id, Key: "system-disabled-token-key", Name: "system-disabled", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, token.Insert())
	require.NoError(t, cacheSetTokenForTest(token))
	require.True(t, server.Exists(getTokenCacheKey(token.Key)))
	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	require.Equal(t, common.UserStatusEnabled, cached.Status)
	now := common.GetTimestamp()
	require.NoError(t, DB.Create(&UserSession{
		SID: "system-disabled-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive,
		RefreshHash: "system-disabled-refresh", LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	changed, err := DisableUserBySystem(user.Id, inviteAbuseRevokeReason)
	require.NoError(t, err)
	assert.True(t, changed)
	cached, err = GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, common.UserStatusDisabled, cached.Status)
	assert.EqualValues(t, 2, cached.AuthVersion)
	assert.False(t, server.Exists(getTokenCacheKey(token.Key)), "the cached token is dropped")
	var session UserSession
	require.NoError(t, DB.First(&session, "sid = ?", "system-disabled-session").Error)
	assert.Equal(t, UserSessionStatusRevoked, session.Status)
	assert.Equal(t, inviteAbuseRevokeReason, session.RevokedReason)

	changed, err = DisableUserBySystem(user.Id, inviteAbuseRevokeReason)
	require.NoError(t, err)
	assert.False(t, changed, "a disabled account is left alone")
	var stored User
	require.NoError(t, DB.Select("auth_version").Where("id = ?", user.Id).Take(&stored).Error)
	assert.EqualValues(t, 2, stored.AuthVersion)

	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusEnabled).Error)
	require.NoError(t, DB.Delete(&user).Error)
	changed, err = DisableUserBySystem(user.Id, inviteAbuseRevokeReason)
	require.NoError(t, err)
	assert.False(t, changed, "a deleted account is left alone")
}
