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
import { AxiosError, AxiosHeaders } from 'axios'
import i18next from 'i18next'
import { describe, expect, test } from 'vitest'

import {
  getServerErrorMessage,
  getServerErrorMessageKey,
  getServerPayloadMessage,
  parseRetryAfter,
  retryAfterMs,
  translateServerText,
} from './server-error-message'

function httpError(
  status: number,
  data: unknown = '',
  headers: Record<string, string> = {}
): AxiosError {
  return new AxiosError(
    `Request failed with status code ${status}`,
    'ERR_BAD_REQUEST',
    undefined,
    undefined,
    {
      data,
      status,
      statusText: '',
      headers: AxiosHeaders.from(headers),
      config: { headers: new AxiosHeaders() },
    }
  )
}

describe('server error message mapping', () => {
  test('maps the active-session limit to recovery instructions', () => {
    const message = getServerErrorMessageKey({ code: 'AUTH_SESSION_LIMIT' })

    expect(message ?? '').toMatch(/Sign out other sessions/)
    expect(message ?? '').toMatch(/reset your password/)
  })

  test('maps an Axios-shaped issuance limit to rolling-window guidance', () => {
    const message = getServerErrorMessageKey({
      response: { data: { code: 'AUTH_SESSION_ISSUANCE_LIMIT' } },
    })

    expect(message ?? '').toMatch(/rolling window/)
    expect(getServerErrorMessageKey({ code: 'UNKNOWN_CODE' })).toBe(null)
  })

  test('maps stable Telegram bind errors without exposing server text', () => {
    const expected = {
      TELEGRAM_BIND_DISABLED: 'Telegram binding is disabled.',
      TELEGRAM_BIND_INVALID_REQUEST:
        'The Telegram authorization request is invalid or expired.',
      TELEGRAM_BIND_FLOW_INVALID:
        'This Telegram binding request has expired or has already been used.',
      TELEGRAM_BIND_SESSION_INVALID:
        'The login session that started this Telegram binding is no longer valid.',
      TELEGRAM_BIND_ALREADY_BOUND: 'This Telegram account is already bound.',
      TELEGRAM_BIND_USER_DELETED: 'This user account no longer exists.',
      TELEGRAM_BIND_USER_DISABLED: 'This user account is disabled.',
      TELEGRAM_BIND_INTERNAL_ERROR:
        'Telegram binding failed. Please try again.',
    }

    for (const [code, message] of Object.entries(expected)) {
      expect(getServerErrorMessageKey({ code })).toBe(message)
    }

    expect(
      getServerErrorMessageKey({
        response: {
          data: { code: 'TELEGRAM_BIND_INTERNAL_ERROR', message: 'raw detail' },
        },
      })
    ).toBe(expected.TELEGRAM_BIND_INTERNAL_ERROR)
  })

  test('translates message_key with message_params in the active language', async () => {
    i18next.addResourceBundle('zhCN', 'translation', {
      'Quota value exceeds valid range, maximum is {{max}}':
        '额度超出有效范围，最大为 {{max}}',
    })
    await i18next.changeLanguage('zhCN')
    try {
      const data = {
        success: false,
        message: 'Quota value exceeds valid range, maximum is 100',
        message_key: 'Quota value exceeds valid range, maximum is {{max}}',
        message_params: { max: 100 },
      }

      expect(getServerErrorMessage(data)).toBe('额度超出有效范围，最大为 100')
      expect(
        getServerErrorMessage({ isAxiosError: true, response: { data } })
      ).toBe('额度超出有效范围，最大为 100')
    } finally {
      await i18next.changeLanguage('en')
    }
  })

  test('renders an untranslated message_key as English text', () => {
    expect(
      getServerErrorMessage({
        success: false,
        message: 'Channel 7 does not exist',
        message_key: 'Channel {{id}} does not exist',
        message_params: { id: 7 },
      })
    ).toBe('Channel 7 does not exist')
  })

  test('translates a backend source key and leaves upstream text as written', async () => {
    i18next.addResourceBundle('zhCN', 'translation', {
      'Task timed out': '任务超时',
    })
    await i18next.changeLanguage('zhCN')
    try {
      expect(translateServerText(i18next.t, 'Task timed out')).toBe('任务超时')
      const upstream = 'upstream said {{bad}} and $t(Task timed out)'
      expect(translateServerText(i18next.t, upstream)).toBe(upstream)
    } finally {
      await i18next.changeLanguage('en')
    }
  })
})

