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

// The backend decodes a missing or null field as 0 and rejects any other
// value that is not an integer.
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
    parsed = JSON.parse(value)
  } catch {
    return null
  }
  if (!isObjectRecord(parsed)) return null

  const rules: ModelRateLimitRule[] = []
  for (const [modelName, rule] of Object.entries(parsed)) {
    if (rule !== null && !isObjectRecord(rule)) return null
    const fields = rule ?? {}
    const durationMinutes = readRuleNumber(fields.duration_minutes)
    const totalCount = readRuleNumber(fields.total_count)
    const successCount = readRuleNumber(fields.success_count)
    if (
      durationMinutes === null ||
      totalCount === null ||
      successCount === null
    ) {
      return null
    }
    rules.push({ modelName, durationMinutes, totalCount, successCount })
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
      model !== model.trim() ||
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

function parseRuleObject(value: string): Record<string, unknown> {
  try {
    const parsed: unknown = JSON.parse(value)
    return isObjectRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

/** Adds or replaces the rule of rule.modelName, keeping the other entries. */
export function upsertModelRateLimitRule(
  value: string,
  rule: ModelRateLimitRule
): string {
  const parsed = parseRuleObject(value)
  parsed[rule.modelName] = {
    duration_minutes: rule.durationMinutes,
    total_count: rule.totalCount,
    success_count: rule.successCount,
  }
  return JSON.stringify(parsed, null, 2)
}

export function removeModelRateLimitRule(
  value: string,
  modelName: string
): string {
  const parsed = parseRuleObject(value)
  delete parsed[modelName]
  return JSON.stringify(parsed, null, 2)
}
