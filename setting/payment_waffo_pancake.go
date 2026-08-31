package setting

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	WaffoPancakeUnitPrice  float64 = 1.0
	WaffoPancakeMinTopUp   int     = 50
	WaffoPancakeStoreID    string
	WaffoPancakeProductID  string

	// 手续费转嫁：开启后向用户收取的金额会上浮，
	// 使得扣除 Waffo 手续费后商户净收入等于原始标价。
	WaffoPancakeFeePassThrough bool    = true
	WaffoPancakeFeeRate        float64 = 0.039
	WaffoPancakeFeeFixed       float64 = 3.6 // 0.5 美元按 7.2 汇率折算成人民币
)
