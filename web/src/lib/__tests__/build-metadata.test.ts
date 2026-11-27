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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { getClientBuildVersion, installBuildMetadata } from '../build-metadata'
import { BUILD_VERSION_META_NAME } from '../build-version-meta'
import {
  checkServerVersionHeader,
  flushPendingStaleBundleNotice,
} from '../stale-bundle'

const toast = vi.hoisted(() => vi.fn())

vi.mock('sonner', () => ({ toast }))

// rsbuild.config.ts writes VITE_REACT_APP_VERSION into index.html as
// <meta name="new-api-version">, outside the cached JavaScript. These tests
// stamp that tag the way the built page has it.
function stampVersion(version: string) {
  const meta = document.createElement('meta')
  meta.setAttribute('name', BUILD_VERSION_META_NAME)
  meta.setAttribute('content', version)
  document.head.appendChild(meta)
}

function removeStamp() {
  document.head
    .querySelectorAll(`meta[name="${BUILD_VERSION_META_NAME}"]`)
    .forEach((meta) => meta.remove())
}

afterEach(() => {
  removeStamp()
  window.sessionStorage.clear()
})

describe('getClientBuildVersion', () => {
  test('returns the version index.html was built with', () => {
    stampVersion('custom-abc1234')

    expect(getClientBuildVersion()).toBe('custom-abc1234')
  })

  test('returns an empty string for an unstamped build', () => {
    expect(getClientBuildVersion()).toBe('')

    stampVersion('')
    expect(getClientBuildVersion()).toBe('')
  })
})

describe('stale-bundle check', () => {
  test('compares the server header with the version of the loaded page', () => {
    stampVersion('v1.0.0')
    flushPendingStaleBundleNotice()

    checkServerVersionHeader({ 'x-new-api-version': 'v1.0.0' })
    expect(toast).not.toHaveBeenCalled()

    checkServerVersionHeader({ 'x-new-api-version': 'v1.0.1' })
    expect(toast).toHaveBeenCalledTimes(1)
    expect(toast.mock.calls[0]?.[0]).toBe(
      'A new version is available. Reload to update.'
    )
  })

  test('stays silent for an unstamped page', () => {
    flushPendingStaleBundleNotice()

    checkServerVersionHeader({ 'x-new-api-version': 'v1.0.2' })
    expect(toast).not.toHaveBeenCalled()
  })
})

describe('installBuildMetadata', () => {
  test('shows the stamped version in every place it did before', () => {
    stampVersion('v1.2.3')
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})

    installBuildMetadata()

    const rev = window.__APP_BUILD__?.rev
    expect(rev).toMatch(/^rv\.v1\.2\.3\.[0-9a-z]+$/)
    expect(document.documentElement.getAttribute('data-build-rev')).toBe(rev)
    expect(
      document.querySelector('meta[name="build-id"]')?.getAttribute('content')
    ).toBe(rev)
    expect(
      document.documentElement.style.getPropertyValue('--app-build-rev')
    ).toBe(`'${rev}'`)
    expect(window.localStorage.getItem('app:rev')).toBe(rev)
    expect(debug).toHaveBeenCalledWith('[build] %s', rev)
  })
})
