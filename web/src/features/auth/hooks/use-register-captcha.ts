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
import { useQueryClient } from '@tanstack/react-query'
import i18next from 'i18next'
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { useStatus } from '@/hooks/use-status'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import {
  createServerError,
  getServerErrorSources,
} from '@/lib/server-error-message'
import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { getRegisterCaptcha } from '../api'
import type { RegisterCaptcha, RegisterCaptchaParams } from '../types'

const CAPTCHA_ERROR_CODES = new Set(['CAPTCHA_REQUIRED', 'CAPTCHA_INVALID'])

/** The server turned a request down because of the registration captcha. */
function isCaptchaRejection(outcome: unknown): boolean {
  return getServerErrorSources(outcome).some(
    (source) =>
      typeof source.code === 'string' && CAPTCHA_ERROR_CODES.has(source.code)
  )
}

/**
 * The arithmetic image captcha of the sign-up page.
 *
 * A challenge is single use: the server deletes it on every check, right or
 * wrong, so a new one is fetched after each request that carried it, after a
 * captcha error and when the challenge expires.
 */
export function useRegisterCaptcha() {
  const { status } = useStatus()
  const queryClient = useQueryClient()
  const enabled = Boolean(status?.register_captcha)
  const [challenge, setChallenge] = useState<RegisterCaptcha | null>(null)
  const [answer, setAnswer] = useState('')
  const [isLoading, setIsLoading] = useState(false)
  // Only the newest request may update the state; older answers are stale.
  const latestRequest = useRef(0)

  const refresh = useCallback(async () => {
    const request = ++latestRequest.current
    setAnswer('')
    setIsLoading(true)
    try {
      const res = await getRegisterCaptcha()
      if (request !== latestRequest.current) return
      if (res?.success && res.data) {
        setChallenge(res.data)
        return
      }
      setChallenge(null)
      handleServerError(
        createServerError(res, i18next.t('Failed to load the captcha'))
      )
    } catch (error) {
      if (request !== latestRequest.current) return
      setChallenge(null)
      handleServerError(
        AuthOperationError.from(error, i18next.t('Failed to load the captcha'))
      )
    } finally {
      if (request === latestRequest.current) setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!enabled) return
    void refresh()
    return () => {
      // Ignore a response that arrives after the field went away.
      latestRequest.current += 1
    }
  }, [enabled, refresh])

  useEffect(() => {
    const seconds = challenge?.expires_in ?? 0
    if (!enabled || !(seconds > 0)) return
    const timer = window.setTimeout(() => void refresh(), seconds * 1000)
    return () => window.clearTimeout(timer)
  }, [enabled, challenge, refresh])

  const trimmedAnswer = answer.trim()
  const params: RegisterCaptchaParams | undefined =
    enabled && challenge
      ? { captcha_id: challenge.captcha_id, captcha: trimmedAnswer }
      : undefined

  /** True when the captcha is off or answered; toasts otherwise. */
  const validate = (): boolean => {
    if (!enabled || (challenge && trimmedAnswer)) return true
    toast.error(i18next.t('Please complete the captcha.'))
    return false
  }

  /**
   * Call after every request that reached the server with its response or
   * error, and whether it carried the captcha.
   */
  const handleRequestSettled = (outcome: unknown, sentCaptcha: boolean) => {
    const rejected = isCaptchaRejection(outcome)
    if (rejected && !enabled) {
      // The cached status predates the captcha being turned on.
      void queryClient.invalidateQueries({ queryKey: STATUS_QUERY_KEY })
      return
    }
    if (sentCaptcha || rejected) void refresh()
  }

  return {
    enabled,
    challenge,
    answer,
    setAnswer,
    isLoading,
    ready: !enabled || Boolean(challenge && trimmedAnswer),
    params,
    refresh,
    validate,
    handleRequestSettled,
  }
}

export type RegisterCaptchaState = ReturnType<typeof useRegisterCaptcha>
