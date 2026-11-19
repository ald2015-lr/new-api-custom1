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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createRouter,
  createRootRoute,
  createMemoryHistory,
  RouterContextProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import {
  MODERATION_PRESET_DEFAULT_IDS,
  MODERATION_PRESETS,
} from '@/lib/content-moderation'
import { createAppQueryClient } from '@/lib/query-client'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { TaskPluginOption } from '../../api'
import {
  CHANNEL_TYPE_NOVELAI,
  CHANNEL_TYPE_OLLAMA,
  CHANNEL_TYPE_SGLANG,
  CHANNEL_TYPE_VLLM,
  GEMINI_FREE_KEY_ALIASES_MAX,
  GEMINI_FREE_KEY_COOLDOWN_REASON_LABELS,
  GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS,
  GEMINI_FREE_KEY_LIMIT_MAX,
  GEMINI_FREE_KEY_LIMIT_MIN,
  GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT,
  GEMINI_FREE_KEY_MAX_ATTEMPTS_MAX,
  GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS,
  GEMINI_FREE_KEY_MODEL_NAME_MAX,
  GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT,
  GEMINI_FREE_KEY_TOKEN_COUNTERS,
} from '../../constants'
import { validateGeminiFreeKeyModelLimitRows } from '../../lib/gemini-free-key'
import {
  channelSchema,
  type Channel,
  type GeminiFreeKeyUsage,
} from '../../types'
import { ChannelPluginExtensions } from '../channel-plugin-extensions'
import { ChannelsProvider } from '../channels-provider'
import { ChannelMutateDrawer } from '../drawers/channel-mutate-drawer'

const originalAuth = useAuthStore.getState().auth
let client: QueryClient
let editingChannel: Channel
let pluginOptions: TaskPluginOption[]
const plugins: TaskPluginOption[] = [
  {
    key: 'video-a',
    name: 'Video A',
    icon: 'text:VA',
    baseUrl: 'https://a.example',
    models: ['video-a-1'],
    upstreams: ['vendor', 'new_api'],
  },
  {
    key: 'video-b',
    name: 'Video B',
    icon: 'text:VB',
    baseUrl: 'https://b.example',
    models: ['video-b-1'],
    upstreams: ['vendor', 'new_api'],
  },
  {
    // Vendor-only: bindable to a Task Plugin channel, never to a New API one.
    key: 'no-address',
    name: 'No Address',
    icon: 'text:NA',
    models: ['video-c-1'],
  },
]

const soraPlugin: TaskPluginOption = {
  key: 'sora',
  name: 'Sora',
  icon: 'text',
  baseUrl: 'https://video.example',
  models: ['sora-2'],
}

function deferredResponse<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((finish) => {
    resolve = finish
  })
  return { promise, resolve }
}

function ConfigurationHarness(props: {
  initialOpen?: boolean
  currentRow?: Channel
  clearRowOnClose?: boolean
}) {
  const [open, setOpen] = useState(props.initialOpen ?? true)
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
          <button type='button' onClick={() => setOpen(true)}>
            Open channel
          </button>
          <ChannelMutateDrawer
            open={open}
            onOpenChange={setOpen}
            currentRow={
              open || !props.clearRowOnClose ? props.currentRow : null
            }
          />
        </ChannelsProvider>
      </RouterContextProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  pluginOptions = plugins
  editingChannel = channelSchema.parse({
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
      return { data: { success: true, data: editingChannel } }
    }
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['upstream-model'] } }
    }
    if (url === '/api/task_plugin_options') {
      return { data: { success: true, data: pluginOptions } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'custom-model' }] } }
    }
    if (url === '/api/channel/default_base_urls') {
      return {
        data: {
          success: true,
          data: {
            22: 'https://fastgpt.server.example/api/openapi',
            24: 'https://gemini.server.example',
            43: 'https://deepseek.server.example',
            45: 'https://volcengine.server.example',
          },
        },
      }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default', 'premium'] } }
    }
    if (url === '/api/prefill_group') {
      return { data: { success: true, data: [] } }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
  window.localStorage.clear()
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: 1024,
  })
})

// Editing opens the sheet on the left, so the sheet spans x = 0..900 and the
// free space for the floating panel is on its right.
function useWideScreen() {
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: 1920,
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 900,
    bottom: 900,
    width: 900,
    height: 900,
    toJSON: () => ({}),
  } as DOMRect)
}

test('changing built-in providers updates server-provided URL placeholders without replacing the draft address', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^DeepSeek / }))
  const address = screen.getByRole('textbox', { name: 'Base URL' })
  await waitFor(() =>
    expect(address).toHaveAttribute(
      'placeholder',
      'https://deepseek.server.example'
    )
  )
  expect(address).toHaveValue('')
  await user.type(address, 'https://custom.example')

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^Gemini / }))
  const geminiAddress = screen.getByRole('textbox', { name: 'Base URL' })
  expect(geminiAddress).toHaveAttribute(
    'placeholder',
    'https://gemini.server.example'
  )
  expect(geminiAddress).toHaveValue('https://custom.example')
  await user.clear(geminiAddress)
  expect(geminiAddress).toHaveValue('')

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^New API / }))
  expect(screen.getByRole('textbox', { name: 'Base URL' })).toHaveAttribute(
    'placeholder',
    'Leave empty to use default'
  )
})

test.each([
  {
    type: 43,
    label: /^Base URL$/,
    url: 'https://deepseek.server.example',
    savedUrl: '',
  },
  {
    type: 22,
    label: /^Private Deployment URL$/,
    url: 'https://fastgpt.server.example/api/openapi',
    savedUrl: '',
  },
  {
    type: 45,
    label: /^API Base URL/,
    url: 'https://volcengine.server.example',
    savedUrl: 'https://custom.example',
  },
  {
    type: CHANNEL_TYPE_VLLM,
    label: /^Base URL/,
    url: 'vLLM server address, without /v1',
    savedUrl: 'http://localhost:8000',
  },
  {
    type: CHANNEL_TYPE_SGLANG,
    label: /^Base URL/,
    url: 'SGLang server address, without /v1',
    savedUrl: 'http://localhost:30000',
  },
])(
  'editing type $type keeps the URL placeholder out of the saved address',
  async ({ type, label, url, savedUrl }) => {
    editingChannel.type = type
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    if (type === 45) {
      const addressLabel = screen.getByText('API Base URL')
      for (let click = 0; click < 10; click++) {
        fireEvent.click(addressLabel)
      }
    }
    const address = screen.getByRole('textbox', { name: label })
    await waitFor(() => expect(address).toHaveAttribute('placeholder', url))
    expect(address).toHaveValue('https://saved.example')
    await user.clear(address)
    expect(address).toHaveValue('')
    if (savedUrl) await user.type(address, savedUrl)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, base_url: savedUrl })
  }
)

test('an unavailable default URL endpoint keeps the fallback placeholder and allows saving a custom address', async () => {
  const onInternalServerError = vi.fn()
  client = createAppQueryClient(onInternalServerError)
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/default_base_urls') {
      throw Object.assign(new Error('Endpoint unavailable'), {
        response: { status: 500 },
      })
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const address = screen.getByRole('textbox', { name: 'Base URL' })
  expect(address).toHaveAttribute('placeholder', 'Leave empty to use default')
  expect(address).toHaveValue('https://saved.example')
  await user.clear(address)
  await user.type(address, 'https://custom.example')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({
    id: 42,
    base_url: 'https://custom.example',
  })
  expect(api.get).toHaveBeenCalledWith('/api/channel/default_base_urls')
  expect(onInternalServerError).not.toHaveBeenCalled()
})

test('model mapping help opens on click, stays open after pointer exit, and closes without dismissing the channel', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  const trigger = screen.getByRole('button', {
    name: 'How model mapping works',
  })
  expect(trigger).toHaveAttribute('type', 'button')
  expect(trigger).toHaveClass('size-4')
  expect(trigger).toHaveAttribute('aria-expanded', 'false')

  await user.click(trigger)
  const help = await screen.findByRole('dialog', { name: 'Request flow' })
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  expect(within(help).getByText('client-model')).toBeVisible()
  expect(within(help).getByText('upstream-model')).toBeVisible()
  await user.unhover(trigger)
  expect(help).toBeVisible()

  await user.keyboard('{Escape}')
  await waitFor(() => expect(help).not.toBeInTheDocument())
  expect(trigger).toHaveFocus()
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(screen.getByRole('dialog', { name: 'Edit Channel' })).toBeVisible()
})

test('model mapping help supports keyboard activation and wraps long model names within the viewport', async () => {
  editingChannel.model_mapping =
    '{"customer-production-reasoning-model-alias":"provider/region/deployment/production-reasoning-model-version"}'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  const trigger = screen.getByRole('button', {
    name: 'How model mapping works',
  })
  trigger.focus()
  await user.keyboard('{Enter}')
  const help = await screen.findByRole('dialog', { name: 'Request flow' })
  expect(help).toHaveClass(
    'flex-col',
    'w-96',
    'max-w-[calc(100vw-2rem)]',
    'text-sm'
  )
  expect(
    within(help).getByText('customer-production-reasoning-model-alias')
  ).toHaveClass('wrap-anywhere')
  expect(
    within(help).getByText(
      'provider/region/deployment/production-reasoning-model-version'
    )
  ).toHaveClass('wrap-anywhere')

  await user.keyboard('{Escape}')
  await waitFor(() => expect(help).not.toBeInTheDocument())
  await user.keyboard(' ')
  expect(
    await screen.findByRole('dialog', { name: 'Request flow' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Request flow' })
    ).not.toBeInTheDocument()
  )
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
})

test('selecting a plugin opens a prefilled channel and creates its explicit binding', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  expect(
    screen.getByRole('combobox', {
      name: 'Search providers, plugins, or type numbers',
    })
  ).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: /^Name\s*\*$/ })
  ).not.toBeInTheDocument()
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByRole('textbox', { name: /^Name\s*\*$/ })).toHaveValue(
    'Video A'
  )
  expect(screen.getByDisplayValue('https://a.example')).toBeVisible()
  expect(screen.queryByLabelText('Task plugin *')).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'channel-secret' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() => expect(post).toHaveBeenCalled())
  const [url, payload] = post.mock.calls[0]
  expect(url).toBe('/api/channel')
  expect(payload).toMatchObject({
    mode: 'single',
    channel: {
      name: 'Video A',
      type: 61,
      key: 'channel-secret',
      models: 'video-a-1',
      base_url: 'https://a.example',
      group: 'default',
    },
  })
  expect(
    JSON.parse((payload as { channel: { setting: string } }).channel.setting)
      .task_plugin_key
  ).toBe('video-a')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Open channel' }))
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByLabelText('API Key *')).toHaveValue('')
})

test('changing plugins preserves credentials and custom settings while applying existing model and address rules', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'keep-secret' },
  })
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'My channel' },
  })
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '7' },
  })
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(
    screen.getByRole('option', { name: 'Video B Plugin video-b' })
  )
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(screen.getByLabelText('API Key *')).toHaveValue('keep-secret')
  expect(screen.getByLabelText('Name *')).toHaveValue('My channel')
  expect(screen.getByDisplayValue('https://b.example')).toBeVisible()
  fireEvent.change(screen.getByDisplayValue('https://b.example'), {
    target: { value: 'https://custom.example' },
  })
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  expect(screen.getByDisplayValue('https://custom.example')).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByLabelText('Priority')).toHaveValue(7)
})

test('canceling provider selection or selecting the same provider preserves adjusted models', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  await user.click(screen.getByRole('button', { name: 'Clear All' }))
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  expect(screen.getByRole('dialog')).toHaveAccessibleDescription(/Video A/)
  expect(screen.getByRole('option', { name: /Video A/ })).toHaveAttribute(
    'aria-current',
    'true'
  )
  expect(
    within(
      screen.getByRole('button', { name: 'Back to configuration' })
    ).getByText('Video A')
  ).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Back to configuration' })
  )
  expect(screen.getByRole('button', { name: 'Change provider' })).toHaveFocus()
  expect(
    screen.getByRole('button', { name: 'Configure Models' })
  ).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  expect(
    screen.getByRole('button', { name: 'Configure Models' })
  ).toBeDisabled()
})

test.each(['Cancel', 'Escape'])(
  '%s while changing an existing provider returns to the same draft and category',
  async (action) => {
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
    fireEvent.change(screen.getByLabelText('Priority'), {
      target: { value: '8' },
    })
    await user.click(screen.getByRole('button', { name: 'Change provider' }))
    if (action === 'Escape') {
      await user.keyboard('{Escape}')
    } else {
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
    }
    expect(screen.getByRole('dialog')).toBeVisible()
    expect(
      screen.getByRole('tab', { name: /Routing & Mapping/ })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText('Priority')).toHaveValue(8)
    const providerControl = screen.getByRole('button', {
      name: 'Change provider',
    })
    expect(providerControl).toHaveFocus()
    expect(providerControl).toHaveTextContent('OpenAI')
  }
)

test('submitting a missing plugin address focuses its field without leaving creation', async () => {
  const post = vi.spyOn(api, 'post')
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /No Address/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(
      screen.getByRole('tab', { name: /Connection & Models/ })
    ).toHaveAttribute('aria-selected', 'true')
  )
  expect(
    screen.getByText('Base URL is required for this channel type')
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  await waitFor(() =>
    expect(
      screen.queryByText('Base URL is required for this channel type')
    ).not.toBeInTheDocument()
  )
})

test('a hidden creation drawer does not request plugin options', async () => {
  render(<ConfigurationHarness initialOpen={false} />)
  expect(api.get).not.toHaveBeenCalledWith('/api/task_plugin_options')
  await userEvent.click(screen.getByRole('button', { name: 'Open channel' }))
  expect(await screen.findByRole('option', { name: /Video A/ })).toBeVisible()
})

test('configuration navigation retains its height when the form content overflows', async () => {
  render(<ConfigurationHarness />)
  await userEvent.click(await screen.findByRole('option', { name: /Video A/ }))
  const navigation = screen.getByRole('tablist', {
    name: 'Channel configuration',
  })
  expect(navigation.parentElement).toHaveClass('shrink-0')
  expect(screen.getByRole('dialog')).toHaveClass('sm:max-w-7xl')
})

test('without plugin binding permission only built-in providers are offered', () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 2,
        username: 'admin',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { sensitive_write: true } },
        },
      },
    },
  })
  render(<ConfigurationHarness />)
  expect(screen.getByRole('option', { name: /^OpenAI / })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Plugins' })
  ).not.toBeInTheDocument()
  expect(api.get).not.toHaveBeenCalledWith('/api/task_plugin_options')
})

test('plugin loading failure can be retried while built-in providers remain selectable', async () => {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options' && fail) {
      fail = false
      throw new Error('Offline')
    }
    return originalGet?.(url, config)
  })
  render(<ConfigurationHarness />)
  expect(await screen.findByText('Failed to load plugins')).toBeVisible()
  expect(screen.getByRole('option', { name: /^OpenAI / })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('option', { name: /Video A/ })).toBeVisible()
})

test('creating a migrated provider uses its plugin binding instead of the legacy type', async () => {
  pluginOptions = [soraPlugin]
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  const plugin = await screen.findByRole('option', { name: 'Sora Plugin sora' })
  expect(
    screen.queryByRole('option', { name: 'Sora Built-in #55' })
  ).not.toBeInTheDocument()
  await user.click(plugin)
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        channel: expect.objectContaining({ type: 61 }),
      }),
      expect.anything()
    )
  )
  const payload = post.mock.calls[0]?.[1] as { channel: { setting: string } }
  expect(JSON.parse(payload.channel.setting)).toMatchObject({
    task_plugin_key: 'sora',
  })
})

test.each(['create', 'edit'])(
  '%s selects plugin models in the shared model dialog while preserving the built-in connection',
  async (mode) => {
    pluginOptions = [
      {
        ...soraPlugin,
        channelTypes: [1, 55],
        description: { en: 'Video generation' },
        models: [
          'custom-model',
          'sora-2',
          'sora-3',
          'sora-4',
          'sora-5',
          'sora-6',
          'sora-7',
        ],
      },
    ]
    editingChannel = {
      ...editingChannel,
      model_mapping: '{"custom-model":"upstream-model"}',
    }
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true } })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(
      <ConfigurationHarness
        currentRow={mode === 'edit' ? editingChannel : undefined}
      />
    )
    if (mode === 'create') {
      await user.click(
        screen.getByRole('option', { name: 'OpenAI Built-in #1' })
      )
      fireEvent.change(screen.getByLabelText('Name *'), {
        target: { value: 'Combined channel' },
      })
      fireEvent.change(screen.getByLabelText('API Key *'), {
        target: { value: 'channel-key' },
      })
      fireEvent.change(screen.getByLabelText(/Base URL/), {
        target: { value: 'https://channel.example' },
      })
    } else {
      await screen.findByDisplayValue('Existing channel')
    }
    const extensions = await screen.findByRole('group', {
      name: 'Plugin extensions',
    })
    expect(extensions).toHaveClass('flex-wrap')
    expect(
      within(extensions).queryByText('Video generation')
    ).not.toBeInTheDocument()
    expect(within(extensions).queryByText('sora-2')).not.toBeInTheDocument()
    const extension = within(extensions).getByRole('button', {
      name: `Sora Selected ${mode === 'edit' ? 1 : 0} / 7`,
    })
    expect(extension).toHaveAttribute('aria-haspopup', 'dialog')
    expect(
      screen.getByRole('button', { name: 'Configure Models' })
    ).toBeEnabled()
    await user.click(extension)
    const dialog = within(
      screen.getByRole('dialog', { name: 'Configure Models' })
    )
    expect(dialog.getByRole('tab', { name: 'Sora' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(dialog.getByRole('checkbox', { name: 'sora-7' })).not.toBeChecked()
    expect(dialog.getByRole('checkbox', { name: 'sora-2' })).not.toBeChecked()
    expect(
      dialog.getByRole('checkbox', { name: 'custom-model' })
    ).toHaveAttribute('aria-checked', String(mode === 'edit'))
    await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
    await user.click(dialog.getByRole('button', { name: 'Apply' }))
    expect(extension).toHaveAccessibleName(
      `Sora Selected ${mode === 'edit' ? 2 : 1} / 7`
    )
    expect(screen.getByRole('button', { name: 'sora-2' })).toBeVisible()
    const selector = screen.getByRole('combobox', {
      name: 'Select models or add custom ones',
    })
    await user.click(selector)
    expect(
      screen.getAllByRole('option', { name: 'custom-model' })
    ).toHaveLength(1)
    expect(screen.getByRole('option', { name: 'sora-2' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await user.keyboard('{Escape}')
    const submitLabel = mode === 'create' ? 'Create Channel' : 'Update Channel'
    await user.click(screen.getByRole('button', { name: submitLabel }))
    if (mode === 'create') {
      await waitFor(() =>
        expect(post).toHaveBeenCalledWith(
          '/api/channel',
          expect.objectContaining({
            channel: expect.objectContaining({
              type: 1,
              name: 'Combined channel',
              base_url: 'https://channel.example',
              key: 'channel-key',
              models: 'sora-2',
            }),
          }),
          expect.anything()
        )
      )
      const payload = post.mock.calls[0]?.[1] as {
        channel: { setting: string }
      }
      expect(JSON.parse(payload.channel.setting)).not.toHaveProperty(
        'task_plugin_key'
      )
    } else {
      await waitFor(() =>
        expect(put).toHaveBeenCalledWith(
          '/api/channel/',
          expect.objectContaining({
            type: 1,
            base_url: 'https://saved.example',
            models: 'custom-model,sora-2',
            model_mapping: '{"custom-model":"upstream-model"}',
          }),
          expect.anything()
        )
      )
      const payload = put.mock.calls[0]?.[1] as { setting: string }
      expect(JSON.parse(payload.setting)).not.toHaveProperty('task_plugin_key')
    }
  }
)

test('plugin model selection shares the field state, preserves other sources, and discards canceled changes', async () => {
  pluginOptions = [
    {
      ...soraPlugin,
      channelTypes: [1],
      models: ['custom-model', 'sora-2', 'sora-2'],
    },
    { ...plugins[0], channelTypes: [1], models: ['custom-model', 'video-a-1'] },
  ]
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  const extensions = within(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  )
  const sora = extensions.getByRole('button', { name: 'Sora Selected 1 / 2' })
  await user.click(sora)
  let dialog = within(screen.getByRole('dialog', { name: 'Configure Models' }))
  await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
  await user.click(dialog.getByRole('tab', { name: 'Video A' }))
  expect(
    dialog.queryByRole('checkbox', { name: 'sora-2' })
  ).not.toBeInTheDocument()
  expect(dialog.getByRole('checkbox', { name: 'custom-model' })).toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'video-a-1' }))
  await user.click(dialog.getByRole('tab', { name: 'All' }))
  expect(
    dialog.getAllByRole('checkbox', { name: 'custom-model' })
  ).toHaveLength(1)
  expect(dialog.getByRole('checkbox', { name: 'sora-2' })).toBeChecked()
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(sora).toHaveFocus())
  expect(sora).toHaveAccessibleName('Sora Selected 1 / 2')
  expect(
    screen.queryByRole('button', { name: 'sora-2' })
  ).not.toBeInTheDocument()

  await user.click(sora)
  dialog = within(screen.getByRole('dialog', { name: 'Configure Models' }))
  expect(dialog.getByRole('checkbox', { name: 'sora-2' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'custom-model' }))
  await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
  await user.click(dialog.getByRole('button', { name: 'Apply' }))
  expect(sora).toHaveAccessibleName('Sora Selected 1 / 2')
  expect(
    extensions.getByRole('button', { name: 'Video A Selected 0 / 2' })
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'custom-model' })
  ).not.toBeInTheDocument()
  const selector = screen.getByRole('combobox', {
    name: 'Select models or add custom ones',
  })
  await user.click(selector)
  await user.click(screen.getByRole('option', { name: 'sora-2' }))
  await user.keyboard('{Escape}')
  expect(sora).toHaveAccessibleName('Sora Selected 0 / 2')
  expect(screen.getByRole('button', { name: 'Configure Models' })).toBeEnabled()
})

