package authz

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	return db
}

func TestInitSeedsBuiltInRolesAndPoliciesOnce(t *testing.T) {
	db := newAuthzTestDB(t)

	require.NoError(t, Init(db))
	require.NoError(t, Init(db))

	// root is a superuser role and is granted everything implicitly, so only the
	// admin baseline is written as explicit policy rows.
	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Count(&count).Error)
	assert.Equal(t, int64(len(PermissionsForRole(BuiltInRoleAdmin))), count)

	var roles []model.AuthzRole
	require.NoError(t, db.Order("sort asc").Find(&roles).Error)
	require.Len(t, roles, 3)
	assert.Equal(t, BuiltInRoleRoot, roles[0].Key)
	assert.Equal(t, BuiltInRoleAdmin, roles[1].Key)
	assert.Equal(t, BuiltInRoleDeputy, roles[2].Key)

	assert.True(t, Can(1, common.RoleRootUser, ChannelSensitiveWrite))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelRead))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelOperate))
	assert.True(t, Can(2, common.RoleAdminUser, ChannelWrite))
	assert.False(t, Can(2, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(3, common.RoleCommonUser, ChannelRead))
	for _, permission := range []Permission{BillingRead, BillingWrite, RedemptionRead, RedemptionWrite} {
		assert.True(t, Can(1, common.RoleRootUser, permission), permission)
		assert.True(t, Can(2, common.RoleAdminUser, permission), permission)
		assert.False(t, Can(3, common.RoleCommonUser, permission), permission)
	}
}

func TestInitOnSlaveOnlyLoadsPolicies(t *testing.T) {
	wasMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))

	require.NoError(t, Init(db))

	var roleCount int64
	require.NoError(t, db.Model(&model.AuthzRole{}).Count(&roleCount).Error)
	assert.Equal(t, int64(0), roleCount)
	var policyCount int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Count(&policyCount).Error)
	assert.Equal(t, int64(0), policyCount)
	assert.False(t, Can(2, common.RoleAdminUser, ChannelRead))
}

func TestLegacyScopedPoliciesDoNotExpandPermissions(t *testing.T) {
	for _, master := range []bool{true, false} {
		name := "master"
		if !master {
			name = "slave"
		}
		t.Run(name, func(t *testing.T) {
			db := newAuthzTestDB(t)
			common.IsMasterNode = master
			rules := []model.CasbinRule{
				{Ptype: "p", V0: "role:vendor", V1: "channel", V2: "read", V3: "allow", V4: "own"},
				{Ptype: "p", V0: UserSubject(42), V1: "channel", V2: "read", V3: "allow", V4: "own"},
				{Ptype: "p", V0: UserSubject(43), V1: "channel", V2: "sensitive_write", V3: "allow", V4: "all"},
				{Ptype: "p", V0: UserSubject(44), V1: "channel", V2: "secret_view", V3: "deny", V4: "all"},
				{Ptype: "p", V0: UserSubject(45), V1: "channel", V2: "read"},
				{Ptype: "p", V0: UserSubject(46), V1: "channel", V2: "read", V3: "allow", V4: "unknown-scope"},
				{Ptype: "p", V0: UserSubject(47), V1: "channel", V2: "read", V3: "allow", V5: "own"},
				{Ptype: "p", V0: UserSubject(48), V1: "channel", V2: "read", V3: "allow", V4: "own"},
				{Ptype: "p", V0: UserSubject(48), V1: "channel", V2: "read", V3: "allow", V4: "all"},
				{Ptype: "p", V0: RoleSubject(BuiltInRoleAdmin), V1: "channel", V2: "operate", V3: "allow", V4: "own"},
				{Ptype: "p", V0: UserSubject(50), V1: "channel", V2: "sensitive_write", V3: "allow"},
				{Ptype: "g", V0: UserSubject(99), V1: RoleSubject(BuiltInRoleAdmin)},
			}
			require.NoError(t, db.Create(&rules).Error)
			ids := make([]uint, len(rules))
			for i := range rules {
				ids[i] = rules[i].Id
			}
			for range 2 {
				require.NoError(t, Init(db))
				require.NoError(t, ReloadPolicy())
				assert.False(t, Can(42, common.RoleAdminUser, ChannelRead), "own must not fall back to the admin allow baseline")
				assert.True(t, Can(43, common.RoleAdminUser, ChannelSensitiveWrite))
				assert.False(t, Can(44, common.RoleAdminUser, ChannelSecretView))
				assert.True(t, Can(45, common.RoleAdminUser, ChannelRead))
				for _, userID := range []int{46, 47, 48} {
					assert.False(t, Can(userID, common.RoleAdminUser, ChannelRead))
				}
				assert.False(t, Can(51, common.RoleAdminUser, ChannelOperate), "reseed must not erase a scoped role restriction")
				assert.True(t, Can(50, common.RoleAdminUser, ChannelSensitiveWrite))
				assert.False(t, Can(99, common.RoleCommonUser, ChannelRead))
				var stored []model.CasbinRule
				require.NoError(t, db.Where("id IN ?", ids).Order("id").Find(&stored).Error)
				assert.Equal(t, rules, stored, "legacy rows must remain available for administrator review")
			}
		})
	}
}

func TestSetUserPermissionsStoresOnlyOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, SetUserPermissions(42, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
			"unknown":            true,
		},
		"unknown": {
			ActionRead: true,
		},
	}))

	assert.True(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(42, common.RoleAdminUser, ChannelWrite))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          false,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		},
		ResourceTaskPlugin: {
			ActionBind: false,
		},
		ResourceAudit:      {ActionRead: false},
		ResourceBilling:    {ActionRead: true, ActionWrite: true},
		ResourceRedemption: {ActionRead: true, ActionWrite: true},
	}, ExplicitUserPermissions(42))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionSensitiveWrite: true,
			ActionWrite:          false,
		},
	}, ExplicitUserOverrides(42))

	var userPolicyCount int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where("v0 = ?", UserSubject(42)).Count(&userPolicyCount).Error)
	assert.Equal(t, int64(2), userPolicyCount)

	require.NoError(t, SetUserPermissions(42, PermissionsMap{ResourceChannel: {
		ActionRead:           true,
		ActionOperate:        true,
		ActionWrite:          true,
		ActionSensitiveWrite: false,
		ActionSecretView:     false,
	}}))
	assert.False(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.Equal(t, PermissionsMap{
		ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: false,
			ActionSecretView:     false,
		},
		ResourceTaskPlugin: {
			ActionBind: false,
		},
		ResourceAudit:      {ActionRead: false},
		ResourceBilling:    {ActionRead: true, ActionWrite: true},
		ResourceRedemption: {ActionRead: true, ActionWrite: true},
	}, ExplicitUserPermissions(42))
	assert.Empty(t, ExplicitUserOverrides(42))
}

func TestClearUserAuthorizationRemovesOverrides(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, SetUserPermissions(90, PermissionsMap{ResourceChannel: {
		ActionWrite:          false,
		ActionSensitiveWrite: true,
	}}))

	assert.True(t, Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(90, common.RoleAdminUser, ChannelWrite))

	require.NoError(t, ClearUserAuthorization(90))

	assert.Empty(t, ExplicitUserOverrides(90))
	assert.True(t, Can(90, common.RoleAdminUser, ChannelRead))
	assert.True(t, Can(90, common.RoleAdminUser, ChannelWrite))
	assert.False(t, Can(90, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(90, common.RoleCommonUser, ChannelRead))
}

func TestSetUserPermissionsInTxDoesNotMutateEnforcerBeforeReload(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return SetUserPermissionsInTx(tx, 42, PermissionsMap{ResourceChannel: {
			ActionRead:           true,
			ActionOperate:        true,
			ActionWrite:          true,
			ActionSensitiveWrite: true,
			ActionSecretView:     false,
		}})
	}))

	assert.False(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
	require.NoError(t, ReloadPolicy())
	assert.True(t, Can(42, common.RoleAdminUser, ChannelSensitiveWrite))
}

