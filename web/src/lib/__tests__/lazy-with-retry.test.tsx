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
import { act, render, screen } from '@testing-library/react'
import { Suspense, type ComponentType } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { LazyChunkBoundary } from '@/components/lazy-chunk-boundary'

import { lazyWithRetry } from '../lazy-with-retry'

const CHUNK_URL = 'http://localhost/static/js/async/9.js'

function chunkLoadError(): Error {
  const error = new Error(`Loading chunk 9 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

type PartModule = { default: ComponentType<{ label: string }> }

/** A loader that fails while `network.up` is false. */
function flakyLoader() {
  const network = { up: false }
  const load = vi.fn((): Promise<PartModule> =>
    network.up
      ? Promise.resolve({ default: (props) => <p>{props.label}</p> })
      : Promise.reject(chunkLoadError())
  )
  return { load, network }
}

function view(Part: ComponentType<{ label: string }>, attempt: number) {
  return (
    <LazyChunkBoundary fallback={<p>fallback</p>} resetKey={attempt}>
      <Suspense fallback={<p>loading</p>}>
        <Part label='the part' />
      </Suspense>
    </LazyChunkBoundary>
  )
}

async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 5))
    })
  }
}

beforeEach(() => {
  // This tab already spent its one automatic reload for the chunk.
  window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  window.sessionStorage.clear()
  vi.restoreAllMocks()
})

describe('lazyWithRetry', () => {
  test('a part whose chunk failed loads again on its next attempt, once the network is back', async () => {
    const { load, network } = flakyLoader()
    const Part = lazyWithRetry(load)
    const { rerender } = render(view(Part, 1))
    await settle()
    expect(screen.getByText('fallback')).toBeVisible()
    expect(load).toHaveBeenCalledTimes(1)

    // Still offline: one more request per attempt, never a loop.
    rerender(view(Part, 2))
    await settle()
    expect(load).toHaveBeenCalledTimes(2)
    expect(screen.getByText('fallback')).toBeVisible()

    network.up = true
    rerender(view(Part, 3))
    await settle()
    expect(load).toHaveBeenCalledTimes(3)
    expect(screen.getByText('the part')).toBeVisible()
  })

  test('a part that failed loads again when it is mounted again', async () => {
    const { load, network } = flakyLoader()
    const Part = lazyWithRetry(load)
    const first = render(view(Part, 1))
    await settle()
    expect(screen.getByText('fallback')).toBeVisible()
    first.unmount()

    network.up = true
    render(view(Part, 1))
    await settle()
    expect(load).toHaveBeenCalledTimes(2)
    expect(screen.getByText('the part')).toBeVisible()
  })

  test('a part that loaded is not loaded again', async () => {
    const { load, network } = flakyLoader()
    network.up = true
    const Part = lazyWithRetry(load)
    const first = render(view(Part, 1))
    await settle()
    expect(screen.getByText('the part')).toBeVisible()
    first.unmount()

    render(view(Part, 2))
    await settle()
    expect(screen.getByText('the part')).toBeVisible()
    expect(load).toHaveBeenCalledTimes(1)
  })
})
