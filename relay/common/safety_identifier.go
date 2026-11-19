package common

import (
	"context"
	"os"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/MoeForge/moego/ojson"
)

// GatewaySafetyIdentifier is the safety_identifier sent for userID: "u_" and
// the first 32 hex characters of HMAC-SHA256 keyed with the server's crypto
// secret, so OpenAI can block one user without learning who they are.
func GatewaySafetyIdentifier(userID int) string {
	return "u_" + common.GenerateHMACWithKey([]byte("safety-identifier-v1:"+common.CryptoSecret), "new-api-user:"+strconv.Itoa(userID))[:32]
}

var unstableSafetyIdentifierWarning sync.Once

// ApplyGatewaySafetyIdentifier sets safety_identifier on an OpenAI Chat
// Completions or Responses body sent to an OpenAI channel that has
// send_safety_identifier on, replacing any client value. Every other member
// keeps the body's bytes and order; any other body is returned unchanged.
func ApplyGatewaySafetyIdentifier(jsonData []byte, info *RelayInfo) ([]byte, error) {
	if info == nil || info.ChannelMeta == nil || !SupportsOpenAISafetyFeatures(info.ChannelType) ||
		!info.ChannelOtherSettings.SendSafetyIdentifier || info.UserId <= 0 {
		return jsonData, nil
	}
	if !ojson.ValidBytes(jsonData) || !ojson.ParseBytes(jsonData).IsObject() {
		return jsonData, nil
	}
	if os.Getenv("CRYPTO_SECRET") == "" && os.Getenv("SESSION_SECRET") == "" {
		unstableSafetyIdentifierWarning.Do(func() {
			logger.LogWarn(context.Background(), common.LogText("send_safety_identifier is on for channel #%d but neither CRYPTO_SECRET nor SESSION_SECRET is set; the identifier sent to OpenAI changes at every restart", info.ChannelId))
		})
	}
	doc := ojson.NewDoc(jsonData)
	if err := SetJSONMember(doc, "", "safety_identifier", GatewaySafetyIdentifier(info.UserId)); err != nil {
		return nil, err
	}
	return doc.Bytes(), nil
}