func TestSetUserPermissionsInTxRollbackLeavesNoPolicy(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, SetUserPermissionsInTx(tx, 43, PermissionsMap{ResourceChannel: {
		ActionSensitiveWrite: true,
	}}))
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, ReloadPolicy())

	assert.False(t, Can(43, common.RoleAdminUser, ChannelSensitiveWrite))
	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where("v0 = ?", UserSubject(43)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestAdapterAddPolicyIsIdempotent(t *testing.T) {
	db := newAuthzTestDB(t)
	adapter := newGormAdapter(db)
	rule := []string{UserSubject(55), ResourceChannel, ActionSensitiveWrite, EffectAllow}

	require.NoError(t, adapter.AddPolicy("p", "p", rule))
	require.NoError(t, adapter.AddPolicy("p", "p", rule))

	var count int64
	require.NoError(t, db.Model(&model.CasbinRule{}).Where(
		"ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ?",
		"p",
		UserSubject(55),
		ResourceChannel,
		ActionSensitiveWrite,
		EffectAllow,
	).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCapabilitiesUseCatalogShape(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	capabilities := Capabilities(7, common.RoleAdminUser)

	assert.True(t, capabilities[ResourceChannel][ActionRead])
	assert.True(t, capabilities[ResourceChannel][ActionOperate])
	assert.True(t, capabilities[ResourceChannel][ActionWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSensitiveWrite])
	assert.False(t, capabilities[ResourceChannel][ActionSecretView])
	assert.False(t, capabilities[ResourceTaskPlugin][ActionBind])
}

func TestTaskPluginBindIsRootOnlyUntilGranted(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))

	var bindAction *ActionDefinition
	for _, resource := range Catalog() {
		if resource.Resource != ResourceTaskPlugin {
			continue
		}
		assert.Equal(t, "Task Plugin", resource.LabelKey)
		for i := range resource.Actions {
			if resource.Actions[i].Action == ActionBind {
				bindAction = &resource.Actions[i]
			}
		}
	}
	require.NotNil(t, bindAction)
	assert.Equal(t, "Bind task plugins", bindAction.LabelKey)
	assert.Equal(t, "List registered task plugins and bind them when creating or editing task plugin channels.", bindAction.DescriptionKey)
	assert.Empty(t, bindAction.DefaultRoles)

	assert.False(t, Can(2, common.RoleAdminUser, TaskPluginBind))
	assert.True(t, Can(1, common.RoleRootUser, TaskPluginBind))

	enforcer := currentEnforcer()
	require.NotNil(t, enforcer)
	_, err := enforcer.AddPolicy(RoleSubject(BuiltInRoleAdmin), ResourceTaskPlugin, ActionBind, EffectAllow)
	require.NoError(t, err)
	assert.True(t, Can(2, common.RoleAdminUser, TaskPluginBind))

	_, err = enforcer.RemovePolicy(RoleSubject(BuiltInRoleAdmin), ResourceTaskPlugin, ActionBind, EffectAllow)
	require.NoError(t, err)
	assert.False(t, Can(2, common.RoleAdminUser, TaskPluginBind))
}

func assignDeputy(t *testing.T, db *gorm.DB, userID int) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return AssignDeputyInTx(tx, userID)
	}))
}

// userRules lists the casbin rows of a user subject as "ptype resource action effect".
func userRules(t *testing.T, db *gorm.DB, userID int) []string {
	t.Helper()
	var rules []model.CasbinRule
	require.NoError(t, db.Where("v0 = ?", UserSubject(userID)).Order("ptype, v1, v2").Find(&rules).Error)
	result := make([]string, 0, len(rules))
	for _, rule := range rules {
		result = append(result, rule.Ptype+" "+rule.V1+" "+rule.V2+" "+rule.V3)
	}
	return result
}

