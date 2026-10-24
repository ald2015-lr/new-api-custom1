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
import { Hourglass } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TitledCard } from '@/components/ui/titled-card'
import { formatQuota } from '@/lib/format'

import { BONUS_SOURCE_LABELS } from '../constants'
import type { BonusGrant, SelfBonusSummary } from '../types'
import { BonusExpiryText } from './bonus-expiry-text'

interface BonusGrantListProps {
  /** The rows the server listed, in the order the rewards are used */
  grants: BonusGrant[]
  /** How many rewards have a remainder, also those the server did not list */
  totalCount: number
  /** What counts as a use of these rewards */
  use: 'spend' | 'transfer'
}

/** Rows shown before the list is expanded; the rest stays behind a toggle. */
const COLLAPSED_GRANT_ROWS = 5

/**
 * One row per expiring reward: source, unspent part of the amount, expiry.
 * Rows come in the order the rewards are used, top row first. Long lists
 * expand on demand, so daily check-in rewards cannot push the rest of the
 * page down.
 */
export function BonusGrantList(props: BonusGrantListProps) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const collapsible = props.grants.length > COLLAPSED_GRANT_ROWS
  const unlisted = props.totalCount > props.grants.length
  const grants =
    collapsible && !expanded
      ? props.grants.slice(0, COLLAPSED_GRANT_ROWS)
      : props.grants
  const showAllLabel = unlisted
    ? t('Show the first {{count}} rewards', { count: props.grants.length })
    : t('Show all {{count}} rewards', { count: props.grants.length })

  const list = (
    <ul className='divide-y'>
      {grants.map((grant) => (
        <li
          key={grant.id}
          className='flex flex-col gap-1 py-2.5 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between sm:gap-4'
        >
          <div className='min-w-0 space-y-0.5'>
            <div className='text-sm font-medium'>
              {t(BONUS_SOURCE_LABELS[grant.source] ?? grant.source)}
            </div>
            <BonusExpiryText
              expiresAt={grant.expires_at}
              expireDays={grant.expire_days}
              expireOnlyWhenUnused={grant.expire_only_when_unused}
              use={props.use}
            />
          </div>
          <div className='shrink-0 font-mono text-sm tabular-nums'>
            <span className='font-semibold'>
              {formatQuota(grant.remaining)}
            </span>
            <span className='text-muted-foreground'>
              {' / '}
              {formatQuota(grant.amount)}
            </span>
          </div>
        </li>
      ))}
    </ul>
  )
  if (!collapsible && !unlisted) {
    return list
  }
  return (
    <div className='space-y-2'>
      {list}
      {unlisted && (
        <p className='text-muted-foreground text-xs'>
          {t('Only the first {{shown}} of {{count}} rewards are listed.', {
            shown: props.grants.length,
            count: props.totalCount,
          })}
        </p>
      )}
      {collapsible && (
        <Button
          variant='ghost'
          size='sm'
          className='w-full'
          aria-expanded={expanded}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? t('Collapse') : showAllLabel}
        </Button>
      )}
    </div>
  )
}

interface BonusGrantsCardProps {
  /** null or undefined once loaded: the summary could not be read */
  bonus: SelfBonusSummary | null | undefined
  loading?: boolean
}

/** Whether a check-in or invitation reward granted from now on can expire. */
function rewardsCanExpire(bonus: SelfBonusSummary): boolean {
  const checkinExpires =
    bonus.checkin.enabled &&
    (bonus.checkin.free_expire_days > 0 || bonus.checkin.paid_expire_days > 0)
  return checkinExpires || bonus.invite.expire_days > 0
}

/**
 * The expiring check-in and invitation rewards inside the wallet balance.
 * Hidden when there are none and the settings make every reward permanent.
 */
export function BonusGrantsCard(props: BonusGrantsCardProps) {
  const { t } = useTranslation()

  if (!props.loading && !props.bonus) {
    return null
  }

  const grants = props.bonus?.wallet_grants ?? []
  if (props.bonus && grants.length === 0 && !rewardsCanExpire(props.bonus)) {
    return null
  }
  // Over every reward, also those beyond the listed rows.
  const totalRemaining = props.bonus?.wallet_totals.remaining ?? 0

  let content = (
    <BonusGrantList
      grants={grants}
      totalCount={props.bonus?.wallet_totals.count ?? grants.length}
      use='spend'
    />
  )
  if (props.loading) {
    content = (
      <div className='space-y-3'>
        <Skeleton className='h-10 w-full' />
        <Skeleton className='h-10 w-full' />
      </div>
    )
  } else if (grants.length === 0) {
    content = (
      <EmptyState
        icon={Hourglass}
        title={t('No expiring rewards')}
        description={t(
          'Check-in and invitation rewards that expire will be listed here.'
        )}
        className='min-h-0 p-4 md:p-6'
      />
    )
  }

  return (
    <TitledCard
      title={t('Expiring rewards')}
      description={t(
        'Rewards are spent before the rest of your balance, in the order listed. Only the unspent part is deducted when a reward expires.'
      )}
      icon={<Hourglass />}
      iconTone='warning'
      disableHoverEffect
      action={
        totalRemaining > 0 ? (
          <div className='text-left sm:text-right'>
            <div className='text-muted-foreground text-[11px] font-medium tracking-wider uppercase'>
              {t('Unspent rewards')}
            </div>
            <div className='font-mono text-lg font-semibold tabular-nums'>
              {formatQuota(totalRemaining)}
            </div>
          </div>
        ) : undefined
      }
    >
      {content}
    </TitledCard>
  )
}
