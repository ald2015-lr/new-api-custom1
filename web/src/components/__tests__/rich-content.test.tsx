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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

// RichContent renders markdown through a lazily loaded renderer; each test
// imports a fresh copy so the lazy component starts unloaded.
async function importRichContent() {
  return (await import('../rich-content')).RichContent
}

function chunkLoadError() {
  const url = 'http://localhost/static/js/async/35875.45effd731f.js'
  return Object.assign(
    new Error(`Loading chunk 35875 failed.\n(error: ${url})`),
    { name: 'ChunkLoadError', type: 'error', request: url }
  )
}

const MARKDOWN = 'Maintenance at **10:00** tonight\nsecond line'

describe('RichContent', () => {
  const reload = vi.fn()

  beforeEach(() => {
    vi.resetModules()
    vi.stubGlobal('location', { ...window.location, reload })
    window.sessionStorage.clear()
  })

  afterEach(() => {
    vi.doUnmock('@/components/ui/markdown')
    vi.unstubAllGlobals()
    reload.mockReset()
    window.sessionStorage.clear()
  })

  test('markdown shows a loading placeholder and then the formatted text once the renderer loads', async () => {
    const RichContent = await importRichContent()

    const { container } = render(<RichContent breaks content={MARKDOWN} />)

    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull()
    const strong = await screen.findByText('10:00')
    expect(strong.tagName).toBe('STRONG')
    expect(container.querySelector('br')).not.toBeNull()
    expect(container.querySelector('[aria-busy="true"]')).toBeNull()
  })

  test('HTML content renders in the first render without the markdown renderer', async () => {
    const RichContent = await importRichContent()

    render(<RichContent mode='html' content='<p>Hello <em>there</em></p>' />)

    expect(screen.getByText('there').tagName).toBe('EM')
  })

  test('a renderer chunk missing after a deploy reloads the page once and keeps the text readable', async () => {
    const error = chunkLoadError()
    vi.doMock('@/components/ui/markdown', () => ({
      get Markdown() {
        throw error
      },
    }))
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const RichContent = await importRichContent()

    render(<RichContent content={MARKDOWN} />)

    expect(
      await screen.findByText(/Maintenance at \*\*10:00\*\* tonight/)
    ).toBeInTheDocument()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('any other renderer failure shows the text as plain text without reloading', async () => {
    vi.doMock('@/components/ui/markdown', () => ({
      get Markdown() {
        throw new TypeError('renderer exploded')
      },
    }))
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const RichContent = await importRichContent()

    render(<RichContent content={MARKDOWN} className='notice-text' />)

    const text = await screen.findByText(/Maintenance at \*\*10:00\*\* tonight/)
    expect(text).toHaveClass('notice-text', 'whitespace-pre-wrap')
    expect(text.textContent).toBe(MARKDOWN)
    expect(reload).not.toHaveBeenCalled()
  })
})
