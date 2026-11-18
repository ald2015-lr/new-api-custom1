package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUsersForPaginationTest(t *testing.T, total int) {
	t.Helper()
	for id := 1; id <= total; id++ {
		user := &User{
			Id:          id,
			Username:    fmt.Sprintf("user%02d", id),
			Password:    "password123",
			DisplayName: fmt.Sprintf("User %02d", id),
			Email:       fmt.Sprintf("user%02d@example.com", id),
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "default",
			AffCode:     fmt.Sprintf("aff%02d", id),
		}
		require.NoError(t, DB.Create(user).Error)
	}
}

func collectUserIDs(users []*User) []int {
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	return ids
}

func TestGetAllUsersSortsBeforePagination(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 42)

	pageOne, total, err := GetAllUsers(&common.PageInfo{Page: 1, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, collectUserIDs(pageOne))

	pageTwo, total, err := GetAllUsers(&common.PageInfo{Page: 2, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40}, collectUserIDs(pageTwo))

	pageThree, total, err := GetAllUsers(&common.PageInfo{Page: 3, PageSize: 20}, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{41, 42}, collectUserIDs(pageThree))
}

func TestSearchUsersSortsBeforePagination(t *testing.T) {
	truncateTables(t)
	insertUsersForPaginationTest(t, 42)

	users, total, err := SearchUsers("user", "", nil, nil, nil, UserIDScope{}, 20, 20, NewUserSortOptions("id", "asc"))
	require.NoError(t, err)
	assert.Equal(t, int64(42), total)
	assert.Equal(t, []int{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40}, collectUserIDs(users))
}

func TestNewUserSortOptionsAcceptsInviteCount(t *testing.T) {
	assert.Equal(t, UserSortOptions{SortBy: UserSortByInviteCount, SortOrder: "asc"}, NewUserSortOptions(" Invite_Count ", "ASC"))
	assert.Equal(t, UserSortOptions{SortBy: UserSortByInviteCount, SortOrder: "desc"}, NewUserSortOptions("invite_count", "sideways"))
	assert.Equal(t, UserSortOptions{SortBy: "id", SortOrder: "desc"}, NewUserSortOptions("aff_count", "asc"))
}

