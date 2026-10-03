package model

import (
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ChannelQuotaLimit caps how much quota a channel may consume, optionally per
// calendar day (server local time). A channel whose usage reached its limit is
// skipped by channel selection; when a group has nothing else left, callers get
// Message (or the default text).
type ChannelQuotaLimit struct {
	ChannelId  int    `json:"channel_id" gorm:"primaryKey;autoIncrement:false"`
	LimitQuota int64  `json:"limit_quota" gorm:"type:bigint;not null;default:0"`
	DailyReset bool   `json:"daily_reset"`
	Message    string `json:"message" gorm:"type:varchar(255);not null;default:''"`
	// UsedQuota counts usage since PeriodStart when DailyReset is on, and since
	// the limit was created (or last reset) otherwise.
	UsedQuota   int64 `json:"used_quota" gorm:"type:bigint;not null;default:0"`
	PeriodStart int64 `json:"period_start" gorm:"type:bigint;not null;default:0"`
	UpdatedAt   int64 `json:"updated_at" gorm:"type:bigint;not null;default:0"`
}

// ChannelQuotaMessageMaxLength bounds the custom error text (characters).
const ChannelQuotaMessageMaxLength = 255

// ChannelQuotaExhaustedError reports that every candidate channel left for a
// request has reached its quota limit. Message is the operator's custom text,
// empty when the default text should be used.
type ChannelQuotaExhaustedError struct {
	Message string
}

func (e *ChannelQuotaExhaustedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "channel quota limit reached"
}

// channelQuotaPeriodStart returns the start of the local calendar day of now.
func channelQuotaPeriodStart(now time.Time) int64 {
	year, month, day := now.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location()).Unix()
}

// CurrentUsed returns the usage that counts against the limit at now.
func (l ChannelQuotaLimit) CurrentUsed(now time.Time) int64 {
	if l.DailyReset && l.PeriodStart < channelQuotaPeriodStart(now) {
		return 0
	}
	return l.UsedQuota
}

// Exhausted reports whether the channel has reached its limit at now.
func (l ChannelQuotaLimit) Exhausted(now time.Time) bool {
	return l.LimitQuota > 0 && l.CurrentUsed(now) >= l.LimitQuota
}

// The selection hot path reads an immutable snapshot that is replaced on every
// change and reloaded from the database at most every refresh interval, so other
// nodes pick up usage within that interval.
const channelQuotaLimitRefreshInterval = 10 * time.Second

var (
	channelQuotaLimitMu       sync.Mutex
	channelQuotaLimitSnapshot map[int]ChannelQuotaLimit
	channelQuotaLimitLoadedAt time.Time
)

func channelQuotaLimits() map[int]ChannelQuotaLimit {
	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	if channelQuotaLimitSnapshot != nil && time.Since(channelQuotaLimitLoadedAt) < channelQuotaLimitRefreshInterval {
		return channelQuotaLimitSnapshot
	}
	channelQuotaLimitLoadedAt = time.Now()
	if DB == nil {
		channelQuotaLimitSnapshot = map[int]ChannelQuotaLimit{}
		return channelQuotaLimitSnapshot
	}
	var rows []ChannelQuotaLimit
	if err := DB.Find(&rows).Error; err != nil {
		common.SysLog("failed to load channel quota limits: " + err.Error())
		if channelQuotaLimitSnapshot == nil {
			channelQuotaLimitSnapshot = map[int]ChannelQuotaLimit{}
		}
		return channelQuotaLimitSnapshot
	}
	snapshot := make(map[int]ChannelQuotaLimit, len(rows))
	for _, row := range rows {
		snapshot[row.ChannelId] = row
	}
	channelQuotaLimitSnapshot = snapshot
	return snapshot
}

// storeChannelQuotaLimit replaces one entry of the snapshot (nil removes it).
func storeChannelQuotaLimit(channelId int, limit *ChannelQuotaLimit) {
	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	next := make(map[int]ChannelQuotaLimit, len(channelQuotaLimitSnapshot)+1)
	maps.Copy(next, channelQuotaLimitSnapshot)
	if limit == nil {
		delete(next, channelId)
	} else {
		next[channelId] = *limit
	}
	channelQuotaLimitSnapshot = next
}

// ChannelQuotaExhaustion reports whether the channel reached its quota limit and
// the custom message to show (empty means the default text).
func ChannelQuotaExhaustion(channelId int) (bool, string) {
	limit, ok := channelQuotaLimits()[channelId]
	if !ok || !limit.Exhausted(time.Now()) {
		return false, ""
	}
	return true, limit.Message
}

// dropQuotaExhaustedChannels removes channels that reached their quota limit.
// When that empties a non-empty list it returns the error to report, carrying
// the first custom message set on those channels.
func dropQuotaExhaustedChannels(ids []int) ([]int, *ChannelQuotaExhaustedError) {
	limits := channelQuotaLimits()
	if len(limits) == 0 || len(ids) == 0 {
		return ids, nil
	}
	now := time.Now()
	kept := make([]int, 0, len(ids))
	var exhausted *ChannelQuotaExhaustedError
	for _, id := range ids {
		if limit, ok := limits[id]; ok && limit.Exhausted(now) {
			// Prefer the first custom message among the exhausted channels.
			if exhausted == nil {
				exhausted = &ChannelQuotaExhaustedError{}
			}
			if exhausted.Message == "" {
				exhausted.Message = limit.Message
			}
			continue
		}
		kept = append(kept, id)
	}
	if len(kept) > 0 {
		return kept, nil
	}
	return kept, exhausted
}