test('plugin model shortcuts truncate long names and omit plugins without model candidates', () => {
  const name = 'Production Video Generation — International Extended Models'
  render(
    <ChannelPluginExtensions
      plugins={[
        { ...soraPlugin, name },
        { ...plugins[0], models: [] },
      ]}
      selected={[]}
      onConfigure={vi.fn()}
    />
  )
  const row = screen.getByRole('group', { name: 'Plugin extensions' })
  expect(row).toHaveClass('flex-wrap')
  expect(screen.getByText(name)).toHaveClass('truncate', 'max-w-36')
  const trigger = screen.getByRole('button', { name: `${name} Selected 0 / 1` })
  expect(trigger).toHaveAttribute('title', name)
  expect(trigger).toHaveClass('min-w-0', 'max-w-full')
  expect(screen.getAllByRole('button')).toHaveLength(1)
})

test('retrying extension metadata preserves the editable built-in draft and selected models', async () => {
  pluginOptions = [{ ...soraPlugin, channelTypes: [1, 55] }]
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options' && fail) {
      fail = false
      throw new Error('Plugin metadata unavailable')
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(await screen.findByText('Failed to load plugins')).toBeVisible()
  expect(
    screen.queryByRole('group', { name: 'Plugin extensions' })
  ).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Keep this draft' },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  ).toBeVisible()
  expect(screen.getByLabelText('Name *')).toHaveValue('Keep this draft')
  expect(screen.getByLabelText(/Base URL/)).toHaveValue('https://saved.example')
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'sora-2' })
  ).not.toBeInTheDocument()
})

test('loading a replacement plugin preserves an already selected legacy creation draft', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; data: TaskPluginOption[] }
  }>()
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options') return reply.promise
    return originalGet?.(url, config)
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: 'Sora Built-in #55' }))
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Legacy draft' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'draft-key' },
  })
  fireEvent.change(screen.getByLabelText(/Base URL/), {
    target: { value: 'https://draft.example' },
  })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'draft-model,'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await act(async () => {
    reply.resolve({ data: { success: true, data: [soraPlugin] } })
    await reply.promise
  })
  expect(
    await screen.findByRole('option', { name: 'Sora Plugin sora' })
  ).toBeVisible()
  const legacy = screen.getByRole('option', { name: 'Sora Built-in #55' })
  expect(legacy).toHaveAttribute('aria-current', 'true')
  await user.click(legacy)
  expect(screen.getByLabelText('Name *')).toHaveValue('Legacy draft')
  expect(screen.getByLabelText('API Key *')).toHaveValue('draft-key')
  expect(screen.getByLabelText(/Base URL/)).toHaveValue('https://draft.example')
  expect(screen.getByRole('button', { name: 'draft-model' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        channel: expect.objectContaining({ type: 55, name: 'Legacy draft' }),
      }),
      expect.anything()
    )
  )
  const payload = post.mock.calls[0]?.[1] as { channel: { setting: string } }
  expect(JSON.parse(payload.channel.setting)).not.toHaveProperty(
    'task_plugin_key'
  )
})

test('an invalid setting in another category is revealed and focused on submission', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'secret' },
  })
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: 'invalid-proxy' },
  })
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Proxy Address')).toHaveFocus()
  )
  expect(screen.getByRole('tab', { name: /Other Settings/ })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByLabelText('Proxy Address')).toHaveAttribute(
    'aria-invalid',
    'true'
  )
})

test.each([
  ['batch', 'Batch Add (one key per line)'],
  ['multi_to_single', 'Multi-Key Mode (multiple keys, one channel)'],
])('plugin creation preserves the %s request contract', async (mode, label) => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  await user.click(screen.getByRole('combobox', { name: 'Add Mode' }))
  await user.click(screen.getByRole('option', { name: label }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'first-key\nsecond-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        mode,
        channel: expect.objectContaining({
          key: 'first-key\nsecond-key',
          type: 61,
        }),
      }),
      expect.anything()
    )
  )
})

test('a failed creation keeps its draft and prevents duplicate submission while pending', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; message: string }
  }>()
  const post = vi.spyOn(api, 'post').mockReturnValue(reply.promise)
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'keep-secret' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  expect(screen.getByRole('button', { name: 'Create Channel' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  expect(post).toHaveBeenCalledTimes(1)
  await act(async () => {
    reply.resolve({
      data: { success: false, message: 'Upstream configuration rejected' },
    })
    await reply.promise
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Create Channel' })).toBeEnabled()
  )
  expect(screen.getByLabelText('API Key *')).toHaveValue('keep-secret')
  expect(screen.getByLabelText('Name *')).toHaveValue('Video A')
})

test('model discovery discards a response for old credentials and retains manually selected models', async () => {
  const oldReply = deferredResponse<{
    data: { success: boolean; data: string[] }
  }>()
  vi.spyOn(api, 'post')
    .mockReturnValueOnce(oldReply.promise)
    .mockResolvedValueOnce({
      data: { success: true, data: ['current-upstream-model'] },
    })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^OpenAI / }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'first-key' },
  })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'custom-model,'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(await screen.findByText('Fetching models...')).toBeVisible()
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'next-key' },
  })
  await act(async () => {
    oldReply.resolve({ data: { success: true, data: ['old-upstream-model'] } })
    await oldReply.promise
  })
  expect(screen.queryByText('old-upstream-model')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Fetch Models' }))
  await user.click(
    await screen.findByRole('checkbox', { name: 'current-upstream-model' })
  )
  expect(
    screen.getByRole('button', { name: 'current-upstream-model' })
  ).toBeVisible()
  expect(screen.getByText('custom-model')).toBeVisible()
})

test('model discovery reports failures inline and allows an empty result to fall back to manual models', async () => {
  vi.spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: { success: false, message: 'Upstream rejected the key' },
    })
    .mockResolvedValueOnce({ data: { success: true, data: [] } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^OpenAI / }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(await screen.findByText('Upstream rejected the key')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByText('No models returned by the upstream')
  ).toBeVisible()
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'custom-model,'
  )
  await user.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
})

test('quick options and detailed settings share changes across tabs and save the same configuration', async () => {
  editingChannel.setting = JSON.stringify({
    pass_through_body_enabled: true,
    responses_websocket_enabled: true,
  })
  editingChannel.settings = JSON.stringify({
    upstream_model_update_check_enabled: true,
    upstream_model_update_auto_sync_enabled: true,
  })
  editingChannel.auto_ban = 0
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  for (const name of [
    'Pass Through Body',
    'Detect model updates',
    'Responses WebSocket',
  ]) {
    const toggle = quick.getByRole('switch', { name })
    expect(toggle).toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).not.toBeChecked()
  }
  await user.click(quick.getByRole('switch', { name: 'Auto-disable channel' }))

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    screen.getByRole('switch', { name: 'Enable Responses WebSocket' })
  ).not.toBeChecked()
  const passthrough = screen.getByRole('switch', { name: 'Pass Through Body' })
  expect(passthrough).not.toBeChecked()
  await user.click(passthrough)
  expect(passthrough).not.toBeChecked()
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(passthrough).toBeChecked()
  expect(screen.getByRole('switch', { name: 'Force Format' })).toBeVisible()
  expect(
    screen.getByRole('switch', { name: 'Allow service_tier passthrough' })
  ).toBeVisible()

  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  const modelCheck = screen.getByRole('switch', {
    name: 'Upstream Model Update Check',
  })
  expect(modelCheck).not.toBeChecked()
  expect(
    screen.getByRole('switch', { name: 'Auto Sync Upstream Models' })
  ).toHaveAttribute('aria-disabled', 'true')
  await user.click(modelCheck)
  expect(
    screen.getByRole('switch', { name: 'Auto Sync Upstream Models' })
  ).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByRole('switch', { name: 'Auto Ban' })).toBeChecked()
  await user.click(screen.getByRole('switch', { name: 'Auto Ban' }))
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(quick.getByRole('switch', { name: 'Pass Through Body' })).toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Detect model updates' })
  ).toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).not.toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Responses WebSocket' })
  ).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as {
    auto_ban: number
    setting: string
    settings: string
  }
  expect(payload.auto_ban).toBe(0)
  expect(JSON.parse(payload.setting)).toMatchObject({
    pass_through_body_enabled: true,
    responses_websocket_enabled: false,
  })
  expect(JSON.parse(payload.settings)).toMatchObject({
    upstream_model_update_check_enabled: true,
    upstream_model_update_auto_sync_enabled: true,
  })
})

test.each(['stacked', 'inline'])(
  'advanced custom body passthrough saves and reloads from %s quick options and can be disabled in detailed settings',
  async (layout) => {
    if (layout === 'inline') useWideScreen()
    const settings = {
      advanced_custom: {
        advanced_routes: [
          {
            incoming_path: '/v1/chat/completions',
            upstream_path: '/v1/chat/completions',
            converter: 'none',
          },
        ],
      },
    }
    editingChannel = {
      ...editingChannel,
      type: 58,
      settings: JSON.stringify(settings),
      header_override: '{"*":true}',
    }
    const put = vi
      .spyOn(api, 'put')
      .mockImplementation(async (_url, payload) => {
        editingChannel = { ...editingChannel, ...(payload as Partial<Channel>) }
        return { data: { success: true } }
      })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const quick = within(screen.getByRole('group', { name: 'Quick options' }))
    const toggle = quick.getByRole('switch', { name: 'Pass Through Body' })
    expect(toggle).not.toBeChecked()
    expect(
      quick.getByRole('switch', { name: 'Pass Through Request Headers' })
    ).toBeChecked()
    await user.click(toggle)
    const warning = await screen.findByRole('alertdialog', {
      name: 'Enable request body passthrough?',
    })
    expect(warning).toHaveTextContent(
      'Channel-level body passthrough applies to all routes. Requests to routes that require format conversion will fail. To enable passthrough for selected routes only, use the route-level switches instead.'
    )
    await user.click(within(warning).getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(toggle).not.toBeChecked()
    await user.click(toggle)
    await user.click(
      await screen.findByRole('button', { name: 'Enable passthrough' })
    )
    expect(toggle).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    expect(JSON.parse(editingChannel.setting ?? '{}')).toMatchObject({
      pass_through_body_enabled: true,
    })
    expect(JSON.parse(editingChannel.settings ?? '{}').advanced_custom).toEqual(
      settings.advanced_custom
    )
    expect(JSON.parse(editingChannel.header_override ?? '{}')).toEqual({
      '*': true,
    })

    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Update Channel' })
      ).not.toBeInTheDocument()
    )
    await user.click(screen.getByRole('button', { name: 'Open channel' }))
    await screen.findByDisplayValue('Existing channel')
    expect(
      within(screen.getByRole('group', { name: 'Quick options' })).getByRole(
        'switch',
        { name: 'Pass Through Body' }
      )
    ).toBeChecked()
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    const detailed = within(
      screen.getByRole('group', { name: 'Request processing' })
    ).getByRole('switch', { name: 'Pass Through Body' })
    expect(detailed).toBeChecked()
    await user.click(detailed)
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
    expect(JSON.parse(editingChannel.setting ?? '{}')).toMatchObject({
      pass_through_body_enabled: false,
    })
    expect(JSON.parse(editingChannel.settings ?? '{}').advanced_custom).toEqual(
      settings.advanced_custom
    )
  }
)

test('quick options show only applicable shortcuts when the provider changes', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: 'OpenAI Built-in #1' }))
  let quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(5)
  expect(
    quick.queryByRole('switch', { name: 'Force Format' })
  ).not.toBeInTheDocument()
  expect(
    quick.queryByRole('switch', { name: 'Thinking to Content' })
  ).not.toBeInTheDocument()
  await user.click(quick.getByRole('switch', { name: 'Pass Through Body' }))
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAccessibleName(/Incomplete/)

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^DeepSeek / }))
  quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(4)
  expect(
    quick.queryByRole('switch', { name: 'Responses WebSocket' })
  ).not.toBeInTheDocument()
  expect(quick.getByRole('switch', { name: 'Pass Through Body' })).toBeChecked()

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(1)
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).toBeChecked()
})

test('editing opens the shared configuration and omits an unchanged key on update', async () => {
  const channel = channelSchema.parse({
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
  })
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42') {
      return { data: { success: true, data: channel } }
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={channel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.queryByLabelText('Type *')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeVisible()
  expect(screen.getAllByRole('tab')).toHaveLength(4)
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAccessibleName(/Ready/)
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Renamed channel' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({ id: 42, name: 'Renamed channel' }),
      expect.anything()
    )
  )
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
})

test('editing legacy channels retains the full provider list and saves the original type', async () => {
  editingChannel = { ...editingChannel, type: 55 }
  pluginOptions = [
    soraPlugin,
    {
      key: 'doubao',
      name: 'Doubao',
      icon: 'text',
      models: ['doubao-video'],
    },
  ]
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  expect(
    await screen.findByRole('option', { name: 'Sora Plugin sora' })
  ).toBeVisible()
  expect(
    screen.getByRole('option', { name: 'Doubao Built-in #54' })
  ).toBeVisible()
  const legacy = screen.getByRole('option', { name: 'Sora Built-in #55' })
  expect(legacy).toHaveAttribute('aria-current', 'true')
  await user.click(legacy)
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Renamed legacy channel' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({
        id: 42,
        type: 55,
        name: 'Renamed legacy channel',
      }),
      expect.anything()
    )
  )
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting)).not.toHaveProperty('task_plugin_key')
})

test('opening and reselecting an existing plugin preserves its saved configuration', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting: '{"task_plugin_key":"video-a"}',
    priority: 7,
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.queryByLabelText('Task plugin *')).not.toBeInTheDocument()
  const providerControl = screen.getByRole('button', {
    name: 'Change provider',
  })
  await user.click(await within(providerControl).findByText('Video A'))
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(
    screen.getByRole('button', { name: 'Back to configuration' })
  )
  expect(screen.getByText('custom-model')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^Video B Plugin/ }))
  expect(screen.getByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  expect(screen.getByText('video-b-1')).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByLabelText('Priority')).toHaveValue(7)
})

test('an unavailable plugin keeps its identifier and binding when other fields are updated', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting: '{"task_plugin_key":"removed-plugin"}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByText('removed-plugin')).toBeVisible()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Updated name' },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting).task_plugin_key).toBe('removed-plugin')
})

test('a failed detail request blocks updating until retry loads the saved channel', async () => {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42' && fail) {
      fail = false
      throw new Error('Channel details unavailable')
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await waitFor(() =>
    expect(screen.getByText('Failed to load channel')).toBeVisible()
  )
  expect(screen.getByRole('button', { name: 'Update Channel' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeDisabled()
  expect(screen.queryByLabelText('Name *')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Update Channel' })).toBeEnabled()
})

test('restoring routing defaults clears the configured indicator for both the block and category', async () => {
  editingChannel = { ...editingChannel, auto_ban: 0 }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName(/Configured/)
  const status = within(tab).getByRole('img', { name: 'Configured' })
  expect(status).toBeVisible()
  expect(tab).not.toHaveTextContent('Configured')
  await user.hover(status)
  expect(await screen.findByText('Configured')).toBeVisible()
  await user.unhover(status)
  await user.click(tab)
  const block = screen.getByRole('group', { name: 'Routing Strategy' })
  expect(within(block).getByRole('img', { name: 'Configured' })).toBeVisible()
  expect(block).toHaveClass('border-primary/35')
  await user.click(screen.getByRole('switch', { name: 'Auto Ban' }))
  expect(tab).not.toHaveAccessibleName(/Configured/)
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(block).not.toHaveClass('border-primary/35')
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '5' },
  })
  expect(tab).toHaveAccessibleName(/Configured/)
  expect(within(block).getByRole('img', { name: 'Configured' })).toBeVisible()
})

test('request processing configuration does not mark the network category as configured', async () => {
  editingChannel = {
    ...editingChannel,
    setting: '{"thinking_to_content":true}',
    param_override: '{}',
    header_override: '{}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    within(screen.getByRole('group', { name: 'Override Rules' })).queryByRole(
      'img',
      { name: 'Configured' }
    )
  ).not.toBeInTheDocument()
  const processing = screen.getByRole('group', { name: 'Request processing' })
  expect(
    within(processing).getByRole('img', { name: 'Configured' })
  ).toBeVisible()
  await user.click(screen.getByRole('switch', { name: 'Thinking to Content' }))
  expect(
    within(processing).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('tab', { name: /Request & Response/ })
  ).not.toHaveAccessibleName(/Configured/)
})

