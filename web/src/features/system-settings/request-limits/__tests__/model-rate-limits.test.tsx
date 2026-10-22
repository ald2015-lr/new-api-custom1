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
  RouterProvider,
} from '@tanstack/react-router'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useMemo, useState, type ReactNode } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import {
  getOptionValue,
  useSystemOptions,
} from '../../hooks/use-system-options'
import { getSecuritySectionContent } from '../../security/section-registry'
import type { SecuritySettings } from '../../types'
import { RateLimitSection } from '../rate-limit-section'

const storedRules = JSON.stringify({
  'gpt-4o': { duration_minutes: 1, total_count: 60, success_count: 0 },
})

function Fixture(props: { modelRules: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RateLimitSection
          defaultValues={{
            ModelRequestRateLimitEnabled: true,
            ModelRequestRateLimitDurationMinutes: 1,
            ModelRequestRateLimitCount: 0,
            ModelRequestRateLimitSuccessCount: 1000,
            ModelRequestRateLimitGroup: '{}',
            ModelRequestRateLimitModel: props.modelRules,
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

const securityDefaults: SecuritySettings = {
  ModelRequestRateLimitEnabled: false,
  ModelRequestRateLimitCount: 0,
  ModelRequestRateLimitSuccessCount: 1000,
  ModelRequestRateLimitDurationMinutes: 1,
  ModelRequestRateLimitGroup: '',
  ModelRequestRateLimitModel: '{}',
  CheckSensitiveEnabled: false,
  CheckSensitiveOnPromptEnabled: false,
  SensitiveWords: '',
  'fetch_setting.enable_ssrf_protection': true,
  'fetch_setting.allow_private_ip': false,
  'fetch_setting.domain_filter_mode': false,
  'fetch_setting.ip_filter_mode': false,
  'fetch_setting.domain_list': [],
  'fetch_setting.ip_list': [],
  'fetch_setting.allowed_ports': [],
  'fetch_setting.apply_ip_filter_for_domain': false,
  'token_setting.max_user_tokens': 1000,
}

// The section as the settings page shows it: built from the options query,
// which every saved option refetches.
function LivePage() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const { data } = useSystemOptions()
  const settings = useMemo(
    () => getOptionValue(data?.data, securityDefaults),
    [data?.data]
  )
  if (!data) return null
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        {getSecuritySectionContent('rate-limit', settings)}
      </SettingsPageProvider>
    </>
  )
}

/** Serves options from a store that api.put writes to, like the server. */
function serveOptions(options: Record<string, string>) {
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: {
      success: true,
      data: Object.entries(options).map(([key, value]) => ({ key, value })),
    },
  }))
  vi.mocked(api.put).mockImplementation(async (_url, body) => {
    const option = body as { key: string; value: unknown }
    options[option.key] = String(option.value)
    return { data: { success: true } }
  })
}

function renderRoute(component: () => ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('group', { name: 'Model-based rate limits' })
}

async function renderSection(modelRules = '{}') {
  return renderRoute(() => <Fixture modelRules={modelRules} />)
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

afterEach(() => {
  vi.restoreAllMocks()
})

test('adding a model rule saves only the model rate limit option', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection()

  await user.click(
    within(modelRules).getByRole('button', { name: 'Add model' })
  )
  const dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Model Name' }),
    // "[[" types a literal "[" in user-event.
    '[[按次]claude-opus-4-6'
  )
  const windowInput = within(dialog).getByRole('spinbutton', {
    name: 'Time window (minutes)',
  })
  await user.clear(windowInput)
  await user.type(windowInput, '5')
  await user.type(
    within(dialog).getByRole('spinbutton', {
      name: 'Max Requests (including failures)',
    }),
    '20'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Save rate limits' }))

  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  const [path, body] = vi.mocked(api.put).mock.calls[0] as [
    string,
    { key: string; value: string },
  ]
  expect(path).toBe('/api/option/')
  expect(body.key).toBe('ModelRequestRateLimitModel')
  expect(JSON.parse(body.value)).toEqual({
    '[按次]claude-opus-4-6': {
      duration_minutes: 5,
      total_count: 20,
      success_count: 0,
    },
  })
})

test('a time window above one day keeps the add dialog open with an error', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection()

  await user.click(
    within(modelRules).getByRole('button', { name: 'Add model' })
  )
  const dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Model Name' }),
    'gpt-4o'
  )
  const windowInput = within(dialog).getByRole('spinbutton', {
    name: 'Time window (minutes)',
  })
  await user.clear(windowInput)
  await user.type(windowInput, '1441')
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))

  expect(
    await within(dialog).findByText('Must be from 1 to 1,440')
  ).toBeVisible()
  expect(windowInput).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByRole('dialog')).toBeInTheDocument()
})

