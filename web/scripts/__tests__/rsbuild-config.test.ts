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

import createConfig from '../../rsbuild.config'
import { BUILD_VERSION_META_NAME } from '../../src/lib/build-version-meta'

type ConfigFn = (env: {
  env: string
  envMode: string
  command: string
}) => Promise<Record<string, unknown>> | Record<string, unknown>

async function productionConfig() {
  const config = await (createConfig as unknown as ConfigFn)({
    env: 'production',
    envMode: 'production',
    command: 'build',
  })
  return config as {
    html: { meta: Record<string, string> }
    source: { define?: Record<string, string> }
    splitChunks: { cacheGroups: Record<string, { chunks?: string }> }
    tools: {
      rspack: { optimization?: { runtimeChunk?: { name: string } } }
    }
  }
}

describe('rsbuild.config.ts', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  // The version goes into index.html (served no-cache), so a release does not
  // rename the cached entry script; lib/build-metadata.ts reads it from there.
  test('writes the build version into index.html, not into the bundle', async () => {
    vi.stubEnv('VITE_REACT_APP_VERSION', 'v9.8.7-test')

    const config = await productionConfig()

    expect(config.html.meta[BUILD_VERSION_META_NAME]).toBe('v9.8.7-test')
    expect(JSON.stringify(config.source.define ?? {})).not.toContain(
      'v9.8.7-test'
    )
  })

  test('adds no version tag to an unstamped build', async () => {
    vi.stubEnv('VITE_REACT_APP_VERSION', '')

    const config = await productionConfig()

    expect(config.html.meta).not.toHaveProperty(BUILD_VERSION_META_NAME)
  })

  // The chunk loader with its map of every chunk's file name lives in its own
  // file, and the named vendor groups only take what the first load needs.
  test('keeps the chunk map and lazy-only vendor code out of the entry', async () => {
    const config = await productionConfig()

    expect(config.tools.rspack.optimization?.runtimeChunk).toEqual({
      name: 'runtime',
    })
    const groups = config.splitChunks.cacheGroups
    expect(groups['vendor-ui-primitives']?.chunks).toBe('initial')
    expect(groups['vendor-tanstack']?.chunks).toBe('initial')
    expect(groups).not.toHaveProperty('vendor-react')
  })
})
