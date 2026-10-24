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
import i18next from 'i18next'
import { z } from 'zod'

// The bounds the server enforces for check-in and invitation reward settings
// (setting/operation_setting/bonus_setting.go). The wallet ceiling is
// common.MaxWalletQuota, 2^53 - 1, the largest integer JavaScript keeps exact.
const MAX_REWARD_EXPIRE_DAYS = 3650
const MAX_QUOTA_THRESHOLD = Number.MAX_SAFE_INTEGER

function emptyToUndefined(value: unknown): unknown {
  return value === '' ? undefined : value
}

/** Reward lifetime in whole days, 0 to 3650; 0 means the reward never expires. */
export function rewardExpireDaysSchema() {
  const error = () =>
    i18next.t('Expiry days must be an integer between 0 and 3650')
  return z.preprocess(
    emptyToUndefined,
    z.coerce
      .number({ error })
      .int({ error })
      .min(0, { error })
      .max(MAX_REWARD_EXPIRE_DAYS, { error })
  )
}

/** A cumulative recharge threshold in raw quota, 0 to the wallet ceiling. */
export function quotaThresholdSchema() {
  const error = () =>
    i18next.t('Quota threshold must be an integer between 0 and {{max}}', {
      max: MAX_QUOTA_THRESHOLD,
    })
  return z.preprocess(
    emptyToUndefined,
    z.coerce
      .number({ error })
      .int({ error })
      .min(0, { error })
      .max(MAX_QUOTA_THRESHOLD, { error })
  )
}
