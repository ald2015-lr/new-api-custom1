/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { z } from 'zod'

import {
  MODERATION_CATEGORIES,
  MODERATION_PRESETS,
} from '@/lib/content-moderation'

import {
  CLAUDE_FIELD_PASSTHROUGH_TYPES,
  CHANNEL_TYPE_GEMINI,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_NOVELAI,
  CHANNEL_TYPE_OLLAMA,
  CHANNEL_TYPE_TASK_PLUGIN,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
  CHANNEL_STATUS,
  ERROR_MESSAGES,
  FIELD_PASSTHROUGH_TYPES,
  GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS,
  GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT,
  GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT,
  GEMINI_FREE_KEY_TOKEN_COUNTERS,
  type GeminiFreeKeyTokenCounter,
  MODEL_FETCHABLE_TYPES,
  MODERATION_MAX_CHARS_DEFAULT,
  MODERATION_SETTINGS_KEYS,
  MODERATION_TIMEOUT_SECONDS_DEFAULT,
  NOVELAI_QUEUE_LIMIT_DEFAULT,
  NOVELAI_QUEUE_LIMIT_MAX,
  OPENAI_FIELD_PASSTHROUGH_TYPES,
  UPSTREAM_QUOTA_ERROR_MESSAGE_MAX,
  UPSTREAM_QUOTA_ERROR_STATUS_CODE_MAX,
  UPSTREAM_QUOTA_ERROR_STATUS_CODE_MIN,
} from '../constants'
import type { Channel } from '../types'
import {
  CHANNEL_TYPE_ADVANCED_CUSTOM,
  advancedCustomConfigUsesRelativeUpstreamPath,
  hasValidAdvancedCustomModelListRoute,
  parseAdvancedCustomConfig,
  stringifyAdvancedCustomConfig,
  validateAdvancedCustomConfig,
} from './advanced-custom'
import { readTaskExtendPluginKeys } from './channel-plugin-extensions'
import {
  isValidModerationMaxChars,
  isValidModerationTimeout,
  validateModerationRuleRows,
  type ModerationRuleRow,
} from './content-moderation-rules'
import {
  isDefaultGeminiFreeKeyModelLimits,
  isValidGeminiFreeKeyMaxAttempts,
  readGeminiFreeKeyModelLimitRows,
  toGeminiFreeKeyModelLimitRows,
  toGeminiFreeKeyModelLimits,
  validateGeminiFreeKeyModelLimitRows,
} from './gemini-free-key'
import { supportsResponsesWebSocket } from './responses-websocket'

// ============================================================================
// Form Validation Schema
// ============================================================================

const SUPPORTED_PROXY_PROTOCOLS = new Set([
  'http:',
  'https:',
  'socks5:',
  'socks5h:',
])

function isOptionalProxyURL(value: string | undefined): boolean {
  const trimmedValue = value?.trim() || ''
  if (!trimmedValue) return true

  const schemeSeparatorIndex = trimmedValue.indexOf('://')
  if (schemeSeparatorIndex <= 0) return false

  const authorityAndSuffix = trimmedValue.slice(schemeSeparatorIndex + 3)
  const suffixIndex = authorityAndSuffix.search(/[/?#]/)
  if (suffixIndex >= 0 && authorityAndSuffix.slice(suffixIndex) !== '/') {
    return false
  }

  try {
    const parsedURL = new URL(trimmedValue)
    return (
      SUPPORTED_PROXY_PROTOCOLS.has(parsedURL.protocol) &&
      Boolean(parsedURL.hostname) &&
      parsedURL.port !== '0'
    )
  } catch {
    return false
  }
}

export const HTTP_PROTOCOL_AUTO = 'auto'
export const HTTP_PROTOCOL_HTTP1 = 'http1'
export const MAX_HTTP2_CONNECTION_SHARDS = 8

export function normalizeHttpProtocol(
  value: string | undefined | null
): 'auto' | 'http1' {
  const normalized = String(value || '')
    .trim()
    .toLowerCase()
  if (normalized === HTTP_PROTOCOL_HTTP1) {
    return HTTP_PROTOCOL_HTTP1
  }
  return HTTP_PROTOCOL_AUTO
}

export function normalizeHttp2ConnectionShards(
  value: number | undefined | null
): number {
  if (value == null || Number.isNaN(value) || value === 0) {
    return 1
  }
  if (value < 1) {
    return 1
  }
  if (value > MAX_HTTP2_CONNECTION_SHARDS) {
    return MAX_HTTP2_CONNECTION_SHARDS
  }
  return value
}

function parseOptionalJson(value: string | undefined): unknown {
  if (!value?.trim()) return undefined
  return JSON.parse(value)
}

function isJsonObjectValue(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isOptionalJsonObject(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    return parsed === undefined || isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function isOptionalModelMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.values(parsed).every((item) => typeof item === 'string')
  } catch {
    return false
  }
}

function isOptionalStatusCodeMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.entries(parsed).every(([from, to]) => {
      const fromCode = Number(from)
      const toCode = Number(to)
      return (
        Number.isInteger(fromCode) &&
        Number.isInteger(toCode) &&
        fromCode >= 100 &&
        fromCode <= 599 &&
        toCode >= 100 &&
        toCode <= 599
      )
    })
  } catch {
    return false
  }
}

function isCodexCredential(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    return (
      isJsonObjectValue(parsed) &&
      typeof parsed.access_token === 'string' &&
      parsed.access_token.trim().length > 0 &&
      typeof parsed.account_id === 'string' &&
      parsed.account_id.trim().length > 0
    )
  } catch {
    return false
  }
}

