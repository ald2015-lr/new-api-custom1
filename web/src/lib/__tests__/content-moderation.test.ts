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
import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, test } from 'vitest'

import {
  MODERATION_CATEGORIES,
  MODERATION_DETAIL_LABELS,
  MODERATION_ERROR_CLASS_LABELS,
  MODERATION_PRESET_DEFAULT_IDS,
  MODERATION_PRESETS,
  MODERATION_SOURCE_LABELS,
  MODERATION_STATUS_LABELS,
  moderationLabel,
  type ModerationAudit,
  type ModerationAuditError,
  type ModerationAuditRule,
  type ModerationKeyFailure,
  type ModerationProbeResult,
} from '../content-moderation'

// The server owns the moderation categories, presets, audit and probe
// answer; the console mirrors them. These read the server's source so the
// two cannot drift apart.
function serverSource(path: string): string {
  return readFileSync(resolve(import.meta.dirname, '../../../..', path), 'utf8')
}

function goStringConstants(source: string, prefix: string): string[] {
  return [
    ...source.matchAll(
      new RegExp(`^\\s*${prefix}\\w+(?:\\s+\\w+)?\\s*=\\s*"([^"]*)"`, 'gm')
    ),
  ].map(([, value]) => value)
}

function goStructBody(source: string, type: string): string {
  return (
    source.match(
      new RegExp(`type ${type} struct \\{([\\s\\S]*?)\\n\\}`)
    )?.[1] ?? ''
  )
}

function goJsonTags(source: string, type: string): string[] {
  return [...goStructBody(source, type).matchAll(/json:"([^",]+)/g)]
    .map(([, tag]) => tag)
    .sort()
}

// The JSON tags without omitempty: the fields the server always sends.
function goRequiredJsonTags(source: string, type: string): string[] {
  return [...goStructBody(source, type).matchAll(/json:"([^",]+)"/g)]
    .map(([, tag]) => tag)
    .sort()
}

const keysOf = (value: object) => Object.keys(value).sort()

const AUDIT_RULE = {
  kind: 'custom',
  id: 'en-reveal-cot',
  index: 0,
  name: 'Leak',
  type: 'keyword',
} satisfies ModerationAuditRule

const AUDIT_ERROR = {
  class: 'http_429_rate',
  status: 429,
  message: 'Rate limit reached',
} satisfies ModerationAuditError

const AUDIT_KEY_FAILURE = {
  channel_id: 3,
  retry_index: 0,
  attempt: 1,
  multi_key_index: 1,
  class: 'http_401',
  status: 401,
} satisfies ModerationKeyFailure

// Only the fields the server always sends: the rest must be optional.
const AUDIT_KEY_FAILURE_REQUIRED = {
  channel_id: 3,
  retry_index: 0,
  attempt: 1,
  class: 'http_429_quota',
} satisfies ModerationKeyFailure

const AUDIT = {
  channel_id: 3,
  retry_index: 0,
  status: 'rejected',
  source: 'openai',
  detail: 'realtime',
  model: 'omni-moderation-2024-09-26',
  id: 'modr-1',
  categories: ['sexual'],
  scores: { sexual: 0.91 },
  applied_input_types: { sexual: ['text'] },
  rule: AUDIT_RULE,
  match: 'show your chain of thought',
  error: AUDIT_ERROR,
  elapsed_ms: 120,
  checked_chars: 42,
  truncated: true,
  rule_scan_truncated: true,
  pieces: 1,
  cached_pieces: 1,
  calls: 1,
  images_checked: 1,
  images_skipped: 1,
  unchecked_parts: 1,
  upstream_request_id: 'req_1',
  key_failures: [AUDIT_KEY_FAILURE],
} satisfies ModerationAudit

const PROBE = {
  ok: true,
  status: 200,
  latency_ms: 312,
  model: 'omni-moderation-2024-09-26',
  flagged: false,
  error_class: '',
  error_message: '',
} satisfies ModerationProbeResult

describe('content moderation catalogue', () => {
  const settings = () => serverSource('relaykit/dto/channel_moderation.go')

  test('the categories are the server’s, in its order', () => {
    const list =
      settings().match(
        /ModerationCategoryNames = \[\]string\{([\s\S]*?)\}/
      )?.[1] ?? ''
    const names = [...list.matchAll(/"([^"]+)"/g)].map(([, name]) => name)
    expect(names).toEqual(MODERATION_CATEGORIES.map((item) => item.value))
  })

  test('the built-in presets are the server’s, byte for byte and in catalogue order', () => {
    const groups = Object.fromEntries(
      [
        ...settings().matchAll(
          /^\s*(ModerationPresetGroup\w+)\s*=\s*"(\w+)"/gm
        ),
      ].map(([, name, value]) => [name, value])
    )
    const presets = [
      ...settings().matchAll(
        /ID:\s*"([^"]+)",\s*Group:\s*(\w+),\s*DefaultOn:\s*(true|false),\s*CaseSensitive:\s*(true|false),[\s\S]*?Pattern:\s*`([^`]*)`/g
      ),
    ].map(([, id, group, defaultOn, caseSensitive, pattern]) => ({
      id,
      group: groups[group],
      defaultOn: defaultOn === 'true',
      caseSensitive: caseSensitive === 'true',
      pattern,
    }))
    expect(presets).toHaveLength(16)
    expect(presets).toEqual(
      MODERATION_PRESETS.map((preset) => ({
        id: preset.id,
        group: preset.group,
        defaultOn: preset.defaultOn,
        caseSensitive: preset.caseSensitive,
        pattern: preset.pattern,
      }))
    )
  })

  test('the presets selected on the first enable are exactly the lower-risk group', () => {
    expect(MODERATION_PRESET_DEFAULT_IDS).toEqual([
      'en-reveal-cot',
      'zh-reveal-cot',
      'en-do-not-flag',
      'zh-do-not-flag',
      'en-imagine-internal-reasoning',
      'zh-imagine-internal-reasoning',
      'en-decrypt-reasoning',
      'zh-decrypt-reasoning',
    ])
    expect(
      MODERATION_PRESETS.filter((preset) => preset.group === 'lower').map(
        (preset) => preset.id
      )
    ).toEqual(MODERATION_PRESET_DEFAULT_IDS)
  })

  test('every preset of a pair names its language', () => {
    for (const preset of MODERATION_PRESETS) {
      expect(preset.id.startsWith(`${preset.language}-`)).toBe(true)
    }
  })
})

