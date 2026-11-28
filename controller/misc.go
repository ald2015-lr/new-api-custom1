package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

func TestStatus(c *gin.Context) {
	err := model.PingDB()
	if err != nil {
		writeAccountStatusError(c, http.StatusServiceUnavailable, "Database connection failed")
		return
	}
	// 获取HTTP统计信息
	httpStats := middleware.GetStats()
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"message":    "Server is running",
		"http_stats": httpStats,
	})
	return
}

func GetStatus(c *gin.Context) {

	cs := console_setting.GetConsoleSetting()
	passkeySetting := system_setting.PasskeySettingsSnapshot()
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()

	legalSetting := system_setting.GetLegalSettings()

	data := gin.H{
		"version":                     common.Version,
		"start_time":                  common.StartTime,
		"email_verification":          common.EmailVerificationEnabled,
		"github_oauth":                common.GitHubOAuthEnabled,
		"github_client_id":            common.GitHubClientId,
		"discord_oauth":               system_setting.GetDiscordSettings().Enabled,
		"discord_client_id":           system_setting.GetDiscordSettings().ClientId,
		"linuxdo_oauth":               common.LinuxDOOAuthEnabled,
		"linuxdo_client_id":           common.LinuxDOClientId,
		"linuxdo_minimum_trust_level": common.LinuxDOMinimumTrustLevel,
		"telegram_oauth":              common.TelegramOAuthEnabled,
		"telegram_oauth_configured":   oauth.TelegramConfigurationError() == nil,
		"telegram_bot_name":           common.TelegramBotName,
		"theme":                       "default",
		"system_name":                 common.SystemName,
		"logo":                        common.Logo,
		"footer_html":                 common.Footer,
		"wechat_qrcode":               common.WeChatAccountQRCodeImageURL,
		"wechat_login":                common.WeChatAuthEnabled,
		"server_address":              system_setting.ServerAddress,
		"turnstile_check":             common.TurnstileCheckEnabled,
		"turnstile_site_key":          common.TurnstileSiteKey,
		"register_captcha":            common.RegisterCaptchaEnabled,
		"email_qq_numeric_only":       common.EmailQQNumericOnlyEnabled,
		"password_reset_captcha":      common.PasswordResetCaptchaEnabled,
		"login_captcha":               common.LoginCaptchaEnabled,
		"docs_link":                   operation_setting.GetGeneralSetting().DocsLink,
		"quota_per_unit":              common.QuotaPerUnit,
		// 兼容旧前端：保留 display_in_currency，同时提供新的 quota_display_type
		"display_in_currency":           operation_setting.IsCurrencyDisplay(),
		"quota_display_type":            operation_setting.GetQuotaDisplayType(),
		"custom_currency_symbol":        operation_setting.GetGeneralSetting().CustomCurrencySymbol,
		"custom_currency_exchange_rate": operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate,
		// Seconds between two reset emails for one address; 0 while the limit is off.
		"password_reset_email_cooldown": middleware.PasswordResetEmailCooldownSeconds(),
		"enable_batch_update":           common.BatchUpdateEnabled,
		"enable_drawing":                common.DrawingEnabled,
		"enable_task":                   common.TaskEnabled,
		"enable_data_export":            common.DataExportEnabled,
		"data_export_default_time":      common.DataExportDefaultTime,
		"default_collapse_sidebar":      common.DefaultCollapseSidebar,
		"mj_notify_enabled":             setting.MjNotifyEnabled,
		"chats":                         setting.Chats,
		"demo_site_enabled":             operation_setting.DemoSiteEnabled,
		"self_use_mode_enabled":         operation_setting.SelfUseModeEnabled,
		"register_enabled":              common.RegisterEnabled,
		"password_login_enabled":        common.PasswordLoginEnabled,
		"password_register_enabled":     common.PasswordRegisterEnabled,
		"default_use_auto_group":        setting.DefaultUseAutoGroup,

		"password_login_encryption_enabled": common.PasswordLoginEncryptionEnabled,

		"usd_exchange_rate": operation_setting.USDExchangeRate,
		"price":             operation_setting.Price,
		"stripe_unit_price": setting.StripeUnitPrice,

		// 面板启用开关
		"api_info_enabled":      cs.ApiInfoEnabled,
		"uptime_kuma_enabled":   cs.UptimeKumaEnabled,
		"announcements_enabled": cs.AnnouncementsEnabled,
		"faq_enabled":           cs.FAQEnabled,

		// 模块管理配置
		"HeaderNavModules":    common.OptionMap["HeaderNavModules"],
		"SidebarModulesAdmin": common.OptionMap["SidebarModulesAdmin"],

		"oidc_enabled":                system_setting.GetOIDCSettings().Enabled,
		"oidc_client_id":              system_setting.GetOIDCSettings().ClientId,
		"oidc_authorization_endpoint": system_setting.GetOIDCSettings().AuthorizationEndpoint,
		"oidc_display_name":           system_setting.GetOIDCSettings().GetEffectiveDisplayName(),
		"passkey_login":               passkeySetting.Enabled,
		"passkey_display_name":        passkeySetting.RPDisplayName,
		"passkey_rp_id":               passkeySetting.EffectiveRPID(),
		"passkey_rp_ids":              passkeySetting.RelyingPartyIDs(),
		"passkey_allow_insecure":      passkeySetting.AllowInsecureOrigin,
		"passkey_user_verification":   passkeySetting.UserVerification,
		"passkey_attachment":          passkeySetting.AttachmentPreference,
		"setup":                       constant.Setup,
		"user_agreement_enabled":      legalSetting.UserAgreement != "",
		"privacy_policy_enabled":      legalSetting.PrivacyPolicy != "",
		"checkin_enabled":             operation_setting.GetCheckinSetting().Enabled,
		"log_export_enabled":          operation_setting.GetLogSetting().UserExportEnabled,
		"log_max_range_days":          operation_setting.GetLogSetting().MaxRangeDays,
		"quota_remind_enabled":        common.QuotaRemindEnabled,
		"quota_remind_threshold":      common.QuotaRemindThreshold,
	}

	// 根据启用状态注入可选内容
	if cs.ApiInfoEnabled {
		data["api_info"] = console_setting.GetApiInfo()
	}
	if cs.AnnouncementsEnabled {
		data["announcements"] = console_setting.GetAnnouncements()
	}
	if cs.FAQEnabled {
		data["faq"] = console_setting.GetFAQ()
	}

	// Add enabled custom OAuth providers
	customProviders := oauth.GetEnabledCustomProviders()
	if len(customProviders) > 0 {
		type CustomOAuthInfo struct {
			Id                    int    `json:"id"`
			Name                  string `json:"name"`
			Slug                  string `json:"slug"`
			Icon                  string `json:"icon"`
			ClientId              string `json:"client_id"`
			AuthorizationEndpoint string `json:"authorization_endpoint"`
			Scopes                string `json:"scopes"`
		}
		providersInfo := make([]CustomOAuthInfo, 0, len(customProviders))
		for _, p := range customProviders {
			config := p.GetConfig()
			providersInfo = append(providersInfo, CustomOAuthInfo{
				Id:                    config.Id,
				Name:                  config.Name,
				Slug:                  config.Slug,
				Icon:                  config.Icon,
				ClientId:              config.ClientId,
				AuthorizationEndpoint: config.AuthorizationEndpoint,
				Scopes:                config.Scopes,
			})
		}
		data["custom_oauth_providers"] = providersInfo
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
	return
}

func GetNotice(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	notice := common.OptionMap["Notice"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, notice)
}

func GetAbout(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	about := common.OptionMap["About"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, about)
}

func GetUserAgreement(c *gin.Context) {
	serveRevalidatedJSON(c, system_setting.GetLegalSettings().UserAgreement)
}

func GetPrivacyPolicy(c *gin.Context) {
	serveRevalidatedJSON(c, system_setting.GetLegalSettings().PrivacyPolicy)
}

func GetMidjourney(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    common.OptionMap["Midjourney"],
	})
	return
}

