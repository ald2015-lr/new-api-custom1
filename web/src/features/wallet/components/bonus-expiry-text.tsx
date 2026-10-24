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

import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

interface BonusExpiryTextProps {
  /** Unix seconds of the current deadline; 0 = the reward never expires */
  expiresAt: number
  expireDays: number
  /** The deadline restarts with every use */
  expireOnlyWhenUnused: boolean
  /**
   * What counts as a use: wallet spending, or transferring the invitation
   * balance for inviter rewards.
   */
  use?: 'spend' | 'transfer'
  className?: string
}

/**
 * When an expiring reward expires: the rolling rule when it applies, then the
 * current deadline.
 */
export function BonusExpiryText(props: BonusExpiryTextProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  if (props.expiresAt <= 0) {
    return (
      <span className={cn('text-muted-foreground text-xs', props.className)}>
        {t('Never expires')}
      </span>
    )
  }

  const date = new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(dayjs.unix(props.expiresAt).toDate())
  let rollingRule = ''
  if (props.expireOnlyWhenUnused) {
    rollingRule =
      props.use === 'transfer'
        ? t('Expires if not transferred for {{days}} days', {
            days: props.expireDays,
          })
        : t('Expires if unused for {{days}} days', { days: props.expireDays })
  }

  return (
    <span
      className={cn(
        'text-muted-foreground flex flex-wrap gap-x-2 gap-y-0.5 text-xs',
        props.className
      )}
    >
      {rollingRule && <span>{rollingRule}</span>}
      <span>{t('Current expiry: {{date}}', { date })}</span>
    </span>
  )
}
