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
import * as z from 'zod'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray, isObjectRecord } from '../utils/json-validators'

export const GROUP_ACCESS_RULES_KEY = 'group_access_setting.rules'
export const GROUP_ACCESS_COUNT_REDEMPTION_KEY =
  'group_access_setting.count_redemption'

/** Highest threshold the server accepts. */
export const GROUP_ACCESS_MAX_MIN_TOPUP = 1_000_000_000

/** A whitelisted user. `id` stays 0 until the server resolves `username`. */
export type GroupAccessUser = {
  id: number
  username: string
}

/**
 * A recharge-gated group. `min_topup` is a cumulative top-up amount in top-up
 * units; 0 means only whitelisted users qualify.
 */
export type GroupAccessRule = {
  group: string
  min_topup: number
  users: GroupAccessUser[]
}

export type ParsedGroupAccessUsers = {
  users: GroupAccessUser[]
  /** Tokens that look like user IDs but are not positive safe integers. */
  invalidIds: string[]
}

/**
 * Reads the stored `group_access_setting.rules` value. Malformed entries are
 * dropped so the editor can still show the valid ones.
 */
export function parseGroupAccessRules(value: string): GroupAccessRule[] {
  const parsed = safeJsonParseWithValidation<unknown[]>(value, {
    fallback: [],
    validator: isArray,
    context: 'group access rules',
  })

  return parsed.flatMap((entry) => {
    if (!isObjectRecord(entry) || typeof entry.group !== 'string') return []
    const group = entry.group.trim()
    if (!group) return []
    const minTopup =
      typeof entry.min_topup === 'number' && Number.isFinite(entry.min_topup)
        ? entry.min_topup
        : 0
    const users = Array.isArray(entry.users)
      ? entry.users.flatMap((user): GroupAccessUser[] => {
          if (!isObjectRecord(user)) return []
          const id =
            Number.isSafeInteger(user.id) && Number(user.id) > 0
              ? Number(user.id)
              : 0
          const username =
            typeof user.username === 'string' ? user.username.trim() : ''
          return id > 0 || username ? [{ id, username }] : []
        })
      : []
    return [{ group, min_topup: minTopup, users }]
  })
}

/**
 * Serializes rules into the option value. Entries keep the stored shape; an
 * unresolved username is sent with id 0 and the server resolves it on save.
 */
export function serializeGroupAccessRules(rules: GroupAccessRule[]): string {
  return JSON.stringify(
    rules.map((rule) => ({
      group: rule.group,
      min_topup: rule.min_topup,
      users: rule.users.map((user) => ({
        id: user.id,
        username: user.username,
      })),
    }))
  )
}

/**
 * Parses the whitelist textarea. Entries are separated by new lines, commas
 * or spaces. A token of only digits is a user ID, anything else a username;
 * a leading @ always means a username, so `@123` is the username "123".
 */
export function parseGroupAccessUsers(text: string): ParsedGroupAccessUsers {
  const users: GroupAccessUser[] = []
  const invalidIds: string[] = []
  const seen = new Set<string>()

  for (const token of text.split(/[\s,，、]+/)) {
    if (!token) continue

    let user: GroupAccessUser
    if (token.startsWith('@')) {
      user = { id: 0, username: token.slice(1) }
    } else if (/^\d+$/.test(token)) {
      const id = Number(token)
      if (!Number.isSafeInteger(id) || id <= 0) {
        invalidIds.push(token)
        continue
      }
      user = { id, username: '' }
    } else {
      user = { id: 0, username: token }
    }
    if (user.id === 0 && !user.username) continue

    const identity = user.id > 0 ? `id:${user.id}` : `name:${user.username}`
    if (seen.has(identity)) continue
    seen.add(identity)
    users.push(user)
  }

  return { users, invalidIds }
}

/**
 * Writes whitelist users back into textarea form. Resolved users are written
 * as IDs because enforcement matches IDs; pending usernames keep the @ prefix
 * so a numeric username is not read back as an ID.
 */
export function formatGroupAccessUsersText(users: GroupAccessUser[]): string {
  return users
    .map((user) => (user.id > 0 ? String(user.id) : `@${user.username}`))
    .join('\n')
}

/** Display label for a whitelist user, e.g. "12 (alice)". */
export function formatGroupAccessUser(user: GroupAccessUser): string {
  if (user.id > 0 && user.username) return `${user.id} (${user.username})`
  if (user.id > 0) return String(user.id)
  return `@${user.username}`
}

export const groupAccessRuleFormSchema = z.object({
  group: z
    .string()
    .trim()
    .min(1, 'Group is required')
    .refine((group) => group !== 'auto', {
      message: 'The auto group cannot be gated',
    }),
  minTopup: z
    .number({ error: 'Must be greater than or equal to 0' })
    .min(0, 'Must be greater than or equal to 0')
    .max(GROUP_ACCESS_MAX_MIN_TOPUP, 'Must be 1,000,000,000 or less'),
  users: z
    .string()
    .refine((text) => parseGroupAccessUsers(text).invalidIds.length === 0, {
      message: 'User IDs must be positive integers',
    }),
})

export type GroupAccessRuleFormValues = z.infer<
  typeof groupAccessRuleFormSchema
>
