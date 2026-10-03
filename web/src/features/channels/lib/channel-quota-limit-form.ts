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
import { z } from 'zod'

import { parseQuotaFromDollars, quotaUnitsToEditableAmount } from '@/lib/format'

import type { ChannelQuotaLimit, UpdateChannelQuotaLimitParams } from '../types'

export const CHANNEL_QUOTA_LIMIT_FORM_ID = 'channel-quota-limit-form'
export const CHANNEL_QUOTA_LIMIT_MESSAGE_MAX_LENGTH = 255

/**
 * The limit is edited in the configured display currency as text so that an
 * empty field can mean "no limit".
 */
export function getChannelQuotaLimitFormSchema(t: TFunction) {
  return z.object({
    limit_amount: z
      .string()
      .trim()
      .refine(
        (value) => value === '' || Number.isFinite(Number(value)),
        t('Please enter a valid number')
      )
      .refine(
        (value) => value === '' || !(Number(value) < 0),
        t('Must be greater than or equal to 0')
      ),
    daily_reset: z.boolean(),
    message: z
      .string()
      .trim()
      .max(
        CHANNEL_QUOTA_LIMIT_MESSAGE_MAX_LENGTH,
        t('Must be {{max}} characters or fewer', {
          max: CHANNEL_QUOTA_LIMIT_MESSAGE_MAX_LENGTH,
        })
      ),
  })
}

export type ChannelQuotaLimitFormValues = z.infer<
  ReturnType<typeof getChannelQuotaLimitFormSchema>
>

export function getChannelQuotaLimitFormDefaults(
  limit: ChannelQuotaLimit | undefined
): ChannelQuotaLimitFormValues {
  if (!limit || limit.limit_quota <= 0) {
    return { limit_amount: '', daily_reset: false, message: '' }
  }
  return {
    limit_amount: String(quotaUnitsToEditableAmount(limit.limit_quota)),
    daily_reset: limit.daily_reset,
    message: limit.message,
  }
}

/**
 * Build the PUT body. `unchangedLimitQuota` keeps the stored quota exactly when
 * the amount was not edited, since the display amount may be rounded.
 */
export function toChannelQuotaLimitPayload(
  values: ChannelQuotaLimitFormValues,
  unchangedLimitQuota?: number
): UpdateChannelQuotaLimitParams {
  const limitQuota =
    unchangedLimitQuota ?? parseQuotaFromDollars(Number(values.limit_amount))
  return {
    limit_quota: Math.max(0, limitQuota),
    daily_reset: values.daily_reset,
    message: values.message.trim(),
  }
}
