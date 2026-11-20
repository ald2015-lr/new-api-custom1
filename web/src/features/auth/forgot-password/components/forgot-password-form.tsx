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
import { ArrowRight, Info, Loader2, TriangleAlert } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { Turnstile } from '@/components/turnstile'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { sendPasswordResetEmail } from '@/features/auth/api'
import { RegisterCaptchaField } from '@/features/auth/components/register-captcha-field'
import {
  forgotPasswordFormSchema,
  PASSWORD_RESET_COUNTDOWN,
} from '@/features/auth/constants'
import { useRegisterCaptcha } from '@/features/auth/hooks/use-register-captcha'
import { useTurnstile } from '@/features/auth/hooks/use-turnstile'
import { useWaitDeadline } from '@/features/auth/hooks/use-wait-deadline'
import {
  PASSWORD_RESET_EMAIL_COOLDOWN,
  PASSWORD_RESET_EMAIL_DAILY_LIMIT,
  readAuthGuardRefusal,
  sameResetAddress,
  serverText,
  type ServerText,
} from '@/features/auth/lib/auth-guard'
import { useStatus } from '@/hooks/use-status'
import dayjs from '@/lib/dayjs'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

/** What the form says under the button about the last request. */
type ResetNotice = { kind: 'sent' } | { kind: 'cooldown'; message: ServerText }

/** A daily limit refusal, shown while the refused address is typed. */
type DailyLimit = { address: string; message: ServerText; until: string }

