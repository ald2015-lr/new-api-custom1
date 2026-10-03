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
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  BadgeListCell,
  StaticDataTable,
  StaticRowActions,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import {
  formatGroupAccessUser,
  type GroupAccessRule,
} from './group-access-rules'

type GroupAccessRulesTableProps = {
  rules: GroupAccessRule[]
  onEdit: (rule: GroupAccessRule) => void
  onDelete: (rule: GroupAccessRule) => void
}

export function GroupAccessRulesTable(props: GroupAccessRulesTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  return (
    <StaticDataTable
      data={props.rules}
      getRowKey={(rule) => rule.group}
      emptyContent={
        <EmptyState
          icon={ShieldCheck}
          className='min-h-40'
          title={t('No group access rules')}
          description={t('Every group stays open to the users who can see it.')}
        />
      }
      columns={[
        {
          id: 'group',
          header: t('Group'),
          cell: (rule) => <GroupBadge group={rule.group} />,
        },
        {
          id: 'min-topup',
          header: t('Minimum cumulative top-up'),
          cell: (rule) =>
            rule.min_topup > 0 ? (
              <span className='font-mono'>
                {formatNumber(rule.min_topup, locale)}
              </span>
            ) : (
              <span className='text-muted-foreground'>
                {t('Whitelist only')}
              </span>
            ),
        },
        {
          id: 'users',
          header: t('Whitelisted users'),
          cell: (rule) => (
            <BadgeListCell
              max={3}
              expandable
              expandLabel={t('Show all users')}
              items={rule.users.map((user) => (
                <StatusBadge
                  key={formatGroupAccessUser(user)}
                  label={formatGroupAccessUser(user)}
                  variant='neutral'
                  copyable={false}
                  className='font-mono'
                />
              ))}
            />
          ),
        },
        {
          id: 'actions',
          header: t('Actions'),
          className: 'text-right',
          cellClassName: 'text-right',
          cell: (rule) => (
            <StaticRowActions
              editLabel={t('Edit')}
              deleteLabel={t('Delete')}
              menuLabel={t('Open menu')}
              onEdit={() => props.onEdit(rule)}
              onDelete={() => props.onDelete(rule)}
            />
          ),
        },
      ]}
    />
  )
}
