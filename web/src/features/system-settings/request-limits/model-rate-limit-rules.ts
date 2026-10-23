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

/** How a rule's key matches model names (setting.ModelRateLimitMatch*). */
export const MODEL_RATE_LIMIT_MATCHES = ['exact', 'prefix', 'suffix'] as const
export type ModelRateLimitMatch = (typeof MODEL_RATE_LIMIT_MATCHES)[number]

/**
 * One ModelRequestRateLimitModel entry: a limit per user for the models its
 * key matches. shared counts all models of a prefix or suffix rule together.
 */
export type ModelRateLimitRule = {
  modelName: string
  match: ModelRateLimitMatch
  shared: boolean
  durationMinutes: number
  totalCount: number
  successCount: number
}

/** A rule as saved, whose match is any string ('' when it has none). */
type StoredModelRateLimitRule = Omit<ModelRateLimitRule, 'match'> & {
  match: string
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
  'match',
  'shared',
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
// for one field the last one wins. A missing or null field decodes as its zero
// value (0, '' or false), and a value of another type is rejected: returns
// null then.
function readRuleFields(
  modelName: string,
  rule: Record<string, unknown>
): StoredModelRateLimitRule | null {
  const values: Partial<Record<RuleField, unknown>> = {}
  for (const [key, value] of Object.entries(rule)) {
    const field = RULE_FIELDS.find((name) => name === key.toLowerCase())
    if (field) values[field] = value
  }
  const durationMinutes = readRuleNumber(values.duration_minutes)
  const totalCount = readRuleNumber(values.total_count)
  const successCount = readRuleNumber(values.success_count)
  const match = readRuleString(values.match)
  const shared = readRuleBoolean(values.shared)
  if (
    durationMinutes === null ||
    totalCount === null ||
    successCount === null ||
    match === null ||
    shared === null
  ) {
    return null
  }
  return { modelName, match, shared, durationMinutes, totalCount, successCount }
}

function readRuleNumber(value: unknown): number | null {
  if (value === undefined || value === null) return 0
  return typeof value === 'number' && Number.isInteger(value) ? value : null
}

function readRuleString(value: unknown): string | null {
  if (value === undefined || value === null) return ''
  return typeof value === 'string' ? value : null
}

function readRuleBoolean(value: unknown): boolean | null {
  if (value === undefined || value === null) return false
  return typeof value === 'boolean' ? value : null
}

/** The match mode a saved match names: none is exact; null when unknown. */
function toModelRateLimitMatch(match: string): ModelRateLimitMatch | null {
  if (match === '') return 'exact'
  return MODEL_RATE_LIMIT_MATCHES.find((mode) => mode === match) ?? null
}

/**
 * Reads the stored option as the backend decodes it. Returns null when the
 * backend could not decode it: not a JSON object, a rule that is not an
 * object, or a field of the wrong type, such as a non-integer count.
 */
function readStoredRules(value: string): StoredModelRateLimitRule[] | null {
  let parsed: unknown
  try {
    parsed = parseRulesJson(value)
  } catch {
    return null
  }
  if (!isObjectRecord(parsed)) return null

  const rules: StoredModelRateLimitRule[] = []
  for (const [modelName, rule] of Object.entries(parsed)) {
    if (rule !== null && !isObjectRecord(rule)) return null
    const fields = readRuleFields(modelName, rule ?? {})
    if (fields === null) return null
    rules.push(fields)
  }
  return rules
}

/**
 * Reads the stored option for the visual editor. Returns null when the
 * backend could not decode it, or when a rule has a match mode the editor
 * cannot show.
 */
export function parseModelRateLimitRules(
  value: string
): ModelRateLimitRule[] | null {
  const stored = readStoredRules(value)
  if (stored === null) return null
  const rules: ModelRateLimitRule[] = []
  for (const rule of stored) {
    const match = toModelRateLimitMatch(rule.match)
    if (match === null) return null
    rules.push({ ...rule, match })
  }
  return rules
}

/**
 * Whether a prefix rule's key ends with "*" or a suffix rule's key starts
 * with it, the way the table shows those rules ("[按次]*", "*-thinking"). The
 * match mode is what makes a key match many models; in the key, "*" is a
 * literal character, so such a rule would match no model.
 */
export function hasModelRateLimitPatternStar(
  modelName: string,
  match: ModelRateLimitMatch
): boolean {
  if (match === 'prefix') return modelName.endsWith('*')
  if (match === 'suffix') return modelName.startsWith('*')
  return false
}

/** How the table names a rule: "[按次]*" for a prefix, "*-thinking" for a suffix. */
export function formatModelRateLimitPattern(rule: ModelRateLimitRule): string {
  if (rule.match === 'prefix') return `${rule.modelName}*`
  if (rule.match === 'suffix') return `*${rule.modelName}`
  return rule.modelName
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
  const rules = readStoredRules(value)
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
    const match = toModelRateLimitMatch(rule.match)
    if (match === null) {
      return {
        key: 'The match of the rate limit for model {{model}} must be exact, prefix or suffix',
        params: { model },
      }
    }
    if (match === 'exact' && rule.shared) {
      return {
        key: 'The rate limit for model {{model}} can count matching models together only with prefix or suffix match',
        params: { model },
      }
    }
    if (match === 'prefix' && hasModelRateLimitPatternStar(model, match)) {
      return {
        key: 'The prefix {{model}} of a model rate limit must not end with *: a prefix rule already matches every model whose name starts with it',
        params: { model },
      }
    }
    if (match === 'suffix' && hasModelRateLimitPatternStar(model, match)) {
      return {
        key: 'The suffix {{model}} of a model rate limit must not start with *: a suffix rule already matches every model whose name ends with it',
        params: { model },
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
 * An exact rule is saved without match and shared, as rules were saved before
 * match modes existed; a prefix or suffix rule names both.
 */
export function upsertModelRateLimitRule(
  value: string,
  rule: ModelRateLimitRule
): string {
  const parsed = parseRuleObject(value)
  if (!parsed) return value
  const stored: Record<string, unknown> = {
    duration_minutes: rule.durationMinutes,
    total_count: rule.totalCount,
    success_count: rule.successCount,
  }
  if (rule.match !== 'exact') {
    stored.match = rule.match
    stored.shared = rule.shared
  }
  parsed[rule.modelName] = stored
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
