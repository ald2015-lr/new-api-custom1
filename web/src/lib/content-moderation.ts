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

// Content moderation of OpenAI channels (settings.moderation_*). The server
// owns every value below; these mirror relaykit/dto/channel_moderation.go
// and relay/common/moderation.go, and a test reads those files so the two
// cannot drift apart. Every label is an i18n key; render it with t().

/** The omni-moderation categories, in OpenAI's order. */
export const MODERATION_CATEGORIES = [
  { value: 'harassment', labelKey: 'Harassment' },
  { value: 'harassment/threatening', labelKey: 'Harassment / threatening' },
  { value: 'hate', labelKey: 'Hate' },
  { value: 'hate/threatening', labelKey: 'Hate / threatening' },
  { value: 'illicit', labelKey: 'Illicit' },
  { value: 'illicit/violent', labelKey: 'Illicit / violent' },
  { value: 'self-harm', labelKey: 'Self-harm' },
  { value: 'self-harm/intent', labelKey: 'Self-harm / intent' },
  { value: 'self-harm/instructions', labelKey: 'Self-harm / instructions' },
  { value: 'sexual', labelKey: 'Sexual' },
  { value: 'sexual/minors', labelKey: 'Sexual / minors' },
  { value: 'violence', labelKey: 'Violence' },
  { value: 'violence/graphic', labelKey: 'Violence / graphic' },
] as const

export type ModerationPresetGroup = 'lower' | 'higher'

export type ModerationPreset = {
  id: string
  /** Lower or higher false-positive risk; the lower group is on by default. */
  group: ModerationPresetGroup
  defaultOn: boolean
  /** False: the server matches the pattern ignoring case. */
  caseSensitive: boolean
  language: 'en' | 'zh'
  labelKey: string
  /** The RE2 pattern the server runs, shown read-only. */
  pattern: string
}

/**
 * The built-in anti-distillation rules, in the server's catalogue order.
 * Patterns are byte-for-byte the server's (Go raw strings, hence String.raw).
 */
