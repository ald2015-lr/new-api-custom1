package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callLogHandler runs a log handler as the given user and returns the JSON
// lines of the response.
func callLogHandler(t *testing.T, handler gin.HandlerFunc, userId int, role int, query string) []map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
	ctx.Set("id", userId)
	ctx.Set("role", role)
	handler(ctx)
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(recorder.Body.String()), "\n") {
		var value map[string]any
		require.NoError(t, common.UnmarshalJsonStr(line, &value))
		lines = append(lines, value)
	}
	return lines
}

func exportedRequestIds(lines []map[string]any) []any {
	ids := make([]any, 0, len(lines))
	for _, line := range lines[:len(lines)-1] {
		ids = append(ids, line["request_id"])
	}
	return ids
}

// seedExportLogs stores logs of two users: three in the last hour and one
// from 400 days ago.
func seedExportLogs(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, now int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.Channel{Id: 9, Name: "primary"}).Error)
	logs := []model.Log{
		{UserId: 1, Username: "alice", CreatedAt: now - 3600, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 9, Quota: 10, RequestId: "a-1", Other: `{"admin_info":{"use_channel":["9"]}}`},
		{UserId: 2, Username: "bob", CreatedAt: now - 1800, Type: model.LogTypeConsume, ModelName: "gpt-b", ChannelId: 9, Quota: 20, RequestId: "b-1"},
		{UserId: 1, Username: "alice", CreatedAt: now - 60, Type: model.LogTypeError, ModelName: "gpt-a", RequestId: "a-2"},
		{UserId: 1, Username: "alice", CreatedAt: now - 400*24*3600, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "a-old"},
	}
	for i := range logs {
		require.NoError(t, logDB.Create(&logs[i]).Error)
	}
}

func useLogExportDatabases(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, mainType common.DatabaseType, logType common.DatabaseType) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousMemory := common.RedisEnabled, common.MemoryCacheEnabled
	model.DB, model.LOG_DB = mainDB, logDB
	common.SetDatabaseTypes(mainType, logType)
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.MemoryCacheEnabled = previousRedis, previousMemory
	})
}

func TestLogExportDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, isolatedDSN := newAuditTestDatabase(t, tc.kind, dsn)
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}))
			if tc.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, model.InitLogDB())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			versionSQL := "SELECT version()"
			if tc.kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, model.LOG_DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database: %s %s", tc.kind, version)
			now := time.Now().Unix()
			seedExportLogs(t, mainDB, model.LOG_DB, now)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now+3600)

			listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, query)[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])
			listed = callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&username=alice")[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

			own := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query)
			require.Len(t, own, 3)
			assert.Equal(t, []any{"a-2", "a-1"}, exportedRequestIds(own))
			assert.Equal(t, map[string]any{"success": true, "message": "", "data": map[string]any{"count": float64(2)}}, own[2])
			assert.Equal(t, float64(1), own[0]["id"], "user exports carry list positions, not row ids")
			assert.Empty(t, own[1]["channel_name"])
			assert.NotContains(t, own[1]["other"], "admin_info")

			all := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&model_name=gpt-%25")
			require.Len(t, all, 4)
			assert.Equal(t, []any{"a-2", "b-1", "a-1"}, exportedRequestIds(all))
			assert.Equal(t, "primary", all[2]["channel_name"])
			assert.Contains(t, all[2]["other"], "admin_info")
			assert.Equal(t, true, all[3]["success"])

			// Recharge rows (types 1 and 3) never reach an administrator
			// without billing:read: not in the count, the page, a search by
			// type or the export. Full admins and root see them.
			require.NoError(t, mainDB.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
			require.NoError(t, authz.Init(mainDB))
			const admin, deputy, root = 3, 4, 5
			require.NoError(t, mainDB.Transaction(func(tx *gorm.DB) error { return authz.AssignDeputyInTx(tx, deputy) }))
			// The top-up belongs to the deputy: their own logs stay unfiltered.
			for i, logType := range []int{model.LogTypeTopup, model.LogTypeManage, model.LogTypeSystem} {
				owner := 2
				if logType == model.LogTypeTopup {
					owner = deputy
				}
				require.NoError(t, model.LOG_DB.Create(&model.Log{UserId: owner, Username: fmt.Sprint(owner), CreatedAt: now - int64(10+i), Type: logType, RequestId: fmt.Sprintf("type-%d", logType)}).Error)
			}
			everything := []any{"type-1", "type-3", "type-4", "a-2", "b-1", "a-1"}
			withoutRecharge := []any{"type-4", "a-2", "b-1", "a-1"}
			for _, viewer := range []struct {
				name     string
				id, role int
				want     []any
			}{
				{"deputy", deputy, common.RoleAdminUser, withoutRecharge},
				{"admin", admin, common.RoleAdminUser, everything},
				{"root", root, common.RoleRootUser, everything},
			} {
				listed := callLogHandler(t, GetAllLogs, viewer.id, viewer.role, query)[0]
				require.Equal(t, true, listed["success"], viewer.name)
				assert.EqualValues(t, len(viewer.want), listed["data"].(map[string]any)["total"], viewer.name)
				assert.Len(t, listed["data"].(map[string]any)["items"], len(viewer.want), viewer.name)
				exported := callLogHandler(t, ExportAllLogs, viewer.id, viewer.role, query)
				assert.Equal(t, viewer.want, exportedRequestIds(exported), viewer.name)
				for _, logType := range []int{model.LogTypeTopup, model.LogTypeManage} {
					typed := callLogHandler(t, GetAllLogs, viewer.id, viewer.role, fmt.Sprintf("%s&type=%d", query, logType))[0]
					want := 1
					if viewer.name == "deputy" {
						want = 0
					}
					assert.EqualValues(t, want, typed["data"].(map[string]any)["total"], "%s type %d", viewer.name, logType)
					typedExport := callLogHandler(t, ExportAllLogs, viewer.id, viewer.role, fmt.Sprintf("%s&type=%d", query, logType))
					assert.Len(t, typedExport, want+1, "%s type %d", viewer.name, logType)
				}
			}
			ownTopups := callLogHandler(t, GetUserLogs, deputy, common.RoleAdminUser, query+"&type=1")[0]
			assert.EqualValues(t, 1, ownTopups["data"].(map[string]any)["total"])
			ownExport := callLogHandler(t, ExportUserLogs, deputy, common.RoleAdminUser, query)
			assert.Equal(t, []any{"type-1"}, exportedRequestIds(ownExport))

			// The lists count at most model.LogCountCap matches of the search.
			// Past the cap the total is the cap, total_capped is set and pages
			// go on past it; at or below the cap the count stays exact. Exports
			// are never capped.
			previousCap := model.LogCountCap
			t.Cleanup(func() { model.LogCountCap = previousCap })
			for _, tc := range []struct {
				name                string
				handler             gin.HandlerFunc
				viewer, role        int
				params              string
				countCap, wantTotal int
				wantCapped          bool
				wantPage            []any
			}{
				{"own logs at the cap", GetUserLogs, 1, common.RoleCommonUser, "", 2, 2, false, []any{"a-2", "a-1"}},
				{"own logs past the cap", GetUserLogs, 2, common.RoleCommonUser, "&page_size=2", 2, 2, true, []any{"type-3", "type-4"}},
				{"own logs, the short last page", GetUserLogs, 2, common.RoleCommonUser, "&page_size=2&p=2", 2, 2, true, []any{"b-1"}},
				{"all logs past the cap", GetAllLogs, root, common.RoleRootUser, "&page_size=2", 4, 4, true, []any{"type-1", "type-3"}},
				{"all logs, a page past the cap", GetAllLogs, root, common.RoleRootUser, "&page_size=2&p=3", 4, 4, true, []any{"b-1", "a-1"}},
				{"all logs, a page past the last match", GetAllLogs, root, common.RoleRootUser, "&page_size=2&p=4", 4, 4, true, []any{}},
				{"recharge rows a deputy cannot see are not counted", GetAllLogs, deputy, common.RoleAdminUser, "&page_size=2", 4, 4, false, []any{"type-4", "a-2"}},
				{"a username search below the cap", GetAllLogs, root, common.RoleRootUser, "&username=alice", 4, 2, false, []any{"a-2", "a-1"}},
				{"a type search below the cap", GetAllLogs, root, common.RoleRootUser, "&type=1", 4, 1, false, []any{"type-1"}},
			} {
				model.LogCountCap = tc.countCap
				listed := callLogHandler(t, tc.handler, tc.viewer, tc.role, query+tc.params)[0]
				require.Equal(t, true, listed["success"], tc.name)
				data := listed["data"].(map[string]any)
				assert.EqualValues(t, tc.wantTotal, data["total"], tc.name)
				assert.Equal(t, tc.wantCapped, data["total_capped"], tc.name)
				items, _ := data["items"].([]any)
				page := []any{}
				for _, item := range items {
					page = append(page, item.(map[string]any)["request_id"])
				}
				assert.ElementsMatch(t, tc.wantPage, page, tc.name)
			}
			model.LogCountCap = 2
			assert.Equal(t, everything, exportedRequestIds(callLogHandler(t, ExportAllLogs, root, common.RoleRootUser, query)), "exports are not capped")
			assert.Len(t, exportedRequestIds(callLogHandler(t, ExportUserLogs, 2, common.RoleCommonUser, query)), 3, "exports are not capped")

			// A page or page size below 1 is the default, like a missing one:
			// a page size below 1 would leave the list query without a LIMIT
			// and answer every matching log at once.
			model.LogCountCap = previousCap
			previousPerPage := common.ItemsPerPage
			common.ItemsPerPage = 2
			t.Cleanup(func() { common.ItemsPerPage = previousPerPage })
			for _, tc := range []struct {
				name         string
				handler      gin.HandlerFunc
				viewer, role int
				params       string
			}{
				{"own logs, page size -1", GetUserLogs, 2, common.RoleCommonUser, "&page_size=-1"},
				{"own logs, page -3", GetUserLogs, 2, common.RoleCommonUser, "&p=-3"},
				{"all logs, page size -1", GetAllLogs, root, common.RoleRootUser, "&page_size=-1"},
				{"all logs, page -3 and page size -5", GetAllLogs, root, common.RoleRootUser, "&p=-3&page_size=-5"},
			} {
				listed := callLogHandler(t, tc.handler, tc.viewer, tc.role, query+tc.params)[0]
				require.Equal(t, true, listed["success"], tc.name)
				data := listed["data"].(map[string]any)
				assert.EqualValues(t, 1, data["page"], tc.name)
				assert.EqualValues(t, 2, data["page_size"], tc.name)
				items, _ := data["items"].([]any)
				require.Len(t, items, 2, tc.name)
				if tc.viewer == 2 {
					assert.EqualValues(t, 1, items[0].(map[string]any)["id"], "%s: user lists number their rows from the first", tc.name)
				}
			}
			// The model bounds a page whatever its caller passes.
			allLogs, _, _, err := model.GetAllLogs(model.LogFilter{}, -8, -1)
			require.NoError(t, err)
			assert.Len(t, allLogs, 2)
			ownLogs, _, _, err := model.GetUserLogs(2, model.LogFilter{}, -8, -1)
			require.NoError(t, err)
			require.Len(t, ownLogs, 2)
			assert.Equal(t, 1, ownLogs[0].Id, "user lists number their rows from the first")
		})
	}
}

func TestLogExportRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.QuotaData{}))
	now := time.Now().Unix()
	seedExportLogs(t, db, db, now)
	logSetting := operation_setting.GetLogSetting()
	previousSetting := *logSetting
	t.Cleanup(func() { *logSetting = previousSetting })
	recent := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now)
	tooLong := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-366*24*3600, now)

	t.Run("time range", func(t *testing.T) {
		rejected := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, tooLong)
		require.Len(t, rejected, 1)
		assert.Equal(t, false, rejected[0]["success"])
		assert.Equal(t, "The time range cannot exceed {{days}} days", rejected[0]["message_key"])
		assert.Equal(t, map[string]any{"days": float64(365)}, rejected[0]["message_params"])

		assert.Equal(t, false, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, false, callLogHandler(t, GetLogsSelfStat, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 3, common.RoleAdminUser, tooLong)[0]["success"], "administrators search their own logs without a limit")

		// The dashboard follows the same range instead of its old one-month limit.
		fortyDays := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-40*24*3600, now)
		for _, handler := range []gin.HandlerFunc{GetUserQuotaDates, GetUserFlowQuotaDates} {
			assert.Equal(t, "The time range cannot exceed {{days}} days", callLogHandler(t, handler, 1, common.RoleCommonUser, tooLong)[0]["message_key"])
			assert.Equal(t, true, callLogHandler(t, handler, 1, common.RoleCommonUser, fortyDays)[0]["success"])
		}

		// Without a start, a user's search reaches back the whole range.
		listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, "")[0]
		assert.Equal(t, true, listed["success"])
		assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

		logSetting.MaxRangeDays = 400
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		logSetting.MaxRangeDays = previousSetting.MaxRangeDays
	})

	t.Run("user export switch", func(t *testing.T) {
		logSetting.UserExportEnabled = false
		t.Cleanup(func() { logSetting.UserExportEnabled = previousSetting.UserExportEnabled })
		disabled := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, disabled, 1)
		assert.Equal(t, "Log export is disabled", disabled[0]["message_key"])
		assert.Equal(t, true, callLogHandler(t, ExportUserLogs, 3, common.RoleAdminUser, recent)[0]["success"], "administrators export their own logs even when users cannot")
	})

	t.Run("one export per user", func(t *testing.T) {
		release, ok, err := service.AcquireLogExportLock(1)
		require.NoError(t, err)
		require.True(t, ok)
		busy := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, busy, 1)
		assert.Equal(t, "Another log export is running. Try again after it finishes.", busy[0]["message_key"])
		other := callLogHandler(t, ExportUserLogs, 2, common.RoleCommonUser, recent)
		assert.Equal(t, true, other[len(other)-1]["success"], "other users are not blocked")
		release()
		assert.Len(t, callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent), 3)
	})
}

func TestAcquireLogExportLock(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			common.RedisEnabled = useRedis
			if useRedis {
				server := miniredis.RunT(t)
				common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				require.NoError(t, common.RDB.Ping(context.Background()).Err())
			}
			t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })

			release, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			require.True(t, ok)
			_, ok, err = service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.False(t, ok, "a second export of the same user must wait")
			releaseOther, ok, err := service.AcquireLogExportLock(2)
			require.NoError(t, err)
			assert.True(t, ok)
			releaseOther()
			release()
			releaseAgain, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.True(t, ok, "a finished export frees the user")
			releaseAgain()
		})
	}
}
