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
import {
  ERROR_MESSAGES,
  GEMINI_FREE_KEY_ALIASES_MAX,
  GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS,
  GEMINI_FREE_KEY_LIMIT_MAX,
  GEMINI_FREE_KEY_LIMIT_MIN,
  GEMINI_FREE_KEY_MAX_ATTEMPTS_MAX,
  GEMINI_FREE_KEY_MAX_ATTEMPTS_MIN,
  GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS,
  GEMINI_FREE_KEY_MODEL_NAME_MAX,
} from '../constants'
import type {
  GeminiFreeKeyCooldownReason,
  GeminiFreeKeyModelLimit,
  GeminiFreeKeyModelUsage,
  GeminiFreeKeyUsageKey,
} from '../types'

/** One row of the model limits table as the channel form edits it. */
export type GeminiFreeKeyModelLimitRow = {
  model: string
  /** Comma-separated. */
  aliases: string
  /** null while the cell is empty. */
  rpm: number | null
  tpm: number | null
  rpd: number | null
}

export type GeminiFreeKeyModelLimitField = keyof GeminiFreeKeyModelLimitRow

const LIMIT_FIELDS = ['rpm', 'tpm', 'rpd'] as const

export function splitGeminiFreeKeyAliases(text: string): string[] {
  return text
    .split(',')
    .map((alias) => alias.trim())
    .filter(Boolean)
}

export function toGeminiFreeKeyModelLimitRows(
  limits: readonly GeminiFreeKeyModelLimit[]
): GeminiFreeKeyModelLimitRow[] {
  return limits.map((limit) => ({
    model: limit.model,
    aliases: (limit.aliases ?? []).join(', '),
    rpm: limit.rpm,
    tpm: limit.tpm,
    rpd: limit.rpd,
  }))
}

function storedLimit(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) ? value : null
}

/**
 * Table rows of a channel's saved model limits. A channel that saved none
 * (absent, null or an empty list, which the server drops when it rewrites
 * the settings) shows the built-in defaults the server applies.
 */
export function readGeminiFreeKeyModelLimitRows(
  value: unknown
): GeminiFreeKeyModelLimitRow[] {
  if (!Array.isArray(value) || value.length === 0) {
    return toGeminiFreeKeyModelLimitRows(GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS)
  }
  return value.map((item: Record<string, unknown> | null) => ({
    model: typeof item?.model === 'string' ? item.model : '',
    aliases: Array.isArray(item?.aliases)
      ? item.aliases
          .filter((alias): alias is string => typeof alias === 'string')
          .join(', ')
      : '',
    rpm: storedLimit(item?.rpm),
    tpm: storedLimit(item?.tpm),
    rpd: storedLimit(item?.rpd),
  }))
}

/** The saved form of table rows: trimmed names, aliases only where a row has some. */
export function toGeminiFreeKeyModelLimits(
  rows: readonly GeminiFreeKeyModelLimitRow[]
): GeminiFreeKeyModelLimit[] {
  return rows.map((row) => {
    const aliases = splitGeminiFreeKeyAliases(row.aliases)
    return {
      model: row.model.trim(),
      ...(aliases.length > 0 && { aliases }),
      rpm: row.rpm ?? 0,
      tpm: row.tpm ?? 0,
      rpd: row.rpd ?? 0,
    }
  })
}

// The server compares names without regard to case, so a row is the same
// whatever the case of its names and the order of its aliases.
function comparableModelLimit(limit: GeminiFreeKeyModelLimit): string {
  return JSON.stringify([
    limit.model.toLowerCase(),
    (limit.aliases ?? []).map((alias) => alias.toLowerCase()).sort(),
    limit.rpm,
    limit.tpm,
    limit.rpd,
  ])
}

const DEFAULT_COMPARABLE_MODEL_LIMITS =
  GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS.map(comparableModelLimit).sort()

/** Whether model limits hold the same rows as the defaults, in any order. */
export function isDefaultGeminiFreeKeyModelLimits(
  limits: readonly GeminiFreeKeyModelLimit[]
): boolean {
  const rows = limits.map(comparableModelLimit).sort()
  return (
    rows.length === DEFAULT_COMPARABLE_MODEL_LIMITS.length &&
    rows.every((row, index) => row === DEFAULT_COMPARABLE_MODEL_LIMITS[index])
  )
}

/** Unset (the default) or a whole number of keys in the allowed range. */
export function isValidGeminiFreeKeyMaxAttempts(
  attempts: number | null | undefined
): boolean {
  return (
    attempts == null ||
    (Number.isInteger(attempts) &&
      attempts >= GEMINI_FREE_KEY_MAX_ATTEMPTS_MIN &&
      attempts <= GEMINI_FREE_KEY_MAX_ATTEMPTS_MAX)
  )
}

