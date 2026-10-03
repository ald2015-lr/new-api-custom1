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
import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray } from '../utils/json-validators'

export const FREE_GROUPS_KEY = 'group_billing_setting.free_groups'

/** The auto group routes to other groups and cannot be free itself. */
const AUTO_GROUP = 'auto'

/**
 * Trims, de-duplicates and sorts group names and drops "auto", so equal sets
 * of free groups always compare and serialize the same way.
 */
export function normalizeFreeGroups(groups: readonly string[]): string[] {
  const names = new Set<string>()
  for (const group of groups) {
    const name = group.trim()
    if (name && name !== AUTO_GROUP) names.add(name)
  }
  return [...names].sort()
}

/**
 * Reads the stored `group_billing_setting.free_groups` value. Entries that are
 * not strings are dropped; a malformed value reads as no free groups.
 */
export function parseFreeGroups(value: string | undefined | null): string[] {
  const parsed = safeJsonParseWithValidation<unknown[]>(value, {
    fallback: [],
    validator: isArray,
    context: 'free groups',
  })
  return normalizeFreeGroups(
    parsed.filter((entry): entry is string => typeof entry === 'string')
  )
}

/** Serializes free groups into the option value, a sorted JSON array. */
export function serializeFreeGroups(groups: readonly string[]): string {
  return JSON.stringify(normalizeFreeGroups(groups))
}

export type GroupBillingRow = {
  group: string
  free: boolean
  /** Stored as free but no longer returned by the group list. */
  missing: boolean
}

/**
 * One row per known group plus every stored free group that no longer
 * exists, so it can still be switched back to charging users.
 */
export function buildGroupBillingRows(
  groups: readonly string[],
  freeGroups: readonly string[]
): GroupBillingRow[] {
  const known = new Set(normalizeFreeGroups(groups))
  const free = new Set(freeGroups)
  const names = new Set([...known, ...normalizeFreeGroups(freeGroups)])
  return [...names]
    .sort((a, b) => a.localeCompare(b))
    .map((group) => ({
      group,
      free: free.has(group),
      missing: !known.has(group),
    }))
}
