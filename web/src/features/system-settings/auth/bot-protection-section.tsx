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
import { useForm, type Control, type Resolver } from 'react-hook-form'
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
  PasswordResetCaptchaEnabled: z.boolean(),
  PasswordResetEmailLimitEnabled: z.boolean(),
  PasswordResetEmailCooldownSeconds: integerRangeSchema(
    30,
    3600,
    'Password reset email cooldown must be an integer between 30 and 3600 seconds'
  ),
  PasswordResetEmailDailyMax: integerRangeSchema(
    1,
    100,
    'Password reset email limit must be an integer between 1 and 100 per day'
  ),
  LoginCaptchaEnabled: z.boolean(),
  LoginLockoutEnabled: z.boolean(),
  LoginLockoutMaxFailures: integerRangeSchema(
    3,
    100,
    'Login lockout failure count must be an integer between 3 and 100'
  ),
  LoginLockoutMinutes: integerRangeSchema(
    1,
    1440,
    'Login lockout duration must be an integer between 1 and 1440 minutes'
  ),
})

type BotProtectionFormValues = z.infer<typeof botProtectionSchema>

// Switches saved only after the settings they rely on in the same save.
const ENABLE_LAST_KEYS = new Set([
  'TurnstileCheckEnabled',
  'RegisterIPRateLimitEnabled',
  'PasswordResetEmailLimitEnabled',
  'LoginLockoutEnabled',
])

type BotProtectionSectionProps = {
  defaultValues: BotProtectionFormValues
}

type SwitchName =
  | 'TurnstileCheckEnabled'
  | 'RegisterCaptchaEnabled'
  | 'RegisterIPRateLimitEnabled'
  | 'PasswordResetCaptchaEnabled'
  | 'PasswordResetEmailLimitEnabled'
  | 'LoginCaptchaEnabled'
  | 'LoginLockoutEnabled'

type NumberName =
  | 'RegisterIPRateLimitNum'
  | 'RegisterIPRateLimitMinutes'
  | 'PasswordResetEmailCooldownSeconds'
  | 'PasswordResetEmailDailyMax'
  | 'LoginLockoutMaxFailures'
  | 'LoginLockoutMinutes'

/** An emptied number input stays empty instead of becoming NaN. */
function numberInputValue(event: ChangeEvent<HTMLInputElement>): number | '' {
  const value = event.currentTarget.valueAsNumber
  return Number.isNaN(value) ? '' : value
}

/** A protection switch with what it does under its name. */
function ProtectionSwitch(props: {
  control: Control<BotProtectionFormValues>
  name: SwitchName
  label: string
  description: string
}) {
  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <SettingsSwitchItem>
          <SettingsSwitchContent>
            <FormLabel>{props.label}</FormLabel>
            <FormDescription>{props.description}</FormDescription>
          </SettingsSwitchContent>
          <FormControl>
            <Switch checked={field.value} onCheckedChange={field.onChange} />
          </FormControl>
        </SettingsSwitchItem>
      )}
    />
  )
}

/**
 * A whole number setting. Top-aligned, so both inputs of a row line up
 * whatever the length of their descriptions.
 */
