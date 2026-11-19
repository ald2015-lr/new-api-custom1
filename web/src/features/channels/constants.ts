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
import type {
  GeminiFreeKeyCooldownReason,
  GeminiFreeKeyModelLimit,
} from './types'

export const CHANNEL_PRIORITY_GUIDE_KEYS = {
  card: 'channels:priority-drag-guide:card:v1',
  table: 'channels:priority-drag-guide:v1',
} as const
// ============================================================================
// Channel Types (from constant/channel.go)
// All label/name values are i18n keys; use t(value) when displaying.
// ============================================================================

export const CHANNEL_TYPE_OLLAMA = 4

export const CHANNEL_TYPE_GEMINI = 24

export const CHANNEL_TYPE_SUB2API = 59

export const CHANNEL_TYPE_NEW_API = 60

export const CHANNEL_TYPE_TASK_PLUGIN = 61

export const CHANNEL_TYPE_VLLM = 62

export const CHANNEL_TYPE_SGLANG = 63

export const CHANNEL_TYPE_NOVELAI = 64

// Requests that may wait for each busy key of a NovelAI channel, the
// generating one aside (settings.novelai_queue_limit; the server applies the
// same default and bounds).
export const NOVELAI_QUEUE_LIMIT_DEFAULT = 10
export const NOVELAI_QUEUE_LIMIT_MAX = 100

// What a channel that hides upstream balance errors answers in their place
// when it sets nothing (settings.upstream_quota_error_message and
// upstream_quota_error_status_code; the server applies the same defaults and
// bounds). The default message is the operator's response text, so it is not
// translated.
export const UPSTREAM_QUOTA_ERROR_MESSAGE_DEFAULT = '上游tokens数量不足'
export const UPSTREAM_QUOTA_ERROR_MESSAGE_MAX = 500
export const UPSTREAM_QUOTA_ERROR_STATUS_CODE_DEFAULT = 403
export const UPSTREAM_QUOTA_ERROR_STATUS_CODE_MIN = 400
export const UPSTREAM_QUOTA_ERROR_STATUS_CODE_MAX = 599

// Content moderation of an OpenAI channel (settings.moderation_*). The
// server applies the same defaults and bounds; the channel form saves only
// what differs from the defaults. Characters are Unicode code points.
export const MODERATION_MAX_CHARS_DEFAULT = 10000
export const MODERATION_MAX_CHARS_MIN = 1000
export const MODERATION_MAX_CHARS_MAX = 200000
export const MODERATION_TIMEOUT_SECONDS_DEFAULT = 10
export const MODERATION_TIMEOUT_SECONDS_MIN = 1
export const MODERATION_TIMEOUT_SECONDS_MAX = 60
export const MODERATION_RULES_MAX = 100
export const MODERATION_REGEX_RULES_MAX = 30
export const MODERATION_RULE_PATTERN_MAX = 512
export const MODERATION_RULE_NAME_MAX = 64
// Every settings key of content moderation and the safety identifier; a
// channel of another type saves none of them.
export const MODERATION_SETTINGS_KEYS = [
  'moderation_enabled',
  'moderation_fail_open',
  'moderation_categories',
  'moderation_skip_images',
  'moderation_max_chars',
  'moderation_timeout_seconds',
  'moderation_rule_presets',
  'moderation_rules',
  'send_safety_identifier',
] as const

// Free key mode of a Gemini (AI Studio) channel (settings.gemini_free_key_*).
// The server applies the same defaults and bounds; the channel form saves
// only what differs from the defaults.
export const GEMINI_FREE_KEY_TOKEN_COUNTERS = ['count_tokens', 'local'] as const
export type GeminiFreeKeyTokenCounter =
  (typeof GEMINI_FREE_KEY_TOKEN_COUNTERS)[number]
export const GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT: GeminiFreeKeyTokenCounter =
  'count_tokens'