// The admin list sorts by invited users (users whose inviter_id is the user,
// deleted ones included), returns that count and the active invite abuse ban
// of each listed user, and filters by inviter, on every database and with a
// table prefix.
func TestUserInviteCountListDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			useInviteTestDatabase(t, dialect, "user_invite_list_test_", &User{}, &InviteAbuseBan{})
			create := func(name string, inviterId int) User {
				user := User{Username: name, DisplayName: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: name, InviterId: inviterId}
				require.NoError(t, DB.Create(&user).Error)
				return user
			}
			first, second, third, idle := create("first", 0), create("second", 0), create("third", 0), create("idle", 0)
			firstInvitees := []User{create("first-1", first.Id), create("first-2", first.Id), create("first-3", first.Id)}
			require.NoError(t, DB.Delete(&firstInvitees[2]).Error)
			create("second-1", second.Id)
			create("second-2", second.Id)
			create("second-3", second.Id)
			thirdInvitee := create("third-1", third.Id)
			require.NoError(t, DB.Create(&InviteAbuseBan{UserId: first.Id, InviterId: first.Id, Kind: InviteAbuseKindInviter, InviteCount: 2, CreatedAt: 1}).Error)
			require.NoError(t, DB.Create(&InviteAbuseBan{UserId: firstInvitees[0].Id, InviterId: first.Id, Kind: InviteAbuseKindInvitee, InviteCount: 2, CreatedAt: 1}).Error)
			require.NoError(t, DB.Create(&InviteAbuseBan{UserId: thirdInvitee.Id, InviterId: third.Id, Kind: InviteAbuseKindInvitee, InviteCount: 1, CreatedAt: 1, ReleasedAt: 2, ReleasedBy: 1}).Error)
			var everyone []int
			require.NoError(t, DB.Unscoped().Model(&User{}).Order("id DESC").Pluck("id", &everyone).Error)
			require.Len(t, everyone, 11)

			// first and second both invited three (first's third invitee is
			// deleted and still counts); the tie and the users who invited
			// nobody go by id DESC.
			withoutInviters := func() []int {
				ids := []int{}
				for _, id := range everyone {
					if id != first.Id && id != second.Id && id != third.Id {
						ids = append(ids, id)
					}
				}
				return ids
			}()
			descending := append([]int{second.Id, first.Id, third.Id}, withoutInviters...)
			ascending := append(append([]int{}, withoutInviters...), third.Id, second.Id, first.Id)
			for _, tc := range []struct {
				order string
				want  []int
			}{{"desc", descending}, {"asc", ascending}} {
				var listed []int
				for page := 1; page <= 3; page++ {
					users, total, err := GetAllUsers(&common.PageInfo{Page: page, PageSize: 4}, NewUserSortOptions(UserSortByInviteCount, tc.order))
					require.NoError(t, err)
					assert.EqualValues(t, 11, total)
					listed = append(listed, collectUserIDs(users)...)
				}
				assert.Equal(t, tc.want, listed, tc.order)
			}

			users, total, err := GetAllUsers(&common.PageInfo{Page: 1, PageSize: 20}, NewUserSortOptions(UserSortByInviteCount, "desc"))
			require.NoError(t, err)
			assert.EqualValues(t, 11, total)
			counts, bans := map[int]int64{}, map[int]string{}
			for _, user := range users {
				counts[user.Id], bans[user.Id] = user.InviteCount, user.InviteAbuseBan
				assert.Empty(t, user.Password)
			}
			assert.Equal(t, int64(3), counts[first.Id])
			assert.Equal(t, int64(3), counts[second.Id])
			assert.Equal(t, int64(1), counts[third.Id])
			assert.Zero(t, counts[idle.Id])
			assert.Equal(t, InviteAbuseKindInviter, bans[first.Id])
			assert.Equal(t, InviteAbuseKindInvitee, bans[firstInvitees[0].Id])
			assert.Empty(t, bans[thirdInvitee.Id], "a released ban is not shown")
			assert.Empty(t, bans[second.Id])

			// The inviter filter lists exactly the users its invite_count
			// counts, stays unambiguous next to the sort's join and combines
			// with the other filters: status -1 keeps only the deleted invited
			// user, any other status only users that are not deleted (the
			// deleted first-3 is still enabled).
			enabled, deleted := common.UserStatusEnabled, -1
			for _, tc := range []struct {
				name    string
				keyword string
				group   string
				status  *int
				want    []int
			}{
				{name: "all, deleted included", want: inviteAbuseTestUserIds(firstInvitees[2], firstInvitees[1], firstInvitees[0])},
				{name: "enabled", status: &enabled, want: inviteAbuseTestUserIds(firstInvitees[1], firstInvitees[0])},
				{name: "deleted", status: &deleted, want: inviteAbuseTestUserIds(firstInvitees[2])},
				{name: "keyword and group", keyword: "first-2", group: "default", want: inviteAbuseTestUserIds(firstInvitees[1])},
			} {
				found, total, err := SearchUsers(tc.keyword, tc.group, nil, tc.status, &first.Id, UserIDScope{}, 0, 20, NewUserSortOptions(UserSortByInviteCount, "desc"))
				require.NoError(t, err, tc.name)
				assert.EqualValues(t, len(tc.want), total, tc.name)
				assert.Equal(t, tc.want, collectUserIDs(found), tc.name)
			}
			_, total, err = SearchUsers("", "", nil, nil, &first.Id, UserIDScope{}, 0, 20)
			require.NoError(t, err)
			assert.Equal(t, counts[first.Id], total, "the list behind the count has as many users as the count")
			found, _, err := SearchUsers("", "", nil, nil, nil, UserIDScope{}, 0, 3, NewUserSortOptions(UserSortByInviteCount, "desc"))
			require.NoError(t, err)
			assert.Equal(t, inviteAbuseTestUserIds(second, first, third), collectUserIDs(found))
			assert.Equal(t, int64(3), found[1].InviteCount)
			assert.Equal(t, InviteAbuseKindInviter, found[1].InviteAbuseBan)

			// The id scope behind the admin role filter: Only keeps just its
			// ids (none at all when empty), Except drops its ids (nothing when
			// empty); both stay unambiguous next to the sort's join.
			for _, tc := range []struct {
				name  string
				scope UserIDScope
				want  []int
			}{
				{name: "only", scope: UserIDScope{Only: []int{third.Id, first.Id}}, want: inviteAbuseTestUserIds(first, third)},
				{name: "only nobody", scope: UserIDScope{Only: []int{}}, want: []int{}},
				{name: "except", scope: UserIDScope{Except: []int{first.Id, second.Id}}, want: append([]int{third.Id}, withoutInviters...)},
				{name: "except nobody", scope: UserIDScope{Except: []int{}}, want: descending},
			} {
				found, total, err := SearchUsers("", "", nil, nil, nil, tc.scope, 0, 20, NewUserSortOptions(UserSortByInviteCount, "desc"))
				require.NoError(t, err, tc.name)
				assert.EqualValues(t, len(tc.want), total, tc.name)
				assert.Equal(t, tc.want, collectUserIDs(found), tc.name)
			}
		})
	}
}
