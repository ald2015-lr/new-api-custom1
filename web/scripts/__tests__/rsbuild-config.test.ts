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
import { isConsoleChunk } from '../console-chunks.mjs'

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
    splitChunks: {
      cacheGroups: Record<
        string,
        {
          chunks?: unknown
          enforce?: boolean
          minChunks?: number
          name?: string
          test?: RegExp
        }
      >
    }
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

  // html-rspack-plugin writes attribute values into index.html as they are,
  // so a quote or an ampersand in a tag name must reach it escaped.
  test('escapes the build version for the attribute it is written into', async () => {
    const version = 'v1"><script>alert(1)</script>&amp;'
    vi.stubEnv('VITE_REACT_APP_VERSION', version)

    const config = await productionConfig()
    const content = config.html.meta[BUILD_VERSION_META_NAME]

    expect(content).toBe(
      'v1&quot;&gt;&lt;script&gt;alert(1)&lt;/script&gt;&amp;amp;'
    )
    // jsdom's parser; the node project's types have no DOM.
    const { DOMParser } = globalThis as unknown as {
      DOMParser: new () => {
        parseFromString(
          html: string,
          type: 'text/html'
        ): {
          querySelectorAll(selector: string): { length: number }
          querySelector(
            selector: string
          ): { getAttribute(name: string): string | null } | null
        }
      }
    }
    const parsed = new DOMParser().parseFromString(
      `<head><meta name="${BUILD_VERSION_META_NAME}" content="${content}"></head>`,
      'text/html'
    )
    expect(parsed.querySelectorAll('script')).toHaveLength(0)
    expect(
      parsed
        .querySelector(`meta[name="${BUILD_VERSION_META_NAME}"]`)
        ?.getAttribute('content')
    ).toBe(version)
  })

  test('writes an ordinary version unchanged', async () => {
    vi.stubEnv('VITE_REACT_APP_VERSION', 'custom-abc1234')

    const config = await productionConfig()

    expect(config.html.meta[BUILD_VERSION_META_NAME]).toBe('custom-abc1234')
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
    expect(groups['vendor-tanstack']?.chunks).toBe('initial')
    expect(groups).not.toHaveProperty('vendor-react')
    // The first load's few @base-ui modules stay in its vendor chunk.
    expect(groups).not.toHaveProperty('vendor-ui-primitives')
  })

  // The console pages share the UI kit and the data tables through chunks
  // of their own, which the public pages never load.
  test('gives the console pages shared chunks for the UI kit and the data tables', async () => {
    const config = await productionConfig()
    const groups = config.splitChunks.cacheGroups

    for (const name of ['console-kit', 'console-table']) {
      expect(groups[name]).toMatchObject({
        name,
        chunks: isConsoleChunk,
        minChunks: 3,
        enforce: true,
      })
    }
    const kit = groups['console-kit']?.test
    const table = groups['console-table']?.test
    const bun = '/w/node_modules/.bun/pkg@1.0.0/node_modules'
    expect(kit?.test('/w/src/components/ui/select.tsx')).toBe(true)
    expect(kit?.test(`${bun}/@base-ui/react/esm/select/index.js`)).toBe(true)
    expect(kit?.test(`${bun}/react-hook-form/dist/index.esm.mjs`)).toBe(true)
    expect(kit?.test('/w/src/components/data-table/core/table-empty.tsx')).toBe(
      false
    )
    expect(
      table?.test('/w/src/components/data-table/core/table-empty.tsx')
    ).toBe(true)
    expect(table?.test(`${bun}/@tanstack/table-core/build/lib/index.mjs`)).toBe(
      true
    )
    expect(table?.test(`${bun}/@tanstack/react-router/dist/esm/index.js`)).toBe(
      false
    )
  })
})