test.each([1, 57, 58, 59, 60])(
  'provider %s marks a saved Responses WebSocket setting in Request & Response and clears the mark when disabled',
  async (type) => {
    editingChannel = {
      ...editingChannel,
      type,
      setting: '{"responses_websocket_enabled":true}',
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    expect(requestTab).toHaveAccessibleName(/Configured/)
    expect(
      screen.getByRole('tab', { name: /Other Settings/ })
    ).not.toHaveAccessibleName(/Configured/)
    await user.click(requestTab)
    const toggle = screen.getByRole('switch', {
      name: 'Enable Responses WebSocket',
    })
    expect(toggle).toBeChecked()
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
    expect(requestTab).not.toHaveAccessibleName(/Configured/)
  }
)

test.each([
  [58, true],
  [1, false],
])(
  'provider %s explains the native-route limit under the Responses WebSocket toggle: %s',
  async (type, shown) => {
    editingChannel = { ...editingChannel, type }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    expect(
      screen.getByRole('switch', { name: 'Enable Responses WebSocket' })
    ).toBeVisible()
    const note = screen.queryByText(
      /applies only to \/v1\/responses routes without protocol conversion/
    )
    if (shown) {
      expect(note).toBeVisible()
    } else {
      expect(note).not.toBeInTheDocument()
    }
  }
)

test('configuration from fields unsupported by the selected provider stays unmarked', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting:
      '{"task_plugin_key":"video-a","force_format":true,"responses_websocket_enabled":true}',
    settings:
      '{"allow_speed":true,"allow_service_tier":true,"upstream_model_update_check_enabled":true,"ollama_openai_chat":true,"novelai_presets_disabled":true,"novelai_hd_disabled":true,"novelai_upscale_enabled":true,"novelai_queue_limit":3}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).not.toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const processing = screen.getByRole('group', { name: 'Request processing' })
  expect(
    within(processing).queryByRole('switch', {
      name: 'Use OpenAI-compatible Ollama chat API',
    })
  ).not.toBeInTheDocument()
  expect(
    within(processing).queryByRole('switch', {
      name: 'Do not add NovelAI quality tags and negative presets',
    })
  ).not.toBeInTheDocument()
  expect(
    within(processing).queryByRole('switch', {
      name: 'NovelAI HD (1.5× Enhance)',
    })
  ).not.toBeInTheDocument()
  expect(
    within(processing).queryByRole('switch', { name: 'Upscale images 2x' })
  ).not.toBeInTheDocument()
  expect(
    within(processing).queryByRole('spinbutton', {
      name: 'Queue limit per key',
    })
  ).not.toBeInTheDocument()
})

test('an OpenAI channel saved with the NovelAI settings saves its other settings without them', async () => {
  editingChannel = {
    ...editingChannel,
    type: 1,
    settings:
      '{"novelai_presets_disabled":true,"novelai_hd_disabled":true,"novelai_upscale_enabled":true,"novelai_queue_limit":3,"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  const settings = JSON.parse(payload.settings)
  expect(settings).not.toHaveProperty('novelai_presets_disabled')
  expect(settings).not.toHaveProperty('novelai_hd_disabled')
  expect(settings).not.toHaveProperty('novelai_upscale_enabled')
  expect(settings).not.toHaveProperty('novelai_queue_limit')
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('a NovelAI channel marks saved disabled prompt presets in Request & Response and saves the toggled value with the other settings intact', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"novelai_presets_disabled":true,"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const toggle = screen.getByRole('switch', {
    name: 'Do not add NovelAI quality tags and negative presets',
  })
  expect(toggle).toBeChecked()
  // V5 Full Medium keeps the negative preset NovelAI requires of it.
  expect(toggle).toHaveAccessibleDescription(/V5 Full Medium/)
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    novelai_presets_disabled: false,
    future_option: { level: 2 },
  })
})

test('a NovelAI channel without an HD setting shows HD on, unmarked, and turning it off marks Request & Response and saves it with the other settings intact', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const hd = screen.getByRole('switch', { name: 'NovelAI HD (1.5× Enhance)' })
  expect(hd).toBeChecked()
  // A request opts out on its own.
  expect(hd).toHaveAccessibleDescription(/"hd": false/)
  // What it spends at the relay's default 28 steps.
  expect(hd).toHaveAccessibleDescription(
    /about 23 on V4\/V4\.5 and 35 on V5 at 28 steps/
  )
  await user.click(hd)
  expect(hd).not.toBeChecked()
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    novelai_hd_disabled: true,
    novelai_presets_disabled: false,
    future_option: { level: 2 },
  })
})

test('a NovelAI channel saved with HD off shows it off and marked, and turning it on saves HD on', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"novelai_hd_disabled":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const hd = screen.getByRole('switch', { name: 'NovelAI HD (1.5× Enhance)' })
  expect(hd).not.toBeChecked()
  await user.click(hd)
  expect(hd).toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    novelai_hd_disabled: false,
  })
})

test('a NovelAI channel without an upscale setting shows the upscale off, unmarked, and turning it on marks Request & Response and saves it with the other settings intact', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const upscale = screen.getByRole('switch', { name: 'Upscale images 2x' })
  expect(upscale).not.toBeChecked()
  // The owner pays by the size sent, and a request opts out on its own.
  expect(upscale).toHaveAccessibleDescription(/1 Anlas up to 1 MP/)
  expect(upscale).toHaveAccessibleDescription(/not yet confirmed/)
  expect(upscale).toHaveAccessibleDescription(/"upscale": false/)
  await user.click(upscale)
  expect(upscale).toBeChecked()
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    novelai_upscale_enabled: true,
    novelai_hd_disabled: false,
    future_option: { level: 2 },
  })
})

test('a NovelAI channel saved with the upscale on shows it on and marked, and turning it off saves it off', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"novelai_upscale_enabled":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const upscale = screen.getByRole('switch', { name: 'Upscale images 2x' })
  expect(upscale).toBeChecked()
  await user.click(upscale)
  expect(upscale).not.toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    novelai_upscale_enabled: false,
  })
})

test('a new NovelAI channel starts with the upscale off', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^NovelAI / }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    screen.getByRole('switch', { name: 'Upscale images 2x' })
  ).not.toBeChecked()
  expect(
    screen.getByRole('switch', { name: 'NovelAI HD (1.5× Enhance)' })
  ).toBeChecked()
})

test('the NovelAI switches and queue limit are locked without sensitive channel write permission', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{}',
  }
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { read: true, write: true } },
        },
      },
    },
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const upscale = screen.getByRole('switch', { name: 'Upscale images 2x' })
  expect(upscale).toHaveAttribute('aria-disabled', 'true')
  await user.click(upscale)
  expect(upscale).not.toBeChecked()
  const hd = screen.getByRole('switch', { name: 'NovelAI HD (1.5× Enhance)' })
  expect(hd).toHaveAttribute('aria-disabled', 'true')
  await user.click(hd)
  expect(hd).toBeChecked()
  const presets = screen.getByRole('switch', {
    name: 'Do not add NovelAI quality tags and negative presets',
  })
  expect(presets).toHaveAttribute('aria-disabled', 'true')
  await user.click(presets)
  expect(presets).not.toBeChecked()
  const limit = screen.getByRole('spinbutton', { name: 'Queue limit per key' })
  expect(limit).toBeDisabled()
  await user.type(limit, '3')
  expect(limit).toHaveValue(10)
})

test.each([
  ['{"novelai_queue_limit":3}', 3, true],
  ['{}', 10, false],
  ['{"novelai_queue_limit":5000}', 100, true],
])(
  'a NovelAI channel saved with settings %s shows a queue limit per key of %i, marked as configured: %s',
  async (settings, shown, configured) => {
    editingChannel = { ...editingChannel, type: CHANNEL_TYPE_NOVELAI, settings }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    if (configured) {
      expect(requestTab).toHaveAccessibleName(/Configured/)
    } else {
      expect(requestTab).not.toHaveAccessibleName(/Configured/)
    }
    await user.click(requestTab)
    expect(
      screen.getByRole('spinbutton', { name: 'Queue limit per key' })
    ).toHaveValue(shown)
  }
)

test('a NovelAI channel queue limit set back to the default of 10 is no longer marked as configured', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_NOVELAI,
    settings: '{"novelai_queue_limit":3}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const limit = screen.getByRole('spinbutton', { name: 'Queue limit per key' })
  await user.clear(limit)
  await user.type(limit, '10')
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
})

test.each([
  ['0', 0],
  ['100', 100],
])(
  'a NovelAI channel saves a queue limit per key of %s with the other settings intact',
  async (typed, saved) => {
    editingChannel = {
      ...editingChannel,
      type: CHANNEL_TYPE_NOVELAI,
      settings: '{"future_option":{"level":2}}',
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    const limit = screen.getByRole('spinbutton', {
      name: 'Queue limit per key',
    })
    await user.clear(limit)
    await user.type(limit, typed)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const payload = put.mock.calls[0]?.[1] as { settings: string }
    expect(JSON.parse(payload.settings)).toMatchObject({
      novelai_queue_limit: saved,
      future_option: { level: 2 },
    })
  }
)

test.each(['-1', '101', '2.5'])(
  'a NovelAI channel with a queue limit per key of %s is not saved and shows the allowed range',
  async (typed) => {
    editingChannel = { ...editingChannel, type: CHANNEL_TYPE_NOVELAI }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    const limit = screen.getByRole('spinbutton', {
      name: 'Queue limit per key',
    })
    await user.clear(limit)
    await user.type(limit, typed)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(
      await screen.findByText(
        'Queue limit per key must be a whole number from 0 to 100'
      )
    ).toBeVisible()
    expect(limit).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

test('an Ollama channel marks a saved OpenAI-compatible chat setting in Request & Response and saves the toggled value', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_OLLAMA,
    settings: '{"ollama_openai_chat":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const toggle = screen.getByRole('switch', {
    name: 'Use OpenAI-compatible Ollama chat API',
  })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    ollama_openai_chat: false,
  })
})

test('a saved upstream TLS verification skip marks Other Settings and turning it off saves the setting without it', async () => {
  editingChannel = {
    ...editingChannel,
    setting: '{"tls_insecure_skip_verify":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const otherTab = screen.getByRole('tab', { name: /Other Settings/ })
  expect(otherTab).toHaveAccessibleName(/Configured/)
  await user.click(otherTab)
  const toggle = screen.getByRole('switch', {
    name: 'Skip Upstream TLS Verification',
  })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(otherTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting)).not.toHaveProperty(
    'tls_insecure_skip_verify'
  )
})

test('turning on the upstream TLS verification skip saves it in the channel setting', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  const toggle = screen.getByRole('switch', {
    name: 'Skip Upstream TLS Verification',
  })
  expect(toggle).not.toBeChecked()
  await user.click(toggle)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting)).toMatchObject({
    tls_insecure_skip_verify: true,
  })
})

test('an invalid edit switches categories and replaces configured styling with the field error', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: 'invalid-proxy' },
  })
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Proxy Address')).toHaveFocus()
  )
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).toHaveAccessibleName(/Error/)
  const block = screen.getByRole('group', { name: 'Channel Extra Settings' })
  expect(within(block).getByRole('img', { name: 'Error' })).toBeVisible()
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(block).toHaveClass('border-destructive/50')
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: '' },
  })
  await waitFor(() =>
    expect(
      within(block).queryByRole('img', { name: 'Error' })
    ).not.toBeInTheDocument()
  )
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
})

test('ordinary edits discover models with saved settings and keep removed draft models available for reselection', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  const put = vi.spyOn(api, 'put')
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByDisplayValue('https://saved.example'), {
    target: { value: 'https://draft.example' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'new-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('checkbox', { name: 'upstream-model' })
  )
  expect(api.get).toHaveBeenCalledWith(
    '/api/channel/fetch_models/42',
    expect.anything()
  )
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'upstream-model' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'New Models (1)' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'Removed Models (1)' })).toBeVisible()
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'manual-draft,'
  )
  await user.keyboard('{Escape}')
  const removedTab = screen.getByRole('tab', { name: 'Removed Models (2)' })
  await user.click(removedTab)
  for (const model of ['custom-model', 'manual-draft']) {
    await user.click(screen.getByRole('checkbox', { name: model }))
    expect(
      screen.queryByRole('button', { name: model })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: model })).not.toBeChecked()
    expect(removedTab).toHaveAttribute('aria-selected', 'true')
  }
  await user.click(screen.getByRole('checkbox', { name: 'manual-draft' }))
  expect(screen.getByRole('button', { name: 'manual-draft' })).toBeVisible()
  expect(screen.getByRole('checkbox', { name: 'manual-draft' })).toBeChecked()
  expect(screen.getAllByRole('dialog')).toHaveLength(1)
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()
})

test('model configuration replaces the selected-count badge at the right of the model field header', async () => {
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')

  const models = screen.getByRole('group', { name: 'Models' })
  const input = within(models).getByRole('combobox', {
    name: 'Select models or add custom ones',
  })
  const configure = within(models).getByRole('button', {
    name: 'Configure Models',
  })
  expect(configure.parentElement).toHaveClass(
    'flex',
    'items-start',
    'justify-between'
  )
  expect(configure).toHaveClass('shrink-0')
  expect(
    configure.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(screen.queryByText('Selected 1')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('button', { name: 'Configure Models' })
  ).toHaveLength(1)
})

test('model configuration uses only the current form models and persists changes only when the channel is saved', async () => {
  editingChannel.models = 'gpt-one,manual-model,alias'
  const post = vi.spyOn(api, 'post')
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'gpt-two,'
  )
  await user.keyboard('{Escape}')
  const models = screen.getByRole('group', { name: 'Models' })
  const manualModelChip = within(models).getByRole('button', {
    name: 'manual-model',
  })
  vi.mocked(api.get).mockClear()

  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  for (const model of ['gpt-one', 'manual-model', 'alias', 'gpt-two']) {
    expect(dialog.getByRole('checkbox', { name: model })).toBeChecked()
  }
  expect(
    dialog.queryByRole('checkbox', { name: 'upstream-model' })
  ).not.toBeInTheDocument()
  expect(
    dialog.queryByRole('checkbox', { name: 'custom-model' })
  ).not.toBeInTheDocument()
  expect(dialog.queryByRole('tab')).not.toBeInTheDocument()
  expect(dialog.getByText('Current models: 4')).toBeVisible()
  await user.click(dialog.getByRole('checkbox', { name: 'manual-model' }))
  expect(manualModelChip).toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()

  await user.click(dialog.getByRole('button', { name: 'Apply' }))

  expect(
    within(models).queryByRole('button', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  await waitFor(() => expect(trigger).toHaveFocus())
  expect(api.get).not.toHaveBeenCalled()
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()
  await user.click(trigger)
  const reopened = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  expect(
    reopened.queryByRole('checkbox', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  expect(reopened.getByRole('checkbox', { name: 'gpt-two' })).toBeChecked()
  await user.click(reopened.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({
    id: 42,
    models: 'gpt-one,alias,gpt-two',
  })
})

test.each(['Cancel', 'Close', 'Escape'])(
  'model configuration discards changes on %s and restores focus to its trigger',
  async (action) => {
    editingChannel.models = 'gpt-one,gpt-two'
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const trigger = screen.getByRole('button', { name: 'Configure Models' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const dialog = within(
      screen.getByRole('dialog', { name: 'Configure Models' })
    )
    const model = dialog.getByRole('checkbox', { name: 'gpt-one' })
    model.focus()
    await user.keyboard(' ')
    expect(model).not.toBeChecked()

    if (action === 'Escape') {
      await user.keyboard('{Escape}')
    } else {
      await user.click(dialog.getByRole('button', { name: action }))
    }

    await waitFor(() => expect(trigger).toHaveFocus())
    expect(screen.getByRole('button', { name: 'gpt-one' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'gpt-two' })).toBeVisible()
    await user.click(trigger)
    expect(
      within(
        screen.getByRole('dialog', { name: 'Configure Models' })
      ).getByRole('checkbox', { name: 'gpt-one' })
    ).toBeChecked()
  }
)

test('model configuration keeps unchecked candidates searchable and supports category selection and applying an empty list', async () => {
  editingChannel.models = 'gpt-one,gpt-two,manual-model'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  const category = dialog.getByRole('checkbox', {
    name: 'Select all models in OpenAI',
  })
  await user.click(category)
  expect(dialog.getByRole('checkbox', { name: 'gpt-one' })).not.toBeChecked()
  expect(dialog.getByRole('checkbox', { name: 'gpt-two' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-one' }))
  expect(category).toHaveAttribute('aria-checked', 'mixed')
  const search = dialog.getByRole('textbox', { name: 'Search models...' })
  await user.type(search, 'gpt-')
  await user.click(
    dialog.getByRole('button', { name: 'Select all matching models' })
  )
  expect(dialog.getByRole('checkbox', { name: 'gpt-two' })).toBeChecked()
  expect(
    dialog.queryByRole('checkbox', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  await user.clear(search)
  await user.type(search, 'missing')
  expect(dialog.getByText('No matching items')).toBeVisible()
  expect(
    dialog.getByRole('button', { name: 'Select all matching models' })
  ).toBeDisabled()
  await user.clear(search)
  expect(dialog.getByRole('checkbox', { name: 'manual-model' })).toBeChecked()
  await user.click(
    dialog.getByRole('checkbox', { name: 'Select all models in OpenAI' })
  )
  await user.click(
    dialog.getByRole('checkbox', { name: 'Select all models in Other' })
  )
  await user.click(dialog.getByRole('button', { name: 'Apply' }))

  expect(
    screen.queryByRole('button', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  expect(trigger).toBeDisabled()
})

test('model configuration is available for a plugin channel without upstream discovery and is disabled when its model list is empty', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  expect(trigger).toBeEnabled()
  expect(
    screen.queryByRole('button', { name: 'Fetch from Upstream' })
  ).not.toBeInTheDocument()
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  expect(dialog.getByRole('checkbox', { name: 'video-a-1' })).toBeChecked()
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Clear All' }))

  expect(trigger).toBeDisabled()
})

test('advanced custom edits preview draft connection settings with the saved key', async () => {
  editingChannel = {
    ...editingChannel,
    type: 58,
    settings: JSON.stringify({
      advanced_custom: {
        advanced_routes: [
          {
            incoming_path: '/v1/models',
            upstream_path: '/v1/models',
            converter: 'none',
          },
        ],
      },
    }),
  }
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: ['preview-model'] } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByDisplayValue('https://saved.example'), {
    target: { value: 'https://draft.example' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'unsaved-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'preview-model' })
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/fetch_models',
    expect.objectContaining({
      type: 58,
      channel_id: 42,
      base_url: 'https://draft.example',
      key: undefined,
    }),
    expect.anything()
  )
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/fetch_models/42',
    expect.anything()
  )
})

test('an operator without sensitive write permission can discover saved models and update routing', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: {
            channel: { read: true, write: true, operate: true },
          },
        },
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeDisabled()
  expect(screen.getByLabelText('API Key *')).toBeDisabled()
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  for (const name of [
    'Pass Through Body',
    'Detect model updates',
    'Responses WebSocket',
  ]) {
    const toggle = quick.getByRole('switch', { name })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
  }
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).not.toHaveAttribute('aria-disabled', 'true')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'upstream-model' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const thinking = screen.getByRole('switch', { name: 'Thinking to Content' })
  expect(thinking).toHaveAttribute('aria-disabled', 'true')
  await user.click(thinking)
  expect(thinking).not.toBeChecked()
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  expect(screen.getByLabelText('Proxy Address')).toBeDisabled()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '8' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, priority: 8 })
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('setting')
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
})

test.each([
  ['random', 'Random', 'polling', 'Polling'],
  ['polling', 'Polling', 'random', 'Random'],
] as const)(
  'multi-key editing switches from %s without replacing keys',
  async (initialMode, initialLabel, nextMode, nextLabel) => {
    editingChannel.channel_info = {
      ...editingChannel.channel_info,
      is_multi_key: true,
      multi_key_size: 2,
      multi_key_mode: initialMode,
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const strategy = screen.getByRole('combobox', {
      name: 'Multi-Key Strategy',
    })
    expect(strategy).toHaveTextContent(initialLabel)
    await user.click(strategy)
    await user.click(screen.getByRole('option', { name: nextLabel }))
    expect(strategy).toHaveTextContent(nextLabel)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({
      id: 42,
      multi_key_mode: nextMode,
    })
    expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
    expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key_mode')
  }
)

test('single-key editing omits the multi-key strategy control and update field', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.queryByRole('combobox', { name: 'Multi-Key Strategy' })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('multi_key_mode')
})

test.each(['append', 'replace'])(
  'multi-key editing submits the selected %s mode with new keys',
  async (mode) => {
    editingChannel = {
      ...editingChannel,
      channel_info: {
        ...editingChannel.channel_info,
        is_multi_key: true,
        multi_key_size: 2,
      },
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    if (mode === 'replace') {
      await user.click(
        screen.getByRole('combobox', { name: 'Key Update Mode' })
      )
      await user.click(
        screen.getByRole('option', { name: 'Replace all existing keys' })
      )
    }
    fireEvent.change(screen.getByLabelText('API Key *'), {
      target: { value: 'next-key' },
    })
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({
      key: 'next-key',
      key_mode: mode,
    })
  }
)

test('a failed update retains the draft through a background detail refetch', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Update failed' },
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Unsaved name' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Update Channel' })).toBeEnabled()
  )
  await act(async () => {
    await client.refetchQueries({ queryKey: ['channels'] })
  })
  expect(screen.getByDisplayValue('Unsaved name')).toBeVisible()
})

test('switching edited channels discards a pending model list from the previous channel', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; data: string[] }
  }>()
  const otherChannel = { ...editingChannel, id: 43, name: 'Second channel' }
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/fetch_models/42') return reply.promise
    if (url === '/api/channel/43') {
      return { data: { success: true, data: otherChannel } }
    }
    if (url === '/api/channel/fetch_models/43') {
      return { data: { success: true, data: ['second-model'] } }
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  const view = render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  view.rerender(<ConfigurationHarness currentRow={otherChannel} />)
  await screen.findByDisplayValue('Second channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'second-model' })
  ).toBeVisible()
  await act(async () => {
    reply.resolve({ data: { success: true, data: ['first-model'] } })
  })
  expect(
    screen.queryByRole('checkbox', { name: 'first-model' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: 'second-model' })).toBeVisible()
})

test('an unknown saved type remains editable without selecting a new provider', async () => {
  editingChannel = { ...editingChannel, type: 999 }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByText('#999')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, type: 999 })
})