// Different keys one request may try inside the channel.
export const GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT = 3
export const GEMINI_FREE_KEY_MAX_ATTEMPTS_MIN = 1
export const GEMINI_FREE_KEY_MAX_ATTEMPTS_MAX = 10
// Bounds of the model limits table: rows, characters of a model name or
// alias, aliases per row, and each RPM / TPM / RPD value (-1 is unlimited).
export const GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS = 300
export const GEMINI_FREE_KEY_MODEL_NAME_MAX = 128
export const GEMINI_FREE_KEY_ALIASES_MAX = 20
export const GEMINI_FREE_KEY_LIMIT_MIN = -1
export const GEMINI_FREE_KEY_LIMIT_MAX = 1_000_000_000

// Model, aliases, RPM, TPM (input tokens per minute), RPD of each key on the
// AI Studio free tier. -1 is unlimited; 0 means the free tier does not offer
// the model, so its requests go to other channels at once.
type GeminiFreeKeyDefaultRow = [
  model: string,
  aliases: string[],
  rpm: number,
  tpm: number,
  rpd: number,
]

const GEMINI_FREE_KEY_DEFAULT_ROWS: GeminiFreeKeyDefaultRow[] = [
  ['gemini-3.8-flash', ['gemini-3.7-flash'], 5, 250_000, 20],
  ['gemini-3.6-flash', ['gemini-3.5-flash'], 5, 250_000, 20],
  ['gemini-3-flash-preview', ['gemini-3-flash'], 5, 250_000, 20],
  ['gemini-2.5-flash', [], 5, 250_000, 20],
  ['gemini-2.5-flash-lite', [], 10, 250_000, 20],
  [
    'gemini-3.1-flash-lite',
    ['gemini-3.1-flash-lite-preview'],
    15,
    250_000,
    500,
  ],
  ['gemini-3.5-flash-lite', [], 15, 250_000, 500],
  ['gemini-4-hydrogen', [], 5, 250_000, 20],
  ['gemini-robotics-er-2-preview', [], 5, 250_000, 20],
  ['gemma-4-26b-a4b-it', ['gemma-4-26b'], 30, 16_000, 14_400],
  ['gemma-4-31b-it', ['gemma-4-31b'], 30, 16_000, 14_400],
  ['gemini-2.5-flash-preview-tts', [], 3, 10_000, 10],
  ['gemini-3.1-flash-tts-preview', [], 3, 10_000, 10],
  ['gemini-3.8-flash-tts', [], 3, 10_000, 10],
  ['gemini-3.8-flash-lite-tts', [], 3, 10_000, 10],
  ['gemini-3.5-transcribe', [], 3, 10_000, 25],
  ['gemini-embedding-001', [], 100, 30_000, 1000],
  ['gemini-embedding-2', ['gemini-embedding-2-preview'], 100, 30_000, 1000],
  ['gemini-decision-1', [], 200, 50_000, 10_000],
  [
    'antigravity-preview-09-2026',
    ['antigravity-preview-05-2026'],
    60,
    100_000,
    100,
  ],
  // Live models: listed so their names do not fall into a Flash row.
  [
    'gemini-2.5-flash-native-audio-preview-09-2025',
    [
      'gemini-2.5-flash-native-audio-preview-12-2025',
      'gemini-2.5-flash-native-audio-latest',
    ],
    -1,
    1_000_000,
    -1,
  ],
  ['gemini-3.1-flash-live-preview', [], -1, 65_000, -1],
  ['gemini-3.5-live-translate-preview', [], -1, 20_000, -1],
  ['gemini-3.5-transcribe-live', [], -1, 20_000, -1],
  ['gemini-3.8-live', [], -1, 65_000, -1],
  ['gemini-3.8-live-extended-thinking', [], -1, 65_000, -1],
  // Not on the free tier.
  ['gemini-2.0-flash', ['gemini-2.0-flash-001'], 0, 0, 0],
  ['gemini-2.0-flash-lite', ['gemini-2.0-flash-lite-001'], 0, 0, 0],
  ['gemini-2.5-pro', [], 0, 0, 0],
  ['gemini-2.5-pro-preview-tts', [], 0, 0, 0],
  [
    'gemini-2.5-flash-image',
    ['gemini-2.5-flash-image-preview', 'gemini-2.5-flash-preview-image'],
    0,
    0,
    0,
  ],
  [
    'gemini-3-pro-image',
    ['gemini-3-pro-image-preview', 'nano-banana-pro-preview'],
    0,
    0,
    0,
  ],
  [
    'gemini-3.1-pro-preview',
    ['gemini-3.1-pro-preview-customtools', 'gemini-3.1-pro'],
    0,
    0,
    0,
  ],
  ['gemini-3.1-flash-image', ['gemini-3.1-flash-image-preview'], 0, 0, 0],
  ['gemini-3.1-flash-lite-image', [], 0, 0, 0],
  ['gemini-4-argon', [], 0, 0, 0],
  ['gemini-4-carbon', [], 0, 0, 0],
  ['gemini-nano-banana-2.1', [], 0, 0, 0],
  ['gemini-omni-1.1-flash', ['gemini-omni-flash-preview'], 0, 0, 0],
  ['gemini-omni-1.2-flash', [], 0, 0, 0],
  ['gemini-2.5-computer-use-preview-10-2025', [], 0, 0, 0],
  ['lyria-3-clip-preview', [], 0, 0, 0],
  ['lyria-3-pro-preview', [], 0, 0, 0],
  ['deep-research-pro-preview-12-2025', [], 0, 0, 0],
]

