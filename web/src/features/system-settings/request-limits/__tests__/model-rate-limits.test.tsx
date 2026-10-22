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
import { useState } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
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

async function renderSection(modelRules = '{}') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Fixture modelRules={modelRules} />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('group', { name: 'Model-based rate limits' })
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
  expect(api.put).not.toHaveBeenCalled()
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