export const MODERATION_PRESETS: readonly ModerationPreset[] = [
  {
    id: 'en-reveal-cot',
    group: 'lower',
    defaultOn: true,
    caseSensitive: false,
    language: 'en',
    labelKey: "Asks for the model's chain of thought",
    pattern: String.raw`\b(?:show|reveal|print|output|dump|display|expose|leak|give|write\s+out|tell|share|repeat|transcribe|return|provide|include)\b.{0,30}?\b(?:your|its|the\s+(?:model|assistant)'?s)\s+(?:[\w,'-]+\s+){0,3}?(?:chain[\s-]*of[\s-]*thoughts?|cot|(?:hidden|internal|raw|private|secret|unfiltered|unsummari[sz]ed|concealed|verbatim)\s+(?:reasoning|thinking|thought\s+process|scratch\s*pad))\b`,
  },
  {
    id: 'zh-reveal-cot',
    group: 'lower',
    defaultOn: true,
    caseSensitive: true,
    language: 'zh',
    labelKey: "Asks for the model's chain of thought",
    pattern: String.raw`(?:你|您)(?:自己)?(?:的[^。！？!?\n]{0,6}?)?(?:原始|隐藏|隱藏|内部|內部|内在|內在|未经?(?:总结|總結|删减|刪減|过滤|過濾|修饰|修飾)|逐字|完整|全部)的?[^。！？!?\n]{0,4}?(?:思[维維][链鏈]|思考[链鏈]|推理[链鏈]|思考过程|思考過程|思[维維]过程|思[维維]過程|推理过程|推理過程|思考内容|思考內容|推理内容|推理內容|推理)|(?:你|您)(?:自己)?的(?:思[维維][链鏈]|思考[链鏈]|推理[链鏈])`,
  },
  {
    id: 'en-do-not-flag',
    group: 'lower',
    defaultOn: true,
    caseSensitive: false,
    language: 'en',
    labelKey: 'Asks not to flag reasoning extraction',
    pattern: String.raw`\b(?:do\s+not|don'?t|never)\s+(?:flag|report|classify|detect|treat|mark|label|consider)\b.{0,40}?\bas\b.{0,20}?\b(?:(?:reasoning|cot|chain[\s-]*of[\s-]*thought)[\s-]+extraction|distill(?:ation|ing)?|extraction\s+attempts?)\b`,
  },
  {
    id: 'zh-do-not-flag',
    group: 'lower',
    defaultOn: true,
    caseSensitive: true,
    language: 'zh',
    labelKey: 'Asks not to flag reasoning extraction',
    pattern: String.raw`(?:不要|别|別|请勿|請勿|无需|無需|切勿)[^。！？!?\n]{0,8}(?:标记|標記|识别|識別|判定|视为|視為|归类|歸類|认定|認定)[^。！？!?\n]{0,6}(?:推理提取|思[维維][链鏈]提取|思考[链鏈]提取|蒸[馏餾](?:攻击|攻擊|行为|行為|请求|請求|尝试|嘗試)?)`,
  },
  {
    id: 'en-imagine-internal-reasoning',
    group: 'lower',
    defaultOn: true,
    caseSensitive: false,
    language: 'en',
    labelKey: 'Asks to reconstruct the reasoning behind an answer',
    pattern: String.raw`\b(?:imagine|reconstruct|infer|articulate|recreate|reverse[\s-]*engineer)\b.{0,60}?\b(?:internal|hidden|underlying|inner)\s+(?:reasoning|thought\s+process|thinking|thoughts|chain[\s-]*of[\s-]*thought)\b.{0,80}?\b(?:behind|that\s+led\s+to|leading\s+to|which\s+produced|for)\b.{0,30}?\b(?:response|answer|output|reply|completion|solution)s?\b`,
  },
  {
    id: 'zh-imagine-internal-reasoning',
    group: 'lower',
    defaultOn: true,
    caseSensitive: true,
    language: 'zh',
    labelKey: 'Asks to reconstruct the reasoning behind an answer',
    pattern: String.raw`(?:想象|想像|推测|推測|重建|还原|還原|补全|補全|反推)[^。！？!?\n]{0,20}(?:回答|答案|回复|回覆|输出|輸出|响应|響應)[^。！？!?\n]{0,6}(?:背后|背後)[^。！？!?\n]{0,6}(?:推理|思考|思[维維])`,
  },
  {
    id: 'en-decrypt-reasoning',
    group: 'lower',
    defaultOn: true,
    caseSensitive: false,
    language: 'en',
    labelKey: 'Asks to decrypt encrypted reasoning',
    pattern: String.raw`\b(?:decrypt|decipher|deobfuscate|crack|unscramble)\w*\b.{0,60}?\b(?:encrypted[\s_]*content|encrypted\s+(?:reasoning|thinking|thoughts?|cot|chain[\s-]*of[\s-]*thought)|reasoning\s+(?:item|block|blob)s?|redacted[\s_]*thinking|thinking\s+signatures?)\b|\b(?:transcribe|decode|reveal|recover)\w*\b.{0,40}?\b(?:encrypted[\s_]*content|encrypted\s+(?:reasoning|thinking))\b`,
  },
  {
    id: 'zh-decrypt-reasoning',
    group: 'lower',
    defaultOn: true,
    caseSensitive: false,
    language: 'zh',
    labelKey: 'Asks to decrypt encrypted reasoning',
    pattern: String.raw`(?:解密|破解)[^。！？!?\n]{0,20}(?:加密的?(?:推理|思考|思[维維])|encrypted_content|推理(?:块|塊))|(?:转录|轉錄|还原|還原)[^。！？!?\n]{0,20}(?:加密的?(?:推理|思考|思[维維])|encrypted_content)`,
  },
  {
    id: 'en-reasoning-trace',
    group: 'higher',
    defaultOn: false,
    caseSensitive: false,
    language: 'en',
    labelKey: "Mentions the model's reasoning trace",
    pattern: String.raw`\b(?:your|its|the\s+model'?s)\s+(?:[\w-]+\s+){0,2}?reasoning[\s-]*traces?\b`,
  },
  {
    id: 'zh-reasoning-trace',
    group: 'higher',
    defaultOn: false,
    caseSensitive: true,
    language: 'zh',
    labelKey: "Mentions the model's reasoning trace",
    pattern: String.raw`(?:你|您)(?:自己)?的[^。！？!?\n]{0,4}?(?:推理|思[维維考])(?:轨迹|軌跡|痕迹|痕跡)`,
  },
  {
    id: 'en-thinking-language-consistent',
    group: 'higher',
    defaultOn: false,
    caseSensitive: false,
    language: 'en',
    labelKey: "Requires the thinking language to match the user's",
    pattern: String.raw`\blanguage\s+(?:used\s+)?in\s+(?:the\s+|your\s+)?(?:thinking|reasoning|thoughts?)(?:\s+content|\s+process)?\b.{0,40}?\b(?:must|should|has\s+to|needs\s+to)\b.{0,40}?\b(?:consistent|same|match)`,
  },
  {
    id: 'zh-thinking-language-consistent',
    group: 'higher',
    defaultOn: false,
    caseSensitive: true,
    language: 'zh',
    labelKey: "Requires the thinking language to match the user's",
    pattern: String.raw`思考(?:内容|內容|过程|過程)?[^。！？!?\n]{0,6}(?:语言|語言)[^。！？!?\n]{0,12}(?:必须|必須)[^。！？!?\n]{0,8}(?:与|與|和)[^。！？!?\n]{0,16}(?:严格|嚴格)?(?:一致|相同)`,
  },
  {
    id: 'en-r1-boxed-template',
    group: 'higher',
    defaultOn: false,
    caseSensitive: false,
    language: 'en',
    labelKey: 'R1-style step-by-step template with a boxed answer',
    pattern: String.raw`reason\s+step\s+by\s+step,?\s+and\s+put\s+your\s+final\s+answer\s+within\s+\\+boxed`,
  },
  {
    id: 'zh-r1-boxed-template',
    group: 'higher',
    defaultOn: false,
    caseSensitive: true,
    language: 'zh',
    labelKey: 'R1-style step-by-step template with a boxed answer',
    pattern: String.raw`请?(?:一步步|逐步|一步一步)地?推理[^。\n]{0,20}(?:最终答案|最終答案)[^。\n]{0,12}\\+boxed`,
  },
  {
    id: 'en-think-tags',
    group: 'higher',
    defaultOn: false,
    caseSensitive: false,
    language: 'en',
    labelKey: 'Asks to put the reasoning in think tags',
    pattern: String.raw`\b(?:put|place|wrap|enclose|write|output|include)\b.{0,50}?\b(?:reasoning|thinking|thoughts|thought\s+process|chain[\s-]*of[\s-]*thought)\b.{0,50}?<\s*/?\s*(?:think|thinking|thought|reasoning|scratchpad|analysis)\s*>`,
  },
  {
    id: 'en-training-data-for-model',
    group: 'higher',
    defaultOn: false,
    caseSensitive: false,
    language: 'en',
    labelKey: 'Asks for training data for another model',
    pattern: String.raw`\b(?:training\s+data|dataset|sft\s+data|fine[\s-]*tun\w*\s+data|distillation\s+data)\b.{0,40}?\b(?:to\s+train|to\s+fine[\s-]*tune|to\s+distill|for\s+training)\b.{0,30}?\b(?:our|my|a|another|the\s+student|student)\s+(?:own\s+)?(?:model|llm)`,
  },
]