// The built-in model limits the server applies while a channel saves none.
export const GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS: readonly GeminiFreeKeyModelLimit[] =
  GEMINI_FREE_KEY_DEFAULT_ROWS.map(([model, aliases, rpm, tpm, rpd]) => ({
    model,
    aliases,
    rpm,
    tpm,
    rpd,
  }))

// Labels (i18n keys) of why a key, or one model of a key, is paused.
export const GEMINI_FREE_KEY_COOLDOWN_REASON_LABELS: Record<
  Exclude<GeminiFreeKeyCooldownReason, ''>,
  string
> = {
  minute: 'Per-minute limit',
  day: 'Daily quota used up',
  not_free: 'Not on the free tier',
  key_error: 'Invalid key',
  unknown: 'Upstream rate limit',
}

export const CHANNEL_TYPES = {
  0: 'Unknown',
  1: 'OpenAI',
  2: 'MjProxy',
  3: 'Azure',
  4: 'Ollama',
  5: 'MjProxyPlus',
  // 6: 'OpenAIMax',
  7: 'OhMyGPT',
  8: 'Custom',
  // 9: 'AILS',
  // 10: 'AI Proxy',
  // 11: 'PaLM',
  // 12: 'API2GPT',
  // 13: 'AIGC2D',
  14: 'Anthropic',
  15: 'Baidu',
  16: 'Zhipu',
  17: 'Ali',
  18: 'Xunfei',
  19: '360',
  20: 'OpenRouter',
  // 21: 'AI Proxy Library',
  22: 'FastGPT',
  23: 'Tencent',
  24: 'Gemini',
  25: 'Moonshot',
  26: 'Zhipu GLM',
  27: 'Perplexity',
  31: 'LingYiWanWu',
  33: 'AWS',
  34: 'Cohere',
  35: 'MiniMax',
  36: 'SunoAPI',
  37: 'Dify',
  38: 'Jina',
  39: 'Cloudflare',
  40: 'SiliconFlow',
  41: 'Vertex AI',
  42: 'Mistral',
  43: 'DeepSeek',
  44: 'MokaAI',
  45: 'VolcEngine',
  46: 'Baidu V2',
  47: 'Xinference',
  48: 'xAI',
  49: 'Coze',
  50: 'Kling',
  51: 'Jimeng',
  52: 'Vidu',
  53: 'Submodel',
  54: 'Doubao',
  55: 'Sora',
  56: 'Replicate',
  57: 'ChatGPT Subscription (Codex)',
  58: 'Advanced Custom',
  59: 'Sub2API',
  60: 'New API',
  61: 'Task Plugin',
  62: 'vLLM',
  63: 'SGLang',
  64: 'NovelAI',
} as const

export type ChannelProviderPresentation = {
  descriptionKey: string
  detailKey?: string
  badge?: { labelKey: string; tone: 'warning' | 'primary' }
}

// Display copy only; channel routing, availability and ordering remain independent.
export const CHANNEL_PROVIDER_PRESENTATION: Partial<
  Record<number, ChannelProviderPresentation>
