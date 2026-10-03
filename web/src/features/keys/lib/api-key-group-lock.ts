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

import type { LockedUserGroup } from '@/lib/api'
import { formatNumber } from '@/lib/format'

/** Why a recharge-gated group is locked for the current user. */
export type ApiKeyGroupLock = Pick<
  LockedUserGroup,
  'min_topup' | 'current_topup' | 'whitelist_only'
>

/** Explains what the user needs before a locked group can be used. */
export function getApiKeyGroupLockReason(
  t: TFunction,
  lock: ApiKeyGroupLock,
  locale: string | undefined
): string {
  if (lock.whitelist_only) return t('Only available to specific users')
  return t('Requires cumulative top-up of {{required}} (current {{current}})', {
    required: formatNumber(lock.min_topup, locale),
    current: formatNumber(lock.current_topup, locale),
  })
}