func TestDeputyAdminBaseline(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	const admin, deputy = 30, 31
	assignDeputy(t, db, deputy)
	// The same override rows for both: a deputy decides like an admin outside
	// recharge resources, overrides included.
	overrides := PermissionsMap{
		ResourceChannel: {ActionSensitiveWrite: true, ActionWrite: false},
		ResourceAudit:   {ActionRead: true},
	}
	require.NoError(t, SetUserPermissions(admin, overrides))
	require.NoError(t, SetUserPermissions(deputy, overrides))

	var deputyGrants PermissionsMap
	for _, role := range Roles() {
		if role.Key == BuiltInRoleDeputy {
			deputyGrants = role.Grants
			assert.Equal(t, "Deputy Admin", role.Name)
			assert.False(t, role.Superuser)
		}
	}
	require.NotNil(t, deputyGrants)

	recharge := map[string]bool{}
	for _, resource := range Catalog() {
		recharge[resource.Resource] = resource.Recharge
		for _, action := range resource.Actions {
			permission := Permission{Resource: resource.Resource, Action: action.Action}
			adminAllowed := Can(admin, common.RoleAdminUser, permission)
			assert.Equal(t, !resource.Recharge && adminAllowed, Can(deputy, common.RoleAdminUser, permission), permission)
			assert.Equal(t, !resource.Recharge && actionHasRole(action, BuiltInRoleAdmin), deputyGrants[resource.Resource][action.Action], permission)
		}
	}
	assert.Equal(t, map[string]bool{
		ResourceAudit: false, ResourceChannel: false, ResourceBilling: true, ResourceRedemption: true, ResourceTaskPlugin: false,
	}, recharge)
	assert.True(t, Can(deputy, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.True(t, Can(deputy, common.RoleAdminUser, AuditRead))
	assert.False(t, Can(deputy, common.RoleAdminUser, ChannelWrite))
	assert.Equal(t, PermissionsMap{
		ResourceAudit:      {ActionRead: true},
		ResourceChannel:    {ActionRead: true, ActionOperate: true, ActionWrite: false, ActionSensitiveWrite: true, ActionSecretView: false},
		ResourceBilling:    {ActionRead: false, ActionWrite: false},
		ResourceRedemption: {ActionRead: false, ActionWrite: false},
		ResourceTaskPlugin: {ActionBind: false},
	}, Capabilities(deputy, common.RoleAdminUser))
}

func TestDeputyRestrictionReadsTheDatabase(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	const deputy = 40
	assert.True(t, Can(deputy, common.RoleAdminUser, BillingRead))

	// Committed without ReloadPolicy: the decision follows at once.
	assignDeputy(t, db, deputy)
	for _, permission := range []Permission{BillingRead, BillingWrite, RedemptionRead, RedemptionWrite} {
		assert.False(t, Can(deputy, common.RoleAdminUser, permission), permission)
	}
	assert.True(t, Can(deputy, common.RoleAdminUser, ChannelRead))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return UnassignDeputyInTx(tx, deputy) }))
	assert.True(t, Can(deputy, common.RoleAdminUser, BillingRead))

	// An allow row stored for a deputy never applies.
	assignDeputy(t, db, deputy)
	require.NoError(t, db.Create(&model.CasbinRule{Ptype: "p", V0: UserSubject(deputy), V1: ResourceBilling, V2: ActionRead, V3: EffectAllow}).Error)
	require.NoError(t, ReloadPolicy())
	assert.False(t, Can(deputy, common.RoleAdminUser, BillingRead))

	// The marker only counts for role 10: root stays a superuser, a common
	// user still has no admin permission.
	assignDeputy(t, db, 41)
	assignDeputy(t, db, 42)
	assert.True(t, Can(41, common.RoleRootUser, BillingRead))
	assert.False(t, Can(42, common.RoleCommonUser, ChannelRead))
	assert.False(t, Can(42, common.RoleCommonUser, BillingRead))

	// A legacy role:admin g row still grants nothing, and g rows do not break
	// reloading.
	require.NoError(t, db.Create(&model.CasbinRule{Ptype: "g", V0: UserSubject(99), V1: RoleSubject(BuiltInRoleAdmin)}).Error)
	require.NoError(t, ReloadPolicy())
	assert.False(t, Can(99, common.RoleCommonUser, ChannelRead))
	assert.True(t, Can(43, common.RoleAdminUser, BillingRead))

	// The marker cannot be read: recharge is refused, the rest is unaffected.
	require.NoError(t, db.Migrator().RenameTable("casbin_rule", "casbin_rule_unavailable"))
	assert.False(t, Can(43, common.RoleAdminUser, BillingRead))
	assert.True(t, Can(43, common.RoleAdminUser, ChannelRead))
	assert.True(t, Can(1, common.RoleRootUser, BillingRead))
	require.NoError(t, db.Migrator().RenameTable("casbin_rule_unavailable", "casbin_rule"))
	assert.True(t, Can(43, common.RoleAdminUser, BillingRead))
}