describe('rate-limited responses', () => {
  test('an empty 429 names the Retry-After wait in the active language', async () => {
    i18next.addResourceBundle('zhCN', 'translation', {
      'Too many requests. Please try again in {{seconds}} seconds.':
        '请求过于频繁，请 {{seconds}} 秒后再试。',
    })
    await i18next.changeLanguage('zhCN')
    try {
      expect(
        getServerErrorMessage(httpError(429, '', { 'Retry-After': '37' }))
      ).toBe('请求过于频繁，请 37 秒后再试。')
    } finally {
      await i18next.changeLanguage('en')
    }
  })

  test('an empty 429 without Retry-After, or with a caller fallback, still explains itself', () => {
    expect(getServerErrorMessage(httpError(429))).toBe(
      'Too many requests. Please try again later.'
    )
    expect(
      getServerErrorMessage(
        httpError(429, '', { 'Retry-After': '5' }),
        'Failed to load logs'
      )
    ).toBe('Too many requests. Please try again in 5 seconds.')
  })

  test("a 429 that carries the server's own message keeps that message", () => {
    expect(
      getServerErrorMessage(
        httpError(
          429,
          { success: false, message: 'Too many sign-in attempts' },
          { 'Retry-After': '60' }
        )
      )
    ).toBe('Too many sign-in attempts')
  })

  test('other statuses keep the generic text', () => {
    expect(getServerErrorMessage(httpError(503))).toBe(
      'Request failed with status code 503'
    )
  })
})

describe('server payload messages', () => {
  test("only what the server sent, never the HTTP client's own text", () => {
    expect(
      getServerPayloadMessage(
        httpError(502, { success: false, message: 'Upstream unavailable' })
      )
    ).toBe('Upstream unavailable')
    expect(
      getServerPayloadMessage(
        new Error('Failed to query logs', {
          cause: { success: false, message: 'Failed to query logs' },
        })
      )
    ).toBe('Failed to query logs')
    expect(getServerPayloadMessage(httpError(503))).toBeUndefined()
    expect(
      getServerPayloadMessage(httpError(502, '<html>Bad Gateway</html>'))
    ).toBeUndefined()
    expect(getServerPayloadMessage(new Error('Network Error'))).toBeUndefined()
  })
})

describe('Retry-After parsing', () => {
  const now = Date.parse('2026-03-02T08:00:00Z')

  test.each([
    ['30', 30_000],
    [' 0 ', 0],
    [12, 12_000],
    ['Mon, 02 Mar 2026 08:00:45 GMT', 45_000],
  ])('%j waits %d ms', (value, expected) => {
    expect(parseRetryAfter(value, now)).toBe(expected)
  })

  test.each([
    undefined,
    '',
    'soon',
    '-5',
    '1.5',
    -1,
    'Mon, 02 Mar 2026 07:59:00 GMT',
  ])('%j is ignored', (value) => {
    expect(parseRetryAfter(value, now)).toBeUndefined()
  })

  test('reads the header from Axios errors and plain response records', () => {
    expect(retryAfterMs(httpError(429, '', { 'retry-after': '9' }))).toBe(9_000)
    expect(
      retryAfterMs({
        response: { status: 429, headers: { 'Retry-After': '4' } },
      })
    ).toBe(4_000)
    expect(
      retryAfterMs(
        new Error('wrapped', {
          cause: httpError(503, '', { 'Retry-After': '2' }),
        })
      )
    ).toBe(2_000)
    expect(retryAfterMs(httpError(429))).toBeUndefined()
    expect(retryAfterMs(new Error('offline'))).toBeUndefined()
  })
})
