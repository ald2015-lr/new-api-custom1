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
import { Gauge } from 'lucide-react'
import { memo, useState } from 'react'
import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import {
  GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT,
  GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT,
} from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import {
  isValidGeminiFreeKeyMaxAttempts,
  validateGeminiFreeKeyModelLimitRows,
} from '../lib/gemini-free-key'
import { GeminiFreeKeyUsageDialog } from './dialogs/gemini-free-key-usage-dialog'
import { GeminiFreeKeyModelLimitsEditor } from './gemini-free-key-model-limits-editor'

/**
 * Free key mode of a Gemini (AI Studio) channel. The settings lock without
 * sensitive channel write permission; the usage of a saved channel stays
 * viewable. Memoized, and given the form instead of reading its context:
 * the channel drawer and the form context change on every keystroke, and
 * the model limits table is large.
 */
export const GeminiFreeKeySettings = memo(
  function GeminiFreeKeySettings(props: {
    form: UseFormReturn<ChannelFormValues>
    channelId: number | null
    channelName: string
    locked: boolean
  }) {
    const { t } = useTranslation()
    const form = props.form
    const enabled =
      useWatch({ control: form.control, name: 'gemini_free_key_enabled' }) ===
      true
    const [usageOpen, setUsageOpen] = useState(false)
    const tokenCounterItems = [
      { value: 'count_tokens', label: t('countTokens API (accurate)') },
      { value: 'local', label: t('Local estimate (faster)') },
    ]
    // The settings below hide with the mode off. A value there that the
    // server would refuse goes back to its saved value, instead of blocking
    // the save with an error nobody can see.
    const resetRefusedSettings = () => {
      if (
        !isValidGeminiFreeKeyMaxAttempts(
          form.getValues('gemini_free_key_max_attempts')
        )
      ) {
        form.resetField('gemini_free_key_max_attempts')
      }
      const limits = form.getValues('gemini_free_key_model_limits') ?? []
      if (validateGeminiFreeKeyModelLimitRows(limits).length > 0) {
        form.resetField('gemini_free_key_model_limits')
      }
    }

    return (
      <div className='space-y-4'>
        <fieldset
          disabled={props.locked}
          className='space-y-4 disabled:opacity-60'
        >
          <FormField
            control={form.control}
            name='gemini_free_key_enabled'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4 px-4 py-3'>
                <div className='space-y-0.5'>
                  <FormLabel>{t('Free key mode')}</FormLabel>
                  <FormDescription>
                    {t(
                      "Applies the Google AI Studio free-tier limits to each model of each key: requests per minute (RPM), input tokens per minute (TPM) and requests per day (RPD). A key that uses up a model's quota is paused for that model; daily quotas come back at midnight Pacific time (16:00 Beijing time, 15:00 during daylight saving time). When no key is available, or a request has more tokens than the model's per-minute limit, the channel answers 429 at once and the system retries other channels."
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    disabled={props.locked}
                    checked={field.value === true}
                    onCheckedChange={(checked) => {
                      field.onChange(checked)
                      if (!checked) resetRefusedSettings()
                    }}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          {enabled && (
            <div className='space-y-4 px-4'>
              <FormField
                control={form.control}
                name='gemini_free_key_reveal_tier_errors'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-4'>
                    <div className='space-y-0.5'>
                      <FormLabel>{t('Hide free-tier errors')}</FormLabel>
                      <FormDescription>
                        {t(
                          "Users get a plain 429 rate-limit error instead of the upstream's free-tier error, and their logs do not show it either. Administrators still see the original error in the log details."
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      {/* A positive switch over the reveal setting: on hides
                        the errors, which an unset setting does. */}
                      <Switch
                        disabled={props.locked}
                        checked={field.value !== true}
                        onCheckedChange={(on) => field.onChange(!on)}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
              <div className='grid gap-4 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='gemini_free_key_token_counter'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Token counting')}</FormLabel>
                      <Select
                        disabled={props.locked}
                        items={tokenCounterItems}
                        onValueChange={field.onChange}
                        value={
                          field.value ?? GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT
                        }
                      >
                        <FormControl>
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            {tokenCounterItems.map((item) => (
                              <SelectItem key={item.value} value={item.value}>
                                {item.label}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='gemini_free_key_max_attempts'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Keys to try per request')}</FormLabel>
                      <FormControl>
                        {/* No min, max or step: the browser's own check would
                          stop the submit before the form shows its message
                          for the field. */}
                        <Input
                          type='number'
                          inputMode='numeric'
                          placeholder={String(
                            GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT
                          )}
                          disabled={props.locked}
                          name={field.name}
                          ref={field.ref}
                          onBlur={field.onBlur}
                          value={field.value ?? ''}
                          onChange={(e) =>
                            field.onChange(
                              e.target.value === ''
                                ? null
                                : Number(e.target.value)
                            )
                          }
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          '1 to 10; empty means 3. When a key answers 429, the request moves on to another key, never the same key twice.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
              <GeminiFreeKeyModelLimitsEditor
                control={form.control}
                disabled={props.locked}
              />
            </div>
          )}
        </fieldset>
        {enabled && props.channelId !== null && (
          <div className='px-4'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setUsageOpen(true)}
            >
              <Gauge aria-hidden />
              {t('View usage')}
            </Button>
          </div>
        )}
        {usageOpen && props.channelId !== null && (
          <GeminiFreeKeyUsageDialog
            channelId={props.channelId}
            channelName={props.channelName}
            onClose={() => setUsageOpen(false)}
          />
        )}
      </div>
    )
  }
)