func TestDeputyOverridesAndClear(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, Init(db))
	const admin, deputy = 50, 51
	assignDeputy(t, db, deputy)

	err := db.Transaction(func(tx *gorm.DB) error {
		return SetUserPermissionsInTx(tx, deputy, PermissionsMap{
			ResourceChannel: {ActionSensitiveWrite: true},
			ResourceBilling: {ActionRead: true, ActionWrite: false},
		})
	})
	var message *common.Message
	require.ErrorAs(t, err, &message)
	assert.Equal(t, "Deputy admins cannot hold recharge permissions", message.Key)
	assert.Equal(t, []string{"g role:deputy  "}, userRules(t, db, deputy))

	matrix := PermissionsMap{
		ResourceChannel:    {ActionRead: true, ActionOperate: true, ActionWrite: false, ActionSensitiveWrite: true, ActionSecretView: false},
		ResourceBilling:    {ActionRead: false, ActionWrite: false},
		ResourceRedemption: {ActionRead: false, ActionWrite: false},
	}
	for _, userID := range []int{admin, deputy} {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return SetUserPermissionsInTx(tx, userID, matrix)
		}))
	}
	channelRules := []string{"p channel sensitive_write allow", "p channel write deny"}
	assert.Equal(t, append([]string{
		"p billing read deny", "p billing write deny",
	}, append(channelRules, "p redemption read deny", "p redemption write deny")...), userRules(t, db, admin))
	assert.Equal(t, append([]string{"g role:deputy  "}, channelRules...), userRules(t, db, deputy))

	// Converting an admin with recharge rows: the rows go, the marker is
	// written once.
	assignDeputy(t, db, admin)
	assignDeputy(t, db, admin)
	assert.Equal(t, append([]string{"g role:deputy  "}, channelRules...), userRules(t, db, admin))
	require.NoError(t, ReloadPolicy())
	assert.True(t, Can(admin, common.RoleAdminUser, ChannelSensitiveWrite))
	assert.False(t, Can(admin, common.RoleAdminUser, BillingRead))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ClearUserAuthorizationInTx(tx, admin)
	}))
	assert.Empty(t, userRules(t, db, admin))
	deputyAdmin, err := IsDeputy(admin)
	require.NoError(t, err)
	assert.False(t, deputyAdmin)

	require.NoError(t, db.Create(&[]model.CasbinRule{
		{Ptype: "g", V0: "user:abc", V1: RoleSubject(BuiltInRoleDeputy)},
		{Ptype: "g", V0: "user:+7", V1: RoleSubject(BuiltInRoleDeputy)},
		{Ptype: "g", V0: "role:admin", V1: RoleSubject(BuiltInRoleDeputy)},
		{Ptype: "g", V0: UserSubject(52), V1: RoleSubject(BuiltInRoleAdmin)},
	}).Error)
	ids, err := AllDeputyUserIDs()
	require.NoError(t, err)
	assert.Equal(t, []int{deputy}, ids)
}

