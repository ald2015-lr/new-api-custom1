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
import userEvent from '@testing-library/user-event'
import { Component, Suspense, type ComponentType, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { lazyWithRetry } from '@/lib/lazy-with-retry'

import { ChunkLoadErrorNotice, LazyChunkBoundary } from '../lazy-chunk-boundary'

const CHUNK_URL = 'http://localhost/static/js/async/42.js'

/** Shape thrown by rspack's chunk loader when a lazy chunk cannot load. */
function chunkLoadError(): Error {
  const error = new Error(`Loading chunk 42 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

function Throws(props: { error: unknown }): ReactNode {
  throw props.error
}

/** A lazy part whose chunk fails to load while `network.up` is false. */
function flakyPart() {
  const network = { up: false }
  const load = vi.fn((): Promise<{ default: ComponentType }> =>
    network.up
      ? Promise.resolve({ default: () => <p>loaded</p> })
      : Promise.reject(chunkLoadError())
  )
  return { Part: lazyWithRetry(load), load, network }
}

class OuterBoundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  state = { error: null as Error | null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  render() {
    return this.state.error ? (
      <p>outer boundary: {this.state.error.message}</p>
    ) : (
      this.props.children
    )
  }
}

const reload = vi.fn()

beforeEach(() => {
  vi.stubGlobal('location', { ...window.location, reload })
  window.sessionStorage.clear()
  // React reports every error a boundary catches.
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
  reload.mockReset()
  window.sessionStorage.clear()
})

describe('LazyChunkBoundary', () => {
  test('a chunk that cannot load gets the one automatic reload, without a fallback flash', () => {
    const onChunkError = vi.fn()
    render(
      <>
        <p>rest of the page</p>
        <LazyChunkBoundary
          fallback={<p>fallback</p>}
          onChunkError={onChunkError}
        >
          <Throws error={chunkLoadError()} />
        </LazyChunkBoundary>
      </>
    )

    expect(reload).toHaveBeenCalledTimes(1)
    expect(screen.queryByText('fallback')).not.toBeInTheDocument()
    expect(onChunkError).not.toHaveBeenCalled()
    expect(screen.getByText('rest of the page')).toBeVisible()
  })

  test('when the reload was already spent, the fallback stands in and the page stays', () => {
    window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
    const onChunkError = vi.fn()
    render(
      <>
        <p>rest of the page</p>
        <LazyChunkBoundary
          fallback={<p>fallback</p>}
          onChunkError={onChunkError}
        >
          <Throws error={chunkLoadError()} />
        </LazyChunkBoundary>
      </>
    )

    expect(reload).not.toHaveBeenCalled()
    expect(screen.getByText('fallback')).toBeVisible()
    expect(onChunkError).toHaveBeenCalledTimes(1)
    expect(screen.getByText('rest of the page')).toBeVisible()
  })

  test('any other error reaches the next boundary up unchanged', () => {
    render(
      <OuterBoundary>
        <LazyChunkBoundary fallback={<p>fallback</p>}>
          <Throws error={new Error('render bug')} />
        </LazyChunkBoundary>
      </OuterBoundary>
    )

    expect(screen.getByText('outer boundary: render bug')).toBeVisible()
    expect(screen.queryByText('fallback')).not.toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  test('a new resetKey loads the part that failed again', async () => {
    window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
    const { Part, load, network } = flakyPart()
    const view = (resetKey: number) => (
      <LazyChunkBoundary fallback={<p>fallback</p>} resetKey={resetKey}>
        <Suspense fallback={<p>loading</p>}>
          <Part />
        </Suspense>
      </LazyChunkBoundary>
    )
    const { rerender } = render(view(1))
    await act(async () => undefined)
    expect(screen.getByText('fallback')).toBeVisible()

    network.up = true
    rerender(view(2))
    expect(await screen.findByText('loaded')).toBeVisible()
    expect(load).toHaveBeenCalledTimes(2)
  })

  test('the chunk error notice in its place retries in place', async () => {
    window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
    const user = userEvent.setup()
    const { Part, load, network } = flakyPart()
    render(
      <LazyChunkBoundary fallback={<ChunkLoadErrorNotice />}>
        <Suspense fallback={<p>loading</p>}>
          <Part />
        </Suspense>
      </LazyChunkBoundary>
    )
    expect(
      await screen.findByText('Failed to load part of the page')
    ).toBeVisible()
    expect(
      screen.getByText(
        'Check your network connection and try again. If the site was just updated, reload the page.'
      )
    ).toBeVisible()

    network.up = true
    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('loaded')).toBeVisible()
    expect(load).toHaveBeenCalledTimes(2)
    expect(reload).not.toHaveBeenCalled()
  })
})

test('the chunk error notice offers a reload', async () => {
  const user = userEvent.setup()
  render(<ChunkLoadErrorNotice />)

  expect(screen.getByText('Failed to load part of the page')).toBeVisible()
  // Outside a boundary there is nothing to retry in place.
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  await user.click(screen.getByRole('button', { name: 'Reload page' }))

  expect(reload).toHaveBeenCalledTimes(1)
})
