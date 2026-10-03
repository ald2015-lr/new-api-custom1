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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { getCurrencyLabel } from '@/lib/currency'
import { getEditableQuotaStep } from '@/lib/format'

import {
  CHANNEL_QUOTA_LIMIT_FORM_ID,
  getChannelQuotaLimitFormDefaults,
  getChannelQuotaLimitFormSchema,
  toChannelQuotaLimitPayload,
  type ChannelQuotaLimitFormValues,
} from '../../lib/channel-quota-limit-form'
import type {
  ChannelQuotaLimit,
  UpdateChannelQuotaLimitParams,
} from '../../types'

type ChannelQuotaLimitFormProps = {
  /** Existing limit; the form keeps its initial values from first render. */
  limit: ChannelQuotaLimit | undefined
  onSubmit: (payload: UpdateChannelQuotaLimitParams) => void
}

export function ChannelQuotaLimitForm(props: ChannelQuotaLimitFormProps) {
  const { t } = useTranslation()
  const schema = useMemo(() => getChannelQuotaLimitFormSchema(t), [t])
  const form = useForm<ChannelQuotaLimitFormValues>({
    resolver: zodResolver(schema),
    defaultValues: getChannelQuotaLimitFormDefaults(props.limit),
  })
  const currencyLabel = getCurrencyLabel()

  const handleSubmit = (values: ChannelQuotaLimitFormValues) => {
    // The displayed amount may be rounded; keep the exact stored quota unless
    // the operator edited it.
    const amountEdited = form.getFieldState('limit_amount').isDirty
    const unchangedLimitQuota =
      props.limit && !amountEdited ? props.limit.limit_quota : undefined
    props.onSubmit(toChannelQuotaLimitPayload(values, unchangedLimitQuota))
  }

  return (
    <Form {...form}>
      <form
        id={CHANNEL_QUOTA_LIMIT_FORM_ID}
        onSubmit={form.handleSubmit(handleSubmit)}
        className='space-y-4'
        noValidate
      >
        <div className='grid items-start gap-4 sm:grid-cols-2'>
          <FormField
            control={form.control}
            name='limit_amount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>
                  {t('Usage limit ({{currency}})', { currency: currencyLabel })}
                </FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    inputMode='decimal'
                    min={0}
                    step={getEditableQuotaStep()}
                    placeholder={t('Unlimited')}
                  />
                </FormControl>
                <FormDescription>
                  {t('Leave empty or enter 0 to remove the limit')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='limit_count'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Request limit')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    inputMode='numeric'
                    min={0}
                    step={1}
                    placeholder={t('Unlimited')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Maximum number of billed requests; 0 or empty = no limit. The channel stops when either limit is reached.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>

        <FormField
          control={form.control}
          name='daily_reset'
          render={({ field }) => (
            <FormItem className='flex items-center justify-between gap-4'>
              <div className='space-y-0.5'>
                <FormLabel>{t('Reset daily')}</FormLabel>
                <FormDescription>
                  {t('Usage resets at 00:00 every day')}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  checked={field.value}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='message'
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {t('Error message when the limit is reached')}
              </FormLabel>
              <FormControl>
                <Input
                  {...field}
                  placeholder={t(
                    'This group has reached its usage limit, please switch to another group'
                  )}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Returned to API callers when every channel in the group is unavailable. Leave empty to use the default message.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </form>
    </Form>
  )
}