> = {
  1: { descriptionKey: 'Connect to the OpenAI API or compatible services' },
  2: { descriptionKey: 'Generate Midjourney images through MjProxy' },
  3: { descriptionKey: 'Connect to OpenAI models deployed on Azure' },
  4: { descriptionKey: 'Connect to local or self-hosted Ollama models' },
  5: { descriptionKey: 'Generate Midjourney images through MjProxyPlus' },
  7: { descriptionKey: 'Access model services through the OhMyGPT gateway' },
  8: {
    descriptionKey:
      'Legacy full-URL integration; use Advanced Custom for new channels',
    badge: { labelKey: 'Deprecated', tone: 'warning' },
  },
  14: { descriptionKey: 'Connect to the Anthropic API or compatible services' },
  15: { descriptionKey: 'Access Baidu Qianfan models through the legacy API' },
  16: { descriptionKey: 'Access Zhipu models through the legacy API' },
  17: { descriptionKey: 'Connect to Alibaba Cloud Bailian model services' },
  18: { descriptionKey: 'Connect to iFlytek Spark model services' },
  19: { descriptionKey: 'Connect to 360 model services' },
  20: {
    descriptionKey: 'Access models from multiple providers through OpenRouter',
  },
  22: { descriptionKey: 'Connect to FastGPT applications' },
  23: { descriptionKey: 'Connect to Tencent Hunyuan model services' },
  24: { descriptionKey: 'Connect to models through the Google Gemini API' },
  25: { descriptionKey: 'Connect to Moonshot AI model services' },
  26: { descriptionKey: 'Access Zhipu models through the V4 API' },
  27: { descriptionKey: 'Connect to Perplexity model services' },
  31: { descriptionKey: 'Connect to LingYiWanWu model services' },
  33: { descriptionKey: 'Access models through Amazon Bedrock' },
  34: { descriptionKey: 'Connect to Cohere model services' },
  35: { descriptionKey: 'Connect to MiniMax model services' },
  36: { descriptionKey: 'Generate music and lyrics through SunoAPI' },
  37: { descriptionKey: 'Connect to Dify applications and workflows' },
  38: { descriptionKey: 'Connect to Jina embedding and reranking services' },
  39: { descriptionKey: 'Access models through Cloudflare Workers AI' },
  40: { descriptionKey: 'Connect to SiliconFlow model inference services' },
  41: { descriptionKey: 'Access models through Google Cloud Vertex AI' },
  42: { descriptionKey: 'Connect to Mistral AI model services' },
  43: { descriptionKey: 'Connect to DeepSeek model services' },
  44: { descriptionKey: 'Access model services through MokaAI' },
  45: { descriptionKey: 'Connect to Volcengine Ark model services' },
  46: { descriptionKey: 'Access Baidu Qianfan models through the V2 API' },
  47: { descriptionKey: 'Connect to self-hosted models served by Xinference' },
  48: { descriptionKey: 'Connect to xAI Grok model services' },
  49: { descriptionKey: 'Connect to Coze bots' },
  50: { descriptionKey: 'Connect to Kling video generation services' },
  51: {
    descriptionKey: 'Connect to Jimeng image and video generation services',
  },
  52: { descriptionKey: 'Connect to Vidu video generation services' },
  53: { descriptionKey: 'Connect to Submodel model services' },
  54: {
    descriptionKey:
      'Connect to Doubao Seedance video and Seedream image generation through Volcengine Ark',
  },
  55: { descriptionKey: 'Connect to OpenAI Sora video generation services' },
  56: { descriptionKey: 'Access hosted model predictions through Replicate' },
  57: { descriptionKey: 'Access Codex using ChatGPT subscription credentials' },
  58: {
    descriptionKey:
      'Configure endpoint routing, authentication and protocol conversion for different upstream services',
    detailKey:
      "New API's flexible channel lets you configure upstream addresses and authentication per endpoint, choose native forwarding or supported protocol conversions, and configure model listing and balance queries independently",
    badge: { labelKey: 'Flexible integration', tone: 'primary' },
  },
  59: { descriptionKey: 'Connect to model services through a Sub2API gateway' },
  60: {
    descriptionKey:
      'Connect to New API model services with support for multiple task plugins',
  },
  62: { descriptionKey: 'Connect to self-hosted models served by vLLM' },
  63: { descriptionKey: 'Connect to self-hosted models served by SGLang' },
  64: {
    descriptionKey:
      "Generate images with NovelAI's official API (native and OpenAI formats)",
  },
} satisfies Record<
  Exclude<keyof typeof CHANNEL_TYPES, 0 | typeof CHANNEL_TYPE_TASK_PLUGIN>,
  ChannelProviderPresentation
