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
import { useQuery } from '@tanstack/react-query'
import { useEffect, type ChangeEvent, type ReactNode } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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

import { getClientIpDiagnostics } from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { integerRangeSchema } from '../utils/numeric-field'

const botProtectionSchema = z.object({
  TurnstileCheckEnabled: z.boolean(),
  TurnstileSiteKey: z.string().optional(),
  TurnstileSecretKey: z.string().optional(),
  RegisterCaptchaEnabled: z.boolean(),
  RegisterIPRateLimitEnabled: z.boolean(),
  RegisterIPRateLimitNum: integerRangeSchema(
    1,
    1000,
    'Registration rate limit count must be an integer between 1 and 1000'
  ),
  RegisterIPRateLimitMinutes: integerRangeSchema(
    1,
    1440,
    'Registration rate limit window must be an integer between 1 and 1440 minutes'
  ),
})

type BotProtectionFormValues = z.infer<typeof botProtectionSchema>

// Switches saved only after the settings they rely on in the same save.
const ENABLE_LAST_KEYS = new Set([
  'TurnstileCheckEnabled',
  'RegisterIPRateLimitEnabled',
])

type BotProtectionSectionProps = {
  defaultValues: BotProtectionFormValues
}

/** An emptied number input stays empty instead of becoming NaN. */
function numberInputValue(event: ChangeEvent<HTMLInputElement>): number | '' {
  const value = event.currentTarget.valueAsNumber
  return Number.isNaN(value) ? '' : value
}

/**
 * The address the server sees for the admin. Per-IP limits only work when it
 * is the admin's real public address, not a reverse proxy in front of it.
 */
function ClientIpDiagnostic() {
  const { t } = useTranslation()
  const { data, isError } = useQuery({
    queryKey: ['system-settings', 'client-ip'],
    queryFn: getClientIpDiagnostics,
    retry: false,
    meta: { errorToast: false },
  })

  let address: ReactNode = t('Loading...')
  let proxies = ''
  if (data) {
    address = <code className='font-mono'>{data.ip || t('Unknown')}</code>
    proxies = data.trusted_proxies_configured
      ? t('TRUSTED_PROXIES is configured.')
      : t(
          'TRUSTED_PROXIES is not configured, so only loopback and private network proxies are trusted.'
        )
  } else if (isError) {
    address = t('Unknown')
  }

  return (
    <>
      {data?.trusted_proxies_catch_all ? (
        <Alert variant='destructive'>
          <AlertTitle>{t('TRUSTED_PROXIES trusts every address')}</AlertTitle>
          <AlertDescription>
            {t(
              'Any client can forge its IP address with X-Forwarded-For, so the registration IP limit and the login rate limit can be bypassed. List only the addresses of your own proxies.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}
      <Alert>
        <AlertTitle>
          {t('The IP address the server sees for you')}: {address}
        </AlertTitle>
        <AlertDescription>
          {proxies ? <p>{proxies}</p> : null}
          <p>
            {t(
              'Compare it with your real public IP before turning on the IP limit. If it shows a reverse proxy or CDN address instead, all visitors share one limit and registration can be blocked for everyone: list those proxies in TRUSTED_PROXIES and make them forward the client address in X-Forwarded-For first.'
            )}
          </p>
          <p>
            {t(
              'TRUSTED_PROXIES replaces the built-in loopback and private network defaults. List only the addresses of your own proxies, never 0.0.0.0/0 or ::/0.'
            )}
          </p>
        </AlertDescription>
      </Alert>
    </>
  )
}

export function BotProtectionSection({
  defaultValues,
}: BotProtectionSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<BotProtectionFormValues>({
    resolver: zodResolver(botProtectionSchema) as Resolver<
      BotProtectionFormValues,
      unknown,
      BotProtectionFormValues
    >,
    defaultValues,
  })

  useEffect(() => {
    form.reset(defaultValues)
  }, [defaultValues, form])

  const onSubmit = async (data: BotProtectionFormValues) => {
    const updates = Object.entries(data).filter(
      ([key, value]) =>
        value !== defaultValues[key as keyof BotProtectionFormValues]
    )
    const enableUpdates = updates.filter(
      ([key, value]) => ENABLE_LAST_KEYS.has(key) && value === true
    )
    const orderedUpdates = [
      ...updates.filter((update) => !enableUpdates.includes(update)),
      ...enableUpdates,
    ]

    for (const [key, value] of orderedUpdates) {
      await updateOption.mutateAsync({ key, value: value ?? '' })
    }
  }

  return (
    <SettingsSection title={t('Bot Protection')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <FormField
            control={form.control}
            name='TurnstileCheckEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable Turnstile')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Protect login and registration with Cloudflare Turnstile'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <FormField
            control={form.control}
            name='TurnstileSiteKey'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Site Key')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Your Turnstile site key')}
                    autoComplete='off'
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='TurnstileSecretKey'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Secret Key')}</FormLabel>
                <FormControl>
                  <Input
                    type='password'
                    placeholder={t('Your Turnstile secret key')}
                    autoComplete='new-password'
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='RegisterCaptchaEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Registration captcha')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Ask for an arithmetic image captcha before a registration email code is sent, or when registering if email verification is off.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <FormField
            control={form.control}
            name='RegisterIPRateLimitEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Registration IP rate limit')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Limits registration submissions and registration email code requests per IP, counted separately. New accounts created through third-party login count as registrations. Client IPs are only correct behind a properly configured reverse proxy and TRUSTED_PROXIES.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {/* Top-aligned, so both inputs of this row line up whatever the
              length of their descriptions. */}
          <FormField
            control={form.control}
            name='RegisterIPRateLimitNum'
            render={({ field }) => (
              <FormItem className='self-start'>
                <FormLabel>{t('Requests per IP')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={1000}
                    step={1}
                    value={field.value ?? ''}
                    onChange={(event) =>
                      field.onChange(numberInputValue(event))
                    }
                    name={field.name}
                    onBlur={field.onBlur}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Allowed in each time window, for registrations and for email codes alike'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='RegisterIPRateLimitMinutes'
            render={({ field }) => (
              <FormItem className='self-start'>
                <FormLabel>{t('Time window (minutes)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={1440}
                    step={1}
                    value={field.value ?? ''}
                    onChange={(event) =>
                      field.onChange(numberInputValue(event))
                    }
                    name={field.name}
                    onBlur={field.onBlur}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Captcha images are limited in the same window too: six times the requests per IP, at least 30'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <ClientIpDiagnostic />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
