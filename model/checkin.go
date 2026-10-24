package model

import (
	"math/rand"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Checkin 签到记录
type Checkin struct {
	Id           int    `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId       int    `json:"user_id" gorm:"not null;uniqueIndex:idx_user_checkin_date"`
	CheckinDate  string `json:"checkin_date" gorm:"type:varchar(10);not null;uniqueIndex:idx_user_checkin_date"` // 格式: YYYY-MM-DD
	QuotaAwarded int    `json:"quota_awarded" gorm:"not null"`
	CreatedAt    int64  `json:"created_at" gorm:"bigint"`
}

// CheckinRecord 用于API返回的签到记录（不包含敏感字段）
type CheckinRecord struct {
	CheckinDate  string `json:"checkin_date"`
	QuotaAwarded int    `json:"quota_awarded"`
}

func (Checkin) TableName() string {
	return "checkins"
}

// GetUserCheckinRecords 获取用户在指定日期范围内的签到记录
func GetUserCheckinRecords(userId int, startDate, endDate string) ([]Checkin, error) {
	var records []Checkin
	err := DB.Where("user_id = ? AND checkin_date >= ? AND checkin_date <= ?",
		userId, startDate, endDate).
		Order("checkin_date DESC").
		Find(&records).Error
	return records, err
}

// HasCheckedInToday 检查用户今天是否已签到
func HasCheckedInToday(userId int) (bool, error) {
	today := time.Now().Format("2006-01-02")
	var count int64
	err := DB.Model(&Checkin{}).
		Where("user_id = ? AND checkin_date = ?", userId, today).
		Count(&count).Error
	return count > 0, err
}

// CheckinResult is the outcome of a successful check-in.
type CheckinResult struct {
	Checkin               *Checkin
	BaseQuota             int // the random reward before the paid multiplier
	PaidMultiplierApplied bool
	PaidMultiplier        float64
	IsPaidUser            bool
	ExpireDays            int   // 0 = never expires
	ExpiresAt             int64 // 0 = never expires; the current effective deadline
	ExpireOnlyWhenUnused  bool
	grant                 *BonusGrant
}

// LogMessage is the check-in log text: the reward, the paid multiplier and
// when the reward expires.
func (r *CheckinResult) LogMessage() *common.Message {
	quota := int64(r.Checkin.QuotaAwarded)
	if r.PaidMultiplierApplied {
		return bonusGrantMessage([3]string{
			"Daily check-in (paid user ×{{multiplier}}), received {{quota}}",
			"Daily check-in (paid user ×{{multiplier}}), received {{quota}}, expires at {{expires_at}}",
			"Daily check-in (paid user ×{{multiplier}}), received {{quota}}, expires if unused for {{days}} days (currently {{expires_at}})",
		}, quota, r.grant, map[string]any{"multiplier": strconv.FormatFloat(r.PaidMultiplier, 'f', -1, 64)})
	}
	return bonusGrantMessage([3]string{
		"Daily check-in, received {{quota}}",
		"Daily check-in, received {{quota}}, expires at {{expires_at}}",
		"Daily check-in, received {{quota}}, expires if unused for {{days}} days (currently {{expires_at}})",
	}, quota, r.grant, nil)
}

// UserCheckin 执行用户签到
// 免费用户获得 min..max 之间的随机额度；累计充值达到门槛的付费用户再乘以倍率（向下取整）。
// 设置了有效天数的奖励记为会过期的赠送额度（先用先扣）。
// MySQL 和 PostgreSQL 使用事务保证原子性
// SQLite 不支持嵌套事务，使用顺序操作 + 手动回滚
func UserCheckin(userId int) (*CheckinResult, error) {
	setting := operation_setting.GetCheckinSetting()
	if !setting.Enabled {
		return nil, common.NewMessage("Check-in feature is not enabled")
	}

	// 检查今天是否已签到
	hasChecked, err := HasCheckedInToday(userId)
	if err != nil {
		return nil, err
	}
	if hasChecked {
		return nil, common.NewMessage("Already checked in today")
	}

	// 计算随机额度奖励
	baseQuota := setting.MinQuota
	if setting.MaxQuota > setting.MinQuota {
		baseQuota = setting.MinQuota + rand.Intn(setting.MaxQuota-setting.MinQuota+1)
	}

	recharged, err := GetUserRechargeQuota(userId)
	if err != nil {
		common.SysError(common.LogText("failed to read the cumulative recharge of user %d: %s", userId, err.Error()))
		return nil, common.NewMessage("Check-in failed, please try again later")
	}
	result := &CheckinResult{
		BaseQuota:      baseQuota,
		PaidMultiplier: setting.PaidMultiplier,
		IsPaidUser:     operation_setting.IsPaidCheckinUser(recharged),
	}
	quotaAwarded := baseQuota
	if result.IsPaidUser && setting.PaidMultiplier > 1 {
		multiplied := common.QuotaFromDecimal(decimal.NewFromInt(int64(baseQuota)).Mul(decimal.NewFromFloat(setting.PaidMultiplier)).Floor())
		quotaAwarded = max(multiplied, baseQuota)
		result.PaidMultiplierApplied = true
	}
	source := BonusSourceCheckinFree
	if result.IsPaidUser {
		source = BonusSourceCheckinPaid
	}
	result.ExpireDays = operation_setting.CheckinExpireDays(result.IsPaidUser)

	today := time.Now().Format("2006-01-02")
	checkin := &Checkin{
		UserId:       userId,
		CheckinDate:  today,
		QuotaAwarded: quotaAwarded,
		CreatedAt:    time.Now().Unix(),
	}
	result.Checkin = checkin
	spec := bonusGrantSpec{Source: source, Amount: int64(quotaAwarded), ExpireDays: result.ExpireDays, RefType: BonusRefCheckin}
	if spec.ExpireDays > 0 {
		// 批量更新模式下先写入本节点暂存的钱包消费，使签到前的消费在新奖励之前结算
		flushUserBatchedDeltas(userId)
	}

	// 根据数据库类型选择不同的策略
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		// SQLite 不支持嵌套事务，使用顺序操作 + 手动回滚
		result.grant, err = userCheckinWithoutTransaction(checkin, spec)
	} else {
		// MySQL 和 PostgreSQL 支持事务，使用事务保证原子性
		result.grant, err = userCheckinWithTransaction(checkin, spec)
	}
	if err != nil {
		return nil, err
	}
	if result.grant == nil {
		result.ExpireDays = 0
	} else {
		result.ExpiresAt = result.grant.ExpiresAt
		result.ExpireOnlyWhenUnused = bonusSliding(source)
	}

	// 事务成功后同步缓存余额，使奖励立即可用
	if err := cacheIncrUserQuota(userId, int64(quotaAwarded)); err != nil {
		common.SysLog(common.LogText("failed to sync check-in quota to user quota cache: %s", err.Error()))
	}
	// 被邀请人签到时顺带检查邀请奖励门槛（例如管理员降低了门槛）
	if err := GrantInviteRewardIfQualified(userId); err != nil {
		common.SysError(common.LogText("failed to grant the invite reward of user %d: %s", userId, err.Error()))
	}
	return result, nil
}

// userCheckinWithTransaction 使用事务执行签到（适用于 MySQL 和 PostgreSQL）
func userCheckinWithTransaction(checkin *Checkin, spec bonusGrantSpec) (*BonusGrant, error) {
	var grant *BonusGrant
	err := DB.Transaction(func(tx *gorm.DB) error {
		// 步骤1: 创建签到记录
		// 数据库有唯一约束 (user_id, checkin_date)，可以防止并发重复签到
		if err := tx.Create(checkin).Error; err != nil {
			return common.NewMessage("Check-in failed, please try again later")
		}

		// 步骤2: 锁定用户行，在事务中增加用户额度并记录会过期的赠送额度
		acct, err := lockBonusAccount(tx, checkin.UserId)
		if err == nil {
			spec.RefId = checkin.Id
			grant, err = grantWalletBonusTx(tx, acct, spec, common.GetTimestamp())
		}
		if err != nil {
			common.SysError(common.LogText("check-in quota update failed for user %d: %s", checkin.UserId, err.Error()))
			return common.NewMessage("Check-in failed: quota update error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return grant, nil
}

// userCheckinWithoutTransaction 不使用事务执行签到（适用于 SQLite）
func userCheckinWithoutTransaction(checkin *Checkin, spec bonusGrantSpec) (*BonusGrant, error) {
	// 步骤1: 创建签到记录
	// 数据库有唯一约束 (user_id, checkin_date)，可以防止并发重复签到
	if err := DB.Create(checkin).Error; err != nil {
		return nil, common.NewMessage("Check-in failed, please try again later")
	}

	// 步骤2: 在独立事务中锁定用户行，增加用户额度并记录会过期的赠送额度
	var grant *BonusGrant
	err := DB.Transaction(func(tx *gorm.DB) error {
		acct, err := lockBonusAccount(tx, checkin.UserId)
		if err != nil {
			return err
		}
		spec.RefId = checkin.Id
		grant, err = grantWalletBonusTx(tx, acct, spec, common.GetTimestamp())
		return err
	})
	if err != nil {
		// 如果增加额度失败，需要回滚签到记录
		common.SysError(common.LogText("check-in quota update failed for user %d: %s", checkin.UserId, err.Error()))
		DB.Delete(checkin)
		return nil, common.NewMessage("Check-in failed: quota update error")
	}
	return grant, nil
}

// GetUserCheckinStats 获取用户签到统计信息
func GetUserCheckinStats(userId int, month string) (map[string]any, error) {
	// 获取指定月份的所有签到记录
	startDate := month + "-01"
	endDate := month + "-31"

	records, err := GetUserCheckinRecords(userId, startDate, endDate)
	if err != nil {
		return nil, err
	}

	// 转换为不包含敏感字段的记录
	checkinRecords := make([]CheckinRecord, len(records))
	for i, r := range records {
		checkinRecords[i] = CheckinRecord{
			CheckinDate:  r.CheckinDate,
			QuotaAwarded: r.QuotaAwarded,
		}
	}

	// 检查今天是否已签到
	hasCheckedToday, _ := HasCheckedInToday(userId)

	// 获取用户所有时间的签到统计
	var totalCheckins int64
	var totalQuota int64
	DB.Model(&Checkin{}).Where("user_id = ?", userId).Count(&totalCheckins)
	DB.Model(&Checkin{}).Where("user_id = ?", userId).Select("COALESCE(SUM(quota_awarded), 0)").Scan(&totalQuota)

	return map[string]any{
		"total_quota":      totalQuota,      // 所有时间累计获得的额度
		"total_checkins":   totalCheckins,   // 所有时间累计签到次数
		"checkin_count":    len(records),    // 本月签到次数
		"checked_in_today": hasCheckedToday, // 今天是否已签到
		"records":          checkinRecords,  // 本月签到记录详情（不含id和user_id）
	}, nil
}
