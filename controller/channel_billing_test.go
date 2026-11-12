package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDeepSeekBalanceUSD(t *testing.T) {
	tests := []struct {
		name            string
		responseJSON    string
		usdExchangeRate float64
		want            float64
		wantErrContains string
	}{
		{
			name:            "prefers USD when USD precedes CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"12.50"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "prefers USD when CNY precedes USD",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"},{"currency":"USD","total_balance":"12.50"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "converts CNY when USD is absent",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "converts CNY when USD balance is zero",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "converts CNY when zero USD follows CNY",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"},{"currency":"USD","total_balance":"0.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "returns zero when USD balance is zero and CNY is absent",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"0.00"}]}`,
			usdExchangeRate: 7.3,
			want:            0,
		},
		{
			name:            "returns error when USD and CNY are absent",
			responseJSON:    `{"balance_infos":[{"currency":"EUR","total_balance":"10.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "currency USD or CNY not found",
		},
		{
			name:            "returns USD parse error instead of falling back to CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"invalid"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "invalid syntax",
		},
		{
			name:            "rejects NaN USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"NaN"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be finite",
		},
		{
			name:            "rejects negative USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"-1.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be non-negative",
		},
		{
			name:            "rejects positive infinity CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"+Inf"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be finite",
		},
		{
			name:            "rejects negative CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"-7.30"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be non-negative",
		},
		{
			name:            "returns error for non-positive CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 0,
			wantErrContains: "USD exchange rate must be greater than zero",
		},
		{
			name:            "rejects NaN CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.NaN(),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects positive infinity CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.Inf(1),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects CNY conversion overflow",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"1.7976931348623157e+308"}]}`,
			usdExchangeRate: math.SmallestNonzeroFloat64,
			wantErrContains: "converted USD balance must be finite",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response DeepSeekUsageResponse
			require.NoError(t, common.Unmarshal([]byte(test.responseJSON), &response))

			balance, err := getDeepSeekBalanceUSD(response, test.usdExchangeRate)
			if test.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErrContains)
				return
			}

			require.NoError(t, err)
			assert.InDelta(t, test.want, balance, 1e-12)
		})
	}
}

func TestUpdateChannelNewAPIBalanceUsesTheSmallerRemainingQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   string
		want    float64
		wantErr string
	}{
		{name: "user quota below the key quota", usage: `{"code":true,"data":{"total_available":2000000,"unlimited_quota":false,"user_quota":{"total_available":500000}}}`, want: 0.5},
		{name: "key quota below the user quota", usage: `{"code":true,"data":{"total_available":500000,"unlimited_quota":false,"user_quota":{"total_available":2000000}}}`, want: 0.5},
		{name: "unlimited key uses the user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true,"user_quota":{"total_available":3000000}}}`, want: 3},
		{name: "key quota alone without user quota", usage: `{"code":true,"data":{"total_available":1500000,"unlimited_quota":false}}`, want: 1.5},
		{name: "unlimited key without user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true}}`, wantErr: "Allow querying user quota"},
		{name: "upstream rejects the key", usage: `{"success":false,"message":"invalid token"}`, wantErr: "invalid token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/usage/token/":
					assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
					_, _ = w.Write([]byte(tc.usage))
				case "/api/status":
					assert.Empty(t, r.Header.Get("Authorization"), "the key is not sent to the public status endpoint")
					_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			baseURL := server.URL + "/"
			channel := &model.Channel{Type: constant.ChannelTypeNewAPI, BaseURL: &baseURL, Key: "sk-upstream", Name: "new-api-upstream"}
			require.NoError(t, model.DB.Create(channel).Error)

			result, err := updateChannelBalance(channel)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, result.Balance, 1e-9)
			assert.InDelta(t, tc.want, channel.Balance, 1e-9)
		})
	}
}

