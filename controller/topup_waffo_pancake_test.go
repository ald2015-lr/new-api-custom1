package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatWaffoPancakeAmount_UsesDisplayPriceString(t *testing.T) {
	testCases := []struct {
		name     string
		amount   float64
		expected string
	}{
		{name: "whole amount", amount: 29, expected: "29.00"},
		{name: "decimal amount", amount: 29.9, expected: "29.90"},
		{name: "round half up to cents", amount: 29.999, expected: "30.00"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, formatWaffoPancakeAmount(tc.amount))
		})
	}
}

func TestGetWaffoPancakePayMoney(t *testing.T) {
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := make(map[int]float64, len(operation_setting.GetPaymentSetting().AmountDiscount))
	for k, v := range operation_setting.GetPaymentSetting().AmountDiscount {
		originalDiscounts[k] = v
	}
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()
	originalFeePassThrough := setting.WaffoPancakeFeePassThrough

	t.Cleanup(func() {
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		setting.WaffoPancakeFeePassThrough = originalFeePassThrough
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	setting.WaffoPancakeUnitPrice = 2.5
	setting.WaffoPancakeFeePassThrough = false
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{
		10:                           0.8,
		int(common.QuotaPerUnit * 3): 0.5,
		20:                           0,
	}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.2}`))

	testCases := []struct {
		name             string
		amount           int64
		group            string
		quotaDisplayType string
		expected         float64
	}{
		{
			name:             "currency display applies unit price group ratio and discount",
			amount:           10,
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         24,
		},
		{
			name:             "tokens display converts quota to display units before pricing",
			amount:           int64(common.QuotaPerUnit * 3),
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeTokens,
			expected:         4.5,
		},
		{
			name:             "non-positive discount falls back to no discount",
			amount:           20,
			group:            "default",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         50,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.quotaDisplayType
			actual := getWaffoPancakePayMoney(tc.amount, tc.group)
			require.InDelta(t, tc.expected, actual, 0.000001)
		})
	}
}

// 手续费转嫁：用户实付金额上浮后，扣除 Waffo 手续费（rate + fixed）
// 的净收入必须回到原始标价。
func TestGetWaffoPancakePayMoney_FeePassThrough(t *testing.T) {
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := make(map[int]float64, len(operation_setting.GetPaymentSetting().AmountDiscount))
	for k, v := range operation_setting.GetPaymentSetting().AmountDiscount {
		originalDiscounts[k] = v
	}
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()
	originalFeePassThrough := setting.WaffoPancakeFeePassThrough

	t.Cleanup(func() {
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		setting.WaffoPancakeFeePassThrough = originalFeePassThrough
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	setting.WaffoPancakeUnitPrice = 1
	setting.WaffoPancakeFeePassThrough = true
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))

	testCases := []struct {
		name        string
		amount      int64
		expectedPay float64
	}{
		{name: "minimum top up", amount: 50, expectedPay: 55.78},
		{name: "hundred", amount: 100, expectedPay: 107.80},
		{name: "five hundred", amount: 500, expectedPay: 524.04},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			payMoney := getWaffoPancakePayMoney(tc.amount, "default")
			require.InDelta(t, tc.expectedPay, payMoney, 0.005)

			net := payMoney - payMoney*setting.WaffoPancakeFeeRate - setting.WaffoPancakeFeeFixed
			assert.InDelta(t, float64(tc.amount), net, 0.01)
		})
	}
}

func TestGetWaffoPancakePayMoney_FeePassThroughDisabled(t *testing.T) {
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()
	originalFeePassThrough := setting.WaffoPancakeFeePassThrough

	t.Cleanup(func() {
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		setting.WaffoPancakeFeePassThrough = originalFeePassThrough
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	setting.WaffoPancakeUnitPrice = 1
	setting.WaffoPancakeFeePassThrough = false
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))

	assert.InDelta(t, 50.0, getWaffoPancakePayMoney(50, "default"), 0.000001)
}