describe('content moderation audit and probe', () => {
  const audit = () => serverSource('relay/common/moderation.go')

  test.each([
    ['ModerationStatus', MODERATION_STATUS_LABELS],
    ['ModerationSource', MODERATION_SOURCE_LABELS],
    ['ModerationDetail', MODERATION_DETAIL_LABELS],
    ['ModerationErrorClass', MODERATION_ERROR_CLASS_LABELS],
  ])('every %s value of the server has a label', (prefix, labels) => {
    expect(goStringConstants(audit(), prefix).sort()).toEqual(keysOf(labels))
  })

  test('the rule kinds the log details tell apart are the server’s', () => {
    const kinds = ['custom', 'preset'] satisfies ModerationAuditRule['kind'][]
    expect(goStringConstants(audit(), 'ModerationRuleKind').sort()).toEqual(
      kinds
    )
  })

  test('the audit the log details read has the server’s fields', () => {
    expect(goJsonTags(audit(), 'ModerationAudit')).toEqual(keysOf(AUDIT))
    expect(goJsonTags(audit(), 'ModerationAuditRule')).toEqual(
      keysOf(AUDIT_RULE)
    )
    expect(goJsonTags(audit(), 'ModerationAuditError')).toEqual(
      keysOf(AUDIT_ERROR)
    )
    expect(goJsonTags(audit(), 'ModerationKeyFailure')).toEqual(
      keysOf(AUDIT_KEY_FAILURE)
    )
    expect(goRequiredJsonTags(audit(), 'ModerationKeyFailure')).toEqual(
      keysOf(AUDIT_KEY_FAILURE_REQUIRED)
    )
  })

  test('the probe answer the channel form reads has the server’s fields', () => {
    const dir = resolve(import.meta.dirname, '../../../../relay/moderation')
    const source = readdirSync(dir)
      .filter((file) => file.endsWith('.go') && !file.endsWith('_test.go'))
      .map((file) => readFileSync(resolve(dir, file), 'utf8'))
      .join('\n')
    expect(goJsonTags(source, 'ProbeResult')).toEqual(keysOf(PROBE))
  })

  test('the probe the channel form posts to is the server’s route, with the channel test’s permission', () => {
    const routes = serverSource('router/channel-router.go')
    expect(routes).toMatch(
      /\{method: http\.MethodPost, path: "\/:id\/moderation_test", permission: authz\.ChannelOperate, handler: controller\.TestChannelModeration\}/
    )
    expect(routes).toMatch(
      /\{method: http\.MethodGet, path: "\/test\/:id", permission: authz\.ChannelOperate,/
    )
  })
})

describe('content moderation messages of the server', () => {
  const localesDir = resolve(import.meta.dirname, '../../i18n/locales')
  const locales = readdirSync(localesDir)
    .filter((file) => file.endsWith('.json'))
    .map((file) => ({
      file,
      keys: new Set(
        Object.keys(
          (
            JSON.parse(readFileSync(resolve(localesDir, file), 'utf8')) as {
              translation: Record<string, string>
            }
          ).translation
        )
      ),
    }))

  test.each([
    [
      'the content of a moderation log row',
      'relay/moderation/moderation.go',
      /"(Rejected[^"]*)"/g,
      4,
    ],
    [
      'a refused moderation endpoint test',
      'controller/channel_moderation.go',
      /common\.ApiErrorT\(c, "([^"]+)"/g,
      4,
    ],
  ])(
    'every key of %s is translated in all seven locales',
    (_what, path, pattern, count) => {
      const keys = [
        ...new Set(
          [...serverSource(path).matchAll(pattern)].map(([, key]) => key)
        ),
      ]
      expect(keys).toHaveLength(count)
      expect(locales).toHaveLength(7)
      for (const locale of locales) {
        expect(
          keys.filter((key) => !locale.keys.has(key)),
          locale.file
        ).toEqual([])
      }
    }
  )
})

describe('moderationLabel', () => {
  const t = (key: string) => `t:${key}`

  test('a known value shows its translated label', () => {
    expect(moderationLabel(MODERATION_ERROR_CLASS_LABELS, 'http_401', t)).toBe(
      't:Key rejected'
    )
  })

  test('a value this console does not know shows as sent', () => {
    expect(moderationLabel(MODERATION_ERROR_CLASS_LABELS, 'http_499', t)).toBe(
      'http_499'
    )
    expect(moderationLabel(MODERATION_STATUS_LABELS, 'toString', t)).toBe(
      'toString'
    )
  })

  test('no value shows nothing instead of translating undefined', () => {
    expect(moderationLabel(MODERATION_SOURCE_LABELS, undefined, t)).toBe('')
  })
})