/**
 * The presets selected the first time moderation is turned on for a channel
 * whose preset list was never saved: the lower false-positive group.
 */
export const MODERATION_PRESET_DEFAULT_IDS: readonly string[] =
  MODERATION_PRESETS.filter((preset) => preset.defaultOn).map(
    (preset) => preset.id
  )

/** Why a moderation call failed (probe results and the log details). */
export const MODERATION_ERROR_CLASS_LABELS: Record<string, string> = {
  timeout: 'Timed out',
  network: 'Network error',
  http_401: 'Key rejected',
  http_403: 'Access denied',
  http_404: 'No moderation endpoint at this base URL',
  http_429_rate: 'Rate limited',
  http_429_size: "Request larger than the organization's moderation limit",
  http_429_quota: 'No quota on the account',
  image_rejected: 'Image could not be checked',
  config: 'Invalid header override',
  http_5xx: 'Moderation service error',
  http_other: 'Unexpected HTTP status',
  decode: 'Unexpected answer',
}

/** What rejected a request. */
export const MODERATION_SOURCE_LABELS: Record<string, string> = {
  openai: 'OpenAI moderation',
  rule: 'Keyword or regex rule',
  unavailable: 'Moderation unavailable',
  unsupported: 'Not supported',
}

/** The outcome recorded on a log row. */
export const MODERATION_STATUS_LABELS: Record<string, string> = {
  passed: 'Passed',
  rejected: 'Rejected',
  fail_open: 'Failed, request let through',
}