export function ForgotPasswordForm({
  className,
  ...props
}: React.HTMLAttributes<HTMLFormElement>) {
  const { t } = useTranslation()
  const [isLoading, setIsLoading] = useState(false)
  const [notice, setNotice] = useState<ResetNotice | null>(null)
  const [dailyLimit, setDailyLimit] = useState<DailyLimit | null>(null)
  const { status } = useStatus()
  const captcha = useRegisterCaptcha({ statusKey: 'password_reset_captcha' })

  const {
    isTurnstileEnabled,
    turnstileSiteKey,
    turnstileToken,
    setTurnstileToken,
    validateTurnstile,
  } = useTurnstile()
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0)
  // Stable, so the widget is not rendered again on every render.
  const clearTurnstileToken = useCallback(
    () => setTurnstileToken(''),
    [setTurnstileToken]
  )
  // The short wait after a sent email, whatever address is typed next.
  const resendWait = useWaitDeadline()
  // The server's cooldown, which holds only the address it counted.
  const addressWait = useWaitDeadline()
  const [heldAddress, setHeldAddress] = useState<string | null>(null)
  // The wait of a daily limit refusal; it holds the refused address only.
  const dailyWait = useWaitDeadline()

  const form = useForm<z.infer<typeof forgotPasswordFormSchema>>({
    resolver: zodResolver(forgotPasswordFormSchema),
    defaultValues: { email: '' },
  })
  const email = useWatch({ control: form.control, name: 'email' })
  const turnstileReady = !isTurnstileEnabled || Boolean(turnstileToken)
  const addressLimited =
    dailyWait.active &&
    dailyLimit !== null &&
    sameResetAddress(email ?? '', dailyLimit.address)
  const addressHeld =
    addressWait.active &&
    heldAddress !== null &&
    sameResetAddress(email ?? '', heldAddress)
  const waitSeconds = Math.max(
    resendWait.secondsLeft,
    addressHeld ? addressWait.secondsLeft : 0
  )

  /** Shows a cooldown or daily limit refusal; false for other answers. */
  function showLimit(outcome: unknown, address: string): boolean {
    const refusal = readAuthGuardRefusal(outcome)
    if (
      refusal?.code !== PASSWORD_RESET_EMAIL_COOLDOWN &&
      refusal?.code !== PASSWORD_RESET_EMAIL_DAILY_LIMIT
    ) {
      return false
    }
    const message = refusal.message
    if (refusal.code === PASSWORD_RESET_EMAIL_COOLDOWN) {
      addressWait.start(refusal.waitSeconds)
      setHeldAddress(address)
      setNotice({ kind: 'cooldown', message })
      return true
    }
    dailyWait.start(refusal.waitSeconds)
    setDailyLimit({
      address,
      message,
      until: dayjs()
        .add(refusal.waitSeconds, 'second')
        .format('YYYY-MM-DD HH:mm'),
    })
    return true
  }

  async function onSubmit(data: z.infer<typeof forgotPasswordFormSchema>) {
    if (!validateTurnstile()) return
    if (!captcha.validate()) return
    const captchaParams = captcha.params
    // The server spends a Turnstile token once it has checked it.
    const submittedTurnstileToken = turnstileToken
    if (isTurnstileEnabled) {
      setTurnstileToken('')
      setTurnstileWidgetKey((current) => current + 1)
    }

    setNotice(null)
    setIsLoading(true)
    let outcome: unknown
    try {
      const res = await sendPasswordResetEmail(
        data.email,
        submittedTurnstileToken,
        captchaParams
      )
      outcome = res
      if (res?.success) {
        form.reset()
        resendWait.start(PASSWORD_RESET_COUNTDOWN)
        addressWait.start(status?.password_reset_email_cooldown ?? 0)
        setHeldAddress(data.email)
        setNotice({ kind: 'sent' })
        toast.success(t('Reset email sent, please check your inbox'))
      } else if (!showLimit(res, data.email)) {
        handleServerError(
          createServerError(res, t('Failed to send reset email'))
        )
      }
    } catch (_error) {
      outcome = _error
      handleServerError(
        AuthOperationError.from(_error, t('Failed to send reset email'))
      )
    } finally {
      setIsLoading(false)
      // A used captcha is replaced; a refusal re-reads the status.
      captcha.handleRequestSettled(outcome, Boolean(captchaParams))
    }
  }

  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit(onSubmit)}
        className={cn('grid gap-2', className)}
        {...props}
      >
        <FormField
          control={form.control}
          name='email'
          render={({ field }) => (
            <FormItem>
              <FormLabel>Email</FormLabel>
              <FormControl>
                <Input
                  placeholder='name@example.com'
                  {...field}
                  onChange={(event) => {
                    // What was said about the previous address is stale.
                    setNotice(null)
                    field.onChange(event)
                  }}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        {captcha.enabled && (
          <RegisterCaptchaField captcha={captcha} disabled={isLoading} />
        )}

        <Button
          type='submit'
          className='mt-2'
          disabled={
            isLoading ||
            waitSeconds > 0 ||
            addressLimited ||
            !turnstileReady ||
            !captcha.ready
          }
        >
          {waitSeconds > 0
            ? t('Resend ({{seconds}}s)', { seconds: waitSeconds })
            : t('Send reset email')}
          {isLoading ? <Loader2 className='animate-spin' /> : <ArrowRight />}
        </Button>

        {/* After a cooldown has run out, only its delivery hint stays. */}
        {(notice?.kind === 'sent' ||
          (notice?.kind === 'cooldown' && !addressHeld)) && (
          <Alert className='mt-2'>
            <Info aria-hidden='true' />
            <AlertDescription>
              {t(
                'If an account uses this address, the email can take a few minutes to arrive. Check your spam folder too. If you get several, use the newest one: earlier links stop working.'
              )}
            </AlertDescription>
          </Alert>
        )}

        {/* The countdown stays on the button, outside the alert. */}
        {notice?.kind === 'cooldown' && addressHeld && (
          <Alert className='mt-2'>
            <TriangleAlert aria-hidden='true' />
            <AlertDescription>{serverText(t, notice.message)}</AlertDescription>
          </Alert>
        )}

        {addressLimited && (
          <Alert className='mt-2'>
            <TriangleAlert aria-hidden='true' />
            <AlertDescription>
              <p>{serverText(t, dailyLimit.message)}</p>
              <p>
                {t(
                  'You can request another reset email for this address after {{time}}.',
                  { time: dailyLimit.until }
                )}
              </p>
            </AlertDescription>
          </Alert>
        )}

        {isTurnstileEnabled && (
          <div className='mt-2'>
            <Turnstile
              key={turnstileWidgetKey}
              siteKey={turnstileSiteKey}
              onVerify={setTurnstileToken}
              onExpire={clearTurnstileToken}
            />
          </div>
        )}
      </form>
    </Form>
  )
}
