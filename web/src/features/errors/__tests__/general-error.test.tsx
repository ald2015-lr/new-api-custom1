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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { GeneralError } from '../general-error'

const navigate = vi.hoisted(() => vi.fn())
const historyGo = vi.hoisted(() => vi.fn())

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouter: () => ({ history: { go: historyGo } }),
}))

const CHUNK_URL = 'http://localhost/static/js/async/42.js'

/** Shape thrown by rspack's JSONP chunk loader in production builds. */
function rspackChunkLoadError(): Error {
  const error = new Error(`Loading chunk 42 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

describe('GeneralError', () => {
  const reload = vi.fn()

  beforeEach(() => {
    vi.stubGlobal('location', { ...window.location, reload })
    window.sessionStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  test('first chunk load failure reloads the page once and renders nothing', () => {
    const { container } = render(
      <GeneralError error={rspackChunkLoadError()} reset={() => undefined} />
    )

    expect(reload).toHaveBeenCalledTimes(1)
    expect(container).toBeEmptyDOMElement()
  })

  test('chunk load failure that already triggered a reload shows the chunk notice with a Reload button and no numeric heading', async () => {
    window.sessionStorage.setItem(
      `newapi:chunk-reload:${CHUNK_URL}`,
      String(Date.now())
    )

    render(
      <GeneralError error={rspackChunkLoadError()} reset={() => undefined} />
    )

    expect(reload).not.toHaveBeenCalled()
    expect(
      screen.getByText('Failed to load part of the page')
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'A new version may have been deployed. Please reload the page.',
        { exact: false }
      )
    ).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Reload page' }))

    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('axios-like 429 error renders the Too many requests notice with the 429 heading', () => {
    render(<GeneralError error={{ response: { status: 429 } }} />)

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('429')
    expect(screen.getByText('Too many requests')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Reload page' })
    ).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  test('no error renders the generic 500 heading and the Reload button', () => {
    render(<GeneralError />)

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('500')
    expect(
      screen.getByText('Oops! Something went wrong', { exact: false })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Reload page' })
    ).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })
})
