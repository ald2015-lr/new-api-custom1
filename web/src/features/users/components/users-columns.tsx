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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ActivityTimeCell } from '@/components/activity-time-cell'
import { BadgeCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { LongText } from '@/components/long-text'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getCurrencyDisplay } from '@/lib/currency'
import { formatQuota } from '@/lib/format'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  INVITER_FILTER_ID,
  USER_STATUS,
  USER_STATUSES,
  USER_ROLES,
  isUserDeleted,
} from '../constants'
import type { User } from '../types'
import { DataTableRowActions } from './data-table-row-actions'
import { UserQuotaCell } from './user-quota-cell'

export function useUsersColumns(): ColumnDef<User>[] {
  const { t } = useTranslation()
  const currencyConfig = useSystemConfigStore((state) => state.config.currency)
  const { meta: currency } = getCurrencyDisplay()
  const quotaUnit = currency.kind === 'tokens' ? t('Tokens') : currency.symbol
  return useMemo<ColumnDef<User>[]>(
    () => [
      {
        id: 'select',
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected()}
            indeterminate={table.getIsSomePageRowsSelected()}
            onCheckedChange={(value) =>
              table.toggleAllPageRowsSelected(!!value)
            }
            aria-label={t('Select all')}
            className='translate-y-[2px]'
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            onCheckedChange={(value) => row.toggleSelected(!!value)}
            aria-label={t('Select row')}
            className='translate-y-[2px]'
          />
        ),
        enableSorting: false,
        enableHiding: false,
        size: 40,
      },
      {
        accessorKey: 'id',
        header: t('ID'),
        cell: ({ row }) => {
          return (
            <TableId
              value={row.getValue('id') as number}
              className='w-[60px] [font-family:inherit] text-sm'
            />
          )
        },
        size: 80,
        meta: { mobileOrder: 10 },
      },
      {
        accessorKey: 'username',
        header: t('Username'),
        cell: ({ row }) => {
          const username = row.getValue('username') as string
          const displayName = row.original.display_name
          const remark = row.original.remark

          return (
            <div className='flex min-w-[160px] flex-col gap-1'>
              <div className='flex items-center gap-2'>
                <LongText className='max-w-[140px] text-sm font-normal'>
                  {username}
                </LongText>
                {remark && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <StatusBadge
                          variant='success'
                          copyable={false}
                          className='font-normal'
                        />
                      }
                    >
                      <LongText className='max-w-[80px]'>{remark}</LongText>
                    </TooltipTrigger>
                    <TooltipContent>
                      <p className='text-xs'>{remark}</p>
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
              {displayName && displayName !== username && (
                <div
                  data-table-text='secondary'
                  className='text-muted-foreground max-w-[180px] text-xs font-normal'
                >
                  <LongText>{displayName}</LongText>
                </div>
              )}
            </div>
          )
        },
        enableHiding: false,
        size: 220,
        meta: { mobileTitle: true },
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const user = row.original
          const requestCount = user.request_count

          const statusConfig = isUserDeleted(user)
            ? USER_STATUSES[USER_STATUS.DELETED]
            : USER_STATUSES[user.status as keyof typeof USER_STATUSES]

          if (!statusConfig) {
            return null
          }

          const statusBadge = (
            <Tooltip>
              <TooltipTrigger render={<div className='-ml-1.5 cursor-help' />}>
                <StatusBadge
                  label={t(statusConfig.labelKey)}
                  variant={
                    isUserDeleted(user) ? 'neutral' : statusConfig.variant
                  }
                  copyable={false}
                  className='font-normal'
                />
              </TooltipTrigger>
              <TooltipContent>
                <p className='text-xs'>
                  {t('Requests:')} {requestCount.toLocaleString()}
                </p>
              </TooltipContent>
            </Tooltip>
          )
          if (!user.invite_abuse_ban) return statusBadge

          return (
            <div className='flex flex-wrap items-center gap-1.5'>
              {statusBadge}
              <Tooltip>
                <TooltipTrigger render={<div className='cursor-help' />}>
                  <StatusBadge
                    label={t('Invite abuse')}
                    variant='danger'
                    copyable={false}
                    className='font-normal'
                  />
                </TooltipTrigger>
                <TooltipContent>
                  <p className='max-w-64 text-xs'>
                    {user.invite_abuse_ban === 'inviter'
                      ? t(
                          'Disabled automatically: none of the users this account invited has recharged.'
                        )
                      : t(
                          'Disabled automatically: the users invited by the same inviter have not recharged.'
                        )}{' '}
                    {t(
                      'Enabling the account again exempts it from automatic invite bans.'
                    )}
                  </p>
                </TooltipContent>
              </Tooltip>
            </div>
          )
        },
        filterFn: (row, id, value) => {
          return value.includes(String(row.getValue(id)))
        },
        enableSorting: false,
        size: 120,
        meta: { mobileBadge: true },
      },
      {
        id: 'quota',
        accessorKey: 'quota',
        header: `${t('Available Balance')} (${quotaUnit})`,
        cell: ({ row }) => {
          const user = row.original
          return <UserQuotaCell remaining={user.quota} used={user.used_quota} />
        },
        size: 180,
        minSize: 160,
        meta: { mobileOrder: 40 },
      },
      {
        accessorKey: 'group',
        header: t('User Group'),
        cell: ({ row }) => {
          const group = row.getValue('group') as string
          return (
            <BadgeCell>
              <GroupBadge group={group} className='font-normal' />
            </BadgeCell>
          )
        },
        filterFn: (row, id, value) => {
          const group = String(
            row.getValue(id) || t('User Group')
          ).toLowerCase()
          const searchValue = String(value).toLowerCase()
          return group.includes(searchValue)
        },
        size: 140,
        meta: { mobileOrder: 30 },
      },
      {
        accessorKey: 'role',
        header: t('Role'),
        cell: ({ row }) => {
          const roleValue = row.getValue('role') as number
          const roleConfig = USER_ROLES[roleValue as keyof typeof USER_ROLES]

          if (!roleConfig) {
            return null
          }

          return <span className='text-sm'>{t(roleConfig.labelKey)}</span>
        },
        filterFn: (row, id, value) => {
          return value.includes(String(row.getValue(id)))
        },
        enableSorting: false,
        size: 120,
        meta: { mobileOrder: 20 },
      },
      {
        // Sorting by this column orders users by how many users they invited.
        id: 'invite_count',
        accessorFn: (user) => user.invite_count ?? 0,
        header: t('Invite Info'),
        cell: ({ row, table }) => {
          const user = row.original
          const inviteCount = user.invite_count ?? 0
          const affHistoryQuota = user.aff_history_quota || 0
          const inviterId = user.inviter_id || 0

          if (inviteCount === 0 && affHistoryQuota === 0 && inviterId === 0) {
            return <span className='text-muted-foreground text-sm'>—</span>
          }

          // Show exactly the users invited by `id`, dropping other filters.
          const filterByInviter = (id: number) => {
            table.setGlobalFilter('')
            table.setColumnFilters([
              { id: INVITER_FILTER_ID, value: String(id) },
            ])
          }
          const invitedText = t('Invited {{count}} users', {
            count: inviteCount,
          })

          return (
            <div
              data-table-text='secondary'
              className='min-w-0 space-y-1 text-xs font-normal'
            >
              {(inviteCount > 0 || affHistoryQuota !== 0) && (
                <LongText>
                  {inviteCount > 0 ? (
                    <Button
                      type='button'
                      variant='link'
                      className='h-auto p-0 text-xs font-normal'
                      title={t('Show the users this user invited')}
                      onClick={() => filterByInviter(user.id)}
                    >
                      {invitedText}
                    </Button>
                  ) : (
                    invitedText
                  )}{' '}
                  · {t('Earnings')}:{' '}
                  <span className='tabular-nums'>
                    {formatQuota(affHistoryQuota)}
                  </span>
                </LongText>
              )}
              {inviterId > 0 && (
                <LongText>
                  <Button
                    type='button'
                    variant='link'
                    className='text-muted-foreground h-auto p-0 text-xs font-normal'
                    title={t('Show the users this inviter invited')}
                    onClick={() => filterByInviter(inviterId)}
                  >
                    {t('Inviter ID: {{id}}', { id: inviterId })}
                  </Button>
                </LongText>
              )}
            </div>
          )
        },
        size: 240,
        meta: { mobileHidden: true },
      },
      {
        accessorKey: 'created_at',
        header: t('Time'),
        cell: ({ row }) => (
          <ActivityTimeCell
            createdAt={row.original.created_at ?? 0}
            lastAt={row.original.last_login_at ?? 0}
            lastLabel={t('Last Login')}
            format='absolute'
          />
        ),
        size: 260,
        minSize: 240,
        meta: { mobileHidden: true },
      },
      {
        id: 'actions',
        header: () => t('Actions'),
        cell: ({ row }) => <DataTableRowActions row={row} />,
        meta: { pinned: 'right' as const },
      },
    ],
    // formatQuota reads the currency configuration from the store.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [t, quotaUnit, currencyConfig]
  )
}
