package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Manual completion credits the same quota as each provider's own callback:
// Creem stores raw quota in Amount, Stripe credits Money, others credit Amount units.
func TestManualCompleteTopUpCreditsProviderQuota(t *testing.T) {
	truncateTables(t)
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	cases := []struct {
		name      string
		userId    int
		topUp     TopUp
		wantQuota int
	}{
		{
			name:      "creem amount is raw quota",
			userId:    901,
			topUp:     TopUp{Amount: 500000, Money: 1, PaymentMethod: PaymentMethodCreem, PaymentProvider: PaymentProviderCreem},
			wantQuota: 500000,
		},
		{
			name:      "legacy creem row without provider",
			userId:    902,
			topUp:     TopUp{Amount: 250000, Money: 0.5, PaymentMethod: PaymentMethodCreem},
			wantQuota: 250000,
		},
		{
			name:      "stripe credits money",
			userId:    903,
			topUp:     TopUp{Amount: 10, Money: 12, PaymentMethod: PaymentMethodStripe},
			wantQuota: 12 * 500000,
		},
		{
			name:      "epay credits amount units",
			userId:    904,
			topUp:     TopUp{Amount: 50, Money: 365, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay},
			wantQuota: 50 * 500000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, DB.Create(&User{Id: tc.userId, Username: tc.name[:6] + common.GetRandomString(6), Status: common.UserStatusEnabled, AffCode: common.GetRandomString(8)}).Error)
			topUp := tc.topUp
			topUp.UserId = tc.userId
			topUp.TradeNo = "manual-" + common.GetRandomString(10)
			topUp.Status = common.TopUpStatusPending
			require.NoError(t, DB.Create(&topUp).Error)

			require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
			quota, err := GetUserQuota(tc.userId, true)
			require.NoError(t, err)
			assert.Equal(t, tc.wantQuota, quota)

			// Completing again is a no-op: no second credit and no log for user 0.
			require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
			quota, err = GetUserQuota(tc.userId, true)
			require.NoError(t, err)
			assert.Equal(t, tc.wantQuota, quota)
			var orphanLogs int64
			require.NoError(t, DB.Model(&Log{}).Where("user_id = ?", 0).Count(&orphanLogs).Error)
			assert.Zero(t, orphanLogs)
		})
	}
}