/** Why a request could not be moderated (unsupported rejections only). */
export const MODERATION_DETAIL_LABELS: Record<string, string> = {
  realtime: 'Realtime',
  novelai: 'NovelAI-native request',
  request_type: 'Request type',
  token_prompt: 'Token-ID prompt',
  ambiguous_keys: 'Ambiguous JSON keys',
}

/**
 * The localized label of a value the server sends, or the value itself when
 * this version of the console has no label for it (a newer server).
 */
export function moderationLabel(
  labels: Record<string, string>,
  value: string | undefined,
  t: (key: string) => string
): string {
  if (!value) return ''
  const key = Object.hasOwn(labels, value) ? labels[value] : undefined
  return key ? t(key) : value
}

/** The rule of a rule hit (admin_info.moderation.rule). */
export interface ModerationAuditRule {
  kind: 'preset' | 'custom' | string
  /** Preset id. */
  id?: string
  /** Custom rule index, 0-based. */
  index?: number
  name?: string
  type: 'keyword' | 'regex' | string
}

/** The failure of a fail-open pass or an unavailable rejection. */
export interface ModerationAuditError {
  class: string
  status?: number
  /** Masked by the server, at most 200 characters. */
  message?: string
}

/**
 * An attempt whose moderation call said the key or its OpenAI account is
 * unusable (http_401, http_403, http_429_quota) on a channel that rejects on
 * failure: the server handled it as that channel's error (auto-disable,
 * retry on another channel).
 */
export interface ModerationKeyFailure {
  channel_id: number
  /**
   * 0 for the first attempt; starts again at 0 when auto cross-group retry
   * moves to the next group.
   */
  retry_index: number
  /**
   * The attempt's 1-based place in the request's retry chain, which never
   * repeats; missing on rows written before the server sent it.
   */
  attempt?: number
  /** The key's 0-based index; multi-key channels only. */
  multi_key_index?: number
  class: string
  status?: number
}

/** What moderation did for one attempt (admin_info.moderation of a log row). */
export interface ModerationAudit {
  channel_id: number
  retry_index: number
  status: 'passed' | 'fail_open' | 'rejected' | string
  source?: string
  detail?: string
  model?: string
  id?: string
  categories?: string[]
  scores?: Record<string, number>
  applied_input_types?: Record<string, string[]>
  rule?: ModerationAuditRule
  /** The matched text of a rule hit, at most 64 characters. */
  match?: string
  error?: ModerationAuditError
  elapsed_ms: number
  checked_chars: number
  truncated?: boolean
  rule_scan_truncated?: boolean
  pieces?: number
  cached_pieces?: number
  calls: number
  images_checked?: number
  images_skipped?: number
  unchecked_parts?: number
  upstream_request_id?: string
  /** The request's attempts that failed this way; on its rejection row only. */
  key_failures?: ModerationKeyFailure[]
}

/** The answer of POST /api/channel/:id/moderation_test. */
export interface ModerationProbeResult {
  ok: boolean
  /** HTTP status of the moderation answer; 0 when none arrived. */
  status: number
  latency_ms: number
  model: string
  flagged: boolean
  error_class: string
  error_message: string
}
