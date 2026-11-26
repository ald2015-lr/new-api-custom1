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
import { describe, expect, test } from 'vitest'

import {
  isConsoleChunk,
  type ChunkGroupLike,
  type ChunkLike,
} from '../console-chunks.mjs'

const SRC = '/build/web/src'

/** The group of a route file's code-split component, as TanStack imports it. */
function routeSplit(file: string): ChunkGroupLike {
  return {
    origins: [{ request: `${SRC}/routes/${file}?tsr-split=component` }],
    getParents: () => [entry],
  }
}

/** A chunk group of an `import()` inside the code of `parents`. */
function lazyImport(...parents: ChunkGroupLike[]): ChunkGroupLike {
  return {
    origins: [{ request: '../components/some-dialog' }],
    getParents: () => parents,
  }
}

const entry: ChunkGroupLike = { origins: [], getParents: () => [] }

function chunk(groups: ChunkGroupLike[], initial = false): ChunkLike {
  return { canBeInitial: () => initial, groupsIterable: groups }
}

describe('isConsoleChunk', () => {
  test.each([
    '_authenticated/route.tsx',
    '_authenticated/keys/index.tsx',
    '_authenticated/system-settings/billing/$section.tsx',
  ])('the code of the console route %s is a console chunk', (file) => {
    expect(isConsoleChunk(chunk([routeSplit(file)]))).toBe(true)
  })

  test.each([
    'index.tsx',
    'pricing/index.tsx',
    'pricing/$modelId/index.tsx',
    '(auth)/sign-in.tsx',
  ])('the code of the public route %s is not', (file) => {
    expect(isConsoleChunk(chunk([routeSplit(file)]))).toBe(false)
  })

  test('a dialog chunk of a console page is a console chunk', () => {
    const channels = routeSplit('_authenticated/channels/index.tsx')
    const editor = lazyImport(channels)
    expect(isConsoleChunk(chunk([lazyImport(editor)]))).toBe(true)
  })

  test('a lazy chunk that a public page loads too is not', () => {
    const channels = routeSplit('_authenticated/channels/index.tsx')
    const pricing = routeSplit('pricing/index.tsx')
    expect(isConsoleChunk(chunk([lazyImport(channels, pricing)]))).toBe(false)
  })

  test('a chunk shared by a console and a public route is not', () => {
    const shared = chunk([
      routeSplit('_authenticated/models/$section.tsx'),
      routeSplit('pricing/$modelId/index.tsx'),
    ])
    expect(isConsoleChunk(shared)).toBe(false)
  })

  test('chunks of the first load and of the entry are not', () => {
    expect(
      isConsoleChunk(chunk([routeSplit('_authenticated/route.tsx')], true))
    ).toBe(false)
    expect(isConsoleChunk(chunk([lazyImport(entry)]))).toBe(false)
    expect(isConsoleChunk(chunk([]))).toBe(false)
  })

  test('lazy imports that load each other end with an answer', () => {
    const parents: ChunkGroupLike[] = []
    const a: ChunkGroupLike = {
      origins: [{ request: './a' }],
      getParents: () => parents,
    }
    const b = lazyImport(a)
    parents.push(b)
    expect(isConsoleChunk(chunk([a]))).toBe(false)
  })
})