>

const CHANNEL_TYPE_DISPLAY_ORDER: number[] = [
  1, 14, 24, 33, 43, 3, 41, 17, 45, 25, 26, 23, 48, 64, 60, 58, 59, 61, 42, 34,
  20, 4, 62, 40, 27, 15, 46, 18, 31, 35, 49, 19, 47, 37, 38, 39, 11, 8, 57, 22,
  21, 44, 2, 5, 36, 50, 51, 52, 53, 54, 55, 56,
]

export const CHANNEL_TYPE_OPTIONS: { value: number; label: string }[] = (() => {
  const ordered: { value: number; label: string }[] = []
  const seen = new Set<number>()
  for (const id of CHANNEL_TYPE_DISPLAY_ORDER) {
    const label = CHANNEL_TYPES[id as keyof typeof CHANNEL_TYPES]
    if (label) {
      ordered.push({ value: id, label })
      seen.add(id)
    }
  }
  for (const [key, label] of Object.entries(CHANNEL_TYPES)) {
    const id = Number(key)
    if (id !== 0 && !seen.has(id)) {
      ordered.push({ value: id, label })
    }
  }
  return ordered
})()

export function channelTypeOptionsForTaskPluginBind(
  canBindTaskPlugin: boolean
): { value: number; label: string }[] {
  if (canBindTaskPlugin) {
    return CHANNEL_TYPE_OPTIONS
  }
  return CHANNEL_TYPE_OPTIONS.filter(
    (option) => option.value !== CHANNEL_TYPE_TASK_PLUGIN
  )
}

// ============================================================================
// Channel Status (label values are i18n keys; use t(config.label) in components)
// ============================================================================

export const CHANNEL_STATUS = {
  UNKNOWN: 0,
  ENABLED: 1,
  MANUAL_DISABLED: 2,
  AUTO_DISABLED: 3,
} as const

export const CHANNEL_STATUS_LABELS = {
  [CHANNEL_STATUS.UNKNOWN]: 'Unknown',
  [CHANNEL_STATUS.ENABLED]: 'Enabled',
  [CHANNEL_STATUS.MANUAL_DISABLED]: 'Disabled',
  [CHANNEL_STATUS.AUTO_DISABLED]: 'Auto Disabled',
} as const

export const CHANNEL_STATUS_OPTIONS = [
  { value: 'all', label: 'All Status' },
  { value: 'enabled', label: 'Enabled' },
  { value: 'disabled', label: 'Disabled' },
] as const

export const CHANNEL_STATUS_CONFIG = {
  [CHANNEL_STATUS.UNKNOWN]: {
    variant: 'neutral' as const,
    label: 'Unknown',
  },
  [CHANNEL_STATUS.ENABLED]: {
    variant: 'success' as const,
    label: 'Enabled',
  },
  [CHANNEL_STATUS.MANUAL_DISABLED]: {
    variant: 'danger' as const,
    label: 'Disabled',
  },
  [CHANNEL_STATUS.AUTO_DISABLED]: {
    variant: 'warning' as const,
    label: 'Auto Disabled',
  },
}

// ============================================================================
// Multi-Key Status
// ============================================================================

export const MULTI_KEY_STATUS = {
  ENABLED: 1,
  MANUAL_DISABLED: 2,
  AUTO_DISABLED: 3,
} as const

export const MULTI_KEY_STATUS_LABELS = {
  [MULTI_KEY_STATUS.ENABLED]: 'Enabled',
  [MULTI_KEY_STATUS.MANUAL_DISABLED]: 'Manual Disabled',
  [MULTI_KEY_STATUS.AUTO_DISABLED]: 'Auto Disabled',
} as const

