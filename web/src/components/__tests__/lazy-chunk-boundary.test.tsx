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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Component, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

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

  test('a new resetKey renders the part again', () => {
    window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
    const { rerender } = render(
      <LazyChunkBoundary fallback={<p>fallback</p>} resetKey={1}>
        <Throws error={chunkLoadError()} />
      </LazyChunkBoundary>
    )
    expect(screen.getByText('fallback')).toBeVisible()

    rerender(
      <LazyChunkBoundary fallback={<p>fallback</p>} resetKey={2}>
        <p>loaded</p>
      </LazyChunkBoundary>
    )

    expect(screen.getByText('loaded')).toBeVisible()
  })
})

test('the chunk error notice offers a reload', async () => {
  const user = userEvent.setup()
  render(<ChunkLoadErrorNotice />)

  expect(screen.getByText('Failed to load part of the page')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Reload page' }))

  expect(reload).toHaveBeenCalledTimes(1)
})
