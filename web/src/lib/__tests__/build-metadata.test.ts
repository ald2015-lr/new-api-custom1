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

import { getClientBuildVersion } from '../build-metadata'

// The stale-bundle check compares this value with the server's
// X-New-Api-Version header, so the build stamp must actually reach the
// bundle through import.meta.env (rsbuild.config.ts `source.define`).
describe('getClientBuildVersion', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  test('returns the VITE_REACT_APP_VERSION stamped at build time', () => {
    vi.stubEnv('VITE_REACT_APP_VERSION', 'custom-abc1234')

    expect(getClientBuildVersion()).toBe('custom-abc1234')
  })

  test('returns an empty string for an unstamped build', () => {
    vi.stubEnv('VITE_REACT_APP_VERSION', '')

    expect(getClientBuildVersion()).toBe('')
  })
})