function isValidLimit(value: number | null): boolean {
  return (
    value !== null &&
    Number.isInteger(value) &&
    value >= GEMINI_FREE_KEY_LIMIT_MIN &&
    value <= GEMINI_FREE_KEY_LIMIT_MAX
  )
}

export type GeminiFreeKeyModelLimitIssue = {
  /** The row and cell the issue is about; absent for the whole table. */
  index?: number
  field?: GeminiFreeKeyModelLimitField
  message: string
}

/**
 * What the server would refuse in the model limits table, cell by cell. A
 * name used twice (as a model or alias, ignoring case) is reported where it
 * appears again. An empty table is refused as well: the server would apply
 * the defaults to it instead.
 */
export function validateGeminiFreeKeyModelLimitRows(
  rows: readonly GeminiFreeKeyModelLimitRow[]
): GeminiFreeKeyModelLimitIssue[] {
  const issues: GeminiFreeKeyModelLimitIssue[] = []
  if (rows.length === 0) {
    issues.push({ message: ERROR_MESSAGES.GEMINI_FREE_KEY_MODEL_LIMITS_EMPTY })
  }
  if (rows.length > GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS) {
    issues.push({
      message: ERROR_MESSAGES.GEMINI_FREE_KEY_MODEL_LIMITS_TOO_MANY,
    })
  }
  const names = new Set<string>()
  rows.forEach((row, index) => {
    const model = row.model.trim()
    let modelIssue: string | undefined
    if (!model) {
      modelIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_MODEL_REQUIRED
    } else if ([...model].length > GEMINI_FREE_KEY_MODEL_NAME_MAX) {
      modelIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_NAME_TOO_LONG
    } else if (names.has(model.toLowerCase())) {
      modelIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_DUPLICATE_NAME
    }
    if (model) names.add(model.toLowerCase())
    if (modelIssue) issues.push({ index, field: 'model', message: modelIssue })

    const aliases = splitGeminiFreeKeyAliases(row.aliases)
    let aliasIssue: string | undefined
    if (aliases.length > GEMINI_FREE_KEY_ALIASES_MAX) {
      aliasIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_TOO_MANY_ALIASES
    } else if (
      aliases.some(
        (alias) => [...alias].length > GEMINI_FREE_KEY_MODEL_NAME_MAX
      )
    ) {
      aliasIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_NAME_TOO_LONG
    }
    for (const alias of aliases) {
      if (!aliasIssue && names.has(alias.toLowerCase())) {
        aliasIssue = ERROR_MESSAGES.GEMINI_FREE_KEY_DUPLICATE_NAME
      }
      names.add(alias.toLowerCase())
    }
    if (aliasIssue) {
      issues.push({ index, field: 'aliases', message: aliasIssue })
    }

    for (const field of LIMIT_FIELDS) {
      if (!isValidLimit(row[field])) {
        issues.push({
          index,
          field,
          message: ERROR_MESSAGES.INVALID_GEMINI_FREE_KEY_LIMIT,
        })
      }
    }
  })
  return issues
}

export type GeminiFreeKeyModelState = {
  /** Unix seconds the model is paused until; 0 when it is not paused. */
  until: number
  /** Empty when the model is available on the key. */
  reason: GeminiFreeKeyCooldownReason
}

/**
 * Whether a key can serve a model now, from the usage the server reports:
 * a paused key or model stays paused until its cooldown ends, a model with
 * a 0 limit is not offered at all, and a model that used up its daily
 * requests is paused until the next daily reset.
 */
export function geminiFreeKeyModelState(
  key: GeminiFreeKeyUsageKey,
  usage: GeminiFreeKeyModelUsage,
  nextResetAt: number,
  nowSeconds: number
): GeminiFreeKeyModelState {
  if (key.cooldown_until > nowSeconds) {
    return {
      until: key.cooldown_until,
      reason: key.cooldown_reason || 'unknown',
    }
  }
  if (usage.cooldown_until > nowSeconds) {
    return {
      until: usage.cooldown_until,
      reason: usage.cooldown_reason || 'unknown',
    }
  }
  if (usage.rpm_limit === 0 || usage.tpm_limit === 0 || usage.rpd_limit === 0) {
    return { until: 0, reason: 'not_free' }
  }
  if (usage.rpd_limit > 0 && usage.rpd_used >= usage.rpd_limit) {
    return { until: nextResetAt, reason: 'day' }
  }
  return { until: 0, reason: '' }
}