test('a background refresh updates untouched values without moving the selected category', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  editingChannel = { ...editingChannel, priority: 5 }
  await act(async () => {
    await client.refetchQueries({ queryKey: ['channels'] })
  })
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  await waitFor(() => expect(screen.getByLabelText('Priority')).toHaveValue(5))
})

test('closing an edited channel retains its left exit direction after the parent clears the row', async () => {
  const animation = deferredResponse<void>()
  const originalGetAnimations = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    'getAnimations'
  )
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
    configurable: true,
    value(this: HTMLElement) {
      return this.hasAttribute('data-ending-style')
        ? [{ finished: animation.promise }]
        : []
    },
  })
  try {
    const user = userEvent.setup()
    const view = render(
      <ConfigurationHarness currentRow={editingChannel} clearRowOnClose />
    )
    await screen.findByDisplayValue('Existing channel')
    const drawer = screen.getByRole('dialog')
    expect(drawer).toHaveAttribute('data-side', 'left')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(drawer).toHaveAttribute('data-ending-style'))
    expect(drawer).toBeInTheDocument()
    expect(drawer).toHaveAttribute('data-side', 'left')
    expect(drawer).toHaveClass('left-0')
    expect(drawer).not.toHaveClass('right-0')
    await act(async () => {
      animation.resolve()
    })
    await waitFor(() => expect(drawer).not.toBeInTheDocument())

    view.rerender(<ConfigurationHarness clearRowOnClose />)
    await user.click(screen.getByRole('button', { name: 'Open channel' }))
    expect(screen.getByRole('dialog')).toHaveAttribute('data-side', 'right')
  } finally {
    animation.resolve()
    if (originalGetAnimations) {
      Object.defineProperty(
        HTMLElement.prototype,
        'getAnimations',
        originalGetAnimations
      )
    } else {
      Reflect.deleteProperty(HTMLElement.prototype, 'getAnimations')
    }
  }
})

test('the model list summarizes redirects, counts them on the routing tab, and jumps to the mapping editor', async () => {
  editingChannel = {
    ...editingChannel,
    model_mapping: '{"custom-model":"upstream-model"}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(screen.getByText('1 model(s) redirected')).toBeVisible()
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName(/^Routing & Mapping\s?1 model mapping\(s\)$/)
  expect(
    within(tab).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Edit mapping' }))
  expect(tab).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('custom-model')
})

test('without mappings the model list offers to set up redirects and the routing tab shows no count', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName('Routing & Mapping')
  expect(screen.queryByText(/model\(s\) redirected/)).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('button', { name: 'Set up model redirects' })
  )
  expect(tab).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('button', { name: 'Add Mapping' })).toBeVisible()
})

test('mapping guardrail alerts link to the tab that holds the fix', async () => {
  editingChannel = {
    ...editingChannel,
    model_mapping: '{"alias":"custom-model"}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const routingTab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  const connectionTab = screen.getByRole('tab', {
    name: /Connection & Models/,
  })
  await user.click(screen.getByRole('button', { name: 'View mapping' }))
  expect(routingTab).toHaveAttribute('aria-selected', 'true')
  await user.click(screen.getByRole('button', { name: 'View models' }))
  expect(connectionTab).toHaveAttribute('aria-selected', 'true')
})

test('redirecting a model from the model picker publishes it, adds a mapping row, and focuses its upstream field', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Configure Models' }))
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  await user.click(
    dialog.getByRole('button', { name: 'Redirect custom-model' })
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Configure Models' })
    ).not.toBeInTheDocument()
  )
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('custom-model')
  await waitFor(() =>
    expect(
      screen.getByRole('combobox', { name: 'Upstream Model Name' })
    ).toHaveFocus()
  )
})

