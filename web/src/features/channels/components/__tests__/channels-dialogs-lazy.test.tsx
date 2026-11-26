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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { TaskPluginOption } from '../../api'
import { channelSchema, type Channel } from '../../types'
import { ChannelsDialogs } from '../channels-dialogs'
import { ChannelsProvider, useChannels } from '../channels-provider'

// The dialogs of the channel list are lazy chunks: these tests open them
// through ChannelsDialogs, as the list does, with their real code.

type Dialog = Parameters<ReturnType<typeof useChannels>['setOpen']>[0]

const originalAuth = useAuthStore.getState().auth
let client: QueryClient
let channel: Channel

const plugins: TaskPluginOption[] = [
  {
    key: 'video-a',
    name: 'Video A',
    icon: 'text:VA',
    baseUrl: 'https://a.example',
    models: ['video-a-1'],
    upstreams: ['vendor', 'new_api'],
  },
]

function Opener(props: { dialog: Dialog; row: Channel | null }) {
  const { setOpen, setCurrentRow } = useChannels()
  return (
    <button
      type='button'
      onClick={() => {
        setCurrentRow(props.row)
        setOpen(props.dialog)
      }}
    >
      open {props.dialog}
    </button>
  )
}

function Harness(props: { dialog: Dialog; row?: Channel }) {
  const [router] = useState(() =>
    createRouter({
      routeTree: createRootRoute(),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
  )
  return (
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <ChannelsProvider>
          <Opener dialog={props.dialog} row={props.row ?? null} />
          <ChannelsDialogs />
        </ChannelsProvider>
      </RouterContextProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  channel = channelSchema.parse({
    id: 42,
    name: 'Existing channel',
    type: 1,
    key: '',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    models: 'custom-model',
    group: 'default',
    base_url: 'https://saved.example',
  })
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
    if (url === '/api/channel/42') {
      return { data: { success: true, data: channel } }
    }
    if (url === '/api/task_plugin_options') {
      return { data: { success: true, data: plugins } }
    }
    if (url === '/api/request_plugin/instances') {
      return { data: { success: true, data: { instances: [] } } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'custom-model' }] } }
    }
    if (url === '/api/channel/default_base_urls') {
      return { data: { success: true, data: {} } }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/prefill_group/') {
      return { data: { success: true, data: [] } }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
})

afterEach(() => {
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
})

describe('lazily loaded channel dialogs', () => {
  test('nothing of a dialog is on the page before it opens', () => {
    render(<Harness dialog='create-channel' />)

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  test('the channel editor opens with every provider, the built-in NovelAI and plugin channels included', async () => {
    const user = userEvent.setup()
    render(<Harness dialog='create-channel' />)

    await user.click(
      screen.getByRole('button', { name: 'open create-channel' })
    )

    expect(
      await screen.findByRole('option', { name: 'NovelAI Built-in #64' })
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('option', { name: /Video A/ })
    ).toBeInTheDocument()
  })

  test('editing a channel loads it and saves the change', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<Harness dialog='update-channel' row={channel} />)

    await user.click(
      screen.getByRole('button', { name: 'open update-channel' })
    )
    const name = await screen.findByDisplayValue('Existing channel')
    await user.clear(name)
    await user.type(name, 'Renamed channel')
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))

    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({
      id: 42,
      name: 'Renamed channel',
    })
  })

  test.each([
    { dialog: 'test-channel' as const, title: /Test Channel Connection/ },
    { dialog: 'copy-channel' as const, title: /Copy Channel/ },
    { dialog: 'balance-query' as const, title: /Query Balance/ },
  ])('the $dialog dialog opens, closes and opens again', async (row) => {
    const user = userEvent.setup()
    render(<Harness dialog={row.dialog} row={channel} />)
    const open = screen.getByRole('button', { name: `open ${row.dialog}` })

    await user.click(open)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getAllByText(row.title)[0]).toBeVisible()

    await user.click(
      within(dialog).getAllByRole('button', { name: 'Close' })[0]
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )

    await user.click(open)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })
})
