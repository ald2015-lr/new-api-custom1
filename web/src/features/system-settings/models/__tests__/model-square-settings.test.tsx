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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { DEFAULT_MODEL_VARIANT_RULES } from '@/features/pricing/lib/model-variants'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { ModelSquareSettingsCard } from '../model-square-settings-card'

const GROUPING_SWITCH = { name: 'Group model variants' }
const SAVE = { name: 'Save Changes' }

function Fixture(props: { enabled: boolean; rules: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <ModelSquareSettingsCard
          defaultValues={{
            variantGroupingEnabled: props.enabled,
            variantRules: props.rules,
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

let client: QueryClient

async function renderSettings(
  enabled = true,
  rules = JSON.stringify(DEFAULT_MODEL_VARIANT_RULES)
) {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Fixture enabled={enabled} rules={rules} />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('switch', GROUPING_SWITCH)
}

function suffixInput(index: number) {
  return screen.getByRole('textbox', { name: `Suffix #${index + 1}` })
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

afterEach(() => {
  vi.restoreAllMocks()
  client?.clear()
})

test('turning grouping off saves only the grouping switch', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings(true)
  expect(toggle).toBeChecked()
  await user.click(toggle)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'model_square_setting.variant_grouping_enabled',
      value: false,
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('saving marks the cached Model Square pricing stale so the square refetches it', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings(true)
  client.setQueryData(['pricing'], { success: true, data: [] })
  expect(client.getQueryState(['pricing'])?.isInvalidated).toBe(false)
  await user.click(toggle)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(client.getQueryState(['pricing'])?.isInvalidated).toBe(true)
  )
})

test('a newly added rule row is empty and shows no example values', async () => {
  const user = userEvent.setup()
  await renderSettings(true, '[{"suffix":"-thinking","label":"thinking"}]')
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  const suffix = suffixInput(1)
  const label = screen.getByRole('textbox', { name: 'Label #2' })
  expect(suffix).toHaveValue('')
  expect(label).toHaveValue('')
  expect(suffix).not.toHaveAttribute('placeholder')
  expect(label).not.toHaveAttribute('placeholder')
})

test('lists the stored rules in order and falls back to the defaults for an empty value', async () => {
  await renderSettings(true, '')
  expect(suffixInput(0)).toHaveValue('-thinking')
  expect(suffixInput(8)).toHaveValue('-128')
  expect(screen.getAllByRole('textbox', { name: /^Suffix #/ })).toHaveLength(
    DEFAULT_MODEL_VARIANT_RULES.length
  )
})

test.each([
  ['-thinking', 'Duplicate suffix'],
  ['-a b', 'Suffix must not contain spaces'],
  ['', 'Suffix is required'],
])(
  'suffix "%s" marks the field invalid and blocks saving',
  async (value, message) => {
    const user = userEvent.setup()
    await renderSettings(true, '[{"suffix":"-thinking","label":"thinking"}]')
    await user.click(screen.getByRole('button', { name: 'Add rule' }))
    const input = suffixInput(1)
    expect(input).toHaveFocus()
    if (value) await user.type(input, value)
    await user.type(screen.getByRole('textbox', { name: 'Label #2' }), 'label')
    await user.click(screen.getByRole('button', SAVE))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.getByText(message)).toBeVisible()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('a label longer than 64 characters shows the limit and blocks saving', async () => {
  const user = userEvent.setup()
  await renderSettings(true, '[{"suffix":"-x","label":"x"}]')
  const label = screen.getByRole('textbox', { name: 'Label #1' })
  await user.clear(label)
  await user.type(label, 'a'.repeat(65))
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() => expect(label).toHaveAttribute('aria-invalid', 'true'))
  expect(screen.getByText('Must be 64 characters or fewer')).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
})

test('restoring the defaults replaces custom rules and saves them as JSON', async () => {
  const user = userEvent.setup()
  await renderSettings(true, '[{"suffix":"-fast","label":"fast"}]')
  expect(suffixInput(0)).toHaveValue('-fast')
  await user.click(screen.getByRole('button', { name: 'Restore defaults' }))
  expect(suffixInput(0)).toHaveValue('-thinking')
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'model_square_setting.variant_rules',
      value: JSON.stringify(DEFAULT_MODEL_VARIANT_RULES),
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('moving and removing rules saves the new order', async () => {
  const user = userEvent.setup()
  await renderSettings(
    true,
    '[{"suffix":"-a","label":"a","description":""},{"suffix":"-b","label":"b","description":""},{"suffix":"-c","label":"c","description":"C"}]'
  )
  expect(screen.getByRole('button', { name: 'Move -a up' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Move -c up' }))
  await user.click(screen.getByRole('button', { name: 'Remove -a' }))
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'model_square_setting.variant_rules',
      value: JSON.stringify([
        { suffix: '-c', label: 'c', description: 'C' },
        { suffix: '-b', label: 'b', description: '' },
      ]),
    })
  )
})

test('the preview splits a model name with the rules being edited', async () => {
  const user = userEvent.setup()
  await renderSettings(true, '[{"suffix":"-max","label":"max"}]')
  const preview = screen.getByRole('textbox', { name: 'Preview' })
  await user.clear(preview)
  // `[[` types a literal bracket in user-event.
  await user.type(preview, '[[按次]claude-opus-4-6-max')
  const result = preview.parentElement?.querySelector('dl')
  if (!result) throw new Error('Preview result is missing')
  expect(within(result).getByText('claude-opus-4-6')).toBeVisible()
  expect(within(result).getByText('按次')).toBeVisible()
  expect(within(result).getByText('max')).toBeVisible()
})

test('saving without changes reports that nothing changed', async () => {
  const user = userEvent.setup()
  const info = vi.spyOn(toast, 'info')
  await renderSettings(false)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() => expect(info).toHaveBeenCalledWith('No changes to save'))
  expect(api.put).not.toHaveBeenCalled()
})