func GetHomePageContent(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	homePageContent := common.OptionMap["HomePageContent"]
	common.OptionMapRWMutex.RUnlock()
	serveRevalidatedJSON(c, homePageContent)
}

// GetRegisterCaptcha issues a single-use arithmetic captcha for the sign-up
// page. It is served whether or not the captcha is required, so the page never
// depends on a cached status to fetch one.
func GetRegisterCaptcha(c *gin.Context) {
	captcha, err := service.NewCaptcha()
	if err != nil {
		logger.LogError(c.Request.Context(), common.LogText("failed to issue a captcha: %v", err))
		common.ApiErrorT(c, "Please try again later.")
		return
	}
	common.ApiSuccess(c, captcha)
}

// SendEmailVerification mails a registration code. The code only serves
// password registration with email verification, so it is refused while
// registration is closed (with Register's messages) and while email
// verification is off (Register then reads no code, and the web sign-up page
// asks for none).
func SendEmailVerification(c *gin.Context) {
	if !common.RegisterEnabled {
		common.ApiErrorT(c, "New user registration has been disabled by administrator")
		return
	}
	if !common.PasswordRegisterEnabled {
		common.ApiErrorT(c, "Password registration has been disabled by administrator, please use third-party account verification")
		return
	}
	if !common.EmailVerificationEnabled {
		common.ApiErrorT(c, "This verification method is currently unavailable.")
		return
	}
	email, err := service.ValidateAccountEmail(c.Query("email"))
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}

	if model.IsEmailAlreadyTaken(email) {
		common.ApiErrorT(c, "Email address is already in use")
		return
	}
	code := common.GenerateVerificationCode(6)
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)
	// The address may not belong to an account yet, so the mail follows the
	// request language.
	systemName := map[string]any{"SystemName": common.SystemName}
	subject := i18n.T(c, i18n.MsgEmailVerificationSubject, systemName)
	content := fmt.Sprintf("<p>%s</p>"+
		"<p>%s</p>"+
		"<p>%s</p>",
		i18n.T(c, i18n.MsgEmailVerificationGreeting, systemName),
		i18n.T(c, i18n.MsgEmailVerificationCode, map[string]any{"Code": code}),
		i18n.T(c, i18n.MsgEmailVerificationValidity, map[string]any{"Minutes": common.VerificationValidMinutes}))
	err = common.SendEmail(subject, email, content)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}