test('fetched upstream models can be redirected in bulk with the model list synced', async () => {
  editingChannel.models = 'custom-model,upstream-model'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('tab', { name: 'Existing Models (1)' })
  )
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(screen.getByRole('button', { name: 'Batch redirect' }))
  const dialog = within(
    screen.getByRole('dialog', { name: 'Batch add mappings' })
  )
  expect(
    dialog.getByRole('tab', { name: /Upstream model list/ })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    dialog.getByRole('checkbox', { name: 'upstream-model' })
  ).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'upstream-model' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-model')
  await user.click(dialog.getByRole('button', { name: 'Add 1 mapping(s)' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Batch add mappings' })
    ).not.toBeInTheDocument()
  )

  expect(screen.getByText('1 model(s) redirected')).toBeVisible()
  expect(screen.getByRole('button', { name: 'upstream' })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'upstream-model' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('upstream')
  expect(
    screen.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')
})

test('a fetched upstream model can be given a request name from its own row', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(
    screen.getByRole('button', { name: 'Redirect upstream-model' })
  )
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  await waitFor(() =>
    expect(
      screen.getByRole('combobox', { name: 'Request Model Name' })
    ).toHaveFocus()
  )
  expect(
    screen.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')
})

test('on wide screens redirecting a fetched model opens the floating panel beside the drawer and syncs the model list as the alias is typed', async () => {
  useWideScreen()
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(
    screen.getByRole('button', { name: 'Redirect upstream-model' })
  )
  const panelElement = await screen.findByRole('complementary', {
    name: 'Model redirects',
  })
  const panel = within(panelElement)
  expect(panelElement).toHaveStyle({
    position: 'fixed',
    left: '916px',
    top: '16px',
    width: '420px',
  })
  // The window floats; the sheet keeps its full width.
  expect(document.querySelector('[data-slot="sheet-content"]')).toHaveClass(
    'sm:max-w-7xl'
  )
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAttribute('aria-selected', 'true')
  const request = panel.getByRole('combobox', { name: 'Request Model Name' })
  await waitFor(() => expect(request).toHaveFocus())
  expect(
    panel.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')

  await user.type(request, 'upstream')
  const picker = within(screen.getByRole('group', { name: 'Models' }))
  expect(
    picker.queryByRole('button', { name: 'upstream' })
  ).not.toBeInTheDocument()
  await user.keyboard('{Enter}')
  expect(screen.getByLabelText('Published as upstream')).toBeVisible()
  expect(picker.getByRole('button', { name: 'upstream' })).toBeVisible()
  expect(picker.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(picker.queryByRole('button', { name: 'ups' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(
    screen.getByText('Model mappings are being edited in the floating panel.')
  ).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Close the panel to edit here' })
  )
  expect(
    screen.queryByRole('complementary', { name: 'Model redirects' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('upstream')
})

test.each(['current session', 'saved preference'])(
  'quick redirect expands a panel collapsed in the %s and focuses the new request name',
  async (collapsedIn) => {
    useWideScreen()
    editingChannel.model_mapping = '{"existing-alias":"existing-upstream"}'
    if (collapsedIn === 'saved preference') {
      window.localStorage.setItem(
        'floating-window:channel-model-redirects',
        JSON.stringify({
          x: 1000,
          y: 80,
          width: 380,
          height: 360,
          collapsed: true,
        })
      )
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(
      screen.getByRole('button', { name: 'Fetch from Upstream' })
    )
    await screen.findByRole('checkbox', { name: 'upstream-model' })
    if (collapsedIn === 'current session') {
      await user.click(screen.getByRole('button', { name: 'Redirect panel' }))
      await user.click(screen.getByRole('button', { name: 'Collapse panel' }))
      expect(screen.getByRole('button', { name: 'Expand panel' })).toBeVisible()
    }

    await user.click(
      screen.getByRole('button', { name: 'Redirect upstream-model' })
    )
    let panelElement = await screen.findByRole('complementary', {
      name: 'Model redirects',
    })
    let panel = within(panelElement)
    await waitFor(() =>
      expect(panel.getByDisplayValue('upstream-model')).toBeVisible()
    )
    const request = panel.getAllByRole('combobox', {
      name: 'Request Model Name',
    })[1]
    await waitFor(() => expect(request).toHaveFocus())
    expect(request).toHaveValue('')
    expect(panel.getByDisplayValue('existing-alias')).toBeVisible()
    expect(panel.getByDisplayValue('existing-upstream')).toBeVisible()
    expect(screen.getByText('1 model(s) redirected')).toBeVisible()
    if (collapsedIn === 'saved preference') {
      expect(panelElement).toHaveStyle({
        left: '1000px',
        top: '80px',
        width: '380px',
        height: '360px',
      })
    }

    // Handling the draft must not prevent collapse or swallow the next shortcut for the same model.
    await user.click(panel.getByRole('button', { name: 'Collapse panel' }))
    expect(
      screen.queryByRole('complementary', { name: 'Model redirects' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Redirect upstream-model' })
    )
    panelElement = await screen.findByRole('complementary', {
      name: 'Model redirects',
    })
    panel = within(panelElement)
    await waitFor(() =>
      expect(panel.getByDisplayValue('upstream-model')).toBeVisible()
    )
    await waitFor(() =>
      expect(
        panel.getAllByRole('combobox', { name: 'Request Model Name' })[1]
      ).toHaveFocus()
    )
  }
)

test('redirecting a recognized upstream name opens an empty alias draft without changing the model list', async () => {
  useWideScreen()
  const baseGet = vi.mocked(api.get).getMockImplementation()
  vi.spyOn(api, 'get').mockImplementation(async (url, ...rest) => {
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['gpt-4o-all'] } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'gpt-4o' }] } }
    }
    return baseGet?.(url, ...rest)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('button', { name: 'Redirect gpt-4o-all' })
  )
  const panel = within(
    await screen.findByRole('complementary', { name: 'Model redirects' })
  )
  const request = panel.getByRole('combobox', { name: 'Request Model Name' })
  await waitFor(() => expect(request).toHaveFocus())
  expect(request).toHaveValue('')
  expect(
    panel.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('gpt-4o-all')
  expect(screen.queryByLabelText('Published as gpt-4o')).not.toBeInTheDocument()
  expect(
    within(screen.getByRole('group', { name: 'Models' })).getByRole('button', {
      name: 'custom-model',
    })
  ).toBeVisible()
  expect(screen.queryByText('1 model(s) redirected')).not.toBeInTheDocument()
})

test('the redirect panel batch action starts empty and only applies explicitly selected models after preview', async () => {
  useWideScreen()
  editingChannel.models = 'custom-model,gpt-4o-all,o3-all'
  const baseGet = vi.mocked(api.get).getMockImplementation()
  vi.spyOn(api, 'get').mockImplementation(async (url, ...rest) => {
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['gpt-4o-all', 'o3-all'] } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'gpt-4o' }] } }
    }
    return baseGet?.(url, ...rest)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('tab', { name: 'Existing Models (2)' })
  )
  await screen.findByRole('checkbox', { name: 'gpt-4o-all' })

  await user.click(screen.getByRole('button', { name: 'Redirect panel' }))
  const panel = within(
    await screen.findByRole('complementary', { name: 'Model redirects' })
  )
  expect(panel.queryByText('Suggested rules')).not.toBeInTheDocument()
  await user.click(panel.getByRole('button', { name: 'Batch Add' }))
  let dialog = within(
    screen.getByRole('dialog', { name: 'Batch add mappings' })
  )
  expect(dialog.getByRole('checkbox', { name: 'gpt-4o-all' })).not.toBeChecked()
  expect(dialog.getByRole('checkbox', { name: 'o3-all' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-all')
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  const picker = within(screen.getByRole('group', { name: 'Models' }))
  expect(picker.getByRole('button', { name: 'gpt-4o-all' })).toBeVisible()
  expect(
    picker.queryByRole('button', { name: 'gpt-4o' })
  ).not.toBeInTheDocument()

  await user.click(panel.getByRole('button', { name: 'Batch Add' }))
  dialog = within(screen.getByRole('dialog', { name: 'Batch add mappings' }))
  expect(dialog.getByRole('checkbox', { name: 'gpt-4o-all' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-all')
  expect(dialog.getByRole('list', { name: 'Preview' })).toHaveTextContent(
    'gpt-4ogpt-4o-all'
  )
  await user.click(dialog.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(
    panel.getAllByRole('combobox', { name: 'Request Model Name' })
  ).toHaveLength(1)
  expect(picker.getByRole('button', { name: 'gpt-4o' })).toBeVisible()
  expect(picker.getByRole('button', { name: 'o3-all' })).toBeVisible()
  expect(picker.queryByRole('button', { name: 'o3' })).not.toBeInTheDocument()
  expect(screen.getByText('1 model(s) redirected')).toBeVisible()

  await user.click(panel.getByRole('button', { name: 'Collapse panel' }))
  expect(
    screen.queryByRole('complementary', { name: 'Model redirects' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Expand panel' }))
  expect(
    screen.getByRole('complementary', { name: 'Model redirects' })
  ).toBeVisible()
})

test('on wide screens the quick options move into the header as a compact toggle strip and leave the form', async () => {
  useWideScreen()
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = screen.getByRole('group', { name: 'Quick options' })
  expect(quick.closest('[data-slot="sheet-header"]')).not.toBeNull()
  expect(within(quick).getAllByRole('switch')).toHaveLength(5)
  // Second header row: status badge and description on the left, toggles right.
  const statusRow = quick.parentElement
  if (!statusRow) throw new Error('status row missing')
  expect(within(statusRow).getByText('Enabled')).toBeVisible()
  expect(
    within(statusRow).queryByRole('heading', { name: /Edit Channel/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText(
      'Preserve upstream-specific fields when API formats match; bypasses model redirect, parameter override and format conversion'
    )
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Quick actions')).toBeVisible()

  const passthrough = within(quick).getByRole('switch', {
    name: 'Pass Through Body',
  })
  expect(passthrough).toHaveAttribute('data-size', 'sm')
  expect(passthrough).not.toBeChecked()
  await user.click(passthrough)
  expect(passthrough).not.toBeChecked()
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(passthrough).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const all = screen.getAllByRole('switch', { name: 'Pass Through Body' })
  expect(all).toHaveLength(2)
  for (const toggle of all) expect(toggle).toBeChecked()
})

test('header passthrough quick option confirms before writing the wildcard rule and clears it without asking', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  const headers = quick.getByRole('switch', {
    name: 'Pass Through Request Headers',
  })
  expect(headers).not.toBeChecked()

  await user.click(headers)
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Enable request header passthrough?',
  })
  expect(dialog).toHaveTextContent(
    'Entries configured in Request Header Override still take priority'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(headers).not.toBeChecked()

  await user.click(headers)
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(headers).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const editor = screen.getByLabelText(
    'Request Header Override'
  ) as HTMLTextAreaElement
  expect(JSON.parse(editor.value)).toEqual({ '*': true })

  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  )
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  ).not.toBeChecked()

  await user.click(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { header_override: string }
  expect(JSON.parse(payload.header_override)).toEqual({ '*': true })
})

test('the header override passthrough template button confirms before filling the wildcard rule', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const editor = screen.getByLabelText(
    'Request Header Override'
  ) as HTMLTextAreaElement
  expect(editor.value).toBe('')

  await user.click(screen.getByRole('button', { name: 'Passthrough Template' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Enable request header passthrough?',
  })
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(editor.value).toBe('')

  await user.click(screen.getByRole('button', { name: 'Passthrough Template' }))
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  await waitFor(() => expect(JSON.parse(editor.value)).toEqual({ '*': true }))
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(
    within(screen.getByRole('group', { name: 'Quick options' })).getByRole(
      'switch',
      { name: 'Pass Through Request Headers' }
    )
  ).toBeChecked()
})

test('a New API channel binds upstream task plugins and publishes their models', async () => {
  const channel = channelSchema.parse({
    ...editingChannel,
    type: 60,
    base_url: 'https://gateway.example',
    models: 'gpt-5',
    setting: JSON.stringify({ task_extend_plugin_keys: ['video-a'] }),
  })
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42') {
      return { data: { success: true, data: channel } }
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={channel} />)
  const extensions = within(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  )
  expect(
    extensions.getByRole('button', { name: 'Video A Selected 0 / 1' })
  ).toBeVisible()
  expect(
    extensions.queryByRole('button', { name: /^Video B/ })
  ).not.toBeInTheDocument()

  const selector = screen.getByRole('combobox', {
    name: 'Select the task plugins installed on the upstream gateway',
  })
  await user.click(selector)
  expect(
    screen.queryByRole('option', { name: /^No Address/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('option', { name: /^Video B/ }))
  await user.keyboard('{Escape}')
  expect(
    extensions.getByRole('button', { name: 'Video B Selected 1 / 1' })
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'video-b-1' })).toBeVisible()

  await user.click(selector)
  await user.click(screen.getByRole('option', { name: /^Video A/ }))
  await user.keyboard('{Escape}')
  expect(
    extensions.queryByRole('button', { name: /^Video A/ })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'gpt-5' })).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as {
    setting?: string
    models?: string
  }
  const setting = JSON.parse(payload.setting ?? '{}')
  expect(setting).toMatchObject({ task_extend_plugin_keys: ['video-b'] })
  expect(setting).not.toHaveProperty('task_plugin_key')
  expect(payload.models?.split(',').sort()).toEqual(['gpt-5', 'video-b-1'])
})

test.each([
  {
    saved: undefined,
    shown: 'InvokeModel (legacy)',
    next: 'Mantle (Messages API)',
    value: 'mantle',
  },
  {
    saved: 'mantle',
    shown: 'Mantle (Messages API)',
    next: 'InvokeModel (legacy)',
    value: 'invoke_model',
  },
])(
  'AWS API defaults or restores $shown and saves the selected interface',
  async ({ saved, shown, next, value }) => {
    editingChannel = {
      ...editingChannel,
      type: 33,
      base_url: '',
      models: 'claude-opus-4-7',
      settings: JSON.stringify({
        aws_key_type: 'api_key',
        aws_api_type: saved,
      }),
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    const selector = await screen.findByRole('combobox', { name: 'AWS API' })
    await waitFor(() => expect(selector).toHaveTextContent(shown))
    await user.click(selector)
    await user.click(screen.getByRole('option', { name: next }))
    expect(selector).toHaveTextContent(next)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const payload = put.mock.calls[0]?.[1] as { settings: string }
    expect(JSON.parse(payload.settings)).toMatchObject({
      aws_key_type: 'api_key',
      aws_api_type: value,
    })
  }
)

test('AWS interface selection is disabled without sensitive channel write permission', async () => {
  editingChannel = {
    ...editingChannel,
    type: 33,
    settings: '{"aws_api_type":"mantle"}',
  }
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { read: true, write: true } },
        },
      },
    },
  })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(
    await screen.findByRole('combobox', { name: 'AWS API' })
  ).toBeDisabled()
})

const HIDE_UPSTREAM_BALANCE_ERRORS = 'Hide upstream insufficient balance errors'

test('a new channel passes upstream balance errors through until the switch is turned on, which shows the message and status with their defaults', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^DeepSeek / }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const toggle = screen.getByRole('switch', {
    name: HIDE_UPSTREAM_BALANCE_ERRORS,
  })
  expect(toggle).not.toBeChecked()
  expect(toggle).toHaveAccessibleDescription(/log details/)
  expect(
    screen.queryByRole('textbox', { name: 'Error returned to users' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('spinbutton', { name: 'Status Code' })
  ).not.toBeInTheDocument()

  await user.click(toggle)

  expect(
    screen.getByRole('textbox', { name: 'Error returned to users' })
  ).toHaveAttribute('placeholder', '上游tokens数量不足')
  expect(
    screen.getByRole('spinbutton', { name: 'Status Code' })
  ).toHaveAttribute('placeholder', '403')
})

test.each([1, 14, 24, CHANNEL_TYPE_NOVELAI])(
  'an existing channel of type %i saved before the setting shows upstream balance errors passed through, unmarked',
  async (type) => {
    editingChannel = { ...editingChannel, type, settings: '{}' }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    expect(requestTab).not.toHaveAccessibleName(/Configured/)
    await user.click(requestTab)
    expect(
      screen.getByRole('switch', { name: HIDE_UPSTREAM_BALANCE_ERRORS })
    ).not.toBeChecked()
    expect(
      screen.queryByRole('textbox', { name: 'Error returned to users' })
    ).not.toBeInTheDocument()
  }
)

test('saving a channel without touching the switch writes no upstream balance error setting', async () => {
  editingChannel = {
    ...editingChannel,
    settings: '{"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  const settings = JSON.parse(payload.settings)
  expect(settings).not.toHaveProperty('upstream_quota_error_mask_enabled')
  expect(settings).not.toHaveProperty('upstream_quota_error_message')
  expect(settings).not.toHaveProperty('upstream_quota_error_status_code')
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('turning the switch on marks Request & Response and saves it with a trimmed Chinese message, a custom status and the other settings intact', async () => {
  editingChannel = {
    ...editingChannel,
    settings: '{"future_option":{"level":2}}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  await user.click(requestTab)
  await user.click(
    screen.getByRole('switch', { name: HIDE_UPSTREAM_BALANCE_ERRORS })
  )
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.type(
    screen.getByRole('textbox', { name: 'Error returned to users' }),
    '  余额维护中，请稍后再试  '
  )
  await user.type(
    screen.getByRole('spinbutton', { name: 'Status Code' }),
    '429'
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    upstream_quota_error_mask_enabled: true,
    upstream_quota_error_message: '余额维护中，请稍后再试',
    upstream_quota_error_status_code: 429,
    future_option: { level: 2 },
  })
})

test('a channel saved with the switch on shows its message and status, and turning it off hides them and saves them without the switch', async () => {
  editingChannel = {
    ...editingChannel,
    settings:
      '{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_message":"余额维护中","upstream_quota_error_status_code":429,"novelai_hd_disabled":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const toggle = screen.getByRole('switch', {
    name: HIDE_UPSTREAM_BALANCE_ERRORS,
  })
  expect(toggle).toBeChecked()
  expect(
    screen.getByRole('textbox', { name: 'Error returned to users' })
  ).toHaveValue('余额维护中')
  expect(screen.getByRole('spinbutton', { name: 'Status Code' })).toHaveValue(
    429
  )

  await user.click(toggle)

  expect(toggle).not.toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  expect(
    screen.queryByRole('textbox', { name: 'Error returned to users' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  const settings = JSON.parse(payload.settings)
  expect(settings).not.toHaveProperty('upstream_quota_error_mask_enabled')
  expect(settings).toMatchObject({
    upstream_quota_error_message: '余额维护中',
    upstream_quota_error_status_code: 429,
  })
})

test.each([
  [
    'a message over 500 characters',
    '额'.repeat(501),
    '',
    'Error returned to users',
    'The error returned to users must be at most 500 characters',
  ],
  [
    'a status below 400',
    '',
    '399',
    'Status Code',
    'The status code must be a whole number from 400 to 599',
  ],
  [
    'a status above 599',
    '',
    '600',
    'Status Code',
    'The status code must be a whole number from 400 to 599',
  ],
  [
    'a fractional status',
    '',
    '403.5',
    'Status Code',
    'The status code must be a whole number from 400 to 599',
  ],
])(
  'a channel with %s is not saved and shows the allowed range',
  async (_case, message, status, field, error) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    await user.click(
      screen.getByRole('switch', { name: HIDE_UPSTREAM_BALANCE_ERRORS })
    )
    if (message) {
      fireEvent.change(
        screen.getByRole('textbox', { name: 'Error returned to users' }),
        { target: { value: message } }
      )
    }
    if (status) {
      await user.type(
        screen.getByRole('spinbutton', { name: 'Status Code' }),
        status
      )
    }
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(await screen.findByText(error)).toBeVisible()
    expect(
      screen.getByRole(field === 'Status Code' ? 'spinbutton' : 'textbox', {
        name: field,
      })
    ).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

// The server counts characters (code points) after trimming.
test.each([
  ['500 Chinese characters', '额'.repeat(500), '额'.repeat(500)],
  ['500 emoji of two UTF-16 units each', '😀'.repeat(500), '😀'.repeat(500)],
  [
    '500 characters between spaces',
    `  ${'额'.repeat(500)}  `,
    '额'.repeat(500),
  ],
])('a message of %s is saved', async (_case, typed, saved) => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  await user.click(
    screen.getByRole('switch', { name: HIDE_UPSTREAM_BALANCE_ERRORS })
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Error returned to users' }),
    { target: { value: typed } }
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    upstream_quota_error_message: saved,
  })
})

test('turning the switch off puts a message or status the server would refuse back to its saved value, so the channel still saves', async () => {
  editingChannel = {
    ...editingChannel,
    settings:
      '{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_message":"余额维护中","upstream_quota_error_status_code":429}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Error returned to users' }),
    { target: { value: '额'.repeat(501) } }
  )
  const status = screen.getByRole('spinbutton', { name: 'Status Code' })
  await user.clear(status)
  await user.type(status, '600')

  await user.click(
    screen.getByRole('switch', { name: HIDE_UPSTREAM_BALANCE_ERRORS })
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))

  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  const settings = JSON.parse(payload.settings)
  expect(settings).not.toHaveProperty('upstream_quota_error_mask_enabled')
  expect(settings).toMatchObject({
    upstream_quota_error_message: '余额维护中',
    upstream_quota_error_status_code: 429,
  })
})

test('a channel saved with status 0 shows the default status and saves without one', async () => {
  editingChannel = {
    ...editingChannel,
    settings:
      '{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_status_code":0}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const status = screen.getByRole('spinbutton', { name: 'Status Code' })
  expect(status).toHaveValue(null)
  expect(status).toHaveAttribute('placeholder', '403')

  await user.click(screen.getByRole('button', { name: 'Update Channel' }))

  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  const settings = JSON.parse(payload.settings)
  expect(settings).toMatchObject({ upstream_quota_error_mask_enabled: true })
  expect(settings).not.toHaveProperty('upstream_quota_error_status_code')
})

test('the upstream balance error switch is locked without sensitive channel write permission', async () => {
  editingChannel = {
    ...editingChannel,
    settings: '{"upstream_quota_error_mask_enabled":true}',
  }
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { read: true, write: true } },
        },
      },
    },
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const toggle = screen.getByRole('switch', {
    name: HIDE_UPSTREAM_BALANCE_ERRORS,
  })
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await user.click(toggle)
  expect(toggle).toBeChecked()
  expect(
    screen.getByRole('textbox', { name: 'Error returned to users' })
  ).toBeDisabled()
  expect(screen.getByRole('spinbutton', { name: 'Status Code' })).toBeDisabled()
})

const FREE_KEY_MODE = 'Free key mode'
const HIDE_FREE_TIER_ERRORS = 'Hide free-tier errors'
const KEYS_PER_REQUEST = 'Keys to try per request'
const MODEL_LIMIT_ERROR = 'Enter a whole number from -1 to 1000000000'

// A saved model limits table that differs from the built-in defaults. Most
// tests use it: role queries over the 44 default rows are slow in jsdom, so
// the tests on the default table find its cells by label instead.
const SAVED_MODEL_LIMITS = [
  { model: 'gemini-2.5-flash', rpm: 4, tpm: 200000, rpd: 15 },
  {
    model: 'gemini-3.8-flash',
    aliases: ['gemini-3.7-flash', 'gemini-flash-latest'],
    rpm: -1,
    tpm: 250000,
    rpd: 0,
  },
]

function geminiChannel(settings: Record<string, unknown>): Channel {
  return {
    ...editingChannel,
    type: 24,
    models: 'gemini-2.5-flash',
    settings: JSON.stringify(settings),
  }
}

function freeKeySettingsOf(
  settings: Record<string, unknown>
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(settings).filter(([key]) =>
      key.startsWith('gemini_free_key')
    )
  )
}

async function openGeminiRequestTab(channel: Channel) {
  editingChannel = channel
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  await user.click(requestTab)
  return { user, requestTab }
}

async function saveAndReadSettings(
  user: ReturnType<typeof userEvent.setup>
): Promise<Record<string, unknown>> {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  return JSON.parse(payload.settings)
}

test.each([1, 41, CHANNEL_TYPE_NOVELAI])(
  'a channel of type %i offers no free key mode',
  async (type) => {
    editingChannel = {
      ...editingChannel,
      type,
      settings: '{"gemini_free_key_enabled":true}',
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    expect(requestTab).not.toHaveAccessibleName(/Configured/)
    await user.click(requestTab)
    expect(
      screen.queryByRole('switch', { name: FREE_KEY_MODE })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'View usage' })
    ).not.toBeInTheDocument()
  }
)

test('a new Gemini channel starts with free key mode off, and turning it on shows the defaults without a usage button', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^Gemini / }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const processing = screen.getByRole('group', { name: 'Request processing' })
  const toggle = within(processing).getByRole('switch', { name: FREE_KEY_MODE })
  expect(toggle).not.toBeChecked()
  // The owner's rules: per key and model, reset at midnight Pacific time,
  // 429 to other channels.
  expect(toggle).toHaveAccessibleDescription(/midnight Pacific time/)
  expect(toggle).toHaveAccessibleDescription(/16:00 Beijing time/)
  expect(
    screen.queryByRole('switch', { name: HIDE_FREE_TIER_ERRORS })
  ).not.toBeInTheDocument()

  await user.click(toggle)

  expect(
    within(processing).getByRole('switch', { name: HIDE_FREE_TIER_ERRORS })
  ).toBeChecked()
  expect(
    within(processing).getByRole('combobox', { name: 'Token counting' })
  ).toHaveTextContent('countTokens API (accurate)')
  const attempts = within(processing).getByLabelText(KEYS_PER_REQUEST)
  expect(attempts).toHaveValue(null)
  expect(attempts).toHaveAttribute('placeholder', '3')
  const limits = within(processing).getByRole('group', { name: 'Model limits' })
  expect(within(limits).getAllByLabelText(/^Model #/)).toHaveLength(44)
  expect(within(limits).getByLabelText('Model #1')).toHaveValue(
    'gemini-3.8-flash'
  )
  expect(within(limits).getByLabelText('Aliases #1')).toHaveValue(
    'gemini-3.7-flash'
  )
  expect(within(limits).getByLabelText('TPM #1')).toHaveValue(250000)
  expect(within(limits).getByLabelText('RPD #44')).toHaveValue(0)
  expect(within(processing).queryByText('View usage')).not.toBeInTheDocument()
})

test('a Gemini channel saved before free key mode shows it off and unmarked, and saving writes no free key setting', async () => {
  const { user, requestTab } = await openGeminiRequestTab(
    geminiChannel({ future_option: { level: 2 } })
  )
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  expect(screen.getByRole('switch', { name: FREE_KEY_MODE })).not.toBeChecked()
  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual({})
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('turning free key mode on marks Request & Response and saves only the switch while everything else keeps its default', async () => {
  const { user, requestTab } = await openGeminiRequestTab(
    geminiChannel({ future_option: { level: 2 } })
  )
  await user.click(screen.getByRole('switch', { name: FREE_KEY_MODE }))
  expect(requestTab).toHaveAccessibleName(/Configured/)
  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual({
    gemini_free_key_enabled: true,
  })
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('a Gemini channel saved with every free key setting shows each one and saves them back unchanged', async () => {
  const saved = {
    gemini_free_key_enabled: true,
    gemini_free_key_reveal_tier_errors: true,
    gemini_free_key_token_counter: 'local',
    gemini_free_key_max_attempts: 5,
    gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
  }
  const { user, requestTab } = await openGeminiRequestTab(
    geminiChannel({ ...saved, future_option: { level: 2 } })
  )
  expect(requestTab).toHaveAccessibleName(/Configured/)
  expect(screen.getByRole('switch', { name: FREE_KEY_MODE })).toBeChecked()
  // Revealing free-tier errors is the switch that hides them turned off.
  expect(
    screen.getByRole('switch', { name: HIDE_FREE_TIER_ERRORS })
  ).not.toBeChecked()
  expect(
    screen.getByRole('combobox', { name: 'Token counting' })
  ).toHaveTextContent('Local estimate (faster)')
  expect(
    screen.getByRole('spinbutton', { name: KEYS_PER_REQUEST })
  ).toHaveValue(5)
  expect(screen.getAllByRole('textbox', { name: /^Model #/ })).toHaveLength(2)
  expect(screen.getByRole('textbox', { name: 'Aliases #2' })).toHaveValue(
    'gemini-3.7-flash, gemini-flash-latest'
  )
  expect(screen.getByRole('spinbutton', { name: 'RPM #2' })).toHaveValue(-1)
  expect(screen.getByRole('spinbutton', { name: 'RPD #2' })).toHaveValue(0)

  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual(saved)
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test.each([
  [false, true],
  [true, false],
])(
  'a Gemini channel saved with free-tier errors revealed: %s saves them revealed: %s after the hide switch is toggled',
  async (revealed, saved) => {
    const { user } = await openGeminiRequestTab(
      geminiChannel({
        gemini_free_key_enabled: true,
        gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
        ...(revealed && { gemini_free_key_reveal_tier_errors: true }),
      })
    )
    const hide = screen.getByRole('switch', { name: HIDE_FREE_TIER_ERRORS })
    expect(hide).toHaveAccessibleDescription(/plain 429/)
    await user.click(hide)
    expect(hide).toHaveAttribute('aria-checked', String(revealed))
    const settings = await saveAndReadSettings(user)
    expect(settings.gemini_free_key_reveal_tier_errors).toBe(
      saved ? true : undefined
    )
  }
)

test.each([
  [undefined, 'Local estimate (faster)', 'local'],
  ['local', 'countTokens API (accurate)', undefined],
])(
  'a Gemini channel saved with token counter %s saves the choice %s as %s',
  async (stored, choice, saved) => {
    const { user } = await openGeminiRequestTab(
      geminiChannel({
        gemini_free_key_enabled: true,
        gemini_free_key_token_counter: stored,
        gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
      })
    )
    await user.click(screen.getByRole('combobox', { name: 'Token counting' }))
    await user.click(screen.getByRole('option', { name: choice }))
    const settings = await saveAndReadSettings(user)
    expect(settings.gemini_free_key_token_counter).toBe(saved)
  }
)

test.each([
  ['1', 1],
  ['10', 10],
  ['3', undefined],
  ['', undefined],
])(
  'keys to try per request of "%s" saves the setting as %s',
  async (typed, saved) => {
    const { user } = await openGeminiRequestTab(
      geminiChannel({
        gemini_free_key_enabled: true,
        gemini_free_key_max_attempts: 7,
        gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
      })
    )
    const attempts = screen.getByRole('spinbutton', { name: KEYS_PER_REQUEST })
    await user.clear(attempts)
    if (typed) await user.type(attempts, typed)
    const settings = await saveAndReadSettings(user)
    expect(settings.gemini_free_key_max_attempts).toBe(saved)
  }
)

test.each(['0', '11', '2.5'])(
  'keys to try per request of %s is not saved and shows the allowed range',
  async (typed) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const { user } = await openGeminiRequestTab(
      geminiChannel({
        gemini_free_key_enabled: true,
        gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
      })
    )
    const attempts = screen.getByRole('spinbutton', { name: KEYS_PER_REQUEST })
    await user.type(attempts, typed)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(
      await screen.findByText(
        'Keys to try per request must be a whole number from 1 to 10'
      )
    ).toBeVisible()
    expect(attempts).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

test('with free key mode on, the settings fieldset can shrink, so the model limits table scrolls inside its block on a narrow screen', async () => {
  await openGeminiRequestTab(geminiChannel({ gemini_free_key_enabled: true }))
  const limits = screen.getByRole('group', { name: 'Model limits' })
  // A fieldset is at least as wide as its content by default, so without
  // min-w-0 it grows to the table's width and the panel scrolls sideways.
  expect(limits.closest('fieldset')).toHaveClass('min-w-0')
})

test('editing one limit of the default table saves the whole table with that change and aliases only where a model has some', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({ gemini_free_key_enabled: true })
  )
  const limits = screen.getByRole('group', { name: 'Model limits' })
  // Row 4 is gemini-2.5-flash.
  expect(within(limits).getByLabelText('Model #4')).toHaveValue(
    'gemini-2.5-flash'
  )
  const rpd = within(limits).getByLabelText('RPD #4')
  await user.clear(rpd)
  await user.type(rpd, '50')
  const settings = await saveAndReadSettings(user)
  const saved = settings.gemini_free_key_model_limits as Array<
    Record<string, unknown>
  >
  expect(saved).toHaveLength(44)
  expect(saved[3]).toEqual({
    model: 'gemini-2.5-flash',
    rpm: 5,
    tpm: 250000,
    rpd: 50,
  })
  expect(saved[0]).toEqual({
    model: 'gemini-3.8-flash',
    aliases: ['gemini-3.7-flash'],
    rpm: 5,
    tpm: 250000,
    rpd: 20,
  })
})

test.each([
  [
    'requests per minute',
    'RPM #4',
    '4',
    3,
    { model: 'gemini-2.5-flash', rpm: 4, tpm: 250000, rpd: 20 },
  ],
  [
    'input tokens per minute',
    'TPM #4',
    '200000',
    3,
    { model: 'gemini-2.5-flash', rpm: 5, tpm: 200000, rpd: 20 },
  ],
  [
    'aliases',
    'Aliases #1',
    'gemini-3.7-flash, gemini-flash-latest',
    0,
    {
      model: 'gemini-3.8-flash',
      aliases: ['gemini-3.7-flash', 'gemini-flash-latest'],
      rpm: 5,
      tpm: 250000,
      rpd: 20,
    },
  ],
])(
  'editing the %s of a default row saves the whole table with that change',
  async (_field, cell, value, index, row) => {
    const { user } = await openGeminiRequestTab(
      geminiChannel({ gemini_free_key_enabled: true })
    )
    const limits = screen.getByRole('group', { name: 'Model limits' })
    fireEvent.change(within(limits).getByLabelText(cell), {
      target: { value },
    })
    const settings = await saveAndReadSettings(user)
    const saved = settings.gemini_free_key_model_limits as Array<
      Record<string, unknown>
    >
    expect(saved).toHaveLength(44)
    expect(saved[index]).toEqual(row)
  }
)

test('the model limits table takes every value the server takes, up to its limits, and refuses one more', () => {
  const row = (model: string, aliases = '') => ({
    model,
    aliases,
    rpm: 1,
    tpm: 1,
    rpd: 1,
  })
  const aliases = (count: number) =>
    Array.from({ length: count }, (_, i) => `alias-${i}`).join(',')
  // Names count in characters, as the server counts them.
  const longest = '𝔪'.repeat(GEMINI_FREE_KEY_MODEL_NAME_MAX)
  expect(
    validateGeminiFreeKeyModelLimitRows([
      {
        ...row(
          longest,
          `${aliases(GEMINI_FREE_KEY_ALIASES_MAX - 1)},a${longest.slice(2)}`
        ),
        rpm: GEMINI_FREE_KEY_LIMIT_MIN,
        tpm: GEMINI_FREE_KEY_LIMIT_MAX,
        rpd: 0,
      },
    ])
  ).toEqual([])
  const full = Array.from(
    { length: GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS },
    (_, i) => row(`model-${i}`)
  )
  expect(validateGeminiFreeKeyModelLimitRows(full)).toEqual([])

  expect(
    validateGeminiFreeKeyModelLimitRows([...full, row('one-more')])
  ).toEqual([{ message: 'At most 300 models' }])
  expect(validateGeminiFreeKeyModelLimitRows([row(`${longest}m`)])).toEqual([
    {
      index: 0,
      field: 'model',
      message: 'Model names and aliases must be at most 128 characters',
    },
  ])
  expect(
    validateGeminiFreeKeyModelLimitRows([
      row('m', aliases(GEMINI_FREE_KEY_ALIASES_MAX + 1)),
    ])
  ).toEqual([
    {
      index: 0,
      field: 'aliases',
      message: 'A model can have at most 20 aliases',
    },
  ])
  expect(
    validateGeminiFreeKeyModelLimitRows([
      {
        ...row('m'),
        rpm: GEMINI_FREE_KEY_LIMIT_MIN - 1,
        tpm: GEMINI_FREE_KEY_LIMIT_MAX + 1,
      },
    ]).map((issue) => issue.field)
  ).toEqual(['rpm', 'tpm'])
})

test('a full model limits table offers no more rows', async () => {
  const full = Array.from(
    { length: GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS },
    (_, i) => ({ model: `model-${i}`, rpm: 1, tpm: 1, rpd: 1 })
  )
  await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: full.slice(0, -1),
    })
  )
  expect(screen.getByRole('button', { name: 'Add model' })).toBeEnabled()
  cleanup()
  await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: full,
    })
  )
  expect(screen.getByRole('button', { name: 'Add model' })).toBeDisabled()
})

test('an added model saves with trimmed names and its comma-separated aliases, full-width commas included', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  await user.click(screen.getByRole('button', { name: 'Add model' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Model #3' }),
    ' my-model '
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Aliases #3' }),
    'my-alias , ，other-alias，third-alias'
  )
  await user.type(screen.getByRole('spinbutton', { name: 'RPM #3' }), '-1')
  await user.type(screen.getByRole('spinbutton', { name: 'TPM #3' }), '1000')
  await user.type(screen.getByRole('spinbutton', { name: 'RPD #3' }), '2')
  const settings = await saveAndReadSettings(user)
  expect(settings.gemini_free_key_model_limits).toEqual([
    ...SAVED_MODEL_LIMITS,
    {
      model: 'my-model',
      aliases: ['my-alias', 'other-alias', 'third-alias'],
      rpm: -1,
      tpm: 1000,
      rpd: 2,
    },
  ])
})

test('removing a model and restoring the defaults saves no model limits, since the table equals the defaults again', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  await user.click(
    screen.getByRole('button', { name: 'Remove gemini-2.5-flash' })
  )
  const limits = screen.getByRole('group', { name: 'Model limits' })
  expect(within(limits).getAllByLabelText(/^Model #/)).toHaveLength(1)
  expect(within(limits).getByLabelText('Model #1')).toHaveValue(
    'gemini-3.8-flash'
  )
  await user.click(
    within(limits).getByRole('button', { name: 'Restore defaults' })
  )
  expect(within(limits).getAllByLabelText(/^Model #/)).toHaveLength(44)
  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual({
    gemini_free_key_enabled: true,
  })
})

test('a saved table with the default rows in another order and letter case counts as the defaults and saves no model limits', async () => {
  const shuffled = [...GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS]
    .reverse()
    .map((limit) => ({
      ...limit,
      model: limit.model.toUpperCase(),
      aliases: [...(limit.aliases ?? [])].reverse(),
    }))
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: shuffled,
    })
  )
  const limits = screen.getByRole('group', { name: 'Model limits' })
  expect(within(limits).getByLabelText('Model #1')).toHaveValue(
    'DEEP-RESEARCH-PRO-PREVIEW-12-2025'
  )
  const settings = await saveAndReadSettings(user)
  expect(settings).not.toHaveProperty('gemini_free_key_model_limits')
})

test.each([
  ['an empty model name', 'Model #2', '', 'Enter a model name'],
  [
    'a model name used by another row in other letter case',
    'Model #2',
    'GEMINI-2.5-FLASH',
    'This name is already used by another model or alias',
  ],
  [
    'an alias that is another row’s model',
    'Aliases #2',
    'gemini-3.7-flash, gemini-2.5-flash',
    'This name is already used by another model or alias',
  ],
  [
    'an alias that is its own model',
    'Aliases #2',
    'gemini-3.8-flash',
    'This name is already used by another model or alias',
  ],
  [
    'a model name of 129 characters',
    'Model #2',
    'm'.repeat(129),
    'Model names and aliases must be at most 128 characters',
  ],
  [
    '21 aliases',
    'Aliases #2',
    Array.from({ length: 21 }, (_, i) => `alias-${i}`).join(','),
    'A model can have at most 20 aliases',
  ],
  ['an RPM of -2', 'RPM #2', '-2', MODEL_LIMIT_ERROR],
  ['a TPM above 1000000000', 'TPM #2', '1000000001', MODEL_LIMIT_ERROR],
  ['a fractional RPD', 'RPD #2', '1.5', MODEL_LIMIT_ERROR],
  ['an empty RPD', 'RPD #2', '', MODEL_LIMIT_ERROR],
])(
  'a model limits table with %s is not saved and marks the cell',
  async (_case, cell, value, error) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const { user } = await openGeminiRequestTab(
      geminiChannel({
        gemini_free_key_enabled: true,
        gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
      })
    )
    const input = screen.getByLabelText(cell)
    fireEvent.change(input, { target: { value } })
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(await screen.findByText(error)).toBeVisible()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

test('a model limits table emptied of every row is not saved and asks to keep a model or restore the defaults', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const { user, requestTab } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  await user.click(
    screen.getByRole('button', { name: 'Remove gemini-2.5-flash' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Remove gemini-3.8-flash' })
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Keep at least one model, or restore the defaults'
  )
  expect(requestTab).toHaveAccessibleName(/Error/)
  expect(put).not.toHaveBeenCalled()
})

test('turning free key mode off puts a refused attempt count and model table back to their saved values, so the channel still saves', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_max_attempts: 5,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  const attempts = screen.getByRole('spinbutton', { name: KEYS_PER_REQUEST })
  await user.clear(attempts)
  await user.type(attempts, '50')
  await user.clear(screen.getByRole('textbox', { name: 'Model #1' }))

  await user.click(screen.getByRole('switch', { name: FREE_KEY_MODE }))
  expect(
    screen.queryByRole('group', { name: 'Model limits' })
  ).not.toBeInTheDocument()

  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual({
    gemini_free_key_max_attempts: 5,
    gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
  })
})

test('an OpenAI channel saved with free key settings, even ones a Gemini channel would refuse, saves its other settings without them', async () => {
  editingChannel = {
    ...editingChannel,
    type: 1,
    settings: JSON.stringify({
      gemini_free_key_enabled: true,
      gemini_free_key_reveal_tier_errors: true,
      gemini_free_key_token_counter: 'local',
      gemini_free_key_max_attempts: 50,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
      future_option: { level: 2 },
    }),
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const settings = await saveAndReadSettings(user)
  expect(freeKeySettingsOf(settings)).toEqual({})
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('the free key settings are locked without sensitive channel write permission while the usage stays viewable', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { read: true, write: true } },
        },
      },
    },
  })
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  const toggle = screen.getByRole('switch', { name: FREE_KEY_MODE })
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await user.click(toggle)
  expect(toggle).toBeChecked()
  expect(
    screen.getByRole('switch', { name: HIDE_FREE_TIER_ERRORS })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Token counting' })
  ).toBeDisabled()
  expect(
    screen.getByRole('spinbutton', { name: KEYS_PER_REQUEST })
  ).toBeDisabled()
  expect(screen.getByRole('textbox', { name: 'Model #1' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Add model' })).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Remove gemini-2.5-flash' })
  ).toBeDisabled()
  expect(screen.getByRole('button', { name: 'View usage' })).toBeEnabled()
})

const FREE_KEY_USAGE = {
  enabled: true,
  timezone: 'America/Los_Angeles',
  // 2100-01-01 00:00 in Los Angeles.
  next_reset_at: 4102473600,
  keys: [
    {
      index: 0,
      key_hint: '…a1B2',
      enabled: true,
      cooldown_until: 0,
      cooldown_reason: '',
      models: [
        {
          model: 'gemini-2.5-flash',
          rpm_used: 2,
          rpm_limit: 5,
          tpm_used: 1200,
          tpm_limit: 250000,
          rpd_used: 20,
          rpd_limit: 20,
          cooldown_until: 0,
          cooldown_reason: '',
        },
        {
          model: 'gemini-3.8-flash',
          rpm_used: 5,
          rpm_limit: 5,
          tpm_used: 0,
          tpm_limit: 250000,
          rpd_used: 7,
          rpd_limit: 20,
          // 2100-01-01 00:01 UTC.
          cooldown_until: 4102444860,
          cooldown_reason: 'minute',
        },
        {
          model: 'gemini-2.5-pro',
          rpm_used: 0,
          rpm_limit: 0,
          tpm_used: 0,
          tpm_limit: 0,
          rpd_used: 0,
          rpd_limit: 0,
          cooldown_until: 0,
          cooldown_reason: '',
        },
        {
          model: 'gemini-3.8-live',
          rpm_used: 3,
          rpm_limit: -1,
          tpm_used: 100,
          tpm_limit: 65000,
          rpd_used: 3,
          rpd_limit: -1,
          // A pause that has ended.
          cooldown_until: 1,
          cooldown_reason: 'minute',
        },
      ],
    },
    {
      index: 1,
      key_hint: '…zZ9x',
      enabled: false,
      // 2100-01-01 01:00 UTC.
      cooldown_until: 4102448400,
      cooldown_reason: 'key_error',
      models: [
        {
          model: 'gemini-2.5-flash',
          rpm_used: 1,
          rpm_limit: 5,
          tpm_used: 10,
          tpm_limit: 250000,
          rpd_used: 1,
          rpd_limit: 20,
          cooldown_until: 0,
          cooldown_reason: '',
        },
      ],
    },
  ],
} satisfies GeminiFreeKeyUsage

// The server owns the free key defaults, bounds and usage report; the form
// and the usage dialog mirror them. These read the server's source so that
// the two cannot drift apart.
function serverSource(path: string): string {
  return readFileSync(
    resolve(import.meta.dirname, '../../../../../..', path),
    'utf8'
  )
}

test('the free key defaults and bounds of the form are the server’s', () => {
  const source = serverSource('relaykit/dto/gemini_free_key.go')
  const constants: Record<string, number | string> = Object.fromEntries(
    [
      ...source.matchAll(
        /^\s*(GeminiFreeKey\w+)\s*=\s*(-?[\d_]+|"[^"]*")\s*$/gm
      ),
    ].map(([, name, value]) => [
      name,
      value.startsWith('"')
        ? JSON.parse(value)
        : Number(value.replaceAll('_', '')),
    ])
  )
  expect(constants).toMatchObject({
    GeminiFreeKeyUnlimited: GEMINI_FREE_KEY_LIMIT_MIN,
    GeminiFreeKeyLimitMax: GEMINI_FREE_KEY_LIMIT_MAX,
    GeminiFreeKeyMaxAttemptsDefault: GEMINI_FREE_KEY_MAX_ATTEMPTS_DEFAULT,
    GeminiFreeKeyMaxAttemptsMax: GEMINI_FREE_KEY_MAX_ATTEMPTS_MAX,
    GeminiFreeKeyModelLimitsMax: GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS,
    GeminiFreeKeyAliasesMax: GEMINI_FREE_KEY_ALIASES_MAX,
    GeminiFreeKeyModelNameMax: GEMINI_FREE_KEY_MODEL_NAME_MAX,
    GeminiFreeKeyTokenCounterCountTokens: GEMINI_FREE_KEY_TOKEN_COUNTER_DEFAULT,
  })
  expect([
    constants.GeminiFreeKeyTokenCounterCountTokens,
    constants.GeminiFreeKeyTokenCounterLocal,
  ]).toEqual([...GEMINI_FREE_KEY_TOKEN_COUNTERS])

  // The rows name -1 and 0 by local constants.
  const [, first = '', second = '', firstValue = '', secondValue = ''] =
    source.match(
      /const (\w+), (\w+) = (GeminiFreeKey\w+), (GeminiFreeKey\w+)/
    ) ?? []
  const named: Record<string, number | string | undefined> = {
    [first]: constants[firstValue],
    [second]: constants[secondValue],
  }
  const rows = [
    ...source.matchAll(/geminiFreeTierLimits\(("[^"]*"[^)]*)\)/g),
  ].map(([, args]) => {
    const [model = '', rpm = '', tpm = '', rpd = '', ...aliases] = args
      .split(',')
      .map((arg) => arg.trim())
    const limit = (arg: string) => named[arg] ?? Number(arg)
    return {
      model: JSON.parse(model),
      aliases: aliases.map((alias) => JSON.parse(alias)),
      rpm: limit(rpm),
      tpm: limit(tpm),
      rpd: limit(rpd),
    }
  })
  expect(named).toEqual({ unlimited: -1, notFree: 0 })
  expect(rows).toEqual(GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS)
})

test('the free key usage the dialog reads has the server’s fields and pause reasons', () => {
  const controller = serverSource('controller/gemini_free_key_usage.go')
  const tagsOf = (type: string) =>
    [
      ...(
        controller.match(
          new RegExp(`type ${type} struct \\{([\\s\\S]*?)\\n\\}`)
        )?.[1] ?? ''
      ).matchAll(/json:"([^",]+)/g),
    ]
      .map(([, tag]) => tag)
      .sort()
  const keysOf = (value: object) => Object.keys(value).sort()
  const [key] = FREE_KEY_USAGE.keys
  expect(tagsOf('geminiFreeKeyUsageResponse')).toEqual(keysOf(FREE_KEY_USAGE))
  expect(tagsOf('geminiFreeKeyKeyUsage')).toEqual(keysOf(key))
  expect(tagsOf('geminiFreeKeyModelUsage')).toEqual(keysOf(key.models[0]))

  const limiter = serverSource('pkg/geminifreekey/limiter.go')
  const reasons = [...limiter.matchAll(/^\s*Reason\w+ Reason = "(\w+)"/gm)].map(
    ([, reason]) => reason
  )
  expect(reasons.sort()).toEqual(
    Object.keys(GEMINI_FREE_KEY_COOLDOWN_REASON_LABELS).sort()
  )
})

async function openFreeKeyUsage(response: unknown) {
  const get = vi.mocked(api.get)
  const answer = get.getMockImplementation()
  get.mockImplementation(async (url, config) => {
    if (url === '/api/channel/42/gemini_free_key_usage') {
      return { data: response }
    }
    if (!answer) throw new Error(`Unexpected GET ${url}`)
    return answer(url, config)
  })
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      gemini_free_key_model_limits: SAVED_MODEL_LIMITS,
    })
  )
  await user.click(screen.getByRole('button', { name: 'View usage' }))
  const dialog = await screen.findByRole('dialog', { name: 'Free key usage' })
  return { get, dialog }
}

