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
  MODERATION_MAX_CHARS_MAX,
  MODERATION_MAX_CHARS_MIN,
  MODERATION_REGEX_RULES_MAX,
  MODERATION_RULE_NAME_MAX,
  MODERATION_RULE_PATTERN_MAX,
  MODERATION_RULES_MAX,
  MODERATION_TIMEOUT_SECONDS_MAX,
  MODERATION_TIMEOUT_SECONDS_MIN,
} from '../constants'

/** One row of the custom content moderation rules table. */
export type ModerationRuleRow = {
  name: string
  type: 'keyword' | 'regex'
  pattern: string
  case_sensitive: boolean
}

/**
 * Whether the server accepts this character budget: empty or 0 (the
 * default), or a whole number in the allowed range.
 */
export function isValidModerationMaxChars(
  value: number | null | undefined
): boolean {
  return (
    value == null ||
    value === 0 ||
    (Number.isInteger(value) &&
      value >= MODERATION_MAX_CHARS_MIN &&
      value <= MODERATION_MAX_CHARS_MAX)
  )
}

/**
 * Whether the server accepts this timeout in seconds: empty or 0 (the
 * default), or a whole number in the allowed range.
 */
export function isValidModerationTimeout(
  value: number | null | undefined
): boolean {
  return (
    value == null ||
    value === 0 ||
    (Number.isInteger(value) &&
      value >= MODERATION_TIMEOUT_SECONDS_MIN &&
      value <= MODERATION_TIMEOUT_SECONDS_MAX)
  )
}

// What the server ignores when it matches a pattern: format and invisible
// characters, combining marks and white space. Tag characters count as the
// ASCII characters they encode.
const TAG_CHARACTERS = /[\u{E0020}-\u{E007E}]/gu
const IGNORED_CHARACTERS = /[\p{Cf}\p{Mn}\p{Default_Ignorable_Code_Point}\s]/gu

function hasVisibleCharacter(pattern: string): boolean {
  return (
    pattern
      .replace(TAG_CHARACTERS, (tag) =>
        String.fromCodePoint((tag.codePointAt(0) ?? 0) - 0xe0000)
      )
      .replace(IGNORED_CHARACTERS, '') !== ''
  )
}

export type ModerationRuleIssue = {
  /** The row and cell the issue is about; absent for the whole table. */
  index?: number
  field?: 'name' | 'pattern'
  message: string
}

/**
 * What the server would refuse in the custom rules table, cell by cell, for
 * the trimmed values the form saves. Lengths count code points, like the
 * server. A keyword used twice (ignoring case) or a regular expression used
 * twice is reported where it appears again. Regular expression syntax is
 * RE2, which only the server can check.
 */
export function validateModerationRuleRows(
  rows: readonly ModerationRuleRow[]
): ModerationRuleIssue[] {
  const issues: ModerationRuleIssue[] = []
  if (rows.length > MODERATION_RULES_MAX) {
    issues.push({ message: ERROR_MESSAGES.MODERATION_RULES_TOO_MANY })
  }
  if (
    rows.filter((row) => row.type === 'regex').length >
    MODERATION_REGEX_RULES_MAX
  ) {
    issues.push({ message: ERROR_MESSAGES.MODERATION_REGEX_RULES_TOO_MANY })
  }
  const keywords = new Set<string>()
  const regexes = new Set<string>()
  rows.forEach((row, index) => {
    const pattern = row.pattern.trim()
    const isRegex = row.type === 'regex'
    const seen = isRegex ? regexes : keywords
    const seenKey = isRegex ? pattern : pattern.toLowerCase()
    let patternIssue: string | undefined
    if (!pattern) {
      patternIssue = ERROR_MESSAGES.MODERATION_RULE_PATTERN_REQUIRED
    } else if (!hasVisibleCharacter(pattern)) {
      patternIssue = ERROR_MESSAGES.MODERATION_RULE_PATTERN_INVISIBLE
    } else if ([...pattern].length > MODERATION_RULE_PATTERN_MAX) {
      patternIssue = ERROR_MESSAGES.MODERATION_RULE_PATTERN_TOO_LONG
    } else if (seen.has(seenKey)) {
      patternIssue = ERROR_MESSAGES.MODERATION_RULE_DUPLICATE
    }
    if (pattern) seen.add(seenKey)
    if (patternIssue) {
      issues.push({ index, field: 'pattern', message: patternIssue })
    }
    if ([...row.name.trim()].length > MODERATION_RULE_NAME_MAX) {
      issues.push({
        index,
        field: 'name',
        message: ERROR_MESSAGES.MODERATION_RULE_NAME_TOO_LONG,
      })
    }
  })
  return issues
}

export type ModerationRuleServerError = {
  index: number
  field: 'type' | 'name' | 'pattern'
  /** The server's message, from the rule's settings key on. */
  error: string
}

const SERVER_RULE_ERROR = /moderation_rules\[(\d+)\]\.(type|name|pattern)\b/

function recordOf(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object'
    ? (value as Record<string, unknown>)
    : undefined
}

/**
 * The rule a refused channel save is about, when the server refused one of
 * the custom moderation rules (for example a regular expression RE2 cannot
 * compile). Reads the server's untranslated message where the error keeps
 * it, so the result does not depend on the console language.
 */
export function readModerationRuleServerError(
  error: unknown
): ModerationRuleServerError | null {
  const cause = recordOf(error instanceof Error ? error.cause : undefined)
  const params = recordOf(cause?.message_params)
  const candidates = [
    params?.error,
    cause?.message,
    error instanceof Error ? error.message : undefined,
  ]
  for (const candidate of candidates) {
    if (typeof candidate !== 'string') continue
    const match = SERVER_RULE_ERROR.exec(candidate)
    if (!match) continue
    return {
      index: Number(match[1]),
      field: match[2] as ModerationRuleServerError['field'],
      error: candidate.slice(match.index),
    }
  }
  return null
}
