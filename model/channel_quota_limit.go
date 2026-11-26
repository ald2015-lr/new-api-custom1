package model

import (
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ChannelQuotaLimit caps how much quota and/or how many billed requests a
// channel may consume, optionally per calendar day (server local time). A
// channel that reached either limit is skipped by channel selection; when a
// group has nothing else left, callers get Message (or the default text).
type ChannelQuotaLimit struct {
	ChannelId  int    `json:"channel_id" gorm:"primaryKey;autoIncrement:false"`
	LimitQuota int64  `json:"limit_quota" gorm:"type:bigint;not null;default:0"`
	LimitCount int64  `json:"limit_count" gorm:"type:bigint;not null;default:0"`
	DailyReset bool   `json:"daily_reset"`
	Message    string `json:"message" gorm:"type:varchar(255);not null;default:''"`
	// UsedQuota counts usage since PeriodStart when DailyReset is on, and since
	// the limit was created (or last reset) otherwise.
	UsedQuota   int64 `json:"used_quota" gorm:"type:bigint;not null;default:0"`
	UsedCount   int64 `json:"used_count" gorm:"type:bigint;not null;default:0"`
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

// CurrentUsedCount returns the billed requests that count against the limit at now.
func (l ChannelQuotaLimit) CurrentUsedCount(now time.Time) int64 {
	if l.DailyReset && l.PeriodStart < channelQuotaPeriodStart(now) {
		return 0
	}
	return l.UsedCount
}

// Exhausted reports whether the channel has reached either limit at now.
func (l ChannelQuotaLimit) Exhausted(now time.Time) bool {
	return (l.LimitQuota > 0 && l.CurrentUsed(now) >= l.LimitQuota) ||
		(l.LimitCount > 0 && l.CurrentUsedCount(now) >= l.LimitCount)
}

// The selection hot path reads an immutable snapshot that is replaced on every
// change and reloaded from the database at most every refresh interval, so other
// nodes pick up usage within that interval. Channel selection reads it without
// a lock and never waits for the database once the first load is done: a stale
// snapshot is served while one background reload replaces it.
const channelQuotaLimitRefreshInterval = 10 * time.Second

// channelQuotaLimitSnapshot is every channel's limits at loadedAt. complete is
// false while it holds only local changes made before the first load.
type channelQuotaLimitSnapshot struct {
	limits   map[int]ChannelQuotaLimit
	loadedAt time.Time
	complete bool
}

var (
	channelQuotaLimitCurrent   atomic.Pointer[channelQuotaLimitSnapshot]
	channelQuotaLimitReloading atomic.Bool
	// channelQuotaLimitFirstLoad makes the callers that find no snapshot wait
	// for one load instead of each querying the database.
	channelQuotaLimitFirstLoad sync.Mutex
	// channelQuotaLimitMu orders the replacements of the snapshot: local
	// changes, and the reloads that install database rows.
	channelQuotaLimitMu sync.Mutex
	// channelQuotaLimitGeneration counts the local changes, and
	// channelQuotaLimitWrites holds the generation of each channel's last one,
	// so a reload keeps the changes made while its query ran. Both are guarded
	// by channelQuotaLimitMu.
	channelQuotaLimitGeneration int64
	channelQuotaLimitWrites     = map[int]int64{}
)

func channelQuotaLimits() map[int]ChannelQuotaLimit {
	snapshot := channelQuotaLimitCurrent.Load()
	if snapshot == nil || !snapshot.complete {
		return loadFirstChannelQuotaLimits()
	}
	if time.Since(snapshot.loadedAt) >= channelQuotaLimitRefreshInterval &&
		channelQuotaLimitReloading.CompareAndSwap(false, true) {
		db := DB
		go func() {
			defer channelQuotaLimitReloading.Store(false)
			defer func() {
				if r := recover(); r != nil {
					common.SysError(common.LogText("channel quota limit reload panicked, keeping the previous limits: %v", r))
				}
			}()
			reloadChannelQuotaLimits(db)
		}()
	}
	return snapshot.limits
}

// loadFirstChannelQuotaLimits loads the snapshot while there is none yet;
// concurrent callers wait for the same load.
func loadFirstChannelQuotaLimits() map[int]ChannelQuotaLimit {
	channelQuotaLimitFirstLoad.Lock()
	defer channelQuotaLimitFirstLoad.Unlock()
	if snapshot := channelQuotaLimitCurrent.Load(); snapshot != nil && snapshot.complete {
		return snapshot.limits
	}
	reloadChannelQuotaLimits(DB)
	return channelQuotaLimitCurrent.Load().limits
}

// reloadChannelQuotaLimits replaces the snapshot with the limits in db, keeping
// the local changes made while the query ran. When the query fails, the
// current limits stay in use until the next refresh interval.
func reloadChannelQuotaLimits(db *gorm.DB) {
	channelQuotaLimitMu.Lock()
	since := channelQuotaLimitGeneration
	channelQuotaLimitMu.Unlock()

	var rows []ChannelQuotaLimit
	var err error
	if db != nil {
		err = db.Find(&rows).Error
	}

	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	var current map[int]ChannelQuotaLimit
	if snapshot := channelQuotaLimitCurrent.Load(); snapshot != nil {
		current = snapshot.limits
	}
	limits := current
	if err != nil {
		common.SysLog("failed to load channel quota limits: " + err.Error())
		if limits == nil {
			limits = map[int]ChannelQuotaLimit{}
		}
	} else {
		limits = make(map[int]ChannelQuotaLimit, len(rows))
		for _, row := range rows {
			limits[row.ChannelId] = row
		}
		for channelId, generation := range channelQuotaLimitWrites {
			if generation <= since {
				// The query started after this change and has read it.
				delete(channelQuotaLimitWrites, channelId)
				continue
			}
			if limit, ok := current[channelId]; ok {
				limits[channelId] = limit
			} else {
				delete(limits, channelId)
			}
		}
	}
	channelQuotaLimitCurrent.Store(&channelQuotaLimitSnapshot{limits: limits, loadedAt: time.Now(), complete: true})
}

// storeChannelQuotaLimit replaces one entry of the snapshot (nil removes it).
func storeChannelQuotaLimit(channelId int, limit *ChannelQuotaLimit) {
	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	next := &channelQuotaLimitSnapshot{}
	if current := channelQuotaLimitCurrent.Load(); current != nil {
		*next = *current
	}
	limits := make(map[int]ChannelQuotaLimit, len(next.limits)+1)
	maps.Copy(limits, next.limits)
	if limit == nil {
		delete(limits, channelId)
	} else {
		limits[channelId] = *limit
	}
	next.limits = limits
	channelQuotaLimitGeneration++
	channelQuotaLimitWrites[channelId] = channelQuotaLimitGeneration
	channelQuotaLimitCurrent.Store(next)
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
// quota and billed requests against the channel's limit, if it has one.
func AddChannelQuotaLimitUsage(channelId int, quota int, requests int) {
	if (quota == 0 && requests == 0) || channelId <= 0 {
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
			row.UsedCount = 0
			row.PeriodStart = periodStart
		}
		row.UsedQuota = max(row.UsedQuota+int64(quota), 0)
		row.UsedCount = max(row.UsedCount+int64(requests), 0)
		row.UpdatedAt = now.Unix()
		updated = row
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"used_quota":   row.UsedQuota,
			"used_count":   row.UsedCount,
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

// SetChannelQuotaLimit creates or updates a channel's limits, keeping its usage.
// With both limits at 0 the limit is removed and nil is returned.
func SetChannelQuotaLimit(channelId int, limitQuota int64, limitCount int64, dailyReset bool, message string) (*ChannelQuotaLimit, error) {
	limitQuota, limitCount = max(limitQuota, 0), max(limitCount, 0)
	if limitQuota == 0 && limitCount == 0 {
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
				ChannelId: channelId, LimitQuota: limitQuota, LimitCount: limitCount, DailyReset: dailyReset, Message: message,
				PeriodStart: channelQuotaPeriodStart(now), UpdatedAt: now.Unix(),
			}
			return tx.Create(&saved).Error
		}
		row := rows[0]
		row.LimitQuota, row.LimitCount, row.DailyReset, row.Message, row.UpdatedAt = limitQuota, limitCount, dailyReset, message, now.Unix()
		saved = row
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"limit_quota": row.LimitQuota,
			"limit_count": row.LimitCount,
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

// ResetChannelQuotaLimitUsage sets the channel's counted usage and requests back to zero.
func ResetChannelQuotaLimitUsage(channelId int) (*ChannelQuotaLimit, error) {
	now := time.Now()
	var saved ChannelQuotaLimit
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("channel_id = ?", channelId).First(&saved).Error; err != nil {
			return err
		}
		saved.UsedQuota, saved.UsedCount, saved.PeriodStart, saved.UpdatedAt = 0, 0, channelQuotaPeriodStart(now), now.Unix()
		return tx.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", channelId).Updates(map[string]any{
			"used_quota":   saved.UsedQuota,
			"used_count":   saved.UsedCount,
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
