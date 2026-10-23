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
import { afterEach, expect, test, vi } from 'vitest'

import {
  formatModelRateLimitPattern,
  getModelRateLimitRulesError,
  parseModelRateLimitRules,
  removeModelRateLimitRule,
  upsertModelRateLimitRule,
  type ModelRateLimitRule,
} from '../model-rate-limit-rules'

afterEach(() => {
  vi.restoreAllMocks()
})

const notObject =
  'Model rate limits must be a JSON object that maps model names to rules'
const badName =
  'Model names in model rate limits must have 1 to {{max}} characters and no leading or trailing spaces: "{{model}}"'

// The backend decodes the fields into integers, which rejects these literals
// although JSON.parse reads them as whole numbers.
test.each(['1.0', '60.0', '1e1', '1E1', '10e-1'])(
  'a rule field written as %s is rejected like the backend rejects it',
  (literal) => {
    expect(
      getModelRateLimitRulesError(
        `{"gpt-4o": {"duration_minutes": ${literal}, "total_count": 5}}`
      )
    ).toEqual({ key: notObject })
  }
)

test('without reviver source text a fractional rule field is still rejected', () => {
  // Browsers before JSON.parse source text access call the reviver with only
  // the key and the value.
  const parse = JSON.parse
  vi.spyOn(JSON, 'parse').mockImplementation((text, reviver) =>
    parse(
      text,
      reviver &&
        function (this: unknown, key: string, value: unknown) {
          return reviver.call(this, key, value)
        }
    )
  )

  expect(
    getModelRateLimitRulesError(
      '{"gpt-4o": {"duration_minutes": 1.0, "total_count": 5}}'
    )
  ).toEqual({ key: notObject })
})

test('a model name that contains a decimal point is not read as a number', () => {
  expect(
    getModelRateLimitRulesError(
      '{"gpt-4.1": {"duration_minutes": 1, "total_count": 5}}'
    )
  ).toBeNull()
})

test('rule field names match in any case, like the backend', () => {
  expect(
    getModelRateLimitRulesError(
      '{"gpt-4o": {"Duration_Minutes": 5, "TOTAL_COUNT": 1}}'
    )
  ).toBeNull()
})

test('model names are trimmed of the spaces the backend trims', () => {
  expect(
    getModelRateLimitRulesError('{"\\ufeffgpt-4o": {"duration_minutes": 1}}')
  ).toBeNull()
  expect(
    getModelRateLimitRulesError('{"gpt-4o\\u0085": {"duration_minutes": 1}}')
  ).toEqual({
    key: badName,
    params: { max: 255, model: 'gpt-4o\u0085' },
  })
})

test('adding or removing a rule leaves rules that are not valid JSON unchanged', () => {
  const draft = '{"gpt-4o": {"duration_minutes": 1},}'
  const rule = {
    modelName: 'o3',
    match: 'exact' as const,
    shared: false,
    durationMinutes: 1,
    totalCount: 0,
    successCount: 0,
  }

  expect(upsertModelRateLimitRule(draft, rule)).toBe(draft)
  expect(removeModelRateLimitRule(draft, 'gpt-4o')).toBe(draft)
})

const badMatch =
  'The match of the rate limit for model {{model}} must be exact, prefix or suffix'
const badShared =
  'The rate limit for model {{model}} can count matching models together only with prefix or suffix match'

test.each([
  [
    'an unknown match',
    '{"gpt-": {"duration_minutes": 1, "match": "contains"}}',
    badMatch,
  ],
  [
    'a match in another case',
    '{"gpt-": {"duration_minutes": 1, "match": "Prefix"}}',
    badMatch,
  ],
  [
    'a shared rule without a match',
    '{"gpt-": {"duration_minutes": 1, "shared": true}}',
    badShared,
  ],
  [
    'a shared exact rule',
    '{"gpt-": {"duration_minutes": 1, "match": "exact", "shared": true}}',
    badShared,
  ],
])('%s is rejected like the backend rejects it', (_name, value, key) => {
  expect(getModelRateLimitRulesError(value)).toEqual({
    key,
    params: { model: 'gpt-' },
  })
})

