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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { isChunkLoadError, reloadOnceForChunkError } from '../chunk-load-error'

const CHUNK_URL = 'http://localhost/static/js/async/12.js'
const RELOAD_KEY = `newapi:chunk-reload:${CHUNK_URL}`

/** Shape thrown by rspack's JSONP chunk loader in production builds. */
function rspackChunkLoadError(): Error {
  const error = new Error(`Loading chunk 12 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

describe('isChunkLoadError', () => {
  test.each([
    {
      name: 'returns true for an error named ChunkLoadError',
      error: rspackChunkLoadError(),
      expected: true,
    },
    {
      name: 'returns true for a plain Error whose message starts with "Loading chunk"',
      error: new Error('Loading chunk 12 failed.'),
      expected: true,
    },
    {
      name: 'returns true for a plain Error whose message starts with "Loading CSS chunk"',
      error: new Error(
        'Loading CSS chunk 12 failed.\n(http://localhost/12.css)'
      ),
      expected: true,
    },
    {
      name: 'returns true for the Chromium native ESM import failure message',
      error: new TypeError(
        'Failed to fetch dynamically imported module: http://localhost/assets/page.js'
      ),
      expected: true,
    },
    {
      name: 'returns true for the Firefox native ESM import failure message',
      error: new TypeError(
        'error loading dynamically imported module: http://localhost/assets/page.js'
      ),
      expected: true,
    },
    {
      name: 'returns true for the Safari native ESM import failure message',
      error: new TypeError('Importing a module script failed.'),
      expected: true,
    },
    {
      name: 'returns false for a plain Error with an unrelated message',
      error: new Error('Request failed with status code 500'),
      expected: false,
    },
    {
      name: 'returns false for a string that merely looks like a chunk message',
      error: 'Loading chunk 12 failed.',
      expected: false,
    },
    {
      name: 'returns false for null',
      error: null,
      expected: false,
    },
    {
      name: 'returns false for undefined',
      error: undefined,
      expected: false,
    },
  ])('$name', ({ error, expected }) => {
    expect(isChunkLoadError(error)).toBe(expected)
  })
})

describe('reloadOnceForChunkError', () => {
  const reload = vi.fn()

  beforeEach(() => {
    vi.stubGlobal('location', { ...window.location, reload })
    window.sessionStorage.clear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  test('first failure of a chunk reloads the page and records the attempt in sessionStorage', () => {
    const result = reloadOnceForChunkError(rspackChunkLoadError())

    expect(result).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(window.sessionStorage.getItem(RELOAD_KEY)).not.toBeNull()
  })

  test('second failure of the same chunk returns false and does not reload, even minutes later', () => {
    vi.useFakeTimers()
    reloadOnceForChunkError(rspackChunkLoadError())
    reload.mockClear()
    // Longer than rspack's 120 s script timeout, so a stalled asset that
    // keeps timing out can never produce a reload loop.
    vi.advanceTimersByTime(10 * 60 * 1000)

    const timedOut = rspackChunkLoadError()
    Object.assign(timedOut, { type: 'timeout' })
    const result = reloadOnceForChunkError(timedOut)

    expect(result).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  test('a chunk with a different URL after a redeploy gets its own reload', () => {
    reloadOnceForChunkError(rspackChunkLoadError())
    reload.mockClear()

    const redeployed = new Error(
      'Loading chunk 42 failed.\n(missing: http://localhost/static/js/async/42.newhash.js)'
    )
    redeployed.name = 'ChunkLoadError'
    Object.assign(redeployed, {
      request: 'http://localhost/static/js/async/42.newhash.js',
    })

    expect(reloadOnceForChunkError(redeployed)).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('falls back to the error message as the storage key when the error carries no request URL', () => {
    const error = new Error('Loading chunk 7 failed.')

    const result = reloadOnceForChunkError(error)

    expect(result).toBe(true)
    expect(
      window.sessionStorage.getItem(
        'newapi:chunk-reload:Loading chunk 7 failed.'
      )
    ).not.toBeNull()
  })

  test('non-chunk error returns false and does not reload', () => {
    const result = reloadOnceForChunkError(new Error('boom'))

    expect(result).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    expect(window.sessionStorage.length).toBe(0)
  })

  test('sessionStorage that throws returns false and does not reload', () => {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new Error('SecurityError: storage is disabled')
    })

    const result = reloadOnceForChunkError(rspackChunkLoadError())

    expect(result).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })
})