test('invalid rules in JSON mode show the backend message and block saving', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection()

  await user.click(
    within(modelRules).getByRole('button', { name: 'JSON Mode' })
  )
  fireEvent.input(
    within(modelRules).getByRole('textbox', {
      name: 'Model-based rate limits',
    }),
    {
      target: {
        value: '{"gpt-4o": {"duration_minutes": 0, "total_count": 5}}',
      },
    }
  )
  await user.click(screen.getByRole('button', { name: 'Save rate limits' }))

  expect(
    await within(modelRules).findByText(
      'The time window of the rate limit for model gpt-4o must be 1 to 1440 minutes'
    )
  ).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
})

test('rules edited in JSON mode show in the visual table after switching back', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(storedRules)

  await user.click(
    within(modelRules).getByRole('button', { name: 'JSON Mode' })
  )
  const textarea = within(modelRules).getByRole('textbox', {
    name: 'Model-based rate limits',
  })
  expect(JSON.parse((textarea as HTMLTextAreaElement).value)).toEqual(
    JSON.parse(storedRules)
  )
  fireEvent.input(textarea, {
    target: {
      value:
        '{"gpt-4o": {"duration_minutes": 3, "total_count": 0, "success_count": 15}}',
    },
  })
  await user.click(
    within(modelRules).getByRole('button', { name: 'Visual Mode' })
  )

  const row = within(modelRules).getByRole('row', { name: /gpt-4o/ })
  expect(
    within(row)
      .getAllByRole('cell')
      .slice(0, 4)
      .map((cell) => cell.textContent)
  ).toEqual(['gpt-4o', '3', 'Unlimited', '15'])
})

function getSavedOption(key: string): unknown {
  const call = vi
    .mocked(api.put)
    .mock.calls.find(
      ([, body]) => (body as { key: string } | undefined)?.key === key
    )
  if (!call) return undefined
  return JSON.parse((call[1] as { value: string }).value)
}

async function switchToJsonMode(modelRules: HTMLElement) {
  await userEvent.click(
    within(modelRules).getByRole('button', { name: 'JSON Mode' })
  )
  return within(modelRules).getByRole('textbox', {
    name: 'Model-based rate limits',
  })
}

test('adding a rule in the dialog does not save the other changed fields', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection()
  // The label points at a wrapper, not at the input.
  const countInput = screen
    .getByText('Max requests per period')
    .closest('[data-slot=form-item]')
    ?.querySelector('input') as HTMLInputElement
  await user.clear(countInput)
  await user.type(countInput, '9')

  await user.click(
    within(modelRules).getByRole('button', { name: 'Add model' })
  )
  const dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Model Name' }),
    'gpt-4o'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )

  expect(api.put).not.toHaveBeenCalled()
})

test('editing a rule saves its new values', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(storedRules)

  const row = within(modelRules).getByRole('row', { name: /gpt-4o/ })
  await user.click(within(row).getByRole('button', { name: 'Edit' }))
  const dialog = await screen.findByRole('dialog')
  const totalInput = within(dialog).getByRole('spinbutton', {
    name: 'Max Requests (including failures)',
  })
  await user.clear(totalInput)
  await user.type(totalInput, '30')
  await user.click(within(dialog).getByRole('button', { name: 'Update' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Save rate limits' }))

  await waitFor(() =>
    expect(getSavedOption('ModelRequestRateLimitModel')).toEqual({
      'gpt-4o': { duration_minutes: 1, total_count: 30, success_count: 0 },
    })
  )
})

test('deleting a rule saves the rules without it', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(
    JSON.stringify({
      'gpt-4o': { duration_minutes: 1, total_count: 60, success_count: 0 },
      o3: { duration_minutes: 5, total_count: 0, success_count: 10 },
    })
  )

  const row = within(modelRules).getByRole('row', { name: /gpt-4o/ })
  await user.click(within(row).getByRole('button', { name: 'Open menu' }))
  await user.click(await screen.findByRole('menuitem', { name: 'Delete' }))
  await user.click(screen.getByRole('button', { name: 'Save rate limits' }))

  await waitFor(() =>
    expect(getSavedOption('ModelRequestRateLimitModel')).toEqual({
      o3: { duration_minutes: 5, total_count: 0, success_count: 10 },
    })
  )
})

test('adding a model that already has a rule keeps the dialog open with an error', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(storedRules)

  await user.click(
    within(modelRules).getByRole('button', { name: 'Add model' })
  )
  const dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Model Name' }),
    'gpt-4o'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))

  expect(
    await within(dialog).findByText('This model already has a rate limit')
  ).toBeVisible()
  expect(screen.getByRole('dialog')).toBeInTheDocument()
})

test('the group and model rate limit tables each span the full settings grid', async () => {
  const modelRules = await renderSection()

  const groupRules = screen
    .getByText('Group-based rate limits')
    .closest('[data-slot=form-item]')
  expect(groupRules).toHaveAttribute('data-settings-form-span', 'full')
  expect(modelRules).toHaveAttribute('data-settings-form-span', 'full')
})

