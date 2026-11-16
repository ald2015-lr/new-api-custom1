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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { getGeminiFreeKeyUsage } from '../../api'
import { GEMINI_FREE_KEY_COOLDOWN_REASON_LABELS } from '../../constants'
import { geminiFreeKeyModelState } from '../../lib/gemini-free-key'
import type { GeminiFreeKeyUsageKey } from '../../types'

type GeminiFreeKeyUsageDialogProps = {
  channelId: number
  channelName: string
  onClose: () => void
}

// Mounted only while the dialog is open; unmount aborts the query.
export function GeminiFreeKeyUsageDialog(props: GeminiFreeKeyUsageDialogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = useQuery({
    queryKey: ['channels', props.channelId, 'gemini-free-key-usage'],
    queryFn: ({ signal }) => getGeminiFreeKeyUsage(props.channelId, signal),
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const usage = query.data
  const nowSeconds = Date.now() / 1000
  const usedOf = (used: number, limit: number) =>
    `${formatNumber(used, locale)} / ${
      limit < 0 ? t('Unlimited') : formatNumber(limit, locale)
    }`

  const renderKey = (key: GeminiFreeKeyUsageKey) => {
    const title = t('Key #{{index}}', { index: key.index + 1 })
    return (
      <section key={key.index} aria-label={title} className='space-y-2'>
        <h3 className='flex flex-wrap items-center gap-2 text-sm font-medium'>
          <span>{title}</span>
          <span className='text-muted-foreground font-mono'>
            {key.key_hint}
          </span>
          {!key.enabled && (
            <StatusBadge
              label={t('Disabled')}
              variant='neutral'
              copyable={false}
            />
          )}
        </h3>
        <StaticDataTable
          data={key.models}
          getRowKey={(model) => model.model}
          emptyContent={t('No requests today')}
          emptyClassName='text-muted-foreground py-4 text-center text-sm'
          columns={[
            {
              id: 'model',
              header: t('Model'),
              cellClassName: 'font-mono text-xs',
              cell: (model) => model.model,
            },
            {
              id: 'rpm',
              header: t('Requests / min'),
              cell: (model) => usedOf(model.rpm_used, model.rpm_limit),
            },
            {
              id: 'tpm',
              header: t('Tokens / min'),
              cell: (model) => usedOf(model.tpm_used, model.tpm_limit),
            },
            {
              id: 'rpd',
              header: t('Requests today'),
              cell: (model) => usedOf(model.rpd_used, model.rpd_limit),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (model) => {
                const state = geminiFreeKeyModelState(
                  key,
                  model,
                  usage?.next_reset_at ?? 0,
                  nowSeconds
                )
                if (!state.reason) {
                  return (
                    <StatusBadge
                      label={t('Available')}
                      variant='success'
                      copyable={false}
                    />
                  )
                }
                return (
                  <div className='flex flex-col items-start gap-1'>
                    <StatusBadge
                      label={t(
                        GEMINI_FREE_KEY_COOLDOWN_REASON_LABELS[state.reason]
                      )}
                      variant='warning'
                      copyable={false}
                    />
                    {state.until > 0 && (
                      <span className='text-muted-foreground text-xs'>
                        {t('Paused until {{time}}', {
                          time: formatTimestampToDate(state.until),
                        })}
                      </span>
                    )}
                  </div>
                )
              },
            },
          ]}
        />
      </section>
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Free key usage')}
      description={props.channelName}
      contentClassName='sm:max-w-4xl'
      bodyClassName='space-y-4'
      footer={
        <Button variant='outline' onClick={props.onClose}>
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='space-y-0.5 text-sm'>
          <p>{t('Daily quotas reset at midnight Pacific time')}</p>
          {usage && (
            <p className='text-muted-foreground'>
              {t('Next reset: {{time}}', {
                time: formatTimestampToDate(usage.next_reset_at),
              })}
            </p>
          )}
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </div>
      {usage && !usage.enabled && (
        <Alert>
          <AlertDescription>
            {t(
              'Free key mode is not saved as on for this channel yet. Counting starts once it is saved.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          title={t('Failed to load free key usage')}
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {usage?.keys.length === 0 && <EmptyState title={t('No keys')} />}
      {usage?.keys.map(renderKey)}
    </Dialog>
  )
}