// SendPasswordResetEmail mails a reset link when the address belongs to an
// account. In order: the captcha (while PasswordResetCaptchaEnabled), the
// address, the per-email limit (while PasswordResetEmailLimitEnabled; every
// admitted request counts, registered or not), then the lookup and the mail.
// An admitted request answers success whether or not the address is
// registered. Every request writes one audit row with the outcome.
func SendPasswordResetEmail(c *gin.Context) {
	email := model.NormalizeEmail(c.Query("email"))
	request := passwordResetRequest{email: email}
	defer request.record(c)

	if common.PasswordResetCaptchaEnabled {
		if code := middleware.CheckRequestCaptcha(c); code != "" {
			middleware.WriteCaptchaRefusal(c, code)
			request.outcome, request.code = "captcha_failed", code
			return
		}
	}
	if err := common.Validate.Var(email, "required,email"); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		request.outcome = "invalid_email"
		return
	}
	if refusal := middleware.TakePasswordResetEmail(c.Request.Context(), email); refusal != nil {
		writePasswordResetLimited(c, refusal)
		request.outcome, request.code, request.waitSeconds = "cooldown", refusal.Code, refusal.WaitSeconds
		if refusal.Code == middleware.PasswordResetEmailDailyLimitCode {
			request.outcome = "daily_limit"
		}
		return
	}
	user, err := model.GetUniqueUserByEmail(email)
	switch {
	case err == nil && model.NormalizeEmail(user.Email) != email:
		// GetUniqueUserByEmail only returns the account whose stored address
		// is this one; anything else gets no mail.
		logger.LogWarn(c.Request.Context(), common.LogText("skip password reset email for %s: the stored address differs", email))
		request.outcome, request.detail = "send_failed", "lookup_error"
	case err == nil:
		// The token is keyed and the link built with the stored address (the
		// same as email here), and the mail goes to the address on file.
		storedEmail := model.NormalizeEmail(user.Email)
		request.user, request.outcome = user, "sent"
		code := common.GenerateVerificationCode(0)
		common.RegisterVerificationCodeWithKey(storedEmail, code, common.PasswordResetPurpose)
		link := fmt.Sprintf("%s/user/reset?email=%s&token=%s", system_setting.ServerAddress, url.QueryEscape(storedEmail), url.QueryEscape(code))
		lang := user.GetSetting().Language
		if lang == "" {
			lang = i18n.StatedLang(c)
		}
		systemName := map[string]any{"SystemName": common.SystemName}
		linkParams := map[string]any{"Link": link}
		subject := i18n.Translate(lang, i18n.MsgEmailPasswordResetSubject, systemName)
		content := fmt.Sprintf("<p>%s</p>"+
			"<p>%s</p>"+
			"<p>%s </p>"+
			"<p>%s</p>",
			i18n.Translate(lang, i18n.MsgEmailPasswordResetGreeting, systemName),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetLink, linkParams),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetLinkFallback, linkParams),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetValidity, map[string]any{"Minutes": common.VerificationValidMinutes}))
		// Keep the mailbox spelling on file for delivery, including legacy mixed-case local parts.
		if err := common.SendEmail(subject, strings.TrimSpace(user.Email), content); err != nil {
			// Revoke the undelivered code; a newer delivery may already have replaced it.
			common.ConsumeCodeWithKey(storedEmail, code, common.PasswordResetPurpose)
			logger.LogError(c.Request.Context(), common.LogText("failed to send password reset email to %s: %s", storedEmail, err.Error()))
			request.outcome, request.detail = "send_failed", "smtp_error"
		}
	case errors.Is(err, model.ErrEmailNotFound):
		request.outcome = "not_registered"
	default:
		logger.LogWarn(c.Request.Context(), common.LogText("skip password reset email for %s: %s", email, err.Error()))
		request.outcome, request.detail = "send_failed", "lookup_error"
		if errors.Is(err, model.ErrEmailAmbiguous) {
			request.detail = "email_ambiguous"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}

