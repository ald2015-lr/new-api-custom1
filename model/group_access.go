package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
)

// Cumulative top-up of a user, in top-up units (the unit typed on the wallet
// top-up page). The two components are cached separately so toggling
// group_access_setting.count_redemption takes effect immediately.
const (
	userTopupTotalCacheNamespace = "new-api:user_topup_total:v1"
	groupAccessLookupChunkSize   = 500
)

var (
	userTopupTotalCacheOnce sync.Once
	userTopupTotalCache     *cachex.HybridCache[string]
)

func userTopupTotalCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("USER_TOPUP_TOTAL_CACHE_TTL", 120)
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}
	return time.Duration(ttlSeconds) * time.Second
}

func getUserTopupTotalCache() *cachex.HybridCache[string] {
	userTopupTotalCacheOnce.Do(func() {
		ttl := userTopupTotalCacheTTL()
		capacity := common.GetEnvOrDefault("USER_TOPUP_TOTAL_CACHE_CAP", 10000)
		if capacity <= 0 {
			capacity = 10000
		}
		userTopupTotalCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace: cachex.Namespace(userTopupTotalCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, capacity).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return userTopupTotalCache
}

func userTopupCacheKey(userId int) string {
	return "topup:" + strconv.Itoa(userId)
}

func userRedemptionCacheKey(userId int) string {
	return "redemption:" + strconv.Itoa(userId)
}

// InvalidateUserTopupTotalCache drops the cached cumulative top-up of a user.
// Called after every committed credit (top-up or redemption).
func InvalidateUserTopupTotalCache(userId int) {
	if userId <= 0 {
		return
	}
	if _, err := getUserTopupTotalCache().DeleteMany([]string{userTopupCacheKey(userId), userRedemptionCacheKey(userId)}); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate top-up total cache for user %d: %s", userId, err.Error()))
	}
}

// quotaToTopupUnits converts raw quota to top-up units.
func quotaToTopupUnits(quota decimal.Decimal) decimal.Decimal {
	if common.QuotaPerUnit <= 0 {
		return decimal.Zero
	}
	return quota.Div(decimal.NewFromFloat(common.QuotaPerUnit))
}

// cachedTopupComponent returns a cached component or computes and caches it.
// Cache failures fall back to the database.
func cachedTopupComponent(key string, compute func() (decimal.Decimal, error)) (decimal.Decimal, error) {
	cache := getUserTopupTotalCache()
	if raw, found, err := cache.Get(key); err == nil && found {
		if value, parseErr := decimal.NewFromString(raw); parseErr == nil {
			return value, nil
		}
	}
	value, err := compute()
	if err != nil {
		return decimal.Zero, err
	}
	_ = cache.SetWithTTL(key, value.String(), userTopupTotalCacheTTL())
	return value, nil
}

// GetUserTopupTotal returns the user's cumulative successful top-up in top-up
// units: online/manual top-ups (amount is stored in units, except Creem which
// stores raw quota), plus used redemption codes when countRedemption is set.
// Subscription purchases are mirrored with amount 0 and do not count.
func GetUserTopupTotal(userId int, countRedemption bool) (decimal.Decimal, error) {
	if userId <= 0 {
		return decimal.Zero, nil
	}
	total, err := cachedTopupComponent(userTopupCacheKey(userId), func() (decimal.Decimal, error) {
		var units, creemQuota decimal.Decimal
		err := DB.Model(&TopUp{}).
			Select("COALESCE(SUM(CASE WHEN payment_method = ? THEN 0 ELSE amount END), 0), COALESCE(SUM(CASE WHEN payment_method = ? THEN amount ELSE 0 END), 0)", PaymentMethodCreem, PaymentMethodCreem).
			Where("user_id = ? AND status = ? AND amount > 0", userId, common.TopUpStatusSuccess).
			Row().Scan(&units, &creemQuota)
		if err != nil {
			return decimal.Zero, err
		}
		return units.Add(quotaToTopupUnits(creemQuota)), nil
	})
	if err != nil || !countRedemption {
		return total, err
	}
	redeemed, err := cachedTopupComponent(userRedemptionCacheKey(userId), func() (decimal.Decimal, error) {
		var quota decimal.Decimal
		// Used codes count even after an administrator deletes them.
		err := DB.Unscoped().Model(&Redemption{}).
			Select("COALESCE(SUM(quota), 0)").
			Where("used_user_id = ? AND status = ?", userId, common.RedemptionCodeStatusUsed).
			Row().Scan(&quota)
		if err != nil {
			return decimal.Zero, err
		}
		return quotaToTopupUnits(quota), nil
	})
	if err != nil {
		return decimal.Zero, err
	}
	return total.Add(redeemed), nil
}

// NormalizeGroupAccessRulesInput turns rules submitted by an administrator
// into the stored form. Whitelist entries may carry only a username or only an
// id: usernames are resolved to ids and ids to their current usernames, and
// unknown users are rejected. Enforcement later relies on ids only.
func NormalizeGroupAccessRulesInput(value string) (string, error) {
	rules, err := operation_setting.ParseGroupAccessRules(value, false)
	if err != nil {
		return "", err
	}
	var ids []int
	var usernames []string
	for _, rule := range rules {
		for _, user := range rule.Users {
			if user.Id > 0 {
				ids = append(ids, user.Id)
			} else {
				usernames = append(usernames, user.Username)
			}
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	slices.Sort(usernames)
	usernames = slices.Compact(usernames)

	usernameById := make(map[int]string, len(ids))
	for chunk := range slices.Chunk(ids, groupAccessLookupChunkSize) {
		var users []User
		if err := DB.Select("id", "username").Where("id IN ?", chunk).Find(&users).Error; err != nil {
			return "", err
		}
		for _, user := range users {
			usernameById[user.Id] = user.Username
		}
	}
	var unknownIds []string
	for _, id := range ids {
		if _, ok := usernameById[id]; !ok {
			unknownIds = append(unknownIds, strconv.Itoa(id))
		}
	}

	// MySQL's default collation compares usernames case-insensitively, so a
	// returned username can differ in case from the input. Prefer an exact
	// match and fall back to a case-insensitive one.
	exactMatches := make(map[string]User, len(usernames))
	foldedMatches := make(map[string]User, len(usernames))
	for chunk := range slices.Chunk(usernames, groupAccessLookupChunkSize) {
		var users []User
		if err := DB.Select("id", "username").Where("username IN ?", chunk).Order("id").Find(&users).Error; err != nil {
			return "", err
		}
		for _, user := range users {
			exactMatches[user.Username] = user
			if _, ok := foldedMatches[strings.ToLower(user.Username)]; !ok {
				foldedMatches[strings.ToLower(user.Username)] = user
			}
		}
	}
	resolvedByUsername := make(map[string]User, len(usernames))
	var unknownUsernames []string
	for _, username := range usernames {
		user, ok := exactMatches[username]
		if !ok {
			user, ok = foldedMatches[strings.ToLower(username)]
		}
		if !ok {
			unknownUsernames = append(unknownUsernames, username)
			continue
		}
		resolvedByUsername[username] = user
	}

	if len(unknownIds) > 0 || len(unknownUsernames) > 0 {
		var problems []string
		if len(unknownIds) > 0 {
			problems = append(problems, "用户 ID: "+strings.Join(unknownIds, ", "))
		}
		if len(unknownUsernames) > 0 {
			problems = append(problems, "用户名: "+strings.Join(unknownUsernames, ", "))
		}
		return "", fmt.Errorf("分组充值门槛白名单中存在不存在的用户（%s）", strings.Join(problems, "；"))
	}

	for i := range rules {
		for j := range rules[i].Users {
			user := &rules[i].Users[j]
			if user.Id > 0 {
				user.Username = usernameById[user.Id]
				continue
			}
			resolved := resolvedByUsername[user.Username]
			user.Id, user.Username = resolved.Id, resolved.Username
		}
	}
	return operation_setting.EncodeGroupAccessRules(rules)
}