// openDeputyMatrixDB returns an empty authz schema on dialect. MySQL and
// PostgreSQL run when TEST_MYSQL_DSN / TEST_POSTGRES_DSN name a dedicated test
// database: casbin_rule ignores table prefixes, so the tables are dropped and
// recreated there.
func openDeputyMatrixDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	if dialect == "sqlite" {
		return newAuthzTestDB(t)
	}
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	if dsn == "" {
		t.Skipf("TEST_%s_DSN not configured", strings.ToUpper(dialect))
	}
	driver := mysql.Open(dsn)
	if dialect == "postgres" {
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	var version string
	require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
	t.Logf("database: %s %s", dialect, version)
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	tables := []any{&model.CasbinRule{}, &model.AuthzRole{}}
	require.NoError(t, db.Migrator().DropTable(tables...))
	require.NoError(t, db.AutoMigrate(tables...))
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
		assert.NoError(t, db.Migrator().DropTable(tables...))
		sqlDB, err := db.DB()
		if err == nil {
			assert.NoError(t, sqlDB.Close())
		}
	})
	return db
}

func TestDeputyAssignmentDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := openDeputyMatrixDB(t, dialect)
			require.NoError(t, Init(db))
			require.NoError(t, Init(db))
			const deputy, other, admin = 60, 61, 62

			assignDeputy(t, db, deputy)
			assignDeputy(t, db, deputy)
			// The insert itself stays idempotent under the unique index when
			// two requests assign the same user at once.
			rule := newRule("g", []string{UserSubject(deputy), RoleSubject(BuiltInRoleDeputy)})
			require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rule).Error)
			var count int64
			require.NoError(t, db.Model(&model.CasbinRule{}).Where("ptype = ? AND v0 = ?", "g", UserSubject(deputy)).Count(&count).Error)
			assert.EqualValues(t, 1, count)

			tx := db.Begin()
			require.NoError(t, tx.Error)
			require.NoError(t, AssignDeputyInTx(tx, other))
			require.NoError(t, tx.Rollback().Error)
			isDeputy, err := IsDeputy(other)
			require.NoError(t, err)
			assert.False(t, isDeputy)

			isDeputy, err = IsDeputy(deputy)
			require.NoError(t, err)
			assert.True(t, isDeputy)
			deputies, err := DeputyUserIDs([]int{deputy, other, admin})
			require.NoError(t, err)
			assert.Equal(t, map[int]bool{deputy: true}, deputies)
			deputies, err = DeputyUserIDs(nil)
			require.NoError(t, err)
			assert.Empty(t, deputies)
			assert.False(t, Can(deputy, common.RoleAdminUser, BillingRead))
			assert.True(t, Can(admin, common.RoleAdminUser, BillingRead))
			assert.True(t, Can(deputy, common.RoleAdminUser, ChannelRead))

			// A marker written earlier in the same transaction applies.
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := AssignDeputyInTx(tx, other); err != nil {
					return err
				}
				return SetUserPermissionsInTx(tx, other, PermissionsMap{ResourceRedemption: {ActionRead: true}})
			})
			var message *common.Message
			require.ErrorAs(t, err, &message)
			assert.Empty(t, userRules(t, db, other))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := AssignDeputyInTx(tx, other); err != nil {
					return err
				}
				return SetUserPermissionsInTx(tx, other, PermissionsMap{
					ResourceChannel:    {ActionSensitiveWrite: true},
					ResourceRedemption: {ActionRead: false, ActionWrite: false},
				})
			}))
			assert.Equal(t, []string{"g role:deputy  ", "p channel sensitive_write allow"}, userRules(t, db, other))

			ids, err := AllDeputyUserIDs()
			require.NoError(t, err)
			assert.Equal(t, []int{deputy, other}, ids)

			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return UnassignDeputyInTx(tx, deputy) }))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return ClearUserAuthorizationInTx(tx, other) }))
			ids, err = AllDeputyUserIDs()
			require.NoError(t, err)
			assert.Empty(t, ids)
			assert.Empty(t, userRules(t, db, other))
			assert.True(t, Can(deputy, common.RoleAdminUser, BillingRead))
		})
	}
}
