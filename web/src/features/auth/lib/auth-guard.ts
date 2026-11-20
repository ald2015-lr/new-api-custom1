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
import type { TFunction } from 'i18next'

import { getServerErrorSources } from '@/lib/server-error-message'

// Codes of the refusals of the sign-in and forgot password protections.
export const PASSWORD_RESET_EMAIL_COOLDOWN = 'PASSWORD_RESET_EMAIL_COOLDOWN'
export const PASSWORD_RESET_EMAIL_DAILY_LIMIT =
  'PASSWORD_RESET_EMAIL_DAILY_LIMIT'
export const LOGIN_LOCKED = 'LOGIN_LOCKED'
export const CAPTCHA_REQUIRED = 'CAPTCHA_REQUIRED'
export const CAPTCHA_INVALID = 'CAPTCHA_INVALID'

/**
 * A server message kept as its locale key and params, so a page shows it in
 * the current language; `text` is the server's English for a reply without
 * a key.
 */
export type ServerText = {
  key?: string
  params?: Record<string, unknown>
  text?: string
}

export type AuthGuardRefusal = {
  code: string
  /** Whole seconds the server asks to wait; 0 when it named none. */
  waitSeconds: number
  message: ServerText
}

/**
 * The machine code, wait and message of a refused sign-in or reset request,
 * read from its response body or from the error that carries it; null
 * without a code.
 */
export function readAuthGuardRefusal(value: unknown): AuthGuardRefusal | null {
  for (const source of getServerErrorSources(value)) {
    if (typeof source.code !== 'string' || !source.code) continue
    const wait = Number(source.wait_seconds)
    const params = source.message_params
    return {
      code: source.code,
      waitSeconds: Number.isFinite(wait) && wait > 0 ? Math.ceil(wait) : 0,
      message: {
        key:
          typeof source.message_key === 'string' && source.message_key
            ? source.message_key
            : undefined,
        params:
          typeof params === 'object' && params !== null
            ? (params as Record<string, unknown>)
            : undefined,
        text: typeof source.message === 'string' ? source.message : undefined,
      },
    }
  }
  return null
}

/** The text of a server message in the language of `t`. */
export function serverText(t: TFunction, message: ServerText): string {
  return message.key ? t(message.key, message.params) : (message.text ?? '')
}

/** A wait as `m:ss`, or `h:mm:ss` from one hour on. */
export function formatWait(seconds: number): string {
  const total = Math.max(0, Math.ceil(seconds))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const rest = String(total % 60).padStart(2, '0')
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${rest}`
  }
  return `${minutes}:${rest}`
}

/**
 * Whether two typed sign-in names are the same for a lock; the server folds
 * case too, so `Alice` stays locked after `alice` was.
 */
export function sameLoginName(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase()
}

/** Whether two typed addresses are the one the reset limit counted. */
export function sameResetAddress(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase()
}