// A NovelAI balance is the account's Anlas, fixed plus purchased, stored as
// the channel balance; an answer without a valid Anlas balance stores
// nothing. Anlas are not USD, so a NovelAI channel at 0 Anlas is not disabled
// as out of balance: its subscription may still cover generations.
func TestUpdateChannelNovelAIBalanceStoresAnlas(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       float64
		wantErr    string
	}{
		{name: "fixed and purchased Anlas", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":10000,"purchasedTrainingSteps":250}}`, want: 10250},
		{name: "no Anlas", status: http.StatusOK, body: `{"tier":3,"active":true}`, wantErr: "NovelAI account response carries no valid Anlas balance"},
		{name: "rejected key", status: http.StatusUnauthorized, body: `{"statusCode":401,"message":"Invalid accessToken."}`, wantErr: "NovelAI account answered HTTP 401: Invalid accessToken."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/user/subscription", r.URL.Path)
				assert.Equal(t, "Bearer pst-test", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			channel := &model.Channel{Type: constant.ChannelTypeNovelAI, BaseURL: common.GetPointer(server.URL), Key: "pst-test", Name: "novelai"}
			require.NoError(t, model.DB.Create(channel).Error)

			result, err := updateChannelBalance(channel)

			stored, getErr := model.GetChannelById(channel.Id, true)
			require.NoError(t, getErr)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				assert.Zero(t, stored.BalanceUpdatedTime)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.Balance)
			assert.Equal(t, tc.want, stored.Balance)
			assert.NotZero(t, stored.BalanceUpdatedTime)
		})
	}

	t.Run("0 Anlas keeps the channel enabled", func(t *testing.T) {
		setupSub2APIBalanceTest(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":0,"purchasedTrainingSteps":0}}`))
		}))
		defer server.Close()
		channel := &model.Channel{Type: constant.ChannelTypeNovelAI, BaseURL: common.GetPointer(server.URL), Key: "pst-test", Name: "novelai", Status: common.ChannelStatusEnabled, Balance: 5, AutoBan: common.GetPointer(1)}
		require.NoError(t, model.DB.Create(channel).Error)

		require.NoError(t, updateAllChannelsBalance())

		stored, err := model.GetChannelById(channel.Id, true)
		require.NoError(t, err)
		assert.Zero(t, stored.Balance)
		assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	})

	// Editing a channel to or from NovelAI clears its balance, which would
	// otherwise be read in the other unit until the next balance query.
	for _, tc := range []struct {
		name                     string
		fromType, toType         int
		balance, wantBalance     float64
		updatedTime, wantUpdated int64
	}{
		{name: "NovelAI to OpenAI", fromType: constant.ChannelTypeNovelAI, toType: constant.ChannelTypeOpenAI, balance: 10250, updatedTime: 1700000000},
		{name: "OpenAI to NovelAI", fromType: constant.ChannelTypeOpenAI, toType: constant.ChannelTypeNovelAI, balance: 500, updatedTime: 1700000000},
		{name: "NovelAI kept", fromType: constant.ChannelTypeNovelAI, toType: constant.ChannelTypeNovelAI, balance: 10250, updatedTime: 1700000000, wantBalance: 10250, wantUpdated: 1700000000},
		{name: "OpenAI to DeepSeek", fromType: constant.ChannelTypeOpenAI, toType: constant.ChannelTypeDeepSeek, balance: 500, updatedTime: 1700000000, wantBalance: 500, wantUpdated: 1700000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupTaskPluginBindChannelTest(t)
			channel := &model.Channel{Type: tc.fromType, Key: "pst-test", Name: "edited", Status: common.ChannelStatusEnabled, Models: "nai-diffusion-3", Group: "default",
				Balance: tc.balance, BalanceUpdatedTime: tc.updatedTime}
			require.NoError(t, channel.Insert())

			recorder := putUpdateChannel(t, 1, common.RoleRootUser,
				fmt.Sprintf(`{"id":%d,"type":%d,"name":"renamed","models":"nai-diffusion-3","group":"default","balance":0}`, channel.Id, tc.toType))

			require.Contains(t, recorder.Body.String(), `"success":true`)
			stored, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, tc.toType, stored.Type)
			assert.Equal(t, tc.wantBalance, stored.Balance)
			assert.Equal(t, tc.wantUpdated, stored.BalanceUpdatedTime)
		})
	}
}
