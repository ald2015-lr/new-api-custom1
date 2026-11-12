package novelai

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
)

const (
	// imageHost is NovelAI's image host. It also serves the account API:
	// NovelAI retired /user/* on api.novelai.net, which now answers every
	// call with 400 "update to the image URL".
	imageHost = "image.novelai.net"

	// subscriptionPath answers the account's tier and Anlas for its API token
	// without spending any.
	subscriptionPath = "/user/subscription"

	// subscriptionResponseLimit bounds the account response read: a NovelAI
	// subscription is well under 1 KiB.
	subscriptionResponseLimit = 256 << 10
	subscriptionTimeout       = 30 * time.Second
	// maxAnlas keeps a balance exact in a float64 balance column and a
	// JavaScript number.
	maxAnlas = 1<<53 - 1
	// maxErrorMessageRunes bounds the upstream message kept in an error,
	// which may become a channel's disable reason.
	maxErrorMessageRunes = 300
)

// AccountBaseURL returns the base URL of the NovelAI account API for a NovelAI
// channel's base URL: the base URL itself, and NovelAI's image host
// (https://image.novelai.net) for an empty one. NovelAI answers
// /user/subscription on its image host, and a NovelAI-compatible forwarder
// serves the account endpoints next to the image ones.
func AccountBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return constant.GetChannelBaseURL(constant.ChannelTypeNovelAI)
	}
	return baseURL
}

// Subscription is what new-api reads of a NovelAI account.
type Subscription struct {
	Tier   int64
	Active bool
	// Anlas is the account's Anlas, fixedTrainingStepsLeft plus
	// purchasedTrainingSteps; nil when the answer lacks either or one is not
	// an integer between 0 and maxAnlas.
	Anlas *int64
}

// FetchSubscription reads the NovelAI account behind key with GET
// AccountBaseURL(baseURL) + /user/subscription, which spends no Anlas. The
// request goes through the channel's proxy and header overrides, like the
// relay. A redirect is returned instead of followed, so the key never reaches
// another host. The answer counts only with status 200 and a JSON object
// carrying a numeric tier and a boolean active, read within
// subscriptionResponseLimit. A failure is a NewAPIError whose message is a
// web console message: with the upstream status and NovelAI's message for
// another status, so that a 401 disables the channel under the automatic
// disable rules.
func FetchSubscription(ctx context.Context, baseURL, key string, setting dto.ChannelSettings, headerOverride map[string]any) (*Subscription, *types.NewAPIError) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	requestFailed := func(err error) *types.NewAPIError {
		// A transport error repeats the URL, which carries no key.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return types.NewError(common.NewMessage("NovelAI account request failed: {{error}}", map[string]any{"error": err.Error()}), types.ErrorCodeDoRequestFailed)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, AccountBaseURL(baseURL)+subscriptionPath, nil)
	if err != nil {
		return nil, requestFailed(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	overrides, err := channel.ResolveHeaderOverride(&relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta:   &relaycommon.ChannelMeta{ApiKey: key, HeadersOverride: headerOverride},
	}, nil)
	if err != nil {
		return nil, requestFailed(err)
	}
	for name, value := range overrides {
		request.Header.Set(name, value)
		if strings.EqualFold(name, "Host") {
			request.Host = value
		}
	}
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return nil, requestFailed(err)
	}
	// The cached client is shared, so the redirect policy goes on a copy.
	accountClient := *client
	accountClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := accountClient.Do(request)
	if err != nil {
		return nil, requestFailed(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, subscriptionResponseLimit+1))
	if err != nil {
		return nil, requestFailed(err)
	}
	if response.StatusCode != http.StatusOK {
		// NovelAI errors are {"statusCode":401,"message":"..."}.
		var failure struct {
			Message string `json:"message"`
		}
		_ = common.Unmarshal(body, &failure)
		message := strings.TrimSpace(failure.Message)
		if runes := []rune(message); len(runes) > maxErrorMessageRunes {
			message = string(runes[:maxErrorMessageRunes]) + "…"
		}
		statusErr := common.NewMessage("NovelAI account answered HTTP {{status}}", map[string]any{"status": response.StatusCode})
		if message != "" {
			statusErr = common.NewMessage("NovelAI account answered HTTP {{status}}: {{message}}", map[string]any{"status": response.StatusCode, "message": message})
		}
		return nil, types.NewErrorWithStatusCode(statusErr, types.ErrorCodeBadResponseStatusCode, response.StatusCode)
	}
	if len(body) > subscriptionResponseLimit {
		return nil, types.NewError(common.NewMessage("NovelAI account response exceeds {{limit}} bytes", map[string]any{"limit": subscriptionResponseLimit}), types.ErrorCodeBadResponseBody)
	}

	var account struct {
		Tier   *int64 `json:"tier"`
		Active *bool  `json:"active"`
		// Decoded on its own, so that an odd balance (a value of another
		// type, or a number past float64) leaves the account readable and
		// only Anlas unknown.
		TrainingStepsLeft common.RawMessage `json:"trainingStepsLeft"`
	}
	if err := common.Unmarshal(body, &account); err != nil || account.Tier == nil || account.Active == nil {
		return nil, types.NewError(common.NewMessage("NovelAI account response is not a subscription"), types.ErrorCodeBadResponseBody)
	}
	subscription := &Subscription{Tier: *account.Tier, Active: *account.Active}
	var steps struct {
		Fixed     common.RawMessage `json:"fixedTrainingStepsLeft"`
		Purchased common.RawMessage `json:"purchasedTrainingSteps"`
	}
	var fixed, purchased *float64
	if common.Unmarshal(account.TrainingStepsLeft, &steps) != nil ||
		common.Unmarshal(steps.Fixed, &fixed) != nil || common.Unmarshal(steps.Purchased, &purchased) != nil ||
		fixed == nil || purchased == nil {
		return subscription, nil
	}
	// Both are whole and within maxAnlas, so the difference and the
	// conversions are exact.
	if *fixed >= 0 && *purchased >= 0 && *fixed == math.Trunc(*fixed) && *purchased == math.Trunc(*purchased) &&
		*purchased <= maxAnlas && *fixed <= maxAnlas-*purchased {
		anlas := int64(*fixed) + int64(*purchased)
		subscription.Anlas = &anlas
	}
	return subscription, nil
}