function isVertexJsonKey(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (Array.isArray(parsed)) {
      return parsed.every((item) => isJsonObjectValue(item))
    }
    return isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function addRequiredIssue(
  ctx: z.RefinementCtx,
  path: string,
  message: string
): void {
  ctx.addIssue({
    code: z.ZodIssueCode.custom,
    path: [path],
    message,
  })
}

export const channelFormSchema = z
  .object({
    name: z.string().min(1, ERROR_MESSAGES.REQUIRED_NAME),
    type: z.number().min(0, ERROR_MESSAGES.REQUIRED_TYPE),
    base_url: z.string().optional(),
    task_plugin_key: z.string().optional(),
    task_extend_plugin_keys: z.array(z.string()).optional(),
    request_plugins: z.array(z.string()).optional(),
    key: z.string(),
    openai_organization: z.string().optional(),
    models: z.string().min(1, ERROR_MESSAGES.REQUIRED_MODELS),
    group: z.array(z.string()).min(1, ERROR_MESSAGES.REQUIRED_GROUP),
    model_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalModelMapping,
        'Model mapping must be a JSON object with string values'
      ),
    priority: z.number().optional(),
    weight: z.number().optional(),
    test_model: z.string().optional(),
    auto_ban: z.number().optional(),
    status: z.number(),
    status_code_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalStatusCodeMapping,
        'Status code mapping must use valid HTTP status codes'
      ),
    tag: z.string().optional(),
    remark: z
      .string()
      .max(255, 'Remark must be less than 255 characters')
      .optional(),
    setting: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    param_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    header_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    settings: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    advanced_custom: z.string().optional(),
    other: z.string().optional(),
    // Multi-key options (not sent to backend directly)
    multi_key_mode: z.enum(['single', 'batch', 'multi_to_single']).optional(),
    multi_key_type: z.enum(['random', 'polling']).optional(),
    batch_add_set_key_prefix_2_name: z.boolean().optional(),
    key_mode: z.enum(['append', 'replace']).optional(), // For editing multi-key channels
    // Channel extra settings (stored in setting JSON, not sent directly)
    force_format: z.boolean().optional(),
    thinking_to_content: z.boolean().optional(),
    proxy: z
      .string()
      .optional()
      .refine(isOptionalProxyURL, ERROR_MESSAGES.INVALID_PROXY),
    http_protocol: z.enum(['auto', 'http1']).optional(),
    http2_connection_shards: z.number().int().optional(),
    tls_insecure_skip_verify: z.boolean().optional(),
    pass_through_body_enabled: z.boolean().optional(),
    responses_websocket_enabled: z.boolean().optional(),
    system_prompt: z.string().optional(),
    system_prompt_override: z.boolean().optional(),
    // Type-specific settings (stored in settings JSON)
    is_enterprise_account: z.boolean().optional(), // OpenRouter specific
    vertex_key_type: z.enum(['json', 'api_key']).optional(), // Vertex AI specific
    vertex_project_id: z.string().optional(), // Vertex AI specific (api_key mode only)
    vertex_api_version: z.enum(['v1', 'v1beta1']).optional(), // Vertex AI Google model (Gemini, Imagen) path version
    aws_key_type: z.enum(['ak_sk', 'api_key']).optional(), // AWS specific
    aws_api_type: z.enum(['invoke_model', 'mantle']).optional(),
    azure_responses_version: z.string().optional(), // Azure specific
    // Field passthrough controls (stored in settings JSON)
    allow_service_tier: z.boolean().optional(), // OpenAI/Anthropic
    disable_store: z.boolean().optional(), // OpenAI only
    allow_safety_identifier: z.boolean().optional(), // OpenAI only
    allow_include_obfuscation: z.boolean().optional(), // OpenAI: include usage obfuscation
    allow_inference_geo: z.boolean().optional(), // OpenAI/Anthropic: inference geography
    allow_speed: z.boolean().optional(), // Anthropic: speed mode control
    claude_beta_query: z.boolean().optional(), // Anthropic: beta query passthrough
    ollama_openai_chat: z.boolean().optional(), // Ollama: OpenAI-compatible /v1/chat/completions instead of native /api/chat
    novelai_presets_disabled: z.boolean().optional(), // NovelAI: no quality tags or UC preset on OpenAI-format requests by default
    novelai_hd_disabled: z.boolean().optional(), // NovelAI: no HD (1.5× Enhance) pass on OpenAI-format text-to-image requests
    novelai_upscale_enabled: z.boolean().optional(), // NovelAI: 2x upscale after generation (and HD)
    // NovelAI: requests that may wait for each busy key; null is a cleared
    // field, saved as the default (react-hook-form shows the default value
    // again for undefined).
    novelai_queue_limit: z.number().nullable().optional(),
    // Any channel type: hide an upstream's out-of-balance error behind the
    // message and status below (stored in settings JSON). A cleared status
    // is null, saved as the default.
    upstream_quota_error_mask_enabled: z.boolean().optional(),
    upstream_quota_error_message: z.string().optional(),
    upstream_quota_error_status_code: z.number().nullable().optional(),
    // Gemini (AI Studio) free key mode (stored in settings JSON). The switch
    // that hides free-tier errors shows the reveal setting inverted. A
    // cleared attempt count or limit is null; the attempt count saves as the
    // default, an empty limit is refused.
    gemini_free_key_enabled: z.boolean().optional(),
    gemini_free_key_reveal_tier_errors: z.boolean().optional(),
    gemini_free_key_token_counter: z
      .enum(GEMINI_FREE_KEY_TOKEN_COUNTERS)
      .optional(),
    gemini_free_key_max_attempts: z.number().nullable().optional(),
    gemini_free_key_model_limits: z
      .array(
        z.object({
          model: z.string(),
          aliases: z.string(),
          rpm: z.number().nullable(),
          tpm: z.number().nullable(),
          rpd: z.number().nullable(),
        })
      )
      .optional(),
    // Gemini (AI Studio): model names may end in thinking suffixes, turned
    // into thinking settings before the request goes upstream (stored in
    // settings JSON); off when unset.
    gemini_thinking_suffix_enabled: z.boolean().optional(),
    // OpenAI: content moderation before a request goes upstream (stored in
    // settings JSON); off when unset. A cleared limit is null, saved as the
    // default. moderation_rule_presets_set is form-only: whether the preset
    // list was ever saved or selected, so the default presets are selected
    // only the first time moderation is turned on.
    moderation_enabled: z.boolean().optional(),
    moderation_fail_open: z.boolean().optional(),
    moderation_categories: z.array(z.string()).optional(),
    moderation_skip_images: z.boolean().optional(),
    moderation_max_chars: z.number().nullable().optional(),
    moderation_timeout_seconds: z.number().nullable().optional(),
    moderation_rule_presets: z.array(z.string()).optional(),
    moderation_rule_presets_set: z.boolean().optional(),
    moderation_rules: z
      .array(
        z.object({
          name: z.string(),
          type: z.enum(['keyword', 'regex']),
          pattern: z.string(),
          case_sensitive: z.boolean(),
        })
      )
      .optional(),
    // OpenAI: send a hashed user ID as safety_identifier (stored in
    // settings JSON); off when unset, independent of moderation.
    send_safety_identifier: z.boolean().optional(),
    disable_task_polling_sleep: z.boolean().optional(),
    // Upstream model update settings (stored in settings JSON)
    upstream_model_update_check_enabled: z.boolean().optional(),
    upstream_model_update_auto_sync_enabled: z.boolean().optional(),
    upstream_model_update_ignored_models: z.string().optional(),
    // The bound plugin's channel settings (type 61, stored in settings JSON)
    plugin_config: z.record(z.string(), z.unknown()).optional(),
    // Switches of the bound plugin's relay routes that differ from their
    // defaults (type 61, stored in settings JSON)
    plugin_routes: z.record(z.string(), z.boolean()).optional(),
    // Their values as typed, null until edited (not sent to backend)
    plugin_config_draft: z
      .record(z.string(), z.unknown())
      .nullable()
      .optional(),
  })
  .superRefine((data, ctx) => {
    if (
      [
        3,
        8,
        36,
        45,
        CHANNEL_TYPE_NEW_API,
        CHANNEL_TYPE_TASK_PLUGIN,
        CHANNEL_TYPE_VLLM,
        CHANNEL_TYPE_SGLANG,
      ].includes(data.type) &&
      !data.base_url?.trim()
    ) {
      addRequiredIssue(
        ctx,
        'base_url',
        'Base URL is required for this channel type'
      )
    }
    if (
      data.type === CHANNEL_TYPE_TASK_PLUGIN &&
      !data.task_plugin_key?.trim()
    ) {
      addRequiredIssue(ctx, 'task_plugin_key', 'Task plugin is required')
    }

    if (data.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
      const advancedCustomConfig = parseAdvancedCustomConfig(
        data.advanced_custom
      )
      const advancedCustomError =
        validateAdvancedCustomConfig(advancedCustomConfig)
      if (advancedCustomError) {
        addRequiredIssue(ctx, 'advanced_custom', advancedCustomError.message)
      }
      if (
        advancedCustomConfigUsesRelativeUpstreamPath(advancedCustomConfig) &&
        !data.base_url?.trim()
      ) {
        addRequiredIssue(
          ctx,
          'base_url',
          'Base URL is required when an advanced route uses an upstream path'
        )
      }
      if (
        data.upstream_model_update_check_enabled === true &&
        !hasValidAdvancedCustomModelListRoute(advancedCustomConfig)
      ) {
        addRequiredIssue(
          ctx,
          'upstream_model_update_check_enabled',
          'OpenAI Models route is required to enable upstream model checks'
        )
      }
    }

    if ([3, 18, 21, 39, 41, 49].includes(data.type) && !data.other?.trim()) {
      addRequiredIssue(
        ctx,
        'other',
        'This channel type requires additional configuration'
      )
    }

    if (data.type === 57) {
      if (data.multi_key_mode && data.multi_key_mode !== 'single') {
        addRequiredIssue(
          ctx,
          'multi_key_mode',
          'Codex channels do not support batch creation'
        )
      }
      if (data.key?.trim() && !isCodexCredential(data.key)) {
        addRequiredIssue(
          ctx,
          'key',
          'Codex credential must be a JSON object with access_token and account_id'
        )
      }
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'json' &&
      data.key?.trim() &&
      !isVertexJsonKey(data.key)
    ) {
      addRequiredIssue(
        ctx,
        'key',
        'Vertex AI service account key must be valid JSON'
      )
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'api_key' &&
      data.multi_key_mode &&
      data.multi_key_mode !== 'single'
    ) {
      addRequiredIssue(
        ctx,
        'multi_key_mode',
        'Vertex AI API Key mode does not support batch creation'
      )
    }

    const protocol = normalizeHttpProtocol(data.http_protocol)
    const shards = data.http2_connection_shards ?? 1
    if (shards < 1 || shards > MAX_HTTP2_CONNECTION_SHARDS) {
      addRequiredIssue(
        ctx,
        'http2_connection_shards',
        ERROR_MESSAGES.INVALID_HTTP2_CONNECTION_SHARDS
      )
    }
    if (protocol === HTTP_PROTOCOL_HTTP1 && shards > 1) {
      addRequiredIssue(
        ctx,
        'http2_connection_shards',
        ERROR_MESSAGES.INVALID_HTTP1_WITH_SHARDS
      )
    }

    const queueLimit = data.novelai_queue_limit
    if (
      data.type === CHANNEL_TYPE_NOVELAI &&
      queueLimit != null &&
      (!Number.isInteger(queueLimit) ||
        queueLimit < 0 ||
        queueLimit > NOVELAI_QUEUE_LIMIT_MAX)
    ) {
      addRequiredIssue(
        ctx,
        'novelai_queue_limit',
        ERROR_MESSAGES.INVALID_NOVELAI_QUEUE_LIMIT
      )
    }

    if (!isValidUpstreamQuotaErrorMessage(data.upstream_quota_error_message)) {
      addRequiredIssue(
        ctx,
        'upstream_quota_error_message',
        ERROR_MESSAGES.INVALID_UPSTREAM_QUOTA_ERROR_MESSAGE
      )
    }
    if (
      !isValidUpstreamQuotaErrorStatusCode(
        data.upstream_quota_error_status_code
      )
    ) {
      addRequiredIssue(
        ctx,
        'upstream_quota_error_status_code',
        ERROR_MESSAGES.INVALID_UPSTREAM_QUOTA_ERROR_STATUS_CODE
      )
    }

    if (data.type === CHANNEL_TYPE_GEMINI) {
      if (!isValidGeminiFreeKeyMaxAttempts(data.gemini_free_key_max_attempts)) {
        addRequiredIssue(
          ctx,
          'gemini_free_key_max_attempts',
          ERROR_MESSAGES.INVALID_GEMINI_FREE_KEY_MAX_ATTEMPTS
        )
      }
      const limitIssues = data.gemini_free_key_model_limits
        ? validateGeminiFreeKeyModelLimitRows(data.gemini_free_key_model_limits)
        : []
      for (const issue of limitIssues) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path:
            issue.field === undefined
              ? ['gemini_free_key_model_limits']
              : ['gemini_free_key_model_limits', issue.index ?? 0, issue.field],
          message: issue.message,
        })
      }
    }

    // The server checks the moderation settings even while moderation is
    // off; regular expression syntax is left to it (RE2, not JavaScript).
    if (data.type === 1) {
      if (!isValidModerationMaxChars(data.moderation_max_chars)) {
        addRequiredIssue(
          ctx,
          'moderation_max_chars',
          ERROR_MESSAGES.INVALID_MODERATION_MAX_CHARS
        )
      }
      if (!isValidModerationTimeout(data.moderation_timeout_seconds)) {
        addRequiredIssue(
          ctx,
          'moderation_timeout_seconds',
          ERROR_MESSAGES.INVALID_MODERATION_TIMEOUT
        )
      }
      for (const issue of validateModerationRuleRows(
        data.moderation_rules ?? []
      )) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path:
            issue.field === undefined
              ? ['moderation_rules']
              : ['moderation_rules', issue.index ?? 0, issue.field],
          message: issue.message,
        })
      }
    }
  })