export const MULTI_KEY_STATUS_CONFIG = {
  [MULTI_KEY_STATUS.ENABLED]: {
    variant: 'success' as const,
    label: 'Enabled',
  },
  [MULTI_KEY_STATUS.MANUAL_DISABLED]: {
    variant: 'neutral' as const,
    label: 'Manual Disabled',
  },
  [MULTI_KEY_STATUS.AUTO_DISABLED]: {
    variant: 'danger' as const,
    label: 'Auto Disabled',
  },
}

// ============================================================================
// Multi-Key Modes
// ============================================================================

export const MULTI_KEY_MODES = [
  { value: 'random', label: 'Random' },
  { value: 'polling', label: 'Polling' },
] as const

export const ADD_MODE_OPTIONS = [
  { value: 'single', label: 'Single Key' },
  { value: 'batch', label: 'Batch Add (one key per line)' },
  {
    value: 'multi_to_single',
    label: 'Multi-Key Mode (multiple keys, one channel)',
  },
] as const

// ============================================================================
// Multi-Key Management
// ============================================================================

export const MULTI_KEY_FILTER_OPTIONS = [
  { value: 'all', label: 'All Status' },
  { value: '1', label: 'Enabled' },
  { value: '2', label: 'Manual Disabled' },
  { value: '3', label: 'Auto Disabled' },
] as const

export const MULTI_KEY_CONFIRM_MESSAGES = {
  DELETE:
    'Are you sure you want to delete this key? This action cannot be undone.',
  ENABLE: 'Enable this key?',
  DISABLE: 'Disable this key?',
  ENABLE_ALL: 'Are you sure you want to enable all keys?',
  DISABLE_ALL: 'Are you sure you want to disable all enabled keys?',
  DELETE_DISABLED:
    'Are you sure you want to delete all auto-disabled keys? This action cannot be undone.',
} as const

// ============================================================================
// Auto Ban Options
// ============================================================================

export const AUTO_BAN_OPTIONS = [
  { value: 1, label: 'Enabled' },
  { value: 0, label: 'Disabled' },
] as const

// ============================================================================
// Error / Success Messages (i18n keys: use t(ERROR_MESSAGES.xxx) when displaying)
// ============================================================================

export const ERROR_MESSAGES = {
  REQUIRED_NAME: 'Channel name is required',
  REQUIRED_TYPE: 'Channel type is required',
  REQUIRED_KEY: 'API key is required',
  REQUIRED_MODELS: 'Models are required',
  REQUIRED_GROUP: 'Group is required',
  INVALID_JSON: 'Invalid JSON format',
  INVALID_MODEL_MAPPING: 'Invalid model mapping format',
  INVALID_PROXY:
    'Proxy address must use HTTP, HTTPS, SOCKS5, or SOCKS5H and include a valid host',
  INVALID_HTTP_PROTOCOL: 'HTTP protocol must be Auto or HTTP/1.1',
  INVALID_HTTP2_CONNECTION_SHARDS:
    'HTTP/2 connection shards must be between 1 and 8',
  INVALID_HTTP1_WITH_SHARDS:
    'HTTP/2 connection shards must be 1 when HTTP/1.1 is selected',
  INVALID_NOVELAI_QUEUE_LIMIT:
    'Queue limit per key must be a whole number from 0 to 100',
  INVALID_UPSTREAM_QUOTA_ERROR_MESSAGE:
    'The error returned to users must be at most 500 characters',
  INVALID_UPSTREAM_QUOTA_ERROR_STATUS_CODE:
    'The status code must be a whole number from 400 to 599',
  INVALID_GEMINI_FREE_KEY_MAX_ATTEMPTS:
    'Keys to try per request must be a whole number from 1 to 10',
  GEMINI_FREE_KEY_MODEL_REQUIRED: 'Enter a model name',
  GEMINI_FREE_KEY_NAME_TOO_LONG:
    'Model names and aliases must be at most 128 characters',
  GEMINI_FREE_KEY_TOO_MANY_ALIASES: 'A model can have at most 20 aliases',
  GEMINI_FREE_KEY_DUPLICATE_NAME:
    'This name is already used by another model or alias',
  INVALID_GEMINI_FREE_KEY_LIMIT: 'Enter a whole number from -1 to 1000000000',
  GEMINI_FREE_KEY_MODEL_LIMITS_EMPTY:
    'Keep at least one model, or restore the defaults',
  GEMINI_FREE_KEY_MODEL_LIMITS_TOO_MANY: 'At most 300 models',
  INVALID_MODERATION_MAX_CHARS:
    'Enter a whole number from 1,000 to 200,000, or leave it empty for 10,000',
  INVALID_MODERATION_TIMEOUT:
    'Enter a whole number from 1 to 60, or leave it empty for 10',
  MODERATION_RULES_TOO_MANY: 'At most 100 rules',
  MODERATION_REGEX_RULES_TOO_MANY: 'At most 30 regular expression rules',
  MODERATION_RULE_PATTERN_REQUIRED: 'Enter a pattern',
  MODERATION_RULE_PATTERN_INVISIBLE:
    'The pattern must contain a visible character',
  MODERATION_RULE_PATTERN_TOO_LONG: 'A pattern must be at most 512 characters',
  MODERATION_RULE_NAME_TOO_LONG: 'A name must be at most 64 characters',
  MODERATION_RULE_DUPLICATE: 'This pattern is already used by another rule',
  CREATE_FAILED: 'Failed to create channel',
  UPDATE_FAILED: 'Failed to update channel',
  DELETE_FAILED: 'Failed to delete channel',
  TEST_FAILED: 'Failed to test channel',
  BALANCE_QUERY_FAILED: 'Failed to query balance',
  FETCH_MODELS_FAILED: 'Failed to fetch models',
} as const

