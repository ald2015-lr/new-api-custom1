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
import i18next from 'i18next'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

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
import { formatQuota } from '@/lib/format'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  quotaThresholdSchema,
  rewardExpireDaysSchema,
} from '../utils/reward-fields'

const multiplierError = () =>
  i18next.t(
    'Paid check-in multiplier must be a number between 1 and 100 with at most 2 decimal places'
  )

// The server keeps the multiplier to two decimals, the precision every page
// shows it with (setting/operation_setting/bonus_setting.go).
function hasAtMostTwoDecimals(value: number): boolean {
  return Math.abs(value * 100 - Math.round(value * 100)) < 1e-9
}

const schema = z.object({
  enabled: z.boolean(),
  minQuota: z.coerce.number().int().min(0),
  maxQuota: z.coerce.number().int().min(0),
  paidThresholdQuota: quotaThresholdSchema(),
  paidMultiplier: z.preprocess(
    (value) => (value === '' ? undefined : value),
    z.coerce
      .number({ error: multiplierError })
      .min(1, { error: multiplierError })
      .max(100, { error: multiplierError })
      .refine(hasAtMostTwoDecimals, { error: multiplierError })
  ),
  freeExpireDays: rewardExpireDaysSchema(),
  paidExpireDays: rewardExpireDaysSchema(),
  expireOnlyWhenUnused: z.boolean(),
})

type Values = z.infer<typeof schema>

// Form field → option key. Every field is saved as its string form.
const OPTION_KEYS: Record<keyof Values, string> = {
  enabled: 'checkin_setting.enabled',
  minQuota: 'checkin_setting.min_quota',
  maxQuota: 'checkin_setting.max_quota',
  paidThresholdQuota: 'checkin_setting.paid_threshold_quota',
  paidMultiplier: 'checkin_setting.paid_multiplier',
  freeExpireDays: 'checkin_setting.free_expire_days',
  paidExpireDays: 'checkin_setting.paid_expire_days',
  expireOnlyWhenUnused: 'checkin_setting.expire_only_when_unused',
}

export function CheckinSettingsSection(props: { defaultValues: Values }) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: props.defaultValues,
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('enabled')
  const saving = updateOption.isPending || isSubmitting

  async function onSubmit(values: Values) {
    const changedFields = (Object.keys(OPTION_KEYS) as Array<keyof Values>)
      .filter((field) => values[field] !== props.defaultValues[field])
      .map((field) => ({
        key: OPTION_KEYS[field],
        value: String(values[field]),
      }))

    if (changedFields.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of changedFields) {
      await updateOption.mutateAsync(update)
    }

    form.reset(values)
  }

  return (
    <SettingsSection title={t('Check-in Settings')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={saving}
            isSaveDisabled={!isDirty}
            saveLabel='Save check-in settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable check-in feature')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Allow users to check in daily for random quota rewards'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={saving}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {enabled && (
            <>
              <div className='grid gap-6 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='minQuota'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Minimum check-in quota')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          placeholder={t('1000')}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Minimum quota amount awarded for check-in')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='maxQuota'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Maximum check-in quota')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          placeholder={t('10000')}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Maximum quota amount awarded for check-in')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='paidThresholdQuota'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Paid user recharge threshold')}</FormLabel>
                      <FormControl>
                        <Input type='number' min={0} step={1} {...field} />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'Users whose cumulative top-ups and redemption codes reach this quota are paid users; 0 = any recharge ({{formattedQuota}})',
                          {
                            formattedQuota: formatQuota(
                              Number(field.value) || 0
                            ),
                          }
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='paidMultiplier'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Paid user reward multiplier')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={1}
                          max={100}
                          step={0.1}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'The check-in reward of paid users is multiplied by this value and rounded down; 1 = same as free users'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='freeExpireDays'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t('Free user reward expiry (days)')}
                      </FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={3650}
                          step={1}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>{t('0 = never expire')}</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='paidExpireDays'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t('Paid user reward expiry (days)')}
                      </FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={3650}
                          step={1}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>{t('0 = never expire')}</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name='expireOnlyWhenUnused'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Expire only when unused')}</FormLabel>
                      <FormDescription>
                        {t(
                          'When on, a reward expires only if it is not used at all during the expiry period; every use restarts the period. Applies to rewards already granted.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={saving}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </>
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
