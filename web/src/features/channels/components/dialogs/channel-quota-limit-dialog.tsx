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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { resetChannelQuotaLimitUsage, updateChannelQuotaLimit } from '../../api'
import {
  CHANNEL_QUOTA_LIMITS_QUERY_KEY,
  useChannelQuotaLimits,
} from '../../hooks/use-channel-quota-limits'
import { CHANNEL_QUOTA_LIMIT_FORM_ID } from '../../lib/channel-quota-limit-form'
import type { Channel, UpdateChannelQuotaLimitParams } from '../../types'
import { ChannelQuotaLimitForm } from './channel-quota-limit-form'

type ChannelQuotaLimitDialogProps = {
  channel: Channel
  onOpenChange: (open: boolean) => void
}

const QUOTA_DISPLAY_OPTIONS = {
  digitsLarge: 2,
  digitsSmall: 4,
  abbreviate: false,
} as const

export function ChannelQuotaLimitDialog(props: ChannelQuotaLimitDialogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const quotaLimits = useChannelQuotaLimits()
  const limit = quotaLimits.data?.get(props.channel.id)
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)

  const saveMutation = useMutation({
    mutationFn: async (payload: UpdateChannelQuotaLimitParams) => {
      const response = await updateChannelQuotaLimit(props.channel.id, payload)
      if (!response.success) {
        throw createServerError(response, t('Failed to save quota limit'))
      }
      return payload
    },
    onSuccess: async (payload) => {
      toast.success(
        payload.limit_quota > 0 || payload.limit_count > 0
          ? t('Quota limit saved')
          : t('Quota limit removed')
      )
      await queryClient.invalidateQueries({
        queryKey: CHANNEL_QUOTA_LIMITS_QUERY_KEY,
      })
      props.onOpenChange(false)
    },
    onError: (error) => {
      handleServerError(error, t('Failed to save quota limit'))
    },
  })

  const resetMutation = useMutation({
    mutationFn: async () => {
      const response = await resetChannelQuotaLimitUsage(props.channel.id)
      if (!response.success) {
        throw createServerError(response, t('Failed to reset usage'))
      }
      return response
    },
    onSuccess: async () => {
      toast.success(t('Usage reset'))
      setResetConfirmOpen(false)
      await queryClient.invalidateQueries({
        queryKey: CHANNEL_QUOTA_LIMITS_QUERY_KEY,
      })
    },
    onError: (error) => {
      handleServerError(error, t('Failed to reset usage'))
    },
  })

  let body = (
    <ChannelQuotaLimitForm
      limit={limit}
      onSubmit={(payload) => saveMutation.mutate(payload)}
    />
  )
  if (quotaLimits.isPending) {
    body = <LoadingState size='sm' className='min-h-[160px]' />
  } else if (quotaLimits.isError) {
    body = (
      <ErrorState
        className='min-h-[160px]'
        description={t('Failed to load quota limits')}
        onRetry={() => void quotaLimits.refetch()}
      />
    )
  }

  return (
    <>
      <Dialog
        open
        onOpenChange={props.onOpenChange}
        title={t('Quota limit')}
        description={t(
          'Channel "{{name}}" is skipped by channel selection once its usage reaches the limit.',
          { name: props.channel.name }
        )}
        contentClassName='sm:max-w-lg'
        bodyClassName='space-y-4'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => props.onOpenChange(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form={CHANNEL_QUOTA_LIMIT_FORM_ID}
              disabled={!quotaLimits.isSuccess || saveMutation.isPending}
            >
              {saveMutation.isPending && (
                <Loader2 className='size-4 animate-spin' aria-hidden='true' />
              )}
              {t('Save')}
            </Button>
          </>
        }
      >
        {limit && (
          <section
            aria-label={t('Current usage')}
            className='bg-muted/40 flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3'
          >
            <div className='min-w-0 space-y-1'>
              <div className='text-muted-foreground text-xs font-medium'>
                {t('Current usage')}
              </div>
              <div className='flex flex-wrap items-center gap-2 text-sm font-medium tabular-nums'>
                {limit.limit_quota > 0 && (
                  <span>
                    {formatQuotaWithCurrency(
                      limit.current_used,
                      QUOTA_DISPLAY_OPTIONS
                    )}{' '}
                    /{' '}
                    {formatQuotaWithCurrency(
                      limit.limit_quota,
                      QUOTA_DISPLAY_OPTIONS
                    )}
                  </span>
                )}
                {limit.limit_count > 0 && (
                  <span>
                    {t('Requests {{used}} / {{limit}}', {
                      used: formatNumber(limit.current_used_count, locale),
                      limit: formatNumber(limit.limit_count, locale),
                    })}
                  </span>
                )}
                {limit.exhausted && (
                  <StatusBadge
                    label={t('Limit Reached')}
                    variant='warning'
                    size='sm'
                    copyable={false}
                  />
                )}
                {limit.daily_reset && (
                  <StatusBadge
                    label={t('Daily')}
                    variant='info'
                    size='sm'
                    copyable={false}
                  />
                )}
              </div>
            </div>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setResetConfirmOpen(true)}
              disabled={resetMutation.isPending}
            >
              <RotateCcw className='size-4' aria-hidden='true' />
              {t('Reset usage')}
            </Button>
          </section>
        )}
        {body}
      </Dialog>

      <ConfirmDialog
        open={resetConfirmOpen}
        onOpenChange={setResetConfirmOpen}
        title={t('Reset usage')}
        desc={t(
          'Reset the usage counted against this limit to 0? The channel becomes selectable again until it reaches the limit.'
        )}
        confirmText={t('Reset')}
        isLoading={resetMutation.isPending}
        handleConfirm={() => resetMutation.mutate()}
      />
    </>
  )
}
