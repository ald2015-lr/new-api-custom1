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
  getModelRateLimitRulesError,
  removeModelRateLimitRule,
  upsertModelRateLimitRule,
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
    durationMinutes: 1,
    totalCount: 0,
    successCount: 0,
  }

  expect(upsertModelRateLimitRule(draft, rule)).toBe(draft)
  expect(removeModelRateLimitRule(draft, 'gpt-4o')).toBe(draft)
})