export const SUCCESS_MESSAGES = {
  CREATED: 'Channel created successfully',
  UPDATED: 'Channel updated successfully',
  DELETED: 'Channel deleted successfully',
  ENABLED: 'Channel enabled successfully',
  DISABLED: 'Channel disabled successfully',
  TESTED: 'Channel test completed',
  BALANCE_QUERIED: 'Balance queried successfully',
  MODELS_FETCHED: 'Models fetched successfully',
  COPIED: 'Channel copied successfully',
  TAG_SET: 'Tag set successfully',
  BATCH_DELETED: 'Channels deleted successfully',
} as const

// ============================================================================
// Default Values
// ============================================================================

export const DEFAULT_PAGE_SIZE = 20

export const DEFAULT_CHANNEL_VALUES = {
  name: '',
  type: 0,
  base_url: '',
  key: '',
  models: '',
  group: 'default',
  status: CHANNEL_STATUS.ENABLED,
  priority: 0,
  weight: 0,
  auto_ban: 1,
  remark: '',
} as const

// ============================================================================
// Table Configuration
// ============================================================================

export const CHANNELS_TABLE_PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

// ============================================================================
// Sort Options (label values are i18n keys)
// ============================================================================

export const SORT_OPTIONS = [
  { value: 'priority', label: 'Priority (Default)' },
  { value: 'id', label: 'ID' },
  { value: 'name', label: 'Name' },
  { value: 'balance', label: 'Balance' },
  { value: 'response_time', label: 'Response Time' },
] as const

// ============================================================================
// Balance Display
// ============================================================================

export const BALANCE_THRESHOLDS = {
  LOW: 1,
  MEDIUM: 10,
  HIGH: 100,
} as const

// ============================================================================
// Response Time Thresholds (in ms)
// ============================================================================

export const RESPONSE_TIME_THRESHOLDS = {
  EXCELLENT: 500,
  GOOD: 1000,
  FAIR: 2000,
  POOR: 5000,
} as const

export const RESPONSE_TIME_CONFIG = {
  EXCELLENT: { variant: 'success' as const, label: 'Excellent' },
  GOOD: { variant: 'success' as const, label: 'Good' },
  FAIR: { variant: 'warning' as const, label: 'Fair' },
  POOR: { variant: 'danger' as const, label: 'Poor' },
  UNKNOWN: { variant: 'neutral' as const, label: 'Not tested' },
} as const

// ============================================================================
// Field Hints and Placeholders (i18n keys; use t() when displaying)
// ============================================================================

