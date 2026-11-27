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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { ChannelsDialogs } from '../channels-dialogs'
import { ChannelsProvider, useChannels } from '../channels-provider'

const CHUNK_URL = 'http://localhost/static/js/async/7.js'

// The chunk of the test dialog cannot be loaded the first time (the network
// dropped, and this tab already spent its one automatic reload on it); it
// loads fine afterwards.
const chunk = vi.hoisted(() => ({ loads: 0 }))
vi.mock('../dialogs/channel-test-dialog', () => ({
  get ChannelTestDialog() {
    chunk.loads += 1
    if (chunk.loads === 1) {
      const error = new Error(
        'Loading chunk 7 failed.\n(error: http://localhost/static/js/async/7.js)'
      )
      error.name = 'ChunkLoadError'
      Object.assign(error, {
        request: 'http://localhost/static/js/async/7.js',
        type: 'error',
      })
      throw error
    }
    return (props: { open: boolean }) =>
      props.open ? <div role='dialog'>channel test dialog</div> : null
  },
}))

function Opener() {
  const { setOpen } = useChannels()
  return (
    <button type='button' onClick={() => setOpen('test-channel')}>
      open test
    </button>
  )
}

function Harness() {
  const [router] = useState(() =>
    createRouter({
      routeTree: createRootRoute(),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
  )
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: false } } })
  )
  return (
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <ChannelsProvider>
          <Opener />
          <ChannelsDialogs />
        </ChannelsProvider>
      </RouterContextProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: [] } })
})

afterEach(() => {
  window.sessionStorage.clear()
  vi.restoreAllMocks()
})

test('a channel dialog whose chunk failed to load loads again when it is opened again', async () => {
  const notify = vi.spyOn(toast, 'error').mockReturnValue('toast')
  const user = userEvent.setup()
  render(<Harness />)
  const open = screen.getByRole('button', { name: 'open test' })

  await user.click(open)
  await waitFor(() =>
    expect(notify).toHaveBeenCalledWith(
      'Failed to load part of the page',
      expect.anything()
    )
  )
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

  await user.click(open)

  expect(await screen.findByRole('dialog')).toHaveTextContent(
    'channel test dialog'
  )
  expect(chunk.loads).toBe(2)
  expect(notify).toHaveBeenCalledTimes(1)
})
