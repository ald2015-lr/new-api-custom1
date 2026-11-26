package constant

type ContextKey string

const (
	ContextKeyTokenCountMeta  ContextKey = "token_count_meta"
	ContextKeyPromptTokens    ContextKey = "prompt_tokens"
	ContextKeyEstimatedTokens ContextKey = "estimated_tokens"

	ContextKeyOriginalModel    ContextKey = "original_model"
	ContextKeyRequestStartTime ContextKey = "request_start_time"

	/* token related keys */
	ContextKeyTokenUnlimited         ContextKey = "token_unlimited_quota"
	ContextKeyTokenKey               ContextKey = "token_key"
	ContextKeyTokenId                ContextKey = "token_id"
	ContextKeyTokenGroup             ContextKey = "token_group"
	ContextKeyOriginTasks            ContextKey = "origin_tasks"
	ContextKeyChannelConstraints     ContextKey = "channel_constraints"
	ContextKeyTokenModelLimitEnabled ContextKey = "token_model_limit_enabled"
	ContextKeyTokenModelLimit        ContextKey = "token_model_limit"
	ContextKeyTokenCrossGroupRetry   ContextKey = "token_cross_group_retry"
	ContextKeyTokenAutoGroups        ContextKey = "token_auto_groups"

	/* channel related keys */
	ContextKeyChannelId                ContextKey = "channel_id"
	ContextKeyChannelName              ContextKey = "channel_name"
	ContextKeyChannelCreateTime        ContextKey = "channel_create_time"
	ContextKeyChannelBaseUrl           ContextKey = "base_url"
	ContextKeyChannelType              ContextKey = "channel_type"
	ContextKeyChannelSetting           ContextKey = "channel_setting"
	ContextKeyChannelOtherSetting      ContextKey = "channel_other_setting"
	ContextKeyChannelParamOverride     ContextKey = "param_override"
	ContextKeyChannelHeaderOverride    ContextKey = "header_override"
	ContextKeyChannelOrganization      ContextKey = "channel_organization"
	ContextKeyChannelAutoBan           ContextKey = "auto_ban"
	ContextKeyChannelModelMapping      ContextKey = "model_mapping"
	ContextKeyChannelStatusCodeMapping ContextKey = "status_code_mapping"
	ContextKeyChannelIsMultiKey        ContextKey = "channel_is_multi_key"
	ContextKeyChannelMultiKeyIndex     ContextKey = "channel_multi_key_index"
	ContextKeyChannelKey               ContextKey = "channel_key"
	// ContextKeyProviderPlugin holds the jsplugin.PinnedPlugin whose relay
	// routes serve this request on a task plugin channel.
	ContextKeyProviderPlugin ContextKey = "provider_plugin"

	// ContextKeyUpstreamRequestOrigin is the scheme and host of the last upstream
	// request, which differs from the base URL for absolute Advanced Custom routes
	// and for adaptors that build official endpoints themselves.
	ContextKeyUpstreamRequestOrigin ContextKey = "upstream_request_origin"

	ContextKeyAutoGroup           ContextKey = "auto_group"
	ContextKeyAutoGroupIndex      ContextKey = "auto_group_index"
	ContextKeyAutoGroupRetryIndex ContextKey = "auto_group_retry_index"

	/* user related keys */
	ContextKeyUserId      ContextKey = "id"
	ContextKeyUserSetting ContextKey = "user_setting"
	ContextKeyUserQuota   ContextKey = "user_quota"
	ContextKeyUserStatus  ContextKey = "user_status"
	ContextKeyUserEmail   ContextKey = "user_email"
	ContextKeyUserGroup   ContextKey = "user_group"
	ContextKeyUsingGroup  ContextKey = "group"
	ContextKeyUserName    ContextKey = "username"

	// ContextKeyGroupAccessSubject stores the requester (user id, role, user
	// group) checked against recharge-gated groups; set by TokenAuth.
	ContextKeyGroupAccessSubject ContextKey = "group_access_subject"
	// ContextKeyGroupAccessTopupTotal memoizes the requester's cumulative
	// top-up within one request.
	ContextKeyGroupAccessTopupTotal ContextKey = "group_access_topup_total"

	ContextKeyLocalCountTokens ContextKey = "local_count_tokens"

	ContextKeySystemPromptOverride ContextKey = "system_prompt_override"

	// ContextKeyFileSourcesToCleanup stores file sources that need cleanup when request ends
	ContextKeyFileSourcesToCleanup ContextKey = "file_sources_to_cleanup"

	// ContextKeyAdminRejectReason stores an admin-only reject/block reason extracted from upstream responses.
	// It is not returned to end users, but can be persisted into consume/error logs for debugging.
	ContextKeyAdminRejectReason ContextKey = "admin_reject_reason"

	// ContextKeyGeminiFreeKeyAudit stores the *geminifreekey.Audit of the
	// request's last attempt on a Gemini channel in free key mode, for the
	// administrators' part of its logs.
	ContextKeyGeminiFreeKeyAudit ContextKey = "gemini_free_key_audit"

	// ContextKeyModerationState stores the content moderation work of the
	// request (extraction, verdicts, rule results) that its later attempts on
	// moderated OpenAI channels reuse.
	ContextKeyModerationState ContextKey = "moderation_state"

	// ContextKeyModerationAudit stores the *relaycommon.ModerationAudit of the
	// attempt that passed content moderation, for the administrators' part of
	// that attempt's consume log.
	ContextKeyModerationAudit ContextKey = "moderation_audit"

	// ContextKeyGeminiFreeKeyRateLimits stores, by channel id, Google's last
	// 429 that ended an attempt of the request on a Gemini channel in free
	// key mode (map[int]*types.NewAPIError), which a later attempt the
	// channel refuses on its own shows on a channel that reveals free tier
	// errors.
	ContextKeyGeminiFreeKeyRateLimits ContextKey = "gemini_free_key_rate_limits"

	// ContextKeyLanguage stores the user's language preference for i18n
	ContextKeyLanguage             ContextKey = "language"
	ContextKeyIsStream             ContextKey = "is_stream"
	ContextKeyResponseStreamStatus ContextKey = "response_stream_status"
	// ContextKeyConsumeLog holds the *model.RecordConsumeLogParams of the
	// request's last consume log, which request plugin report hooks read.
	ContextKeyConsumeLog ContextKey = "consume_log"

	// ContextKeyAuditLogged marks that the current request has already recorded
	// a manage/operation audit log inside the handler. When set, the admin-audit
	// fallback in authHelper (finishAdminAudit) skips its record to avoid
	// duplicate entries.
	ContextKeyAuditLogged ContextKey = "audit_logged"

	// ContextKeyTokenAuditParams contains only the API token operation's safe metadata.
	ContextKeyTokenAuditParams ContextKey = "token_audit_params"
	// ContextKeyTokenAuditSucceeded disambiguates token responses that exceed the audit buffer.
	ContextKeyTokenAuditSucceeded ContextKey = "token_audit_succeeded"
)