/**
 * Whether the server accepts this message for an upstream balance error:
 * at most UPSTREAM_QUOTA_ERROR_MESSAGE_MAX characters once trimmed, counted
 * as code points like the server, not as UTF-16 units.
 */
export function isValidUpstreamQuotaErrorMessage(
  message: string | undefined
): boolean {
  return [...(message?.trim() ?? '')].length <= UPSTREAM_QUOTA_ERROR_MESSAGE_MAX
}

/**
 * Whether the server accepts this status for an upstream balance error:
 * unset (the default) or a whole number in the allowed range.
 */
export function isValidUpstreamQuotaErrorStatusCode(
  status: number | null | undefined
): boolean {
  return (
    status == null ||
    (Number.isInteger(status) &&
      status >= UPSTREAM_QUOTA_ERROR_STATUS_CODE_MIN &&
      status <= UPSTREAM_QUOTA_ERROR_STATUS_CODE_MAX)
  )
}

export type ChannelFormValues = z.infer<typeof channelFormSchema>

// ============================================================================
// Default Form Values
// ============================================================================

export const CHANNEL_FORM_DEFAULT_VALUES: ChannelFormValues = {
  name: '',
  type: 1,
  base_url: '',
  task_plugin_key: '',
  task_extend_plugin_keys: [],
  request_plugins: [],
  key: '',
  openai_organization: '',
  models: '',
  group: ['default'],
  model_mapping: '',
  priority: 0,
  weight: 0,
  test_model: '',
  auto_ban: 1,
  status: CHANNEL_STATUS.ENABLED,
  status_code_mapping: '',
  tag: '',
  remark: '',
  setting: '',
  param_override: '',
  header_override: '',
  settings: '{}',
  other: '',
  multi_key_mode: 'single',
  multi_key_type: 'random',
  batch_add_set_key_prefix_2_name: false,
  key_mode: 'append',
  // Channel extra settings
  force_format: false,
  thinking_to_content: false,
  proxy: '',
  http_protocol: HTTP_PROTOCOL_AUTO,
  http2_connection_shards: 1,
  tls_insecure_skip_verify: false,
  pass_through_body_enabled: false,
  responses_websocket_enabled: false,
  system_prompt: '',
  system_prompt_override: false,
  // Type-specific settings
  is_enterprise_account: false,
  vertex_key_type: 'json',
  vertex_project_id: '',
  vertex_api_version: 'v1',
  aws_key_type: 'ak_sk',
  aws_api_type: 'invoke_model',
  azure_responses_version: '',
  // Field passthrough controls
  allow_service_tier: false,
  disable_store: false,
  allow_safety_identifier: false,
  allow_include_obfuscation: false,
  allow_inference_geo: false,
  allow_speed: false,
  claude_beta_query: false,
  ollama_openai_chat: false,
  novelai_presets_disabled: false,
  novelai_hd_disabled: false,
  novelai_upscale_enabled: false,
  novelai_queue_limit: NOVELAI_QUEUE_LIMIT_DEFAULT,
  upstream_quota_error_mask_enabled: false,
  upstream_quota_error_message: '',
  upstream_quota_error_status_code: null,
  gemini_free_key_enabled: false,
  gemini_free_key_reveal_tier_errors: false,
  gemini_free_key_token_counter: GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT,
  gemini_free_key_max_attempts: null,
  gemini_free_key_model_limits: toGeminiFreeKeyModelLimitRows(
    GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS
  ),
  gemini_thinking_suffix_enabled: false,
  moderation_enabled: false,
  moderation_fail_open: false,
  moderation_categories: [],
  moderation_skip_images: false,
  moderation_max_chars: null,
  moderation_timeout_seconds: null,
  moderation_rule_presets: [],
  moderation_rule_presets_set: false,
  moderation_rules: [],
  send_safety_identifier: false,
  disable_task_polling_sleep: false,
  upstream_model_update_check_enabled: false,
  upstream_model_update_auto_sync_enabled: false,
  upstream_model_update_ignored_models: '',
  advanced_custom: '',
  plugin_config: {},
  plugin_routes: {},
  plugin_config_draft: null,
}

