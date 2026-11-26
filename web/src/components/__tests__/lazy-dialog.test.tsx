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
import type { ComponentType } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { lazyWithRetry } from '@/lib/lazy-with-retry'

import { LazyDialog } from '../lazy-dialog'

type DialogProps = { open: boolean }
type DialogModule = { default: ComponentType<DialogProps> }

const CHUNK_URL = 'http://localhost/static/js/async/7.js'

function chunkLoadError(): Error {
  const error = new Error(`Loading chunk 7 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

/** A lazy dialog whose chunk arrives when the test says so. */
function deferredDialog() {
  let settle!: { resolve: () => void; reject: (error: unknown) => void }
  const load = vi.fn(
    () =>
      new Promise<DialogModule>((resolve, reject) => {
        settle = {
          resolve: () =>
            resolve({
              default: (props) => (
                <div role='dialog' data-open={String(props.open)}>
                  dialog
                </div>
              ),
            }),
          reject,
        }
      })
  )
  return { Dialog: lazyWithRetry(load), load, settle: () => settle }
}

const reload = vi.fn()

beforeEach(() => {
  vi.stubGlobal('location', { ...window.location, reload })
  window.sessionStorage.clear()
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
  reload.mockReset()
  window.sessionStorage.clear()
})

describe('LazyDialog', () => {
  test('loads nothing before the first opening, then shows a loading notice until the dialog arrives', async () => {
    const { Dialog, load, settle } = deferredDialog()
    const onLoadError = vi.fn()
    const { rerender } = render(
      <LazyDialog open={false} onLoadError={onLoadError}>
        <Dialog open={false} />
      </LazyDialog>
    )
    expect(load).not.toHaveBeenCalled()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

    rerender(
      <LazyDialog open onLoadError={onLoadError}>
        <Dialog open />
      </LazyDialog>
    )
    expect(load).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status')).toHaveTextContent('Loading...')

    await act(async () => settle().resolve())
    expect(screen.getByRole('dialog')).toHaveAttribute('data-open', 'true')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  test('a closed dialog stays mounted, so it can play its closing animation', async () => {
    const { Dialog, load, settle } = deferredDialog()
    const { rerender } = render(
      <LazyDialog open onLoadError={vi.fn()}>
        <Dialog open />
      </LazyDialog>
    )
    await act(async () => settle().resolve())

    rerender(
      <LazyDialog open={false} onLoadError={vi.fn()}>
        <Dialog open={false} />
      </LazyDialog>
    )

    expect(screen.getByRole('dialog')).toHaveAttribute('data-open', 'false')

    // Opening it again loads nothing: the dialog that loaded stays.
    rerender(
      <LazyDialog open onLoadError={vi.fn()}>
        <Dialog open />
      </LazyDialog>
    )
    expect(screen.getByRole('dialog')).toHaveAttribute('data-open', 'true')
    expect(load).toHaveBeenCalledTimes(1)
  })

  test('a chunk that cannot load gets the one automatic reload', async () => {
    const { Dialog, settle } = deferredDialog()
    const onLoadError = vi.fn()
    render(
      <LazyDialog open onLoadError={onLoadError}>
        <Dialog open />
      </LazyDialog>
    )

    await act(async () => settle().reject(chunkLoadError()))

    expect(reload).toHaveBeenCalledTimes(1)
    expect(onLoadError).not.toHaveBeenCalled()
  })

  test('when the reload was already spent, the dialog closes, a toast offers a reload, and opening it again loads it again', async () => {
    window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
    const notify = vi.spyOn(toast, 'error').mockReturnValue('toast')
    const { Dialog, load, settle } = deferredDialog()
    const onLoadError = vi.fn()
    const { rerender } = render(
      <LazyDialog open onLoadError={onLoadError}>
        <Dialog open />
      </LazyDialog>
    )

    await act(async () => settle().reject(chunkLoadError()))

    expect(reload).not.toHaveBeenCalled()
    expect(onLoadError).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledWith(
      'Failed to load part of the page',
      expect.objectContaining({
        action: expect.objectContaining({ label: 'Reload page' }),
      })
    )

    // Closing does not try again; opening again loads the chunk again.
    rerender(
      <LazyDialog open={false} onLoadError={onLoadError}>
        <Dialog open={false} />
      </LazyDialog>
    )
    expect(notify).toHaveBeenCalledTimes(1)
    expect(load).toHaveBeenCalledTimes(1)
    rerender(
      <LazyDialog open onLoadError={onLoadError}>
        <Dialog open />
      </LazyDialog>
    )
    expect(load).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('status')).toHaveTextContent('Loading...')

    // Still unreachable: the same toast again, and the dialog closes.
    await act(async () => settle().reject(chunkLoadError()))
    expect(notify).toHaveBeenCalledTimes(2)
    expect(onLoadError).toHaveBeenCalledTimes(2)

    // The network is back: the next opening shows the dialog.
    rerender(
      <LazyDialog open={false} onLoadError={onLoadError}>
        <Dialog open={false} />
      </LazyDialog>
    )
    rerender(
      <LazyDialog open onLoadError={onLoadError}>
        <Dialog open />
      </LazyDialog>
    )
    expect(load).toHaveBeenCalledTimes(3)
    await act(async () => settle().resolve())
    expect(screen.getByRole('dialog')).toHaveAttribute('data-open', 'true')
    expect(notify).toHaveBeenCalledTimes(2)
    expect(notify).toHaveBeenLastCalledWith(
      'Failed to load part of the page',
      expect.objectContaining({
        description:
          'Check your network connection and try again. If the site was just updated, reload the page.',
      })
    )
  })
})
