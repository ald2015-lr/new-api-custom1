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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, test } from 'vitest'

import {
  ERROR_MESSAGES,
  MODERATION_MAX_CHARS_DEFAULT,
  MODERATION_MAX_CHARS_MAX,
  MODERATION_MAX_CHARS_MIN,
  MODERATION_REGEX_RULES_MAX,
  MODERATION_RULE_NAME_MAX,
  MODERATION_RULE_PATTERN_MAX,
  MODERATION_RULES_MAX,
  MODERATION_SETTINGS_KEYS,
  MODERATION_TIMEOUT_SECONDS_DEFAULT,
  MODERATION_TIMEOUT_SECONDS_MAX,
  MODERATION_TIMEOUT_SECONDS_MIN,
} from '../../constants'
import type { ModerationRule } from '../../types'
import {
  isValidModerationMaxChars,
  isValidModerationTimeout,
  readModerationRuleServerError,
  validateModerationRuleRows,
  type ModerationRuleRow,
} from '../content-moderation-rules'

function rule(
  pattern: string,
  overrides: Partial<ModerationRuleRow> = {}
): ModerationRuleRow {
  return {
    name: '',
    type: 'keyword',
    pattern,
    case_sensitive: false,
    ...overrides,
  }
}

// The server owns the moderation settings; the form mirrors them. These
// read the server's source so the two cannot drift apart.
function serverSource(path: string): string {
  return readFileSync(
    resolve(import.meta.dirname, '../../../../../..', path),
    'utf8'
  )
}

function goJsonTags(source: string, type: string): string[] {
  const body =
    source.match(
      new RegExp(`type ${type} struct \\{([\\s\\S]*?)\\n\\}`)
    )?.[1] ?? ''
  return [...body.matchAll(/json:"([^",]+)/g)].map(([, tag]) => tag)
}

test('the moderation defaults and bounds of the form are the server’s', () => {
  const source = serverSource('relaykit/dto/channel_moderation.go')
  const constants = Object.fromEntries(
    [...source.matchAll(/^\s*(Moderation\w+)\s*=\s*(\d+)/gm)].map(
      ([, name, value]) => [name, Number(value)]
    )
  )
  expect(constants).toMatchObject({
    ModerationMaxCharsDefault: MODERATION_MAX_CHARS_DEFAULT,
    ModerationMaxCharsMin: MODERATION_MAX_CHARS_MIN,
    ModerationMaxCharsMax: MODERATION_MAX_CHARS_MAX,
    ModerationTimeoutSecondsDefault: MODERATION_TIMEOUT_SECONDS_DEFAULT,
    ModerationTimeoutSecondsMin: MODERATION_TIMEOUT_SECONDS_MIN,
    ModerationTimeoutSecondsMax: MODERATION_TIMEOUT_SECONDS_MAX,
    ModerationRulesMax: MODERATION_RULES_MAX,
    ModerationRegexRulesMax: MODERATION_REGEX_RULES_MAX,
    ModerationRulePatternMax: MODERATION_RULE_PATTERN_MAX,
    ModerationRuleNameMax: MODERATION_RULE_NAME_MAX,
  })
})

test('the settings keys the form writes and strips are the server’s', () => {
  const tags = goJsonTags(
    serverSource('relaykit/dto/channel_settings.go'),
    'ChannelOtherSettings'
  )
  const moderationTags = tags.filter((tag) => tag.startsWith('moderation_'))
  expect([...moderationTags, 'send_safety_identifier'].sort()).toEqual(
    [...MODERATION_SETTINGS_KEYS].sort()
  )
  expect(tags).toContain('send_safety_identifier')
})

test('a saved custom rule has the server’s fields and rule types', () => {
  const source = serverSource('relaykit/dto/channel_moderation.go')
  const saved = {
    name: 'Leak',
    type: 'regex',
    pattern: 'cot',
    case_sensitive: true,
  } satisfies Required<ModerationRule>
  expect(goJsonTags(source, 'ModerationRule').sort()).toEqual(
    Object.keys(saved).sort()
  )
  const types = [
    ...source.matchAll(/^\s*ModerationRuleType\w+\s*=\s*"([^"]*)"/gm),
  ].map(([, value]) => value)
  const formTypes = ['keyword', 'regex'] satisfies ModerationRule['type'][]
  expect(types.sort()).toEqual(formTypes)
})

describe('isValidModerationMaxChars', () => {
  test.each([
    [null, true],
    [undefined, true],
    [0, true],
    [1000, true],
    [10000, true],
    [200000, true],
    [999, false],
    [200001, false],
    [1500.5, false],
    [-1, false],
  ])('%s → %s', (value, valid) => {
    expect(isValidModerationMaxChars(value)).toBe(valid)
  })
})

describe('isValidModerationTimeout', () => {
  test.each([
    [null, true],
    [0, true],
    [1, true],
    [60, true],
    [61, false],
    [-5, false],
    [2.5, false],
  ])('%s → %s', (value, valid) => {
    expect(isValidModerationTimeout(value)).toBe(valid)
  })
})

