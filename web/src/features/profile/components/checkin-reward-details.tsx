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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { BonusExpiryText } from '@/features/wallet/components/bonus-expiry-text'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import type { CheckinResponse, CheckinStatusResponse } from '../types'

/**
 * The paid multiplier and expiry rules that apply to the user's check-in
 * rewards, as items of the card's rules list.
 */
export function CheckinRewardRuleItems(props: {
  status: CheckinStatusResponse
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const multiplier = formatNumber(props.status.paid_multiplier, locale)
  const expireDays = props.status.is_paid_user
    ? props.status.paid_expire_days
    : props.status.free_expire_days

  let paidRule = ''
  if (props.status.paid_multiplier > 1) {
    paidRule =
      props.status.paid_threshold_quota > 0
        ? t(
            'Paid users (cumulative recharge ≥ {{threshold}}) get ×{{multiplier}} check-in rewards',
            {
              threshold: formatQuotaWithCurrency(
                props.status.paid_threshold_quota
              ),
              multiplier,
            }
          )
        : t('Users who have recharged get ×{{multiplier}} check-in rewards', {
            multiplier,
          })
  }

  let expiryRule = ''
  if (expireDays > 0) {
    expiryRule = props.status.expire_only_when_unused
      ? t('Rewards expire if unused for {{days}} days', { days: expireDays })
      : t('Rewards expire after {{days}} days', { days: expireDays })
  }

  return (
    <>
      {paidRule && <li>{paidRule}</li>}
      {expiryRule && (
        <>
          <li>{expiryRule}</li>
          <li>
            {t(
              'Rewards are spent before the rest of your balance; only the unspent part is deducted when they expire'
            )}
          </li>
        </>
      )}
    </>
  )
}

/** What the check-in just granted: amount, paid multiplier and expiry. */
export function CheckinRewardResult(props: { result: CheckinResponse }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  return (
    <div className='mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
      <span className='font-semibold tabular-nums'>
        +{formatQuotaWithCurrency(props.result.quota_awarded)}
      </span>
      {props.result.paid_multiplier_applied && (
        <Badge variant='secondary'>
          {t('Paid user ×{{multiplier}}', {
            multiplier: formatNumber(props.result.paid_multiplier, locale),
          })}
        </Badge>
      )}
      <BonusExpiryText
        expiresAt={props.result.expires_at}
        expireDays={props.result.expire_days}
        expireOnlyWhenUnused={props.result.expire_only_when_unused}
      />
    </div>
  )
}