test('JSON mode describes the editor with the format hint under one unique id', async () => {
  const modelRules = await renderSection()

  const textarea = await switchToJsonMode(modelRules)
  const ids = (textarea.getAttribute('aria-describedby') ?? '').split(' ')
  const descriptions = ids.map((id) =>
    document.querySelectorAll(`[id="${id}"]`)
  )
  expect(descriptions.map((matches) => matches.length)).toEqual(
    ids.map(() => 1)
  )
  expect(
    descriptions.map((matches) => matches[0].textContent).join(' ')
  ).toContain(
    'duration_minutes 1 to 1,440; total_count and success_count 0 to 100,000,000, 0 = unlimited'
  )
})

test('rules the visual table cannot read show a notice and cannot be replaced by adding a model', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(storedRules)

  const textarea = await switchToJsonMode(modelRules)
  fireEvent.input(textarea, {
    target: {
      value:
        '{"gpt-4o": {"duration_minutes": 1, "total_count": 60}, "o3": {"duration_minutes": 5},}',
    },
  })
  await user.click(
    within(modelRules).getByRole('button', { name: 'Visual Mode' })
  )

  expect(
    within(modelRules).getByText(
      'These rules cannot be shown in visual mode. Fix them in JSON mode.'
    )
  ).toBeVisible()
  expect(
    within(modelRules).getByRole('button', { name: 'Add model' })
  ).toBeDisabled()
})

test('adding a group after a model rule saves neither until Save, then both', async () => {
  const user = userEvent.setup()
  const options: Record<string, string> = {
    ModelRequestRateLimitEnabled: 'true',
    ModelRequestRateLimitCount: '0',
    ModelRequestRateLimitSuccessCount: '1000',
    ModelRequestRateLimitDurationMinutes: '1',
    ModelRequestRateLimitGroup: '{}',
    ModelRequestRateLimitModel: '{}',
  }
  serveOptions(options)
  const modelRules = await renderRoute(LivePage)

  await user.click(
    within(modelRules).getByRole('button', { name: 'Add model' })
  )
  let dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Model Name' }),
    'gpt-4o'
  )
  await user.type(
    within(dialog).getByRole('spinbutton', {
      name: 'Max Requests (including failures)',
    }),
    '20'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )

  await user.click(screen.getByRole('button', { name: 'Add group' }))
  dialog = await screen.findByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Group Name' }),
    'vip'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )

  expect(api.put).not.toHaveBeenCalled()
  expect(screen.getByRole('row', { name: /vip/ })).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Save rate limits' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(2))
  expect(JSON.parse(options.ModelRequestRateLimitGroup)).toEqual({
    vip: [0, 1],
  })
  expect(JSON.parse(options.ModelRequestRateLimitModel)).toEqual({
    'gpt-4o': { duration_minutes: 1, total_count: 20, success_count: 0 },
  })
})

test('editing a rule whose name has surrounding spaces changes only that rule', async () => {
  const user = userEvent.setup()
  const modelRules = await renderSection(
    JSON.stringify({
      'gpt-4o': { duration_minutes: 1, total_count: 60, success_count: 0 },
      'gpt-4o ': { duration_minutes: 5, total_count: 3, success_count: 0 },
    })
  )

  const row = within(modelRules)
    .getAllByRole('row')
    .find(
      (candidate) =>
        within(candidate).queryAllByRole('cell')[0]?.textContent === 'gpt-4o '
    ) as HTMLElement
  await user.click(within(row).getByRole('button', { name: 'Edit' }))
  const dialog = await screen.findByRole('dialog')
  const totalInput = within(dialog).getByRole('spinbutton', {
    name: 'Max Requests (including failures)',
  })
  await user.clear(totalInput)
  await user.type(totalInput, '7')
  await user.click(within(dialog).getByRole('button', { name: 'Update' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )

  const textarea = await switchToJsonMode(modelRules)
  expect(JSON.parse((textarea as HTMLTextAreaElement).value)).toEqual({
    'gpt-4o': { duration_minutes: 1, total_count: 60, success_count: 0 },
    'gpt-4o ': { duration_minutes: 5, total_count: 7, success_count: 0 },
  })
})

/** The element of the form item that holds the field's table. */
function getTableEditor(formItem: HTMLElement): HTMLElement {
  let editor = within(formItem).getByRole('table') as HTMLElement
  while (editor.parentElement !== formItem) {
    editor = editor.parentElement as HTMLElement
  }
  return editor
}

test('the rate limit tables scroll inside their fields instead of widening them', async () => {
  const modelRules = await renderSection(storedRules)

  // A grid item's min-content width otherwise follows its nowrap table.
  const groupRules = screen
    .getByText('Group-based rate limits')
    .closest('[data-slot=form-item]') as HTMLElement
  expect(getTableEditor(groupRules)).toHaveClass('min-w-0')
  expect(getTableEditor(modelRules)).toHaveClass('min-w-0')
})
