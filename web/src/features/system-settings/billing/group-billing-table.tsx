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
import { Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { GroupBadge } from '@/components/group-badge'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Switch } from '@/components/ui/switch'

import type { GroupBillingRow } from './free-groups'

type GroupBillingTableProps = {
  rows: GroupBillingRow[]
  isLoading: boolean
  disabled: boolean
  onChargeChange: (group: string, charge: boolean) => void
}

function ChargeStateLabel(props: { free: boolean }) {
  const { t } = useTranslation()
  if (props.free) {
    return (
      <StatusBadge
        data-free-group-badge=''
        label={t('Not charged')}
        variant='success'
        copyable={false}
      />
    )
  }
  return (
    <span className='text-muted-foreground text-sm'>{t('Charge users')}</span>
  )
}

export function GroupBillingTable(props: GroupBillingTableProps) {
  const { t } = useTranslation()

  const emptyContent = props.isLoading ? (
    <LoadingState className='min-h-40' size='sm' />
  ) : (
    <EmptyState
      icon={Users}
      className='min-h-40'
      title={t('No group found.')}
    />
  )

  return (
    <StaticDataTable
      data={props.rows}
      getRowKey={(row) => row.group}
      emptyContent={emptyContent}
      columns={[
        {
          id: 'group',
          header: t('Group'),
          cell: (row) => (
            <span className='flex min-w-0 flex-wrap items-center gap-1.5'>
              <GroupBadge group={row.group} />
              {row.missing && (
                <StatusBadge
                  label={t('Group no longer exists')}
                  variant='warning'
                  copyable={false}
                />
              )}
            </span>
          ),
        },
        {
          id: 'billing',
          header: t('Billing'),
          className: 'text-right',
          cellClassName: 'text-right',
          cell: (row) => (
            <span className='inline-flex items-center justify-end gap-2'>
              <ChargeStateLabel free={row.free} />
              <Switch
                checked={!row.free}
                onCheckedChange={(checked) =>
                  props.onChargeChange(row.group, checked)
                }
                disabled={props.disabled}
                aria-label={t('Charge users in group {{group}}', {
                  group: row.group,
                })}
              />
            </span>
          ),
        },
      ]}
    />
  )
}
