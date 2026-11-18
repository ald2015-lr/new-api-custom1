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
import i18next from 'i18next'
import { useState } from 'react'
import { toast } from 'sonner'

import { useCountdown } from '@/hooks/use-countdown'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { createServerError } from '@/lib/server-error-message'

import { sendEmailVerification } from '../api'
import { EMAIL_VERIFICATION_COUNTDOWN } from '../constants'
import type { RegisterCaptchaParams } from '../types'

interface UseEmailVerificationOptions {
  turnstileToken?: string
  validateTurnstile?: () => boolean
  /** Registration captcha answer sent with the request */
  captcha?: RegisterCaptchaParams
  validateCaptcha?: () => boolean
  /** Runs after every request that reached the server, with its outcome */
  onRequestSettled?: (outcome: unknown) => void
}

/**
 * Hook for managing email verification code sending
 */
export function useEmailVerification(options?: UseEmailVerificationOptions) {
  const [isSending, setIsSending] = useState(false)
  const {
    secondsLeft,
    isActive,
    start: startCountdown,
  } = useCountdown({ initialSeconds: EMAIL_VERIFICATION_COUNTDOWN })

  /**
   * Send verification code to email
   */
  const sendCode = async (email: string) => {
    if (!email) {
      toast.error(i18next.t('Please enter your email first'))
      return false
    }

    // Validate turnstile if validation function is provided
    if (options?.validateTurnstile && !options.validateTurnstile()) {
      return false
    }
    if (options?.validateCaptcha && !options.validateCaptcha()) {
      return false
    }

    setIsSending(true)
    let outcome: unknown
    try {
      const res = await sendEmailVerification(
        email,
        options?.turnstileToken,
        options?.captcha
      )
      outcome = res
      if (res?.success) {
        startCountdown()
        toast.success(i18next.t('Verification email sent'))
        return true
      }
      handleServerError(
        createServerError(res, i18next.t('Failed to send verification email'))
      )
      return false
    } catch (_error) {
      outcome = _error
      handleServerError(
        AuthOperationError.from(
          _error,
          i18next.t('Failed to send verification email')
        )
      )
      return false
    } finally {
      setIsSending(false)
      options?.onRequestSettled?.(outcome)
    }
  }

  return {
    isSending,
    secondsLeft,
    isActive,
    sendCode,
  }
}