export const FIELD_PLACEHOLDERS = {
  NAME: 'e.g., OpenAI GPT-4 Production',
  BASE_URL: 'Leave empty to use default',
  KEY: 'API Key (one per line for batch mode)',
  MODELS: 'Comma-separated model names, e.g., gpt-4,gpt-3.5-turbo',
  GROUP: 'Please Select user groups that can access this channel.',
  MODEL_MAPPING: '{"request_model": "actual_model"}',
  TEST_MODEL: 'Model to use for testing',
  TAG: 'Optional tag for grouping channels',
  REMARK: 'Optional notes about this channel',
  PARAM_OVERRIDE: '{"temperature": 0.7}',
  HEADER_OVERRIDE: '{"X-Custom-Header": "value"}',
  STATUS_CODE_MAPPING: '{"400": "500"}',
} as const

export const FIELD_DESCRIPTIONS = {
  NAME: 'Friendly name to identify this channel',
  TYPE: 'Provider type (OpenAI, Anthropic, etc.)',
  BASE_URL: 'Custom API base URL. Leave empty to use provider default.',
  KEY: 'API key from the provider',
  MODELS:
    'List of models supported by this channel. Use comma to separate multiple models.',
  GROUP: 'User groups that can access this channel. ',
  MODEL_MAPPING:
    'For this channel, map the model name in client requests to the model name sent upstream.',
  PRIORITY: 'Higher priority channels are selected first',
  WEIGHT: 'Used for load balancing. Higher weight = more requests',
  TEST_MODEL: 'Model to use when testing channel connectivity',
  AUTO_BAN: 'Automatically disable channel on repeated failures',
  STATUS_CODE_MAPPING: 'Map response status codes (JSON format)',
  TAG: 'Group channels by tag for batch operations',
  REMARK: 'Internal notes (not shown to users)',
  SETTING: 'Channel-specific settings (JSON format)',
  PARAM_OVERRIDE: 'Override request parameters (JSON format)',
  HEADER_OVERRIDE: 'Override request headers (JSON format)',
  MULTI_KEY_MODE: 'How to select keys: random or sequential polling',
  BATCH_ADD: 'Create multiple channels from multiple keys',
  OPENAI_ORG: 'OpenAI Organization ID (optional)',
} as const

// ============================================================================
// Channel Type Specific Configurations
// ============================================================================

export const MODEL_FETCHABLE_TYPES = new Set([
  1,
  4,
  14,
  17,
  20,
  23,
  24,
  25,
  26,
  27,
  31,
  34,
  35,
  40,
  42,
  43,
  47,
  48,
  57,
  58,
  59,
  60,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
  // The server answers with NovelAI's built-in models, without a request.
  CHANNEL_TYPE_NOVELAI,
])

export const FIELD_PASSTHROUGH_TYPES = new Set([
  1,
  14,
  57,
  58,
  59,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
])

export const OPENAI_FIELD_PASSTHROUGH_TYPES = new Set([
  1,
  57,
  58,
  59,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
])

export const CLAUDE_FIELD_PASSTHROUGH_TYPES = new Set([
  14,
  58,
  59,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
])

export const TYPE_TO_KEY_PROMPT: Record<number, string> = {
  15: 'Format: APIKey|SecretKey',
  18: 'Format: APPID|APISecret|APIKey',
  22: 'Format: APIKey-AppId, e.g., fastgpt-0sp2gtvfdgyi4k30jwlgwf1i-64f335d84283f05518e9e041',
  23: 'Format: TokenHub API Key, or legacy AppId|SecretId|SecretKey',
  33: 'Format: Ak|Sk|Region',
  50: 'Format: AccessKey|SecretKey (or just ApiKey if upstream is New API)',
  51: 'Format: Access Key ID|Secret Access Key',
  57: 'Paste Codex OAuth JSON credential (access_token / refresh_token / account_id)',
  59: 'Enter API key for this channel',
  60: 'Enter API key for this channel',
  62: 'vLLM API key, or EMPTY if authentication is disabled',
  63: 'SGLang API key, or EMPTY if authentication is disabled',
  [CHANNEL_TYPE_NOVELAI]: 'NovelAI persistent API token (pst-...)',
}

export const CHANNEL_TYPE_WARNINGS: Record<number, string> = {
  3: 'For channels added after May 10, 2025, no need to remove "." from model names during deployment',
  8: 'If connecting to upstream One API or New API relay projects, use OpenAI type instead unless you know what you are doing',
  37: 'Dify channels only support chatflow and agent, and agent does not support images',
}
