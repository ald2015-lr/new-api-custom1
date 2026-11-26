package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The session refresh limit is on whenever the critical limit is, unless
// AUTH_REFRESH_RATE_LIMIT_ENABLE says otherwise; a count or window that is
// not positive falls back to the default.
func TestAuthRefreshRateLimitEnv(t *testing.T) {
	previous := []any{CriticalRateLimitEnable, AuthRefreshRateLimitEnable, AuthRefreshRateLimitNum, AuthRefreshRateLimitDuration}
	t.Cleanup(func() {
		CriticalRateLimitEnable, AuthRefreshRateLimitEnable = previous[0].(bool), previous[1].(bool)
		AuthRefreshRateLimitNum, AuthRefreshRateLimitDuration = previous[2].(int), previous[3].(int64)
	})
	for _, tc := range []struct {
		name         string
		critical     bool
		env          map[string]string
		wantEnabled  bool
		wantNum      int
		wantDuration int64
	}{
		{name: "defaults", critical: true, wantEnabled: true, wantNum: 300, wantDuration: 1200},
		{name: "critical limit off", critical: false, wantEnabled: false, wantNum: 300, wantDuration: 1200},
		{name: "on with the critical limit off", critical: false, env: map[string]string{"AUTH_REFRESH_RATE_LIMIT_ENABLE": "true"}, wantEnabled: true, wantNum: 300, wantDuration: 1200},
		{name: "off", critical: true, env: map[string]string{"AUTH_REFRESH_RATE_LIMIT_ENABLE": "false"}, wantEnabled: false, wantNum: 300, wantDuration: 1200},
		{name: "custom budget", critical: true, env: map[string]string{"AUTH_REFRESH_RATE_LIMIT": "50", "AUTH_REFRESH_RATE_LIMIT_DURATION": "600"}, wantEnabled: true, wantNum: 50, wantDuration: 600},
		{name: "not positive", critical: true, env: map[string]string{"AUTH_REFRESH_RATE_LIMIT": "0", "AUTH_REFRESH_RATE_LIMIT_DURATION": "-5"}, wantEnabled: true, wantNum: 300, wantDuration: 1200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"AUTH_REFRESH_RATE_LIMIT_ENABLE", "AUTH_REFRESH_RATE_LIMIT", "AUTH_REFRESH_RATE_LIMIT_DURATION"} {
				t.Setenv(name, tc.env[name])
			}
			CriticalRateLimitEnable = tc.critical
			initAuthRefreshRateLimitEnv()
			assert.Equal(t, tc.wantEnabled, AuthRefreshRateLimitEnable)
			assert.Equal(t, tc.wantNum, AuthRefreshRateLimitNum)
			assert.Equal(t, tc.wantDuration, AuthRefreshRateLimitDuration)
		})
	}
}