test('the free key usage dialog shows each key and model in local time with why it is paused', async () => {
  const timeZone = process.env.TZ
  process.env.TZ = 'Asia/Shanghai'
  try {
    const { get, dialog } = await openFreeKeyUsage({
      success: true,
      data: FREE_KEY_USAGE,
    })
    expect(get).toHaveBeenCalledWith(
      '/api/channel/42/gemini_free_key_usage',
      expect.anything()
    )
    expect(
      within(dialog).getByText('Daily quotas reset at midnight Pacific time')
    ).toBeVisible()
    // Midnight in Los Angeles is 16:00 in Beijing.
    expect(
      await within(dialog).findByText('Next reset: 2100-01-01 16:00:00')
    ).toBeVisible()

    const first = within(dialog).getByRole('region', { name: 'Key #1' })
    expect(first).toHaveTextContent('…a1B2')
    const rows = within(first).getAllByRole('row')
    // Daily requests used up: paused until the next reset.
    expect(rows[1]).toHaveTextContent('gemini-2.5-flash')
    expect(rows[1]).toHaveTextContent('2 / 5')
    expect(rows[1]).toHaveTextContent('1,200 / 250,000')
    expect(rows[1]).toHaveTextContent('20 / 20')
    expect(rows[1]).toHaveTextContent('Daily quota used up')
    expect(rows[1]).toHaveTextContent('Paused until 2100-01-01 16:00:00')
    expect(rows[2]).toHaveTextContent('Per-minute limit')
    expect(rows[2]).toHaveTextContent('Paused until 2100-01-01 08:01:00')
    // A 0 limit: the free tier does not offer the model, with no end time.
    expect(rows[3]).toHaveTextContent('Not on the free tier')
    expect(rows[3]).not.toHaveTextContent('Paused until')
    // Unlimited requests, and a pause that has ended.
    expect(rows[4]).toHaveTextContent('3 / Unlimited')
    expect(rows[4]).toHaveTextContent('Available')

    expect(
      within(dialog).queryByText(
        'Free key mode is not saved as on for this channel yet. Counting starts once it is saved.'
      )
    ).not.toBeInTheDocument()

    const second = within(dialog).getByRole('region', { name: 'Key #2' })
    expect(second).toHaveTextContent('…zZ9x')
    expect(second).toHaveTextContent('Disabled')
    expect(second).toHaveTextContent('Invalid key')
    expect(second).toHaveTextContent('Paused until 2100-01-01 09:00:00')
  } finally {
    process.env.TZ = timeZone
  }
})