// ============================================================================
// Transform Functions
// ============================================================================

/**
 * Transform Channel from API to Form default values
 */
export function transformChannelToFormDefaults(
  channel: Channel
): ChannelFormValues {
  // Parse channel extra settings from setting field
  let extraSettings = {
    task_plugin_key: '',
    task_extend_plugin_keys: [] as string[],
    request_plugins: [] as string[],
    force_format: false,
    thinking_to_content: false,
    proxy: '',
    http_protocol: HTTP_PROTOCOL_AUTO as 'auto' | 'http1',
    http2_connection_shards: 1,
    tls_insecure_skip_verify: false,
    pass_through_body_enabled: false,
    responses_websocket_enabled: false,
    system_prompt: '',
    system_prompt_override: false,
  }

  if (channel.setting) {
    try {
      const parsed = JSON.parse(channel.setting)
      const protocol = normalizeHttpProtocol(parsed.http_protocol)
      const shards = normalizeHttp2ConnectionShards(
        parsed.http2_connection_shards
      )
      extraSettings = {
        task_plugin_key: parsed.task_plugin_key || '',
        task_extend_plugin_keys: readTaskExtendPluginKeys(channel.type, parsed),
        request_plugins: Array.isArray(parsed.request_plugins)
          ? parsed.request_plugins.filter(
              (id: unknown): id is string => typeof id === 'string'
            )
          : [],
        force_format: parsed.force_format || false,
        thinking_to_content: parsed.thinking_to_content || false,
        proxy: parsed.proxy || '',
        http_protocol: protocol,
        http2_connection_shards: protocol === HTTP_PROTOCOL_HTTP1 ? 1 : shards,
        tls_insecure_skip_verify: parsed.tls_insecure_skip_verify === true,
        pass_through_body_enabled: parsed.pass_through_body_enabled || false,
        responses_websocket_enabled:
          parsed.responses_websocket_enabled === true,
        system_prompt: parsed.system_prompt || '',
        system_prompt_override: parsed.system_prompt_override || false,
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel setting:', error)
    }
  }

  // Parse type-specific settings from settings field
  let vertexKeyType: 'json' | 'api_key' = 'json'
  let vertexProjectId = ''
  let vertexApiVersion: 'v1' | 'v1beta1' = 'v1'
  let azureResponsesVersion = ''
  let isEnterpriseAccount = false
  let awsKeyType: 'ak_sk' | 'api_key' = 'ak_sk'
  let awsApiType: 'invoke_model' | 'mantle' = 'invoke_model'
  let allowServiceTier = false
  let disableStore = false
  let allowSafetyIdentifier = false
  let allowIncludeObfuscation = false
  let allowInferenceGeo = false
  let allowSpeed = false
  let claudeBetaQuery = false
  let ollamaOpenAIChat = false
  let novelAIPresetsDisabled = false
  let novelAIHDDisabled = false
  let novelAIUpscaleEnabled = false
  let novelAIQueueLimit = NOVELAI_QUEUE_LIMIT_DEFAULT
  let upstreamQuotaErrorMaskEnabled = false
  let upstreamQuotaErrorMessage = ''
  let upstreamQuotaErrorStatusCode: number | null = null
  let geminiFreeKeyEnabled = false
  let geminiFreeKeyRevealTierErrors = false
  let geminiFreeKeyTokenCounter: GeminiFreeKeyTokenCounter =
    GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT
  let geminiFreeKeyMaxAttempts: number | null = null
  let geminiFreeKeyModelLimits = readGeminiFreeKeyModelLimitRows(undefined)
  let geminiThinkingSuffixEnabled = false
  let moderationEnabled = false
  let moderationFailOpen = false
  let moderationCategories: string[] = []
  let moderationSkipImages = false
  let moderationMaxChars: number | null = null
  let moderationTimeoutSeconds: number | null = null
  let moderationRulePresets: string[] = []
  let moderationRulePresetsSet = false
  let moderationRules: ModerationRuleRow[] = []
  let sendSafetyIdentifier = false
  let disableTaskPollingSleep = false
  let upstreamModelUpdateCheckEnabled = false
  let upstreamModelUpdateAutoSyncEnabled = false
  let upstreamModelUpdateIgnoredModels = ''
  let advancedCustom = ''
  let pluginConfig: Record<string, unknown> = {}
  let pluginRoutes: Record<string, boolean> = {}

  if (channel.settings) {
    try {
      const parsed = JSON.parse(channel.settings)
      vertexKeyType = parsed.vertex_key_type || 'json'
      vertexProjectId = parsed.vertex_project_id || ''
      vertexApiVersion =
        parsed.vertex_api_version === 'v1beta1' ? 'v1beta1' : 'v1'
      azureResponsesVersion = parsed.azure_responses_version || ''
      isEnterpriseAccount = parsed.openrouter_enterprise === true
      awsKeyType = parsed.aws_key_type || 'ak_sk'
      awsApiType = parsed.aws_api_type || 'invoke_model'
      allowServiceTier = parsed.allow_service_tier === true
      disableStore = parsed.disable_store === true
      allowSafetyIdentifier = parsed.allow_safety_identifier === true
      allowIncludeObfuscation = parsed.allow_include_obfuscation === true
      allowInferenceGeo = parsed.allow_inference_geo === true
      allowSpeed = parsed.allow_speed === true
      claudeBetaQuery = parsed.claude_beta_query === true
      ollamaOpenAIChat = parsed.ollama_openai_chat === true
      novelAIPresetsDisabled = parsed.novelai_presets_disabled === true
      novelAIHDDisabled = parsed.novelai_hd_disabled === true
      novelAIUpscaleEnabled = parsed.novelai_upscale_enabled === true
      // The limit the server applies: unset or invalid is the default, and
      // a stored value above the maximum acts as the maximum.
      if (
        Number.isInteger(parsed.novelai_queue_limit) &&
        parsed.novelai_queue_limit >= 0
      ) {
        novelAIQueueLimit = Math.min(
          parsed.novelai_queue_limit,
          NOVELAI_QUEUE_LIMIT_MAX
        )
      }
      upstreamQuotaErrorMaskEnabled =
        parsed.upstream_quota_error_mask_enabled === true
      upstreamQuotaErrorMessage =
        typeof parsed.upstream_quota_error_message === 'string'
          ? parsed.upstream_quota_error_message
          : ''
      // 0 or absent is the default, shown as the placeholder.
      if (
        Number.isInteger(parsed.upstream_quota_error_status_code) &&
        parsed.upstream_quota_error_status_code !== 0
      ) {
        upstreamQuotaErrorStatusCode = parsed.upstream_quota_error_status_code
      }
      geminiFreeKeyEnabled = parsed.gemini_free_key_enabled === true
      geminiFreeKeyRevealTierErrors =
        parsed.gemini_free_key_reveal_tier_errors === true
      if (parsed.gemini_free_key_token_counter === 'local') {
        geminiFreeKeyTokenCounter = 'local'
      }
      // 0 or absent is the default, shown as the placeholder.
      if (
        Number.isInteger(parsed.gemini_free_key_max_attempts) &&
        parsed.gemini_free_key_max_attempts !== 0
      ) {
        geminiFreeKeyMaxAttempts = parsed.gemini_free_key_max_attempts
      }
      geminiFreeKeyModelLimits = readGeminiFreeKeyModelLimitRows(
        parsed.gemini_free_key_model_limits
      )
      geminiThinkingSuffixEnabled =
        parsed.gemini_thinking_suffix_enabled === true
      moderationEnabled = parsed.moderation_enabled === true
      moderationFailOpen = parsed.moderation_fail_open === true
      moderationSkipImages = parsed.moderation_skip_images === true
      sendSafetyIdentifier = parsed.send_safety_identifier === true
      // Values this console does not know are dropped, as the server would
      // refuse them.
      if (Array.isArray(parsed.moderation_categories)) {
        moderationCategories = parsed.moderation_categories.filter(
          (value: unknown) =>
            MODERATION_CATEGORIES.some((category) => category.value === value)
        )
      }
      // An explicit list, even an empty one, was saved: the default presets
      // are not selected again.
      if (Array.isArray(parsed.moderation_rule_presets)) {
        moderationRulePresetsSet = true
        moderationRulePresets = parsed.moderation_rule_presets.filter(
          (value: unknown) =>
            MODERATION_PRESETS.some((preset) => preset.id === value)
        )
      }
      // 0 or absent is the default, shown as the placeholder.
      if (
        Number.isInteger(parsed.moderation_max_chars) &&
        parsed.moderation_max_chars !== 0
      ) {
        moderationMaxChars = parsed.moderation_max_chars
      }
      if (
        Number.isInteger(parsed.moderation_timeout_seconds) &&
        parsed.moderation_timeout_seconds !== 0
      ) {
        moderationTimeoutSeconds = parsed.moderation_timeout_seconds
      }
      if (Array.isArray(parsed.moderation_rules)) {
        moderationRules = parsed.moderation_rules.map(
          (rule: Record<string, unknown> | null): ModerationRuleRow => ({
            name: typeof rule?.name === 'string' ? rule.name : '',
            type: rule?.type === 'regex' ? 'regex' : 'keyword',
            pattern: typeof rule?.pattern === 'string' ? rule.pattern : '',
            case_sensitive: rule?.case_sensitive === true,
          })
        )
      }
      disableTaskPollingSleep = parsed.disable_task_polling_sleep === true
      upstreamModelUpdateCheckEnabled =
        parsed.upstream_model_update_check_enabled === true
      upstreamModelUpdateAutoSyncEnabled =
        parsed.upstream_model_update_auto_sync_enabled === true
      upstreamModelUpdateIgnoredModels = Array.isArray(
        parsed.upstream_model_update_ignored_models
      )
        ? parsed.upstream_model_update_ignored_models.join(',')
        : ''
      if (parsed.advanced_custom) {
        advancedCustom = stringifyAdvancedCustomConfig(parsed.advanced_custom)
      }
      if (isJsonObjectValue(parsed.plugin_config)) {
        pluginConfig = parsed.plugin_config
      }
      if (isJsonObjectValue(parsed.plugin_routes)) {
        pluginRoutes = Object.fromEntries(
          Object.entries(parsed.plugin_routes).filter(
            (entry): entry is [string, boolean] => typeof entry[1] === 'boolean'
          )
        )
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel settings:', error)
    }
  }

  return {
    name: channel.name || '',
    type: channel.type,
    base_url: channel.base_url || '',
    key: '', // Never populate key from backend for security
    openai_organization: channel.openai_organization || '',
    models: channel.models || '',
    group: parseGroups(channel.group || 'default'),
    model_mapping: channel.model_mapping || '',
    priority: channel.priority || 0,
    weight: channel.weight || 0,
    test_model: channel.test_model || '',
    auto_ban: channel.auto_ban ?? 1,
    status: channel.status,
    status_code_mapping: channel.status_code_mapping || '',
    tag: channel.tag || '',
    remark: channel.remark || '',
    setting: channel.setting || '',
    param_override: channel.param_override || '',
    header_override: channel.header_override || '',
    settings: channel.settings || '{}',
    other: channel.other || '',
    multi_key_mode: 'single',
    multi_key_type: channel.channel_info.multi_key_mode || 'random',
    batch_add_set_key_prefix_2_name: false,
    key_mode: 'append', // Default to append mode for editing multi-key channels
    // Channel extra settings
    ...extraSettings,
    // Type-specific settings
    is_enterprise_account: isEnterpriseAccount,
    vertex_key_type: vertexKeyType,
    vertex_project_id: vertexProjectId,
    vertex_api_version: vertexApiVersion,
    azure_responses_version: azureResponsesVersion,
    aws_key_type: awsKeyType,
    aws_api_type: awsApiType,
    allow_service_tier: allowServiceTier,
    disable_store: disableStore,
    allow_include_obfuscation: allowIncludeObfuscation,
    allow_inference_geo: allowInferenceGeo,
    allow_speed: allowSpeed,
    claude_beta_query: claudeBetaQuery,
    ollama_openai_chat: ollamaOpenAIChat,
    novelai_presets_disabled: novelAIPresetsDisabled,
    novelai_hd_disabled: novelAIHDDisabled,
    novelai_upscale_enabled: novelAIUpscaleEnabled,
    novelai_queue_limit: novelAIQueueLimit,
    upstream_quota_error_mask_enabled: upstreamQuotaErrorMaskEnabled,
    upstream_quota_error_message: upstreamQuotaErrorMessage,
    upstream_quota_error_status_code: upstreamQuotaErrorStatusCode,
    gemini_free_key_enabled: geminiFreeKeyEnabled,
    gemini_free_key_reveal_tier_errors: geminiFreeKeyRevealTierErrors,
    gemini_free_key_token_counter: geminiFreeKeyTokenCounter,
    gemini_free_key_max_attempts: geminiFreeKeyMaxAttempts,
    gemini_free_key_model_limits: geminiFreeKeyModelLimits,
    gemini_thinking_suffix_enabled: geminiThinkingSuffixEnabled,
    moderation_enabled: moderationEnabled,
    moderation_fail_open: moderationFailOpen,
    moderation_categories: moderationCategories,
    moderation_skip_images: moderationSkipImages,
    moderation_max_chars: moderationMaxChars,
    moderation_timeout_seconds: moderationTimeoutSeconds,
    moderation_rule_presets: moderationRulePresets,
    moderation_rule_presets_set: moderationRulePresetsSet,
    moderation_rules: moderationRules,
    send_safety_identifier: sendSafetyIdentifier,
    disable_task_polling_sleep: disableTaskPollingSleep,
    allow_safety_identifier: allowSafetyIdentifier,
    upstream_model_update_check_enabled: upstreamModelUpdateCheckEnabled,
    upstream_model_update_auto_sync_enabled: upstreamModelUpdateAutoSyncEnabled,
    upstream_model_update_ignored_models: upstreamModelUpdateIgnoredModels,
    advanced_custom: advancedCustom,
    plugin_config: pluginConfig,
    plugin_routes: pluginRoutes,
    plugin_config_draft: null,
  }
}

/**
 * Build the setting JSON string from form extra settings
 */
export function buildSettingJSON(formData: ChannelFormValues): string {
  const settingObj: Record<string, unknown> = {
    task_plugin_key:
      formData.type === CHANNEL_TYPE_TASK_PLUGIN
        ? formData.task_plugin_key?.trim() || ''
        : undefined,
    task_extend_plugin_keys:
      formData.type === CHANNEL_TYPE_NEW_API &&
      formData.task_extend_plugin_keys?.length
        ? formData.task_extend_plugin_keys
        : undefined,
    request_plugins: formData.request_plugins?.length
      ? formData.request_plugins
      : undefined,
    force_format: formData.force_format || false,
    thinking_to_content: formData.thinking_to_content || false,
    proxy: formData.proxy?.trim() || '',
    pass_through_body_enabled: formData.pass_through_body_enabled === true,
    // A plugin channel shows the switch only when its plugin can serve it.
    responses_websocket_enabled:
      (supportsResponsesWebSocket(formData.type) ||
        formData.type === CHANNEL_TYPE_TASK_PLUGIN) &&
      formData.responses_websocket_enabled === true,
    system_prompt: formData.system_prompt || '',
    system_prompt_override: formData.system_prompt_override || false,
  }

  const protocol = normalizeHttpProtocol(formData.http_protocol)
  const shards =
    protocol === HTTP_PROTOCOL_HTTP1
      ? 1
      : normalizeHttp2ConnectionShards(formData.http2_connection_shards)

  // Omit defaults so unchanged channels keep equivalent JSON.
  if (protocol === HTTP_PROTOCOL_HTTP1) {
    settingObj.http_protocol = HTTP_PROTOCOL_HTTP1
  } else if (shards > 1) {
    settingObj.http2_connection_shards = shards
  }
  if (formData.tls_insecure_skip_verify === true) {
    settingObj.tls_insecure_skip_verify = true
  }

  return JSON.stringify(settingObj)
}

/**
 * Build the settings JSON string (for type-specific config like vertex_key_type)
 */
function buildSettingsJSON(formData: ChannelFormValues): string {
  let settingsObj: Record<string, unknown> = {}

  // Try to parse existing settings first
  if (formData.settings && formData.settings !== '{}') {
    try {
      settingsObj = JSON.parse(formData.settings)
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse existing settings:', error)
    }
  }

  // Add vertex_key_type for Vertex AI channels (type 41)
  if (formData.type === 41) {
    settingsObj.vertex_key_type = formData.vertex_key_type || 'json'
    const vertexProjectId = formData.vertex_project_id?.trim()
    if (settingsObj.vertex_key_type === 'api_key' && vertexProjectId) {
      settingsObj.vertex_project_id = vertexProjectId
    } else {
      delete settingsObj.vertex_project_id
    }
    settingsObj.vertex_api_version = formData.vertex_api_version || 'v1'
  } else {
    delete settingsObj.vertex_key_type
    delete settingsObj.vertex_project_id
    delete settingsObj.vertex_api_version
  }

  // Add azure_responses_version for Azure channels (type 3)
  if (formData.type === 3 && formData.azure_responses_version) {
    settingsObj.azure_responses_version = formData.azure_responses_version
  } else if ('azure_responses_version' in settingsObj) {
    delete settingsObj.azure_responses_version
  }

  // Add enterprise account setting for OpenRouter (type 20)
  if (formData.type === 20) {
    settingsObj.openrouter_enterprise = formData.is_enterprise_account === true
  } else if ('openrouter_enterprise' in settingsObj) {
    delete settingsObj.openrouter_enterprise
  }

  // AWS interface selection is opt-in; existing channels retain InvokeModel.
  if (formData.type === 33) {
    settingsObj.aws_key_type = formData.aws_key_type || 'ak_sk'
    settingsObj.aws_api_type = formData.aws_api_type || 'invoke_model'
  } else {
    delete settingsObj.aws_key_type
    delete settingsObj.aws_api_type
  }

  // Field passthrough controls:
  // - OpenAI, Anthropic, Codex, and New API: allow_service_tier
  // - OpenAI request fields: OpenAI, Codex, and New API
  // - Claude request fields: Anthropic and New API
  if (FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.allow_service_tier = formData.allow_service_tier === true
  } else if ('allow_service_tier' in settingsObj) {
    delete settingsObj.allow_service_tier
  }

  if (OPENAI_FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.disable_store = formData.disable_store === true
    settingsObj.allow_safety_identifier =
      formData.allow_safety_identifier === true
    settingsObj.allow_include_obfuscation =
      formData.allow_include_obfuscation === true
  } else {
    if ('disable_store' in settingsObj) {
      delete settingsObj.disable_store
    }
    if ('allow_safety_identifier' in settingsObj) {
      delete settingsObj.allow_safety_identifier
    }
    if ('allow_include_obfuscation' in settingsObj) {
      delete settingsObj.allow_include_obfuscation
    }
  }

  if (
    OPENAI_FIELD_PASSTHROUGH_TYPES.has(formData.type) ||
    CLAUDE_FIELD_PASSTHROUGH_TYPES.has(formData.type)
  ) {
    settingsObj.allow_inference_geo = formData.allow_inference_geo === true
  } else if ('allow_inference_geo' in settingsObj) {
    delete settingsObj.allow_inference_geo
  }

  if (CLAUDE_FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.allow_speed = formData.allow_speed === true
  } else if ('allow_speed' in settingsObj) {
    delete settingsObj.allow_speed
  }

  // Only the Anthropic adaptor supports forcing the Claude beta query.
  if (formData.type === 14) {
    settingsObj.claude_beta_query = formData.claude_beta_query === true
  } else if ('claude_beta_query' in settingsObj) {
    delete settingsObj.claude_beta_query
  }

  // Only the Ollama adaptor can switch chat completions to the OpenAI-compatible endpoint.
  if (formData.type === CHANNEL_TYPE_OLLAMA) {
    settingsObj.ollama_openai_chat = formData.ollama_openai_chat === true
  } else if ('ollama_openai_chat' in settingsObj) {
    delete settingsObj.ollama_openai_chat
  }

  // Only the NovelAI adaptor adds the web client's prompt presets, enhances
  // (HD) and upscales its images and queues generations per key.
  if (formData.type === CHANNEL_TYPE_NOVELAI) {
    settingsObj.novelai_presets_disabled =
      formData.novelai_presets_disabled === true
    settingsObj.novelai_hd_disabled = formData.novelai_hd_disabled === true
    settingsObj.novelai_upscale_enabled =
      formData.novelai_upscale_enabled === true
    settingsObj.novelai_queue_limit =
      formData.novelai_queue_limit ?? NOVELAI_QUEUE_LIMIT_DEFAULT
  } else {
    delete settingsObj.novelai_presets_disabled
    delete settingsObj.novelai_hd_disabled
    delete settingsObj.novelai_upscale_enabled
    delete settingsObj.novelai_queue_limit
  }

  // Every channel type can hide upstream balance errors. Only what differs
  // from the defaults is written, so an untouched channel saves no key; the
  // message and status are kept while the switch is off.
  if (formData.upstream_quota_error_mask_enabled === true) {
    settingsObj.upstream_quota_error_mask_enabled = true
  } else {
    delete settingsObj.upstream_quota_error_mask_enabled
  }
  const quotaErrorMessage = formData.upstream_quota_error_message?.trim()
  if (quotaErrorMessage) {
    settingsObj.upstream_quota_error_message = quotaErrorMessage
  } else {
    delete settingsObj.upstream_quota_error_message
  }
  if (formData.upstream_quota_error_status_code) {
    settingsObj.upstream_quota_error_status_code =
      formData.upstream_quota_error_status_code
  } else {
    delete settingsObj.upstream_quota_error_status_code
  }

  // Only a Gemini (AI Studio) channel has the free key mode. Only what
  // differs from the defaults is written, so an untouched channel saves no
  // key; the other free key settings are kept while the mode is off.
  if (formData.type === CHANNEL_TYPE_GEMINI) {
    if (formData.gemini_free_key_enabled === true) {
      settingsObj.gemini_free_key_enabled = true
    } else {
      delete settingsObj.gemini_free_key_enabled
    }
    if (formData.gemini_free_key_reveal_tier_errors === true) {
      settingsObj.gemini_free_key_reveal_tier_errors = true
    } else {
      delete settingsObj.gemini_free_key_reveal_tier_errors
    }
    if (formData.gemini_free_key_token_counter === 'local') {
      settingsObj.gemini_free_key_token_counter = 'local'
    } else {
      delete settingsObj.gemini_free_key_token_counter
    }
    const attempts = formData.gemini_free_key_max_attempts
    if (attempts != null && attempts !== GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT) {
      settingsObj.gemini_free_key_max_attempts = attempts
    } else {
      delete settingsObj.gemini_free_key_max_attempts
    }
    const modelLimits = toGeminiFreeKeyModelLimits(
      formData.gemini_free_key_model_limits ?? []
    )
    if (
      modelLimits.length > 0 &&
      !isDefaultGeminiFreeKeyModelLimits(modelLimits)
    ) {
      settingsObj.gemini_free_key_model_limits = modelLimits
    } else {
      delete settingsObj.gemini_free_key_model_limits
    }
  } else {
    delete settingsObj.gemini_free_key_enabled
    delete settingsObj.gemini_free_key_reveal_tier_errors
    delete settingsObj.gemini_free_key_token_counter
    delete settingsObj.gemini_free_key_max_attempts
    delete settingsObj.gemini_free_key_model_limits
  }

  // Only a Gemini (AI Studio) channel reads thinking suffixes; the key is
  // written only while the switch is on.
  if (
    formData.type === CHANNEL_TYPE_GEMINI &&
    formData.gemini_thinking_suffix_enabled === true
  ) {
    settingsObj.gemini_thinking_suffix_enabled = true
  } else {
    delete settingsObj.gemini_thinking_suffix_enabled
  }

  // Only an OpenAI channel moderates its requests and sends the safety
  // identifier. Only what differs from the defaults is written, so an
  // untouched channel saves no key; the other moderation settings are kept
  // while the switch is off.
  if (formData.type === 1) {
    if (formData.moderation_enabled === true) {
      settingsObj.moderation_enabled = true
    } else {
      delete settingsObj.moderation_enabled
    }
    if (formData.moderation_fail_open === true) {
      settingsObj.moderation_fail_open = true
    } else {
      delete settingsObj.moderation_fail_open
    }
    const selectedCategories = formData.moderation_categories ?? []
    const categories = MODERATION_CATEGORIES.map(
      (category) => category.value
    ).filter((value) => selectedCategories.includes(value))
    if (categories.length > 0) {
      settingsObj.moderation_categories = categories
    } else {
      delete settingsObj.moderation_categories
    }
    if (formData.moderation_skip_images === true) {
      settingsObj.moderation_skip_images = true
    } else {
      delete settingsObj.moderation_skip_images
    }
    const maxChars = formData.moderation_max_chars
    if (maxChars && maxChars !== MODERATION_MAX_CHARS_DEFAULT) {
      settingsObj.moderation_max_chars = maxChars
    } else {
      delete settingsObj.moderation_max_chars
    }
    const timeout = formData.moderation_timeout_seconds
    if (timeout && timeout !== MODERATION_TIMEOUT_SECONDS_DEFAULT) {
      settingsObj.moderation_timeout_seconds = timeout
    } else {
      delete settingsObj.moderation_timeout_seconds
    }
    // An explicit list, even an empty one, once loaded or selected: the
    // server keeps [] apart from a list that was never saved.
    const selectedPresets = formData.moderation_rule_presets ?? []
    const presets = MODERATION_PRESETS.map((preset) => preset.id).filter((id) =>
      selectedPresets.includes(id)
    )
    if (presets.length > 0 || formData.moderation_rule_presets_set === true) {
      settingsObj.moderation_rule_presets = presets
    } else {
      delete settingsObj.moderation_rule_presets
    }
    const rules = (formData.moderation_rules ?? []).map((row) => {
      const name = row.name.trim()
      return {
        ...(name ? { name } : {}),
        type: row.type,
        pattern: row.pattern.trim(),
        ...(row.case_sensitive ? { case_sensitive: true } : {}),
      }
    })
    if (rules.length > 0) {
      settingsObj.moderation_rules = rules
    } else {
      delete settingsObj.moderation_rules
    }
    if (formData.send_safety_identifier === true) {
      settingsObj.send_safety_identifier = true
    } else {
      delete settingsObj.send_safety_identifier
    }
  } else {
    for (const key of MODERATION_SETTINGS_KEYS) delete settingsObj[key]
  }

  settingsObj.disable_task_polling_sleep =
    formData.disable_task_polling_sleep === true

  // Upstream model update settings (for model-fetchable channel types; a
  // plugin channel shows them when its plugin can list models)
  if (
    MODEL_FETCHABLE_TYPES.has(formData.type) ||
    formData.type === CHANNEL_TYPE_TASK_PLUGIN
  ) {
    settingsObj.upstream_model_update_check_enabled =
      formData.upstream_model_update_check_enabled === true
    settingsObj.upstream_model_update_auto_sync_enabled =
      settingsObj.upstream_model_update_check_enabled === true &&
      formData.upstream_model_update_auto_sync_enabled === true
    settingsObj.upstream_model_update_ignored_models = [
      ...new Set(
        String(formData.upstream_model_update_ignored_models || '')
          .split(',')
          .map((model) => model.trim())
          .filter(Boolean)
      ),
    ]
    if (
      !Array.isArray(settingsObj.upstream_model_update_last_detected_models) ||
      settingsObj.upstream_model_update_check_enabled !== true
    ) {
      settingsObj.upstream_model_update_last_detected_models = []
    }
    if (typeof settingsObj.upstream_model_update_last_check_time !== 'number') {
      settingsObj.upstream_model_update_last_check_time = 0
    }
  }

  if (formData.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
    const advancedCustomConfig = parseAdvancedCustomConfig(
      formData.advanced_custom
    )
    if (advancedCustomConfig) {
      settingsObj.advanced_custom = advancedCustomConfig
    }
  } else if ('advanced_custom' in settingsObj) {
    delete settingsObj.advanced_custom
  }

  // Only set values are kept; the plugin's defaults apply to the rest.
  if (
    formData.type === CHANNEL_TYPE_TASK_PLUGIN &&
    Object.keys(formData.plugin_config ?? {}).length > 0
  ) {
    settingsObj.plugin_config = formData.plugin_config
  } else {
    delete settingsObj.plugin_config
  }
  if (
    formData.type === CHANNEL_TYPE_TASK_PLUGIN &&
    Object.keys(formData.plugin_routes ?? {}).length > 0
  ) {
    settingsObj.plugin_routes = formData.plugin_routes
  } else {
    delete settingsObj.plugin_routes
  }

  return JSON.stringify(settingsObj)
}

function normalizeBaseUrl(value: string | undefined): string {
  return String(value || '')
    .trim()
    .replace(/\/+$/, '')
}

/**
 * Transform form data to API payload for creating channel
 */
export function transformFormDataToCreatePayload(formData: ChannelFormValues): {
  mode: 'single' | 'batch' | 'multi_to_single'
  multi_key_mode?: 'random' | 'polling'
  batch_add_set_key_prefix_2_name?: boolean
  channel: Partial<Channel>
} {
  const mode = formData.multi_key_mode || 'single'

  const channel: Partial<Channel> = {
    name: formData.name,
    type: formData.type,
    base_url: normalizeBaseUrl(formData.base_url) || null,
    key: formData.key,
    openai_organization: formData.openai_organization || null,
    models: formData.models,
    group: formatGroups(formData.group),
    model_mapping: formData.model_mapping || null,
    priority: formData.priority || null,
    weight: formData.weight || null,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    status: formData.status,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Clean up empty strings to null for optional fields
  Object.keys(channel).forEach((key) => {
    if (channel[key as keyof typeof channel] === '') {
      ;(channel as Record<string, unknown>)[key] = null
    }
  })

  return {
    mode,
    multi_key_mode:
      mode === 'multi_to_single' ? formData.multi_key_type : undefined,
    batch_add_set_key_prefix_2_name:
      mode === 'batch' ? formData.batch_add_set_key_prefix_2_name : undefined,
    channel,
  }
}

/**
 * Transform form data to API payload for updating channel
 */
export function transformFormDataToUpdatePayload(
  formData: ChannelFormValues,
  channelId: number
): Partial<Channel> {
  const payload: Partial<Channel> = {
    id: channelId,
    name: formData.name,
    type: formData.type,
    base_url: normalizeBaseUrl(formData.base_url) || null,
    openai_organization: formData.openai_organization || null,
    models: formData.models,
    group: formatGroups(formData.group),
    model_mapping: formData.model_mapping || null,
    priority: formData.priority ?? 0,
    weight: formData.weight ?? 0,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Only include key if it was changed (not empty)
  if (formData.key && formData.key.trim()) {
    payload.key = formData.key
  }

  // Clean up empty strings to null for optional fields
  Object.keys(payload).forEach((key) => {
    if (payload[key as keyof typeof payload] === '') {
      ;(payload as Record<string, unknown>)[key] = null
    }
  })

  // Send explicit empty strings for nullable fields so GORM updates can clear them.
  payload.base_url = normalizeBaseUrl(formData.base_url) || ''
  payload.openai_organization = formData.openai_organization || ''
  payload.test_model = formData.test_model || ''
  payload.tag = formData.tag || ''
  payload.remark = formData.remark || ''
  payload.model_mapping = formData.model_mapping || ''
  payload.status_code_mapping = formData.status_code_mapping || ''
  payload.param_override = formData.param_override || ''
  payload.header_override = formData.header_override || ''

  return payload
}

// ============================================================================
// Validation Helpers
// ============================================================================

/**
 * Validate JSON string
 */
export function validateJSON(value: string): boolean {
  if (!value || value.trim() === '') return true
  try {
    JSON.parse(value)
    return true
  } catch {
    return false
  }
}

/**
 * Validate model mapping format
 */
export function validateModelMapping(value: string): boolean {
  if (!value || value.trim() === '') return true
  return validateJSON(value)
}

/**
 * Parse models string to array
 */
export function parseModels(models: string): string[] {
  if (!models) return []
  return models
    .split(',')
    .map((m) => m.trim())
    .filter((m) => m.length > 0)
}

/**
 * Parse groups string to array
 */
export function parseGroups(groups: string): string[] {
  if (!groups) return []
  return groups
    .split(',')
    .map((g) => g.trim())
    .filter((g) => g.length > 0)
}

/**
 * Format models array to string
 */
export function formatModels(models: string[]): string {
  return models.join(',')
}

/**
 * Format groups array to string
 */
export function formatGroups(groups: string[]): string {
  return groups.join(',')
}
