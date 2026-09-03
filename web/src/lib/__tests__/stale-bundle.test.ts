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

import {
  checkServerVersionHeader,
  flushPendingStaleBundleNotice,
  isStaleBundle,
  notifyStaleBundleOnce,
} from '../stale-bundle'

const toast = vi.hoisted(() => vi.fn())

vi.mock('sonner', () => ({ toast }))

// Build-time stamp is a compile-time boundary; pin it so the header comparison
// is deterministic regardless of the environment running the tests.
vi.mock('@/lib/build-metadata', () => ({
  getClientBuildVersion: () => 'custom-client-1',
}))

type ToastOptions = {
  action?: { label: string; onClick: () => void }
}

function lastToastOptions(): ToastOptions {
  const call = toast.mock.calls.at(-1)
  return (call?.[1] ?? {}) as ToastOptions
}

describe('isStaleBundle', () => {
  test.each([
    {
      name: 'returns false when server and client versions are equal',
      server: 'custom-abc1234',
      client: 'custom-abc1234',
      expected: false,
    },
    {
      name: 'returns true when server and client versions differ',
      server: 'custom-abc1234',
      client: 'custom-def5678',
      expected: true,
    },
    {
      name: 'returns false when the server version is empty',
      server: '',
      client: 'custom-abc1234',
      expected: false,
    },
    {
      name: 'returns false when the client version is empty',
      server: 'custom-abc1234',
      client: '',
      expected: false,
    },
    {
      name: 'returns false when the server version is undefined',
      server: undefined,
      client: 'custom-abc1234',
      expected: false,
    },
    {
      name: 'returns false when the server runs the v0.0.0 dev placeholder',
      server: 'v0.0.0',
      client: 'custom-abc1234',
      expected: false,
    },
    {
      name: 'returns false when the client is the v0.0.0 dev placeholder',
      server: 'custom-abc1234',
      client: 'v0.0.0',
      expected: false,
    },
  ])('$name', ({ server, client, expected }) => {
    expect(isStaleBundle(server, client)).toBe(expected)
  })
})

// Runs first: the module starts with the toaster not yet mounted.
describe('notice raised before the Toaster is mounted', () => {
  afterEach(() => {
    window.sessionStorage.clear()
  })

  test('is held back and shown exactly once when the app reports the toaster ready', () => {
    notifyStaleBundleOnce('custom-early-1')
    expect(toast).not.toHaveBeenCalled()

    flushPendingStaleBundleNotice()
    expect(toast).toHaveBeenCalledTimes(1)
    expect(toast.mock.calls[0]?.[0]).toBe(
      'A new version is available. Reload to update.'
    )

    flushPendingStaleBundleNotice()
    notifyStaleBundleOnce('custom-early-1')
    expect(toast).toHaveBeenCalledTimes(1)
  })
})

describe('notifyStaleBundleOnce', () => {
  const reload = vi.fn()

  beforeEach(() => {
    flushPendingStaleBundleNotice()
    vi.stubGlobal('location', { ...window.location, reload })
    window.sessionStorage.clear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  test('reporting the same server version twice shows a single toast', () => {
    notifyStaleBundleOnce('custom-once-1')
    notifyStaleBundleOnce('custom-once-1')

    expect(toast).toHaveBeenCalledTimes(1)
    expect(toast).toHaveBeenCalledWith(
      'A new version is available. Reload to update.',
      expect.objectContaining({
        action: expect.objectContaining({ label: 'Reload' }),
      })
    )
  })

  test('reporting a different server version shows another toast', () => {
    notifyStaleBundleOnce('custom-multi-1')
    notifyStaleBundleOnce('custom-multi-2')

    expect(toast).toHaveBeenCalledTimes(2)
  })

  test('a version already recorded in sessionStorage for this tab shows no toast', () => {
    window.sessionStorage.setItem(
      'newapi:version-notice:custom-seen-1',
      String(Date.now())
    )

    notifyStaleBundleOnce('custom-seen-1')

    expect(toast).not.toHaveBeenCalled()
  })

  test('the toast action reloads the page', () => {
    notifyStaleBundleOnce('custom-action-1')

    lastToastOptions().action?.onClick()

    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('sessionStorage that throws still shows the toast exactly once per version', () => {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new Error('SecurityError: storage is disabled')
    })

    notifyStaleBundleOnce('custom-nostorage-1')
    notifyStaleBundleOnce('custom-nostorage-1')

    expect(toast).toHaveBeenCalledTimes(1)
  })
})

describe('checkServerVersionHeader', () => {
  beforeEach(() => {
    flushPendingStaleBundleNotice()

    window.sessionStorage.clear()
  })

  afterEach(() => {
    window.sessionStorage.clear()
  })

  test('a response without the version header shows no toast', () => {
    checkServerVersionHeader({ 'content-type': 'application/json' })
    checkServerVersionHeader(undefined)

    expect(toast).not.toHaveBeenCalled()
  })

  test('a header matching the client build shows no toast', () => {
    checkServerVersionHeader({ 'x-new-api-version': 'custom-client-1' })

    expect(toast).not.toHaveBeenCalled()
  })

  test('a header that differs from the client build shows the stale-bundle toast', () => {
    checkServerVersionHeader({ 'x-new-api-version': 'custom-server-9' })

    expect(toast).toHaveBeenCalledTimes(1)
  })
})