type PasswordResetRequest struct {
	Email string `json:"email"`
	Token string `json:"token"`
}

// ResetPassword completes a reset through the email link. Each request
// writes one audit row; success also ends the login lock of the account.
func ResetPassword(c *gin.Context) {
	var req PasswordResetRequest
	err := common.DecodeJson(c.Request.Body, &req)
	if err != nil {
		common.ApiError(c, err)
		recordPasswordResetAudit(c, model.NormalizeEmail(req.Email), nil, "invalid_request")
		return
	}
	req.Email = model.NormalizeEmail(req.Email)
	if req.Email == "" || req.Token == "" {
		common.ApiErrorT(c, "Invalid parameters")
		recordPasswordResetAudit(c, req.Email, nil, "invalid_request")
		return
	}
	if !common.ConsumeCodeWithKey(req.Email, req.Token, common.PasswordResetPurpose) {
		common.ApiErrorT(c, "Password reset link is invalid or has expired")
		recordPasswordResetAudit(c, req.Email, nil, "invalid_link")
		return
	}
	password := common.GenerateVerificationCode(12)
	user, err := model.ResetUserPasswordByEmailUser(req.Email, password)
	if err != nil {
		if errors.Is(err, model.ErrEmailNotFound) || errors.Is(err, model.ErrEmailAmbiguous) {
			common.ApiErrorT(c, "Password reset link is invalid or has expired")
			recordPasswordResetAudit(c, req.Email, nil, "invalid_link")
			return
		}
		common.ApiError(c, err)
		recordPasswordResetAudit(c, req.Email, nil, "server_error")
		return
	}
	middleware.ClearLoginLockout(c.Request.Context(), user.Id, user.Username, user.Email)
	recordPasswordResetAudit(c, req.Email, user, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    password,
	})
}