// AddChannelQuotaLimitUsage records consumed (or refunded, when negative)
// quota against the channel's limit, if it has one.
func AddChannelQuotaLimitUsage(channelId int, quota int) {
	if quota == 0 || channelId <= 0 {
		return
	}
	if _, limited := channelQuotaLimits()[channelId]; !limited {
		return
	}
	now := time.Now()
	periodStart := channelQuotaPeriodStart(now)
	var updated ChannelQuotaLimit
	err := DB.Transaction(func(tx *gorm.DB) error {
		var row ChannelQuotaLimit
		if err := lockForUpdate(tx).Where("channel_id = ?", channelId).First(&row).Error; err != nil {
			return err
		}
		if row.DailyReset && row.PeriodStart < periodStart {
			row.UsedQuota = 0
			row.PeriodStart = periodStart
		}
		row.UsedQuota = max(row.UsedQuota+int64(quota), 0)
		row.UpdatedAt = now.Unix()
		updated = row
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"used_quota":   row.UsedQuota,
			"period_start": row.PeriodStart,
			"updated_at":   row.UpdatedAt,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		storeChannelQuotaLimit(channelId, nil)
		return
	}
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to record channel quota limit usage: channel_id=%d, quota=%d, error=%v", channelId, quota, err))
		return
	}
	storeChannelQuotaLimit(channelId, &updated)
}

// GetChannelQuotaLimits lists every configured limit from the database.
func GetChannelQuotaLimits() ([]ChannelQuotaLimit, error) {
	var rows []ChannelQuotaLimit
	err := DB.Order("channel_id").Find(&rows).Error
	return rows, err
}

// SetChannelQuotaLimit creates or updates a channel's limit, keeping its usage.
// A limitQuota of 0 removes the limit and returns nil.
func SetChannelQuotaLimit(channelId int, limitQuota int64, dailyReset bool, message string) (*ChannelQuotaLimit, error) {
	if limitQuota <= 0 {
		if err := DB.Where("channel_id = ?", channelId).Delete(&ChannelQuotaLimit{}).Error; err != nil {
			return nil, err
		}
		storeChannelQuotaLimit(channelId, nil)
		return nil, nil
	}
	now := time.Now()
	var saved ChannelQuotaLimit
	err := DB.Transaction(func(tx *gorm.DB) error {
		var rows []ChannelQuotaLimit
		if err := lockForUpdate(tx).Where("channel_id = ?", channelId).Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			saved = ChannelQuotaLimit{
				ChannelId: channelId, LimitQuota: limitQuota, DailyReset: dailyReset, Message: message,
				PeriodStart: channelQuotaPeriodStart(now), UpdatedAt: now.Unix(),
			}
			return tx.Create(&saved).Error
		}
		row := rows[0]
		row.LimitQuota, row.DailyReset, row.Message, row.UpdatedAt = limitQuota, dailyReset, message, now.Unix()
		saved = row
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"limit_quota": row.LimitQuota,
			"daily_reset": row.DailyReset,
			"message":     row.Message,
			"updated_at":  row.UpdatedAt,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	storeChannelQuotaLimit(channelId, &saved)
	return &saved, nil
}

// ResetChannelQuotaLimitUsage sets the channel's counted usage back to zero.
func ResetChannelQuotaLimitUsage(channelId int) (*ChannelQuotaLimit, error) {
	now := time.Now()
	var saved ChannelQuotaLimit
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("channel_id = ?", channelId).First(&saved).Error; err != nil {
			return err
		}
		saved.UsedQuota, saved.PeriodStart, saved.UpdatedAt = 0, channelQuotaPeriodStart(now), now.Unix()
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"used_quota":   saved.UsedQuota,
			"period_start": saved.PeriodStart,
			"updated_at":   saved.UpdatedAt,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	storeChannelQuotaLimit(channelId, &saved)
	return &saved, nil
}

// dropQuotaExhaustedAbilities is dropQuotaExhaustedChannels for the
// database (no memory cache) selection path.
func dropQuotaExhaustedAbilities(abilities []Ability) ([]Ability, *ChannelQuotaExhaustedError) {
	ids := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		ids = append(ids, ability.ChannelId)
	}
	keptIds, exhausted := dropQuotaExhaustedChannels(ids)
	if len(keptIds) == len(ids) {
		return abilities, nil
	}
	keep := make(map[int]struct{}, len(keptIds))
	for _, id := range keptIds {
		keep[id] = struct{}{}
	}
	kept := make([]Ability, 0, len(keptIds))
	for _, ability := range abilities {
		if _, ok := keep[ability.ChannelId]; ok {
			kept = append(kept, ability)
		}
	}
	return kept, exhausted
}
