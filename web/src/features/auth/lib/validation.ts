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
import { BACKUP_CODE_REGEX, OTP_REGEX } from '../constants'

/**
 * Validation utilities for authentication forms
 */

// ============================================================================
// OTP Validation
// ============================================================================

/**
 * Validate OTP code (6 digits)
 */
export function isValidOTP(code: string): boolean {
  return OTP_REGEX.test(code)
}

/**
 * Validate backup code (XXXX-XXXX format)
 */
export function isValidBackupCode(code: string): boolean {
  return BACKUP_CODE_REGEX.test(code)
}

/**
 * Format backup code with hyphen (XXXX-XXXX)
 */
export function formatBackupCode(value: string): string {
  // Remove all non-alphanumeric characters and convert to uppercase
  let cleaned = value.toUpperCase().replaceAll(/[^A-Z0-9]/g, '')

  // Limit to 8 characters
  if (cleaned.length > 8) {
    cleaned = cleaned.slice(0, 8)
  }

  // Add hyphen after 4th character
  if (cleaned.length > 4) {
    return `${cleaned.slice(0, 4)}-${cleaned.slice(4)}`
  }

  return cleaned
}

/**
 * Remove hyphens from backup code before sending to server
 */
export function cleanBackupCode(code: string): string {
  return code.replaceAll('-', '')
}

// ============================================================================
// Email Validation
// ============================================================================

/**
 * Basic email validation
 */
export function isValidEmail(email: string): boolean {
  const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
  return emailRegex.test(email)
}

// A QQ number has 5 to 11 ASCII digits and no leading zero.
const QQ_NUMBER_REGEX = /^[1-9][0-9]{4,10}$/

/**
 * True for a qq.com address whose local part is not a QQ number, such as an
 * English alias. The server rejects these when EmailQQNumericOnlyEnabled is
 * on and stays the authority; this only lets the form say so early.
 */
export function isNonNumericQQEmail(email: string): boolean {
  const normalized = email.trim().toLowerCase()
  const at = normalized.lastIndexOf('@')
  if (at < 0 || normalized.slice(at + 1) !== 'qq.com') return false
  return !QQ_NUMBER_REGEX.test(normalized.slice(0, at))
}
