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
import { Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { GroupAccessRequirement } from '../types'

type GroupAccessBadgeProps = {
  requirement?: GroupAccessRequirement
  className?: string
}

/** Marks a recharge-gated group with what it takes to use it. */
export function GroupAccessBadge(props: GroupAccessBadgeProps) {
  const { t, i18n } = useTranslation()
  if (!props.requirement) return null

  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const label = props.requirement.whitelist_only
    ? t('Restricted')
    : t('Top-up ≥ {{amount}}', {
        amount: formatNumber(props.requirement.min_topup, locale),
      })

  return (
    <StatusBadge
      data-group-access-badge=''
      label={label}
      icon={Lock}
      variant='warning'
      copyable={false}
      className={props.className}
    />
  )
}
