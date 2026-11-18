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

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'

import type { InviteAbusePreview } from '../types'

type InviteAbusePreviewDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  preview: InviteAbusePreview | undefined
}

/** Totals and top inviters the invite abuse rule would disable. */
export function InviteAbusePreviewDialog(props: InviteAbusePreviewDialogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const preview = props.preview
  const items = preview?.items ?? []

  const totals = preview
    ? [
        {
          label: t('Inviters matching the rule'),
          value: preview.abusive_inviters,
        },
        {
          label: t('Inviters to disable'),
          value: preview.inviters_to_disable,
        },
        {
          label: t('Invited users to disable'),
          value: preview.invitees_to_disable,
        },
      ]
    : []

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Invite abuse preview')}
      description={
        preview
          ? t(
              'Threshold: {{threshold}} invited users · minimum redeemed quota: {{amount}}',
              {
                threshold: formatNumber(preview.threshold, locale),
                amount: formatQuota(preview.min_redeem_quota),
              }
            )
          : undefined
      }
      contentClassName='sm:max-w-3xl'
      bodyClassName='space-y-4'
    >
      <dl className='grid gap-3 sm:grid-cols-3'>
        {totals.map((total) => (
          <div key={total.label} className='rounded-lg border px-3 py-2'>
            <dt className='text-muted-foreground text-xs'>{total.label}</dt>
            <dd className='text-lg font-semibold tabular-nums'>
              {formatNumber(total.value, locale)}
            </dd>
          </div>
        ))}
      </dl>

      {items.length === 0 ? (
        <p className='text-muted-foreground text-sm'>
          {t('No inviter matches the rule.')}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('ID')}</TableHead>
              <TableHead>{t('Username')}</TableHead>
              <TableHead>{t('Status')}</TableHead>
              <TableHead className='text-right'>{t('Invited Users')}</TableHead>
              <TableHead className='text-right'>
                {t('Invited users to disable')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => (
              <TableRow key={item.id}>
                <TableCell className='tabular-nums'>{item.id}</TableCell>
                <TableCell>
                  <div>{item.username}</div>
                  {item.display_name && item.display_name !== item.username ? (
                    <div
                      data-table-text='secondary'
                      className='text-muted-foreground'
                    >
                      {item.display_name}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell>
                  <StatusBadge
                    label={item.status === 1 ? t('Enabled') : t('Disabled')}
                    variant={item.status === 1 ? 'success' : 'neutral'}
                    copyable={false}
                    className='font-normal'
                  />
                </TableCell>
                <TableCell className='text-right tabular-nums'>
                  {formatNumber(item.invite_count, locale)}
                </TableCell>
                <TableCell className='text-right tabular-nums'>
                  {formatNumber(item.invitees_to_disable, locale)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {preview?.truncated ? (
        <p className='text-muted-foreground text-xs'>
          {t('Only the {{count}} inviters with the most invites are listed.', {
            count: items.length,
          })}
        </p>
      ) : null}
    </Dialog>
  )
}
