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
type Translate = (key: string, options?: Record<string, unknown>) => string

/**
 * Sign-in and password reset events of the audit log, as options of the
 * console's event filter. `labelKey` is the i18n key of the option.
 */
export const AUTH_AUDIT_EVENTS = [
  { value: 'login', labelKey: 'Successful logins' },
  { value: 'user.login_failed', labelKey: 'Failed logins' },
  {
    value: 'user.login_2fa_failed',
    labelKey: 'Failed two-factor verifications',
  },
  {
    value: 'user.password_reset_request',
    labelKey: 'Password reset requests',
  },
  { value: 'user.password_reset', labelKey: 'Password resets' },
  { value: 'user.password_reset_failed', labelKey: 'Failed password resets' },
  { value: 'user.register', labelKey: 'Registrations' },
  { value: 'user.login_unlock', labelKey: 'Login unlocks' },
] as const

// Events whose `outcome`, `reason` and `detail` params are machine values.
const AUTH_GUARD_ACTIONS = new Set<string>([
  'user.login_failed',
  'user.login_2fa_failed',
  'user.password_reset_request',
  'user.password_reset',
  'user.password_reset_failed',
])

/**
 * The reader's wording of an `outcome`, `reason` or `detail` value of a
 * sign-in or password reset event. Other params, other events and values
 * this page does not know pass through unchanged. `lockStarted` marks the
 * wrong password that started a lock.
 */
export function authAuditValueLabel(
  action: string,
  key: string,
  value: unknown,
  t: Translate,
  lockStarted = false
): unknown {
  if (!AUTH_GUARD_ACTIONS.has(action) || typeof value !== 'string') {
    return value
  }
  if (key === 'outcome') {
    switch (value) {
      case 'sent':
        return t('email sent')
      case 'not_registered':
        return t('no account uses this address')
      case 'send_failed':
        return t('email could not be sent')
      case 'cooldown':
        return t('requested again too soon')
      case 'daily_limit':
        return t('daily limit reached')
      case 'captcha_failed':
        return t('captcha missing or incorrect')
      case 'invalid_email':
        return t('invalid email address')
      default:
        return value
    }
  }
  if (key === 'detail') {
    switch (value) {
      case 'smtp_error':
        return t('SMTP error')
      case 'email_ambiguous':
        return t('email matches several accounts')
      case 'lookup_error':
        return t('account lookup failed')
      default:
        return value
    }
  }
  if (key !== 'reason') return value
  switch (value) {
    case 'invalid_credentials':
      return lockStarted
        ? t('wrong username or password, sign-in now locked')
        : t('wrong username or password')
    case 'disabled':
      return t('account disabled')
    case 'locked':
      return t('too many failed attempts, sign-in locked')
    case 'captcha_failed':
      return t('captcha missing or incorrect')
    case 'invalid_request':
      return t('invalid request')
    case 'invalid_code':
      return t('wrong code')
    case 'twofa_locked':
      return t('two-factor authentication temporarily locked')
    case 'invalid_link':
      return t('invalid or expired link')
    case 'server_error':
      return t('server error')
    default:
      return value
  }
}

/**
 * Whether the IP filter holds a whole address the server accepts; a partial
 * one is not sent while it is typed.
 */
export function isAuditIpFilter(value: string): boolean {
  const ip = value.trim()
  if (ip.includes('.') && !ip.includes(':')) {
    const parts = ip.split('.')
    return (
      parts.length === 4 &&
      parts.every((part) => /^(0|[1-9]\d{0,2})$/.test(part) && +part <= 255)
    )
  }
  if (!ip.includes(':') || !/^[0-9a-f:.]+$/i.test(ip)) return false
  try {
    return new URL(`http://[${ip}]/`).hostname !== ''
  } catch {
    return false
  }
}

/** Whether the email filter holds a whole address; see isAuditIpFilter. */
export function isAuditEmailFilter(value: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
}

/**
 * A typed name or address the server did not store (it matched no account
 * and is no address, so it may be a password typed in the wrong box): "…"
 * and its length. Other values are shown as stored.
 */
export function authAuditTypedValue(
  value: unknown,
  identifierLength: unknown,
  t: Translate
): string {
  if (typeof value !== 'string' || !value) return ''
  if (typeof identifierLength !== 'number') return value
  return t('{{value}} ({{count}} characters)', {
    value,
    count: identifierLength,
  })
}
