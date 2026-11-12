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
  createRouter,
  createRootRoute,
  createMemoryHistory,
  RouterContextProvider,
} from '@tanstack/react-router'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { ChannelsProvider } from '../channels-provider'
import { ChannelMutateDrawer } from '../drawers/channel-mutate-drawer'

const originalAuth = useAuthStore.getState().auth
let client: QueryClient

// /api/channel/models lists every adaptor's models with the adaptor as owner.
const catalogue = [
  { id: 'gpt-4o', owned_by: 'openai' },
  { id: 'claude-sonnet-4-5', owned_by: 'anthropic' },
  { id: 'nai-diffusion-4-5-full', owned_by: 'novelai' },
  { id: 'nai-diffusion-3', owned_by: 'novelai' },
]

async function createNovelAIChannel() {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <ChannelsProvider>
          <ChannelMutateDrawer open onOpenChange={vi.fn()} />
        </ChannelsProvider>
      </RouterContextProvider>
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('option', { name: 'NovelAI Built-in #64' }))
  return user
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/models') {
      return { data: { success: true, data: catalogue } }
    }
    if (url === '/api/channel/default_base_urls') {
      return {
        data: { success: true, data: { 64: 'https://image.novelai.net' } },
      }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    return { data: { success: true, data: [] } }
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
  window.localStorage.clear()
})

test('filling related models on a NovelAI channel adds only NovelAI models', async () => {
  const user = await createNovelAIChannel()
  const fill = await screen.findByRole('button', {
    name: 'Fill Related Models',
  })
  await waitFor(() => expect(fill).toBeEnabled())

  await user.click(fill)

  const models = within(screen.getByRole('group', { name: 'Models' }))
  expect(
    models.getByRole('button', { name: 'nai-diffusion-4-5-full' })
  ).toBeVisible()
  expect(models.getByRole('button', { name: 'nai-diffusion-3' })).toBeVisible()
  expect(models.queryByRole('button', { name: 'gpt-4o' })).toBeNull()
  expect(models.queryByRole('button', { name: 'claude-sonnet-4-5' })).toBeNull()
})

test('fetching from upstream on a NovelAI channel offers the models the server lists for type 64', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: ['nai-diffusion-5-full', 'nai-diffusion-3'] },
  })
  const user = await createNovelAIChannel()
  await user.type(screen.getByLabelText('API Key *'), 'pst-test')

  await user.click(
    await screen.findByRole('button', { name: 'Fetch from Upstream' })
  )

  expect(
    await screen.findByRole('checkbox', { name: 'nai-diffusion-5-full' })
  ).toBeInTheDocument()
  expect(
    screen.getByRole('checkbox', { name: 'nai-diffusion-3' })
  ).toBeInTheDocument()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/fetch_models',
    expect.objectContaining({ type: 64, key: 'pst-test' }),
    expect.anything()
  )
})
