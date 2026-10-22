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
import { isObjectRecord } from '../utils/json-validators'

// Bounds of setting.CheckModelRequestRateLimitModel on the backend.
export const MODEL_RATE_LIMIT_MAX_MINUTES = 1440
export const MODEL_RATE_LIMIT_MAX_COUNT = 100_000_000
export const MODEL_RATE_LIMIT_MAX_MODELS = 1000
export const MODEL_RATE_LIMIT_MAX_NAME_LENGTH = 255

/** One ModelRequestRateLimitModel entry: a limit per user for one model. */
export type ModelRateLimitRule = {
  modelName: string
  durationMinutes: number
  totalCount: number
  successCount: number
}

/** A backend validation message key and its params. */
export type ModelRateLimitRulesError = {
  key: string
  params?: Record<string, string | number>
}

// The characters Go's strings.TrimSpace removes (unicode.IsSpace), as a
// RegExp character class. Unlike JS trim(), it keeps U+FEFF and removes U+0085.
const GO_SPACE = String.raw`\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000`
const SURROUNDING_GO_SPACE = new RegExp(`^[${GO_SPACE}]|[${GO_SPACE}]$`)

// A JSON number the backend can decode into an int: no fraction or exponent,
// although JSON.parse reads 1.0 or 1e1 as whole numbers.
const INTEGER_LITERAL = /^-?(?:0|[1-9]\d*)$/

// The same check over the raw text, for browsers whose JSON.parse does not
// give a reviver the source of a value.
const NON_INTEGER_RULE_FIELD =
  /"(?:duration_minutes|total_count|success_count)"\s*:\s*-?\d+(?:\.|[eE])/i

const RULE_FIELDS = [
  'duration_minutes',
  'total_count',
  'success_count',
] as const
type RuleField = (typeof RULE_FIELDS)[number]

/**
 * JSON.parse that turns a number written with a fraction or an exponent into
 * NaN, so it reads as a value the backend cannot decode.
 */
function parseRulesJson(value: string): unknown {
  let sourceUnavailable = false
  const parsed: unknown = JSON.parse(
    value,
    (_key: string, field: unknown, context?: { source?: string }) => {
      if (typeof field !== 'number') return field
      if (context?.source === undefined) {
        sourceUnavailable = true
        return field
      }
      return INTEGER_LITERAL.test(context.source) ? field : Number.NaN
    }
  )
  if (sourceUnavailable && NON_INTEGER_RULE_FIELD.test(value)) return null
  return parsed
}

// The backend matches a key to a rule field ignoring case; of several keys
// for one field the last one wins. A missing or null field decodes as 0, and
// any other value that is not an integer is rejected.
function readRuleFields(
  rule: Record<string, unknown>
): Record<RuleField, number | null> {
  const values: Record<RuleField, unknown> = {
    duration_minutes: undefined,
    total_count: undefined,
    success_count: undefined,
  }
  for (const [key, value] of Object.entries(rule)) {
    const field = RULE_FIELDS.find((name) => name === key.toLowerCase())
    if (field) values[field] = value
  }
  return {
    duration_minutes: readRuleNumber(values.duration_minutes),
    total_count: readRuleNumber(values.total_count),
    success_count: readRuleNumber(values.success_count),
  }
}

function readRuleNumber(value: unknown): number | null {
  if (value === undefined || value === null) return 0
  return typeof value === 'number' && Number.isInteger(value) ? value : null
}

/**
 * Reads the stored option. Returns null when the backend could not decode
 * it: not a JSON object, a rule that is not an object, or a non-integer field.
 */
export function parseModelRateLimitRules(
  value: string
): ModelRateLimitRule[] | null {
  let parsed: unknown
  try {
    parsed = parseRulesJson(value)
  } catch {
    return null
  }
  if (!isObjectRecord(parsed)) return null

  const rules: ModelRateLimitRule[] = []
  for (const [modelName, rule] of Object.entries(parsed)) {
    if (rule !== null && !isObjectRecord(rule)) return null
    const fields = readRuleFields(rule ?? {})
    if (
      fields.duration_minutes === null ||
      fields.total_count === null ||
      fields.success_count === null
    ) {
      return null
    }
    rules.push({
      modelName,
      durationMinutes: fields.duration_minutes,
      totalCount: fields.total_count,
      successCount: fields.success_count,
    })
  }
  return rules
}

function isCountInRange(count: number): boolean {
  return count >= 0 && count <= MODEL_RATE_LIMIT_MAX_COUNT
}

/**
 * Applies the backend's validation of ModelRequestRateLimitModel and returns
 * the message it would answer with, or null when the value would be saved.
 */
export function getModelRateLimitRulesError(
  value: string
): ModelRateLimitRulesError | null {
  const rules = parseModelRateLimitRules(value)
  if (rules === null) {
    return {
      key: 'Model rate limits must be a JSON object that maps model names to rules',
    }
  }
  if (rules.length > MODEL_RATE_LIMIT_MAX_MODELS) {
    return {
      key: 'Model rate limits can list at most {{max}} models',
      params: { max: MODEL_RATE_LIMIT_MAX_MODELS },
    }
  }
  // Like the backend, report the first invalid model in name order.
  const sorted = [...rules].sort((a, b) =>
    a.modelName < b.modelName ? -1 : Number(a.modelName > b.modelName)
  )
  for (const rule of sorted) {
    const model = rule.modelName
    if (
      model === '' ||
      SURROUNDING_GO_SPACE.test(model) ||
      [...model].length > MODEL_RATE_LIMIT_MAX_NAME_LENGTH
    ) {
      return {
        key: 'Model names in model rate limits must have 1 to {{max}} characters and no leading or trailing spaces: "{{model}}"',
        params: { max: MODEL_RATE_LIMIT_MAX_NAME_LENGTH, model },
      }
    }
    if (
      rule.durationMinutes < 1 ||
      rule.durationMinutes > MODEL_RATE_LIMIT_MAX_MINUTES
    ) {
      return {
        key: 'The time window of the rate limit for model {{model}} must be 1 to {{max}} minutes',
        params: { max: MODEL_RATE_LIMIT_MAX_MINUTES, model },
      }
    }
    if (
      !isCountInRange(rule.totalCount) ||
      !isCountInRange(rule.successCount)
    ) {
      return {
        key: 'The request counts of the rate limit for model {{model}} must be 0 to {{max}}',
        params: { max: MODEL_RATE_LIMIT_MAX_COUNT, model },
      }
    }
  }
  return null
}

function parseRuleObject(value: string): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(value)
    return isObjectRecord(parsed) ? parsed : null
  } catch {
    return null
  }
}

/**
 * Adds or replaces the rule of rule.modelName, keeping the other entries. A
 * value that is not a JSON object is returned unchanged rather than replaced.
 */
export function upsertModelRateLimitRule(
  value: string,
  rule: ModelRateLimitRule
): string {
  const parsed = parseRuleObject(value)
  if (!parsed) return value
  parsed[rule.modelName] = {
    duration_minutes: rule.durationMinutes,
    total_count: rule.totalCount,
    success_count: rule.successCount,
  }
  return JSON.stringify(parsed, null, 2)
}

/** Removes the rule of modelName. A value that is not a JSON object is returned unchanged. */
export function removeModelRateLimitRule(
  value: string,
  modelName: string
): string {
  const parsed = parseRuleObject(value)
  if (!parsed) return value
  delete parsed[modelName]
  return JSON.stringify(parsed, null, 2)
}
