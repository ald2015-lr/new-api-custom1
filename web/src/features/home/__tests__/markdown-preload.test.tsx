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
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

// A markdown home page renders through LazyMarkdown; the hook that loads the
// content starts downloading the renderer as soon as it knows the content is
// markdown. Downloads are counted through a mock that returns the real module.
const renderer = { loads: 0 }
const homeApi = vi.hoisted(() => ({ getHomePageContent: vi.fn() }))
vi.mock('../api', () => homeApi)

const STORAGE_KEY = 'home_page_content'

async function renderContentHook() {
  const { useHomePageContent } = await import('../hooks/use-home-page-content')
  return renderHook(() => useHomePageContent())
}

describe('home page content and the markdown renderer', () => {
  beforeEach(() => {
    vi.resetModules()
    renderer.loads = 0
    vi.doMock('@/components/ui/markdown', async (importOriginal) => {
      renderer.loads += 1
      return importOriginal()
    })
    window.localStorage.clear()
  })

  afterEach(() => {
    vi.doUnmock('@/components/ui/markdown')
    window.localStorage.clear()
  })

  test('cached markdown content starts the renderer download while the content request is still open', async () => {
    window.localStorage.setItem(STORAGE_KEY, '# Welcome\n\nRead the **docs**.')
    homeApi.getHomePageContent.mockReturnValue(new Promise(() => undefined))

    await renderContentHook()

    await waitFor(() => expect(renderer.loads).toBe(1))
  })

  test('markdown content from the server starts the renderer download', async () => {
    homeApi.getHomePageContent.mockResolvedValue({
      success: true,
      data: '## Plans\n\n- Free\n- Pro',
    })

    const { result } = await renderContentHook()

    await waitFor(() => expect(result.current.isLoaded).toBe(true))
    await waitFor(() => expect(renderer.loads).toBe(1))
  })

  test.each([
    ['an HTML page', '<div class="hero"><h1>Welcome</h1></div>'],
    ['an embedded URL', 'https://example.com/landing'],
    ['an empty page', ''],
  ])('%s does not download the markdown renderer', async (_label, data) => {
    homeApi.getHomePageContent.mockResolvedValue({ success: true, data })

    const { result } = await renderContentHook()

    await waitFor(() => expect(result.current.isLoaded).toBe(true))
    expect(result.current.content).toBe(data)
    expect(renderer.loads).toBe(0)
  })
})