test('the free key usage dialog explains a mode not saved as on yet', async () => {
  const { dialog } = await openFreeKeyUsage({
    success: true,
    data: { ...FREE_KEY_USAGE, enabled: false, keys: [] },
  })
  expect(
    await within(dialog).findByText(
      'Free key mode is not saved as on for this channel yet. Counting starts once it is saved.'
    )
  ).toBeVisible()
  expect(within(dialog).getByText('No keys')).toBeVisible()
})

test('the free key usage dialog reports a failed request with the server message', async () => {
  const { dialog } = await openFreeKeyUsage({
    success: false,
    message: 'channel not found',
  })
  expect(
    await within(dialog).findByText('Failed to load free key usage')
  ).toBeVisible()
  expect(within(dialog).getByText('channel not found')).toBeVisible()
})

const THINKING_SUFFIXES = 'Thinking suffixes'

test('a Gemini channel shows the thinking suffix switch right above free key mode, with the suffix rules listed under it', async () => {
  await openGeminiRequestTab(geminiChannel({}))
  const processing = screen.getByRole('group', { name: 'Request processing' })
  const toggle = within(processing).getByRole('switch', {
    name: THINKING_SUFFIXES,
  })
  const switches = within(processing).getAllByRole('switch')
  const freeKey = within(processing).getByRole('switch', {
    name: FREE_KEY_MODE,
  })
  expect(switches[switches.indexOf(freeKey) - 1]).toBe(toggle)
  expect(toggle).toHaveAccessibleDescription(/thinking setting/)
  const rules = within(within(processing).getByRole('list')).getAllByRole(
    'listitem'
  )
  expect(rules).toHaveLength(5)
  expect(rules[2]).toHaveTextContent('gemini-3.8-flash-128-nothinking')
})

test.each([1, 41])(
  'a channel of type %i saved with thinking suffixes on offers no switch and stays unmarked',
  async (type) => {
    editingChannel = {
      ...editingChannel,
      type,
      settings: '{"gemini_thinking_suffix_enabled":true}',
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    expect(requestTab).not.toHaveAccessibleName(/Configured/)
    await user.click(requestTab)
    expect(
      screen.queryByRole('switch', { name: THINKING_SUFFIXES })
    ).not.toBeInTheDocument()
  }
)

test('a new Gemini channel starts with thinking suffixes off', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^Gemini / }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    screen.getByRole('switch', { name: THINKING_SUFFIXES })
  ).not.toBeChecked()
})

test('a Gemini channel saved before thinking suffixes shows the switch off and saves no thinking suffix setting', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({ future_option: { level: 2 } })
  )
  expect(
    screen.getByRole('switch', { name: THINKING_SUFFIXES })
  ).not.toBeChecked()
  const settings = await saveAndReadSettings(user)
  expect(settings).not.toHaveProperty('gemini_thinking_suffix_enabled')
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('turning thinking suffixes on marks Request processing as configured, and turning them off clears the mark', async () => {
  const { user, requestTab } = await openGeminiRequestTab(geminiChannel({}))
  const processing = screen.getByRole('group', { name: 'Request processing' })
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  const toggle = screen.getByRole('switch', { name: THINKING_SUFFIXES })

  await user.click(toggle)
  expect(
    within(processing).getByRole('img', { name: 'Configured' })
  ).toBeVisible()
  expect(requestTab).toHaveAccessibleName(/Configured/)

  await user.click(toggle)
  expect(
    within(processing).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
})

test('turning thinking suffixes on saves the switch and keeps the other settings, free key mode included', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_free_key_enabled: true,
      future_option: { level: 2 },
    })
  )
  await user.click(screen.getByRole('switch', { name: THINKING_SUFFIXES }))
  const settings = await saveAndReadSettings(user)
  expect(settings).toMatchObject({
    gemini_thinking_suffix_enabled: true,
    gemini_free_key_enabled: true,
    future_option: { level: 2 },
  })
})

test('a Gemini channel saved with thinking suffixes on shows the switch on and marked, and turning it off saves the settings without it', async () => {
  const { user, requestTab } = await openGeminiRequestTab(
    geminiChannel({
      gemini_thinking_suffix_enabled: true,
      future_option: { level: 2 },
    })
  )
  expect(requestTab).toHaveAccessibleName(/Configured/)
  const toggle = screen.getByRole('switch', { name: THINKING_SUFFIXES })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  const settings = await saveAndReadSettings(user)
  expect(settings).not.toHaveProperty('gemini_thinking_suffix_enabled')
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('changing a Gemini channel with thinking suffixes on to OpenAI saves its settings without them', async () => {
  const { user } = await openGeminiRequestTab(
    geminiChannel({
      gemini_thinking_suffix_enabled: true,
      future_option: { level: 2 },
    })
  )
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: 'OpenAI Built-in #1' }))
  const settings = await saveAndReadSettings(user)
  expect(settings).not.toHaveProperty('gemini_thinking_suffix_enabled')
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('the thinking suffix switch is locked without sensitive channel write permission', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { read: true, write: true } },
        },
      },
    },
  })
  const { user } = await openGeminiRequestTab(
    geminiChannel({ gemini_thinking_suffix_enabled: true })
  )
  const toggle = screen.getByRole('switch', { name: THINKING_SUFFIXES })
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await user.click(toggle)
  expect(toggle).toBeChecked()
})

const MODERATION = 'Enable external moderation'
const FAIL_OPEN = 'Allow requests when moderation fails'
const SAFETY_IDENTIFIER = 'Send a user safety identifier to OpenAI'
const MAX_CHARS = 'Maximum characters checked per request'
const MODERATION_TIMEOUT = 'Moderation timeout (seconds)'
const SAVE_TO_TEST =
  'Save the channel as an OpenAI channel to test its moderation endpoint.'

function presetName(preset: (typeof MODERATION_PRESETS)[number]): string {
  return `${preset.labelKey} (${preset.language === 'en' ? 'English' : 'Chinese'})`
}

function checkedPresetIds(): string[] {
  return MODERATION_PRESETS.filter(
    (preset) =>
      screen
        .getByRole('checkbox', { name: presetName(preset) })
        .getAttribute('aria-checked') === 'true'
  ).map((preset) => preset.id)
}

function openAIChannel(settings: Record<string, unknown>): Channel {
  return { ...editingChannel, type: 1, settings: JSON.stringify(settings) }
}

async function openModerationCard(channel: Channel) {
  editingChannel = channel
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  await user.click(requestTab)
  return { user, requestTab }
}

function moderationSettingsOf(
  settings: Record<string, unknown>
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(settings).filter(
      ([key]) =>
        key.startsWith('moderation_') || key === 'send_safety_identifier'
    )
  )
}

function operatorWith(channelPermissions: Record<string, boolean>) {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: channelPermissions },
        },
      },
    },
  })
}

test('an OpenAI channel shows the content moderation card with moderation off and its other settings hidden', async () => {
  await openModerationCard(openAIChannel({}))
  const card = screen.getByRole('group', { name: 'Content moderation' })
  const toggle = within(card).getByRole('switch', { name: MODERATION })
  expect(toggle).not.toBeChecked()
  expect(toggle).toHaveAccessibleDescription(/omni-moderation-latest/)
  expect(
    within(card).queryByRole('switch', { name: FAIL_OPEN })
  ).not.toBeInTheDocument()
  expect(
    within(card).getByRole('switch', { name: SAFETY_IDENTIFIER })
  ).not.toBeChecked()
})

test('with moderation on, the category selector is named by its label and described by its help text', async () => {
  await openModerationCard(openAIChannel({ moderation_enabled: true }))
  const card = screen.getByRole('group', { name: 'Content moderation' })
  const categories = within(card).getByRole('combobox', {
    name: 'Block only these categories',
  })
  expect(categories).toHaveAccessibleDescription(
    /Empty: reject whatever OpenAI flags/
  )
  expect(categories).toHaveAttribute('placeholder', 'Select categories')
})

test('with moderation on, every content moderation fieldset can shrink, so the custom rules table scrolls inside the card on a narrow screen', async () => {
  await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_rules: [{ type: 'keyword', pattern: 'cot' }],
    })
  )
  const card = screen.getByRole('group', { name: 'Content moderation' })
  const rules = within(card).getByRole('group', { name: 'Custom rules' })
  // A fieldset is at least as wide as its content by default, so without
  // min-w-0 it grows to the rules table's width and the panel scrolls
  // sideways instead of the table's own scroll box.
  expect(rules.closest('fieldset')).toHaveClass('min-w-0')
  const fieldsets = card.querySelectorAll('fieldset')
  expect(fieldsets).toHaveLength(4)
  for (const fieldset of fieldsets) expect(fieldset).toHaveClass('min-w-0')
})

test('with moderation on, the character limit and the timeout share a row aligned to its top, so the timeout field is not spread over the height of the long character limit help', async () => {
  await openModerationCard(openAIChannel({ moderation_enabled: true }))
  const card = screen.getByRole('group', { name: 'Content moderation' })
  const maxChars = within(card).getByRole('spinbutton', {
    name: 'Maximum characters checked per request',
  })
  const timeout = within(card).getByRole('spinbutton', {
    name: 'Moderation timeout (seconds)',
  })
  const row = maxChars.closest('[data-slot="form-item"]')?.parentElement
  expect(row).toBe(timeout.closest('[data-slot="form-item"]')?.parentElement)
  // A grid stretches its items to the row height, and a stretched form item
  // (itself a grid) spreads its label, input and help over that height.
  expect(row).toHaveClass('items-start')
})

test('with moderation on, the custom rules help and the normalisation note are separate paragraphs, so no space is put after a Chinese full stop', async () => {
  await openModerationCard(openAIChannel({ moderation_enabled: true }))
  const rules = screen.getByRole('group', { name: 'Custom rules' })
  expect(
    within(rules).getByText(
      'Keywords match anywhere in the text. Regular expressions use RE2 syntax (no look-around or back-references), are checked when saved and should stay simple: each one runs on up to 256 KB of every request. Up to 100 rules, at most 30 regular expressions.'
    ).tagName
  ).toBe('P')
  expect(within(rules).getByText(/^Text is normalised first/).tagName).toBe('P')
})

test('a custom rule row centers its case-sensitive switch on the height of the row inputs', async () => {
  await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_rules: [{ type: 'keyword', pattern: 'cot' }],
    })
  )
  const caseSensitive = screen.getByRole('switch', {
    name: 'Case sensitive #1',
  })
  // The cells are top-aligned so row errors line up; the switch is shorter
  // than the h-8 inputs and sat above their middle.
  expect(caseSensitive.closest('[data-slot="form-item"]')).toHaveClass(
    'h-8',
    'content-center'
  )
})

test.each([3, 8, 57, 59, 60, 14])(
  'a channel of type %i, next to OpenAI, has no content moderation card',
  async (type) => {
    await openModerationCard({ ...editingChannel, type, settings: '{}' })
    expect(
      screen.queryByRole('group', { name: 'Content moderation' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('switch', { name: SAFETY_IDENTIFIER })
    ).not.toBeInTheDocument()
  }
)

test('saving an OpenAI channel without touching moderation writes no moderation setting and keeps unknown settings', async () => {
  const { user, requestTab } = await openModerationCard(
    openAIChannel({ future_option: { level: 2 } })
  )
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({})
  expect(settings).toMatchObject({ future_option: { level: 2 } })
})

test('turning moderation on for the first time selects exactly the lower-risk presets and marks Request & Response', async () => {
  const { user, requestTab } = await openModerationCard(openAIChannel({}))
  await user.click(screen.getByRole('switch', { name: MODERATION }))
  expect(requestTab).toHaveAccessibleName(/Configured/)
  expect(checkedPresetIds()).toEqual(MODERATION_PRESET_DEFAULT_IDS)
  expect(screen.getByRole('switch', { name: FAIL_OPEN })).not.toBeChecked()
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    moderation_enabled: true,
    moderation_rule_presets: MODERATION_PRESET_DEFAULT_IDS,
  })
})

test('a channel saved with presets keeps its selection when moderation is turned off and on', async () => {
  const { user } = await openModerationCard(
    openAIChannel({ moderation_rule_presets: ['en-think-tags'] })
  )
  const toggle = screen.getByRole('switch', { name: MODERATION })
  await user.click(toggle)
  expect(checkedPresetIds()).toEqual(['en-think-tags'])
  await user.click(toggle)
  await user.click(toggle)
  expect(checkedPresetIds()).toEqual(['en-think-tags'])
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    moderation_enabled: true,
    moderation_rule_presets: ['en-think-tags'],
  })
})

test('saving writes trimmed rules without empty names or false case flags, and categories in OpenAI order', async () => {
  const { user } = await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_fail_open: true,
      moderation_skip_images: true,
      moderation_categories: ['violence', 'hate'],
      moderation_rule_presets: [],
    })
  )
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  await user.type(screen.getByRole('textbox', { name: 'Name #1' }), '   ')
  await user.type(screen.getByRole('textbox', { name: 'Pattern #1' }), ' cot ')
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  await user.type(screen.getByRole('textbox', { name: 'Name #2' }), ' Leak ')
  await user.click(screen.getByRole('combobox', { name: 'Type #2' }))
  await user.click(screen.getByRole('option', { name: 'Regular expression' }))
  await user.click(screen.getByRole('textbox', { name: 'Pattern #2' }))
  await user.paste(String.raw`show\s+your\s+cot`)
  await user.click(screen.getByRole('switch', { name: 'Case sensitive #2' }))
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    moderation_enabled: true,
    moderation_fail_open: true,
    moderation_skip_images: true,
    moderation_categories: ['hate', 'violence'],
    moderation_rule_presets: [],
    moderation_rules: [
      { type: 'keyword', pattern: 'cot' },
      {
        name: 'Leak',
        type: 'regex',
        pattern: String.raw`show\s+your\s+cot`,
        case_sensitive: true,
      },
    ],
  })
})

test.each([
  ['10000', '10', undefined, undefined],
  ['5000', '30', 5000, 30],
  ['', '', undefined, undefined],
])(
  'a character limit of "%s" and a timeout of "%s" save as %s and %s',
  async (chars, timeout, savedChars, savedTimeout) => {
    const { user } = await openModerationCard(
      openAIChannel({
        moderation_enabled: true,
        moderation_max_chars: 20000,
        moderation_timeout_seconds: 20,
        moderation_rule_presets: [],
      })
    )
    const charsInput = screen.getByRole('spinbutton', { name: MAX_CHARS })
    expect(charsInput).toHaveValue(20000)
    await user.clear(charsInput)
    if (chars) await user.type(charsInput, chars)
    const timeoutInput = screen.getByRole('spinbutton', {
      name: MODERATION_TIMEOUT,
    })
    await user.clear(timeoutInput)
    if (timeout) await user.type(timeoutInput, timeout)
    const settings = await saveAndReadSettings(user)
    expect(settings.moderation_max_chars).toBe(savedChars)
    expect(settings.moderation_timeout_seconds).toBe(savedTimeout)
  }
)