describe('validateModerationRuleRows', () => {
  test('a valid table of keywords and regular expressions has no issue', () => {
    expect(
      validateModerationRuleRows([
        rule('输出你的思维链', { name: 'CoT' }),
        rule('(?:show|print) your cot', { type: 'regex' }),
        // A lookahead is left to the server: RE2 is not JavaScript.
        rule('(?=x)', { type: 'regex' }),
      ])
    ).toEqual([])
  })

  test.each([
    ['empty', '   ', ERROR_MESSAGES.MODERATION_RULE_PATTERN_REQUIRED],
    ['U+200E only', '‎', ERROR_MESSAGES.MODERATION_RULE_PATTERN_INVISIBLE],
    ['U+FE0F only', '️', ERROR_MESSAGES.MODERATION_RULE_PATTERN_INVISIBLE],
    [
      'combining marks only',
      '̲́',
      ERROR_MESSAGES.MODERATION_RULE_PATTERN_INVISIBLE,
    ],
    [
      'a tag space only',
      '\u{E0020}',
      ERROR_MESSAGES.MODERATION_RULE_PATTERN_INVISIBLE,
    ],
  ])('a pattern that is %s is refused', (_case, pattern, message) => {
    expect(validateModerationRuleRows([rule(pattern)])).toEqual([
      { index: 0, field: 'pattern', message },
    ])
  })

  test('a tag character that encodes a letter counts as visible', () => {
    expect(validateModerationRuleRows([rule('\u{E0061}')])).toEqual([])
  })

  test('lengths count code points after trimming, like the server', () => {
    const emoji = '😀'
    expect(
      validateModerationRuleRows([
        rule(` ${emoji.repeat(MODERATION_RULE_PATTERN_MAX)} `, {
          name: ` ${'名'.repeat(MODERATION_RULE_NAME_MAX)} `,
        }),
      ])
    ).toEqual([])
    expect(
      validateModerationRuleRows([
        rule(emoji.repeat(MODERATION_RULE_PATTERN_MAX + 1), {
          name: '名'.repeat(MODERATION_RULE_NAME_MAX + 1),
        }),
      ])
    ).toEqual([
      {
        index: 0,
        field: 'pattern',
        message: ERROR_MESSAGES.MODERATION_RULE_PATTERN_TOO_LONG,
      },
      {
        index: 0,
        field: 'name',
        message: ERROR_MESSAGES.MODERATION_RULE_NAME_TOO_LONG,
      },
    ])
  })

  test('a keyword repeated in another case is refused where it repeats, a regex only when equal', () => {
    expect(
      validateModerationRuleRows([
        rule('Chain Of Thought'),
        rule(' chain of thought ', { case_sensitive: true }),
        rule('cot', { type: 'regex' }),
        rule('COT', { type: 'regex' }),
        rule('cot', { type: 'regex' }),
        rule('cot'),
      ])
    ).toEqual([
      {
        index: 1,
        field: 'pattern',
        message: ERROR_MESSAGES.MODERATION_RULE_DUPLICATE,
      },
      {
        index: 4,
        field: 'pattern',
        message: ERROR_MESSAGES.MODERATION_RULE_DUPLICATE,
      },
    ])
  })

  test('the table takes as many rules and regular expressions as the server, and refuses one more', () => {
    const regexes = Array.from({ length: MODERATION_REGEX_RULES_MAX }, (_, i) =>
      rule(`r${i}`, { type: 'regex' })
    )
    const keywords = Array.from(
      { length: MODERATION_RULES_MAX - MODERATION_REGEX_RULES_MAX },
      (_, i) => rule(`k${i}`)
    )
    expect(validateModerationRuleRows([...regexes, ...keywords])).toEqual([])
    expect(
      validateModerationRuleRows([...regexes, ...keywords, rule('extra')])
    ).toEqual([{ message: ERROR_MESSAGES.MODERATION_RULES_TOO_MANY }])
    expect(
      validateModerationRuleRows([...regexes, rule('extra', { type: 'regex' })])
    ).toEqual([{ message: ERROR_MESSAGES.MODERATION_REGEX_RULES_TOO_MANY }])
  })
})

describe('readModerationRuleServerError', () => {
  test('reads the rule from the untranslated server message', () => {
    const error = new Error('渠道额外设置格式错误：…', {
      cause: {
        success: false,
        message:
          'Invalid channel settings format: moderation_rules[1].pattern is not a valid regular expression: error parsing regexp',
        message_key: 'Invalid channel settings format: {{error}}',
        message_params: {
          error:
            'moderation_rules[1].pattern is not a valid regular expression: error parsing regexp',
        },
      },
    })
    expect(readModerationRuleServerError(error)).toEqual({
      index: 1,
      field: 'pattern',
      error:
        'moderation_rules[1].pattern is not a valid regular expression: error parsing regexp',
    })
  })

  test('every rule error the server sends is mapped onto its row and cell, and table errors are not', () => {
    const source = serverSource('relaykit/dto/channel_moderation.go')
    const formats = [
      ...source.matchAll(/fmt\.Errorf\("(moderation_rules[^"]*)"/g),
    ].map(([, format]) => format)
    const rowFormats = formats.filter((format) =>
      format.startsWith('moderation_rules[%d].')
    )
    expect(rowFormats.length).toBeGreaterThanOrEqual(10)
    for (const format of formats) {
      const message = format
        .replaceAll('%d', '7')
        .replaceAll('%q', '"x"')
        .replaceAll('%s', 'x')
      const error = new Error(`Invalid channel settings format: ${message}`, {
        cause: {
          success: false,
          message: `Invalid channel settings format: ${message}`,
          message_key: 'Invalid channel settings format: {{error}}',
          message_params: { error: message },
        },
      })
      const field = /^moderation_rules\[%d\]\.(\w+)/.exec(format)?.[1]
      expect(readModerationRuleServerError(error), format).toEqual(
        field ? { index: 7, field, error: message } : null
      )
    }
  })

  test('other settings errors are not about a rule', () => {
    const error = new Error('Invalid channel settings format: …', {
      cause: {
        success: false,
        message:
          'Invalid channel settings format: moderation_max_chars must be 0 or between 1000 and 200000',
      },
    })
    expect(readModerationRuleServerError(error)).toBeNull()
    expect(readModerationRuleServerError('moderation_rules[0]')).toBeNull()
  })
})