test.each([
  [
    'a match that is not a string',
    '{"gpt-": {"duration_minutes": 1, "match": 1}}',
  ],
  [
    'a shared value that is not a boolean',
    '{"gpt-": {"duration_minutes": 1, "match": "prefix", "shared": "yes"}}',
  ],
])('%s cannot be decoded, like on the backend', (_name, value) => {
  expect(getModelRateLimitRulesError(value)).toEqual({ key: notObject })
})

// The way the table shows a pattern rule, which as a key would match no
// model.
test.each([
  [
    'a prefix ending with *',
    '{"[按次]*": {"duration_minutes": 1, "match": "prefix"}}',
    'The prefix {{model}} of a model rate limit must not end with *: a prefix rule already matches every model whose name starts with it',
    '[按次]*',
  ],
  [
    'a suffix starting with *',
    '{"*-thinking": {"duration_minutes": 1, "match": "suffix", "shared": true}}',
    'The suffix {{model}} of a model rate limit must not start with *: a suffix rule already matches every model whose name ends with it',
    '*-thinking',
  ],
])('%s is rejected like the backend rejects it', (_name, value, key, model) => {
  expect(getModelRateLimitRulesError(value)).toEqual({
    key,
    params: { model },
  })
})

// "*" is a literal character that wildcard-normalized names contain.
test.each([
  '{"gpt-4-gizmo-*": {"duration_minutes": 1}}',
  '{"-thinking-*": {"duration_minutes": 1, "match": "suffix"}}',
])('a * the match mode does not stand for is valid: %s', (value) => {
  expect(getModelRateLimitRulesError(value)).toBeNull()
})

test('a prefix rule that counts its models together is valid', () => {
  expect(
    getModelRateLimitRulesError(
      '{"[按次]": {"duration_minutes": 1, "total_count": 8, "MATCH": "prefix", "shared": true}}'
    )
  ).toBeNull()
})

test('a rule saved without a match reads as an exact rule', () => {
  expect(
    parseModelRateLimitRules(
      '{"gpt-4o": {"duration_minutes": 1, "total_count": 5}}'
    )
  ).toEqual([
    {
      modelName: 'gpt-4o',
      match: 'exact',
      shared: false,
      durationMinutes: 1,
      totalCount: 5,
      successCount: 0,
    },
  ])
})

test('a rule with an unknown match cannot be shown in the visual editor', () => {
  expect(
    parseModelRateLimitRules(
      '{"gpt-": {"duration_minutes": 1, "match": "contains"}}'
    )
  ).toBeNull()
})

const prefixRule: ModelRateLimitRule = {
  modelName: '[按次]',
  match: 'prefix',
  shared: false,
  durationMinutes: 1,
  totalCount: 8,
  successCount: 8,
}

test('saving a prefix rule names its match and counting', () => {
  expect(JSON.parse(upsertModelRateLimitRule('{}', prefixRule))).toEqual({
    '[按次]': {
      duration_minutes: 1,
      total_count: 8,
      success_count: 8,
      match: 'prefix',
      shared: false,
    },
  })
})

test('saving an exact rule keeps the shape rules had before match modes', () => {
  const rule: ModelRateLimitRule = {
    ...prefixRule,
    modelName: 'gpt-4o',
    match: 'exact',
  }
  expect(JSON.parse(upsertModelRateLimitRule('{}', rule))).toEqual({
    'gpt-4o': { duration_minutes: 1, total_count: 8, success_count: 8 },
  })
})

test.each([
  ['prefix', '[按次]', '[按次]*'],
  ['suffix', '-thinking', '*-thinking'],
  ['exact', 'gpt-4o', 'gpt-4o'],
] as const)('a %s rule for %s is shown as %s', (match, modelName, pattern) => {
  expect(formatModelRateLimitPattern({ ...prefixRule, match, modelName })).toBe(
    pattern
  )
})