function ProtectionNumberInput(props: {
  control: Control<BotProtectionFormValues>
  name: NumberName
  label: string
  description: string
  min: number
  max: number
}) {
  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <FormItem className='self-start'>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <Input
              type='number'
              min={props.min}
              max={props.max}
              step={1}
              value={field.value ?? ''}
              onChange={(event) => field.onChange(numberInputValue(event))}
              name={field.name}
              onBlur={field.onBlur}
              ref={field.ref}
            />
          </FormControl>
          <FormDescription>{props.description}</FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )
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
          <ProtectionSwitch
            control={form.control}
            name='TurnstileCheckEnabled'
            label={t('Enable Turnstile')}
            description={t(
              'Optional. Cloudflare Turnstile often fails to load for visitors in mainland China, so do not rely on it there: the arithmetic captchas, the reset email limit and the login lockout below work without it.'
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

          <ProtectionSwitch
            control={form.control}
            name='RegisterCaptchaEnabled'
            label={t('Registration captcha')}
            description={t(
              'Ask for an arithmetic image captcha before a registration email code is sent, or when registering if email verification is off.'
            )}
          />

          <ProtectionSwitch
            control={form.control}
            name='RegisterIPRateLimitEnabled'
            label={t('Registration IP rate limit')}
            description={t(
              'Limits registration submissions and registration email code requests per IP, counted separately. New accounts created through third-party login count as registrations. Client IPs are only correct behind a properly configured reverse proxy and TRUSTED_PROXIES.'
            )}
          />

          <ProtectionNumberInput
            control={form.control}
            name='RegisterIPRateLimitNum'
            label={t('Requests per IP')}
            description={t(
              'Allowed in each time window, for registrations and for email codes alike'
            )}
            min={1}
            max={1000}
          />

          <ProtectionNumberInput
            control={form.control}
            name='RegisterIPRateLimitMinutes'
            label={t('Time window (minutes)')}
            description={t(
              "Captcha images are limited per IP too. While this limit is on: six times the requests per IP in this window (at least 30), plus three for each request the server's critical rate limit allows while the login or password reset captcha is on. While it is off and any captcha is on: three images per such request (60 per 20 minutes by default)."
            )}
            min={1}
            max={1440}
          />

          <ClientIpDiagnostic />

          <ProtectionSwitch
            control={form.control}
            name='PasswordResetCaptchaEnabled'
            label={t('Password reset captcha')}
            description={t(
              'Ask for an arithmetic image captcha on the forgot password page before a reset email is sent.'
            )}
          />

          <ProtectionSwitch
            control={form.control}
            name='PasswordResetEmailLimitEnabled'
            label={t('Password reset email limit')}
            description={t(
              'Per email address, whether or not an account uses it: at most one reset email per cooldown and at most the daily maximum in any 24 hours. Refused requests send nothing and do not count. Anyone can use up an address\'s daily emails; its owner then waits, or an administrator clears it with "Unlock login".'
            )}
          />

          <ProtectionNumberInput
            control={form.control}
            name='PasswordResetEmailCooldownSeconds'
            label={t('Cooldown (seconds)')}
            description={t(
              'Time between two reset emails to one address, 30 to 3600'
            )}
            min={30}
            max={3600}
          />

          <ProtectionNumberInput
            control={form.control}
            name='PasswordResetEmailDailyMax'
            label={t('Emails per address in 24 hours')}
            description={t('Counted over a rolling 24 hours, 1 to 100')}
            min={1}
            max={100}
          />

          <ProtectionSwitch
            control={form.control}
            name='LoginCaptchaEnabled'
            label={t('Login captcha')}
            description={t(
              'Ask for an arithmetic image captcha on every password login. Scripts and apps that sign in with a username and password through the API stop working while it is on; API keys are not affected.'
            )}
          />

          <ProtectionSwitch
            control={form.control}
            name='LoginLockoutEnabled'
            label={t('Login lockout')}
            description={t(
              'After too many wrong passwords for one account or one typed name, password login is refused for a while, even with the right password. A password reset through the email link or "Unlock login" in user management ends it early. Passkey and third-party login are not locked, so a passkey bound to the administrator account (with passkey login enabled) is the way in while it is locked. Two-factor authentication stops a guessed password but does not lift a lock.'
            )}
          />

          <ProtectionNumberInput
            control={form.control}
            name='LoginLockoutMaxFailures'
            label={t('Failed attempts before locking')}
            description={t(
              'Wrong passwords counted within the lock duration, 3 to 100'
            )}
            min={3}
            max={100}
          />

          <ProtectionNumberInput
            control={form.control}
            name='LoginLockoutMinutes'
            label={t('Lock duration (minutes)')}
            description={t(
              'Also the window in which failures are counted, 1 to 1440'
            )}
            min={1}
            max={1440}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