test.each([
  [
    MAX_CHARS,
    '999',
    'Enter a whole number from 1,000 to 200,000, or leave it empty for 10,000',
  ],
  [
    MAX_CHARS,
    '1500.5',
    'Enter a whole number from 1,000 to 200,000, or leave it empty for 10,000',
  ],
  [
    MODERATION_TIMEOUT,
    '61',
    'Enter a whole number from 1 to 60, or leave it empty for 10',
  ],
])(
  '%s of %s is not saved and shows the allowed range',
  async (label, typed, message) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const { user } = await openModerationCard(
      openAIChannel({ moderation_enabled: true, moderation_rule_presets: [] })
    )
    const input = screen.getByRole('spinbutton', { name: label })
    await user.type(input, typed)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(await screen.findByText(message)).toBeVisible()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

test.each([
  ['Pattern #2', '   ', 'Enter a pattern'],
  ['Pattern #2', '‎️', 'The pattern must contain a visible character'],
  ['Pattern #2', 'x'.repeat(513), 'A pattern must be at most 512 characters'],
  ['Name #2', '名'.repeat(65), 'A name must be at most 64 characters'],
  [
    'Pattern #2',
    'CHAIN OF THOUGHT',
    'This pattern is already used by another rule',
  ],
])(
  'a rule whose %s is %j is not saved and shows why',
  async (cell, typed, message) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const { user } = await openModerationCard(
      openAIChannel({
        moderation_enabled: true,
        moderation_rule_presets: [],
        moderation_rules: [
          { type: 'keyword', pattern: 'chain of thought' },
          { type: 'keyword', pattern: 'ok' },
        ],
      })
    )
    const input = screen.getByRole('textbox', { name: cell })
    await user.clear(input)
    await user.click(input)
    await user.paste(typed)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(await screen.findByText(message)).toBeVisible()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  }
)

test.each([
  [101, 'keyword', 'At most 100 rules', true],
  [31, 'regex', 'At most 30 regular expression rules', false],
])(
  '%i saved %s rules are not saved again and the table says why',
  async (count, type, message, full) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const { user } = await openModerationCard(
      openAIChannel({
        moderation_enabled: true,
        moderation_rule_presets: [],
        moderation_rules: Array.from({ length: count }, (_, i) => ({
          type,
          pattern: `p${i}`,
        })),
      })
    )
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    expect(await screen.findByText(message)).toBeVisible()
    expect(screen.getByRole('button', { name: 'Add rule' })).toHaveProperty(
      'disabled',
      full
    )
    expect(put).not.toHaveBeenCalled()
  }
)

test('turning moderation off puts refused limits and new regular expressions back to their saved values, so the channel still saves', async () => {
  const savedRules = [{ type: 'regex', pattern: 'a+' }]
  const { user } = await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_max_chars: 5000,
      moderation_timeout_seconds: 20,
      moderation_rule_presets: [],
      moderation_rules: savedRules,
    })
  )
  const chars = screen.getByRole('spinbutton', { name: MAX_CHARS })
  await user.clear(chars)
  await user.type(chars, '999')
  const timeout = screen.getByRole('spinbutton', { name: MODERATION_TIMEOUT })
  await user.clear(timeout)
  await user.type(timeout, '99')
  // Valid for the console, but only the server can check RE2 syntax.
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  await user.click(screen.getByRole('combobox', { name: 'Type #2' }))
  await user.click(screen.getByRole('option', { name: 'Regular expression' }))
  await user.type(screen.getByRole('textbox', { name: 'Pattern #2' }), 'b+')

  await user.click(screen.getByRole('switch', { name: MODERATION }))
  expect(
    screen.queryByRole('spinbutton', { name: MAX_CHARS })
  ).not.toBeInTheDocument()
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    moderation_max_chars: 5000,
    moderation_timeout_seconds: 20,
    moderation_rule_presets: [],
    moderation_rules: savedRules,
  })
})

test.each([
  ['ChatGPT Subscription (Codex) Built-in #57'],
  ['Azure Built-in #3'],
])(
  'changing an OpenAI channel to %s saves its settings without any moderation or safety identifier setting',
  async (provider) => {
    const { user } = await openModerationCard({
      ...openAIChannel({
        moderation_enabled: true,
        moderation_fail_open: true,
        moderation_categories: ['hate'],
        moderation_skip_images: true,
        moderation_max_chars: 5000,
        moderation_timeout_seconds: 20,
        moderation_rule_presets: ['en-reveal-cot'],
        moderation_rules: [{ type: 'keyword', pattern: 'cot' }],
        send_safety_identifier: true,
        future_option: { level: 2 },
      }),
      // The API version an Azure channel requires.
      other: '2025-04-01-preview',
    })
    await user.click(screen.getByRole('button', { name: 'Change provider' }))
    await user.click(screen.getByRole('option', { name: provider }))
    const settings = await saveAndReadSettings(user)
    expect(moderationSettingsOf(settings)).toEqual({})
    expect(settings).toMatchObject({ future_option: { level: 2 } })
  }
)

test('without sensitive write permission the moderation settings are locked while the endpoint test still works with operate permission', async () => {
  operatorWith({ read: true, write: true, operate: true })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        ok: true,
        status: 200,
        latency_ms: 87,
        model: 'omni-moderation-2024-09-26',
        flagged: false,
        error_class: '',
        error_message: '',
      },
    },
  })
  const { user } = await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_rule_presets: ['en-reveal-cot'],
      moderation_rules: [{ type: 'keyword', pattern: 'cot' }],
    })
  )
  const toggle = screen.getByRole('switch', { name: MODERATION })
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await user.click(toggle)
  expect(toggle).toBeChecked()
  expect(screen.getByRole('switch', { name: FAIL_OPEN })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(
    screen.getByRole('switch', { name: SAFETY_IDENTIFIER })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('spinbutton', { name: MAX_CHARS })).toBeDisabled()
  expect(
    screen.getByRole('checkbox', { name: presetName(MODERATION_PRESETS[0]) })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('textbox', { name: 'Pattern #1' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Add rule' })).toBeDisabled()
  const showRule = screen.getByRole('button', {
    name: `Show rule ${presetName(MODERATION_PRESETS[0])}`,
  })
  await user.click(showRule)
  expect(showRule).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByText(MODERATION_PRESETS[0].pattern)).toBeVisible()

  await user.click(
    screen.getByRole('button', { name: 'Test moderation endpoint' })
  )
  expect(
    await screen.findByText('Moderation endpoint works (87 ms)')
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/42/moderation_test',
    undefined,
    expect.anything()
  )
})

test.each([
  [
    {
      error_class: 'http_401',
      status: 401,
      error_message: 'Incorrect API key provided: ***',
    },
    'Moderation endpoint failed: Key rejected (HTTP 401)',
  ],
  [
    { error_class: 'timeout', status: 0, error_message: '' },
    'Moderation endpoint failed: Timed out',
  ],
  [
    { error_class: 'http_499_new', status: 499, error_message: '' },
    'Moderation endpoint failed: http_499_new (HTTP 499)',
  ],
])('a failed endpoint test shows why: %j', async (failure, line) => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        ok: false,
        latency_ms: 120,
        model: '',
        flagged: false,
        ...failure,
      },
    },
  })
  const { user } = await openModerationCard(openAIChannel({}))
  await user.click(
    screen.getByRole('button', { name: 'Test moderation endpoint' })
  )
  const status = await screen.findByRole('status')
  await waitFor(() => expect(status).toHaveTextContent(line))
  if (failure.error_message) {
    expect(status).toHaveTextContent(failure.error_message)
  }
})

test('an endpoint test the server refuses shows its message and no result line', async () => {
  const error = vi.spyOn(toast, 'error')
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      message: 'The channel has no enabled key',
      message_key: 'The channel has no enabled key',
    },
  })
  const { user } = await openModerationCard(openAIChannel({}))
  await user.click(
    screen.getByRole('button', { name: 'Test moderation endpoint' })
  )
  await waitFor(() =>
    expect(error).toHaveBeenCalledWith('The channel has no enabled key')
  )
  expect(screen.getByRole('status')).toBeEmptyDOMElement()
})

test('a new OpenAI channel offers no endpoint test until it is saved', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: 'OpenAI Built-in #1' }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(screen.getByText(SAVE_TO_TEST)).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Test moderation endpoint' })
  ).not.toBeInTheDocument()
})

test('a saved Azure channel changed to OpenAI offers no endpoint test until it is saved as OpenAI', async () => {
  const { user } = await openModerationCard({
    ...editingChannel,
    type: 3,
    settings: '{}',
  })
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: 'OpenAI Built-in #1' }))
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(screen.getByText(SAVE_TO_TEST)).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Test moderation endpoint' })
  ).not.toBeInTheDocument()
})

test('without operate permission an OpenAI channel offers no endpoint test', async () => {
  operatorWith({ read: true, write: true, sensitive_write: true })
  await openModerationCard(openAIChannel({}))
  expect(screen.getByText(SAVE_TO_TEST)).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Test moderation endpoint' })
  ).not.toBeInTheDocument()
})

test('the safety identifier saves alone with moderation off, marks Request & Response, and overrides the passthrough switch', async () => {
  const { user, requestTab } = await openModerationCard(openAIChannel({}))
  const passthrough = screen.getByRole('switch', {
    name: 'Allow safety_identifier passthrough',
  })
  expect(passthrough).toHaveAccessibleDescription(
    'Pass through the safety_identifier field'
  )
  await user.click(screen.getByRole('switch', { name: SAFETY_IDENTIFIER }))
  expect(requestTab).toHaveAccessibleName(/Configured/)
  expect(passthrough).toHaveAccessibleDescription(
    'Ignored while “Send a user safety identifier to OpenAI” is on'
  )
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    send_safety_identifier: true,
  })
})

test('a regular expression JavaScript cannot compile is left to the server', async () => {
  const { user } = await openModerationCard(
    openAIChannel({ moderation_enabled: true, moderation_rule_presets: [] })
  )
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  await user.click(screen.getByRole('combobox', { name: 'Type #1' }))
  await user.click(screen.getByRole('option', { name: 'Regular expression' }))
  await user.click(screen.getByRole('textbox', { name: 'Pattern #1' }))
  await user.paste('(?=cot)[')
  const settings = await saveAndReadSettings(user)
  expect(settings.moderation_rules).toEqual([
    { type: 'regex', pattern: '(?=cot)[' },
  ])
})

test('a channel saved with an empty preset list selects none when moderation is turned off and on, and saves it empty', async () => {
  const { user } = await openModerationCard(
    openAIChannel({ moderation_enabled: true, moderation_rule_presets: [] })
  )
  const toggle = screen.getByRole('switch', { name: MODERATION })
  await user.click(toggle)
  await user.click(toggle)
  expect(checkedPresetIds()).toEqual([])
  const settings = await saveAndReadSettings(user)
  expect(settings.moderation_rule_presets).toEqual([])
})

test('presets cleared after the first enable are not selected again when moderation is turned off and on', async () => {
  const { user } = await openModerationCard(openAIChannel({}))
  const toggle = screen.getByRole('switch', { name: MODERATION })
  await user.click(toggle)
  for (const preset of MODERATION_PRESETS.filter((item) =>
    MODERATION_PRESET_DEFAULT_IDS.includes(item.id)
  )) {
    await user.click(screen.getByRole('checkbox', { name: presetName(preset) }))
  }
  await user.click(toggle)
  await user.click(toggle)
  expect(checkedPresetIds()).toEqual([])
  const settings = await saveAndReadSettings(user)
  expect(settings.moderation_rule_presets).toEqual([])
})

test('turning moderation on and off without changing the presets saves no preset list', async () => {
  const { user } = await openModerationCard(openAIChannel({}))
  const toggle = screen.getByRole('switch', { name: MODERATION })
  await user.click(toggle)
  await user.click(toggle)
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({})
})

test('clearing every saved preset saves an empty preset list', async () => {
  const { user } = await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_rule_presets: ['en-reveal-cot'],
    })
  )
  await user.click(
    screen.getByRole('checkbox', { name: presetName(MODERATION_PRESETS[0]) })
  )
  const settings = await saveAndReadSettings(user)
  expect(settings.moderation_rule_presets).toEqual([])
})

test('a rule the server refuses is marked on its row in Request & Response, even with moderation off', async () => {
  const error =
    'moderation_rules[1].pattern is not a valid regular expression: error parsing regexp: missing closing ]: `[`'
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: `Invalid channel settings format: ${error}`,
      message_key: 'Invalid channel settings format: {{error}}',
      message_params: { error },
    },
  })
  editingChannel = openAIChannel({
    moderation_rules: [
      { type: 'keyword', pattern: 'cot' },
      { type: 'regex', pattern: '[' },
    ],
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.queryByRole('textbox', { name: 'Pattern #2' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const pattern = await screen.findByRole('textbox', { name: 'Pattern #2' })
  expect(pattern).toHaveAttribute('aria-invalid', 'true')
  expect(
    screen.getByText(`The server rejected this rule: ${error}`)
  ).toBeVisible()
  expect(
    screen.getByRole('tab', { name: /Request & Response/ })
  ).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('switch', { name: MODERATION })).not.toBeChecked()
})

async function refuseSecondRuleWithModerationOff(
  error: string,
  field: { role: 'textbox' | 'combobox'; name: string }
) {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValueOnce({
      data: {
        success: false,
        message: `Invalid channel settings format: ${error}`,
        message_key: 'Invalid channel settings format: {{error}}',
        message_params: { error },
      },
    })
    .mockResolvedValue({ data: { success: true } })
  editingChannel = openAIChannel({
    moderation_rules: [
      { type: 'keyword', pattern: 'cot' },
      { type: 'regex', pattern: '[' },
    ],
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  const refused = await screen.findByRole(field.role, { name: field.name })
  await waitFor(() => expect(refused).toHaveFocus())
  return { user, put, refused }
}

function savedSettingsOf(
  put: { mock: { calls: unknown[][] } },
  call: number
): Record<string, unknown> {
  const payload = put.mock.calls[call]?.[1] as { settings: string }
  return JSON.parse(payload.settings)
}

test('a refused rule stays editable with moderation off while the admin types the fix, and the fix is saved', async () => {
  const {
    user,
    put,
    refused: pattern,
  } = await refuseSecondRuleWithModerationOff(
    'moderation_rules[1].pattern is not a valid regular expression: error parsing regexp: missing closing ]: `[`',
    { role: 'textbox', name: 'Pattern #2' }
  )
  for (const key of ['{Home}', '\\', '{End}', 'x', '{Backspace}']) {
    await user.keyboard(key)
    expect(screen.getByRole('textbox', { name: 'Pattern #2' })).toBe(pattern)
    expect(pattern).toHaveFocus()
  }
  expect(pattern).toHaveValue('\\[')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(savedSettingsOf(put, 1).moderation_rules).toEqual([
    { type: 'keyword', pattern: 'cot' },
    { type: 'regex', pattern: '\\[' },
  ])
  expect(savedSettingsOf(put, 1)).not.toHaveProperty('moderation_enabled')
})

test('a rule type the server refuses can be changed with moderation off', async () => {
  const { user, put, refused } = await refuseSecondRuleWithModerationOff(
    'moderation_rules[1].type must be keyword or regex',
    { role: 'combobox', name: 'Type #2' }
  )
  expect(refused).toHaveAttribute('aria-invalid', 'true')
  await user.click(refused)
  await user.click(screen.getByRole('option', { name: 'Keyword' }))
  expect(screen.getByRole('combobox', { name: 'Type #2' })).toHaveTextContent(
    'Keyword'
  )
  expect(screen.getByRole('textbox', { name: 'Pattern #2' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(savedSettingsOf(put, 1).moderation_rules).toEqual([
    { type: 'keyword', pattern: 'cot' },
    { type: 'keyword', pattern: '[' },
  ])
})

test('turning moderation off keeps every other setting the server accepts', async () => {
  const saved = {
    moderation_fail_open: true,
    moderation_categories: ['hate'],
    moderation_skip_images: true,
    moderation_max_chars: 5000,
    moderation_timeout_seconds: 20,
    moderation_rule_presets: ['en-reveal-cot'],
    moderation_rules: [{ name: 'CoT', type: 'keyword', pattern: 'cot' }],
  }
  const { user, requestTab } = await openModerationCard(
    openAIChannel({ moderation_enabled: true, ...saved })
  )
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('switch', { name: MODERATION }))
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual(saved)
})

test('settings this console does not accept are dropped on load and not saved back', async () => {
  const { user } = await openModerationCard({
    ...editingChannel,
    type: 1,
    settings:
      '{"moderation_rule_presets":["gone","en-reveal-cot"],"moderation_categories":["x"],"moderation_max_chars":"x","moderation_rules":{}}',
  })
  const settings = await saveAndReadSettings(user)
  expect(moderationSettingsOf(settings)).toEqual({
    moderation_rule_presets: ['en-reveal-cot'],
  })
})

test('the two presets of a pair have different names and each shows its rule on request', async () => {
  const { user } = await openModerationCard(
    openAIChannel({ moderation_enabled: true, moderation_rule_presets: [] })
  )
  expect(
    screen.getByRole('checkbox', {
      name: "Asks for the model's chain of thought (English)",
    })
  ).toBeInTheDocument()
  expect(
    screen.getByRole('checkbox', {
      name: "Asks for the model's chain of thought (Chinese)",
    })
  ).toBeInTheDocument()
  expect(screen.getAllByRole('button', { name: /^Show rule/ })).toHaveLength(
    MODERATION_PRESETS.length
  )
  for (const preset of MODERATION_PRESETS) {
    const item = screen
      .getByRole('checkbox', { name: presetName(preset) })
      .closest('li') as HTMLElement
    // Each trigger is named after its preset: exactly one button on the
    // card has this name, and it is the one in the preset's row.
    const trigger = screen.getByRole('button', {
      name: `Show rule ${presetName(preset)}`,
    })
    expect(item).toContainElement(trigger)
    expect(within(item).queryByText(preset.pattern)).not.toBeInTheDocument()
    await user.click(trigger)
    expect(within(item).getByText(preset.pattern)).toBeVisible()
  }
})

test('the custom rules table numbers its rows from 1', async () => {
  await openModerationCard(
    openAIChannel({
      moderation_enabled: true,
      moderation_rule_presets: [],
      moderation_rules: [
        { type: 'keyword', pattern: 'a' },
        { type: 'keyword', pattern: 'b' },
      ],
    })
  )
  const table = screen.getByRole('table')
  expect(within(table).getByRole('columnheader', { name: '#' })).toBeVisible()
  const rows = within(table).getAllByRole('row').slice(1)
  expect(rows.map((row) => within(row).getAllByRole('cell')[0])).toEqual([
    expect.objectContaining({ textContent: '1' }),
    expect.objectContaining({ textContent: '2' }),
  ])
})
