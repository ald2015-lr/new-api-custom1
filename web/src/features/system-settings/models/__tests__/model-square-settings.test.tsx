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
import { getOptionValue } from '../../hooks/use-system-options'
import type { ModelSettings } from '../../types'
import { ModelSquareSettingsCard } from '../model-square-settings-card'
import { getModelsSectionContent } from '../section-registry'

const GROUPING_SWITCH = { name: 'Group model variants' }
const VERSION_FOLD_SWITCH = { name: 'Merge version spellings' }
const SNAPSHOT_FOLD_SWITCH = { name: 'Merge dated snapshots' }
const SECTION_SWITCHES = [
  'Groups',
  'Vendors',
  'Model Tags',
  'Pricing Type',
  'Endpoint Type',
]
const SAVE = { name: 'Save Changes' }

type CardValues = Parameters<typeof ModelSquareSettingsCard>[0]['defaultValues']

function Fixture(props: {
  enabled: boolean
  rules: string
  bases?: string
  extra?: Partial<CardValues>
  /** Render the section through the registry with these settings instead. */
  settings?: ModelSettings
}) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        {props.settings ? (
          getModelsSectionContent('model-square', props.settings)
        ) : (
          <ModelSquareSettingsCard
            defaultValues={{
              variantGroupingEnabled: props.enabled,
              variantRules: props.rules,
              familyBases: props.bases,
              ...props.extra,
            }}
          />
        )}
      </SettingsPageProvider>
    </>
  )
}

let client: QueryClient

async function renderSettings(
  enabled = true,
  rules = JSON.stringify(DEFAULT_MODEL_VARIANT_RULES),
  bases = '[]',
  extra: Partial<CardValues> = {},
  settings?: ModelSettings
) {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <Fixture
          enabled={enabled}
          rules={rules}
          bases={bases}
          extra={extra}
          settings={settings}
        />
      ),
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

/** Whether each filter section switch is on, in sidebar order. */
function sectionStates() {
  return SECTION_SWITCHES.map(
    (name) =>
      screen.getByRole('switch', { name }).getAttribute('aria-checked') ===
      'true'
  )
}

/** The `options` of each request to the bulk Model Square endpoint. */
function bulkSaves() {
  return vi
    .mocked(api.put)
    .mock.calls.filter(([url]) => url === '/api/option/model_square')
    .map(([, body]) => (body as { options: Record<string, unknown> }).options)
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
    expect(api.put).toHaveBeenCalledWith('/api/option/model_square', {
      options: { 'model_square_setting.variant_grouping_enabled': false },
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
    expect(api.put).toHaveBeenCalledWith('/api/option/model_square', {
      options: {
        'model_square_setting.variant_rules': JSON.stringify(
          DEFAULT_MODEL_VARIANT_RULES
        ),
      },
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
    expect(api.put).toHaveBeenCalledWith('/api/option/model_square', {
      options: {
        'model_square_setting.variant_rules': JSON.stringify([
          { suffix: '-c', label: 'c', description: 'C' },
          { suffix: '-b', label: 'b', description: '' },
        ]),
      },
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

const BASES = { name: 'Merged base models' }

test('lists the stored base models one per line', async () => {
  await renderSettings(true, undefined, '["gemini-4-argon","gemini-4"]')
  expect(screen.getByRole('textbox', BASES)).toHaveValue(
    'gemini-4-argon\ngemini-4'
  )
})

test('saving base models stores them as a JSON list without blanks or repeats', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  await user.type(
    screen.getByRole('textbox', BASES),
    ' gemini-4-argon {enter}{enter}gemini-4{enter}gemini-4-argon'
  )
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/model_square', {
      options: {
        'model_square_setting.family_bases': '["gemini-4-argon","gemini-4"]',
      },
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('an invalid base model is named under the field and blocks saving', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  const bases = screen.getByRole('textbox', BASES)
  await user.type(bases, 'gemini-4-argon{enter}gemini 4')
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() => expect(bases).toHaveAttribute('aria-invalid', 'true'))
  expect(screen.getByText('gemini 4: must not contain spaces')).toBeVisible()
  expect(bases).toHaveAccessibleDescription(
    expect.stringContaining('gemini 4: must not contain spaces')
  )
  expect(api.put).not.toHaveBeenCalled()
})

/** The preview result as label -> value, each term with the value after it. */
function previewRows(preview: HTMLElement): Record<string, string> {
  const result = preview.parentElement?.querySelector('dl')
  if (!result) throw new Error('Preview result is missing')
  return Object.fromEntries(
    [...result.querySelectorAll('dt')].map((term) => [
      term.textContent ?? '',
      term.nextElementSibling?.textContent ?? '',
    ])
  )
}

test('the preview folds a name into a base model being edited', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  await user.type(screen.getByRole('textbox', BASES), 'gemini-4-argon')
  const preview = screen.getByRole('textbox', { name: 'Preview' })
  await user.clear(preview)
  await user.type(preview, 'gemini-4-argon-medium')
  expect(previewRows(preview)).toMatchObject({
    'Base model': 'gemini-4-argon',
    'Merged as': 'gemini-4-argon',
    Variant: 'medium',
  })
})

test('the preview shows the base model without its variant while both merge switches are off', async () => {
  const user = userEvent.setup()
  await renderSettings(
    true,
    JSON.stringify(DEFAULT_MODEL_VARIANT_RULES),
    JSON.stringify(['claude-haiku-4-5']),
    { versionSpellingFoldEnabled: false, snapshotFoldEnabled: false }
  )
  const preview = screen.getByRole('textbox', { name: 'Preview' })
  await user.clear(preview)
  await user.type(preview, 'claude-haiku-4-5-20251001')
  expect(previewRows(preview)).toMatchObject({
    'Base model': 'claude-haiku-4-5',
    'Merged as': 'claude-haiku-4-5',
    Variant: '20251001',
  })
})

test('a long invalid base model wraps in the error list instead of widening the form', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  const bases = screen.getByRole('textbox', BASES)
  // `[[` types a literal bracket in user-event.
  await user.type(bases, `[[按次]${'a_'.repeat(40)}`)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() => expect(bases).toHaveAttribute('aria-invalid', 'true'))
  const issues = screen
    .getByText(/leave out the leading \[\.\.\.\] line tag/)
    .closest('ul')
  expect(issues).toHaveClass('min-w-0', '[overflow-wrap:anywhere]')
})

test('both merge switches are on by default and a change saves only its key', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  const version = screen.getByRole('switch', VERSION_FOLD_SWITCH)
  const snapshot = screen.getByRole('switch', SNAPSHOT_FOLD_SWITCH)
  expect(version).toBeChecked()
  expect(snapshot).toBeChecked()
  await user.click(snapshot)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/model_square', {
      options: { 'model_square_setting.snapshot_fold_enabled': false },
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('the merge switches are disabled and keep their values while grouping is off', async () => {
  const user = userEvent.setup()
  const grouping = await renderSettings(false, undefined, undefined, {
    versionSpellingFoldEnabled: false,
  })
  const version = screen.getByRole('switch', VERSION_FOLD_SWITCH)
  const snapshot = screen.getByRole('switch', SNAPSHOT_FOLD_SWITCH)
  expect(version).toHaveAttribute('aria-disabled', 'true')
  expect(snapshot).toHaveAttribute('aria-disabled', 'true')
  expect(version).not.toBeChecked()
  expect(snapshot).toBeChecked()
  // The filter sections do not depend on grouping.
  for (const name of SECTION_SWITCHES) {
    expect(screen.getByRole('switch', { name })).not.toHaveAttribute(
      'aria-disabled',
      'true'
    )
  }
  await user.click(grouping)
  expect(version).not.toHaveAttribute('aria-disabled', 'true')
  expect(version).not.toBeChecked()
})

test('the filter section switches show the stored values before the base models', async () => {
  await renderSettings(true, undefined, undefined, {
    showGroupFilter: false,
    showVendorFilter: true,
    showTagFilter: true,
    showQuotaTypeFilter: false,
    showEndpointTypeFilter: false,
  })
  expect(sectionStates()).toEqual([false, true, true, false, false])
  const bases = screen.getByRole('textbox', BASES)
  for (const name of SECTION_SWITCHES) {
    expect(
      screen.getByRole('switch', { name }).compareDocumentPosition(bases) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
  }
  expect(
    screen
      .getByRole('switch', SNAPSHOT_FOLD_SWITCH)
      .compareDocumentPosition(screen.getByRole('switch', { name: 'Groups' })) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
})

test('stored option strings reach the switches through the section registry', async () => {
  const options = [
    { key: 'model_square_setting.variant_grouping_enabled', value: 'true' },
    {
      key: 'model_square_setting.version_spelling_fold_enabled',
      value: 'false',
    },
    { key: 'model_square_setting.snapshot_fold_enabled', value: 'true' },
    { key: 'model_square_setting.show_group_filter', value: 'false' },
    { key: 'model_square_setting.show_tag_filter', value: 'true' },
  ]
  const defaults = {
    'model_square_setting.variant_grouping_enabled': true,
    'model_square_setting.version_spelling_fold_enabled': true,
    'model_square_setting.snapshot_fold_enabled': true,
    'model_square_setting.variant_rules': '',
    'model_square_setting.family_bases': '[]',
    'model_square_setting.show_group_filter': true,
    'model_square_setting.show_vendor_filter': true,
    'model_square_setting.show_tag_filter': false,
    'model_square_setting.show_quota_type_filter': true,
    'model_square_setting.show_endpoint_type_filter': false,
  }
  await renderSettings(
    true,
    undefined,
    undefined,
    {},
    getOptionValue(options, defaults) as unknown as ModelSettings
  )
  expect(screen.getByRole('switch', VERSION_FOLD_SWITCH)).not.toBeChecked()
  expect(screen.getByRole('switch', SNAPSHOT_FOLD_SWITCH)).toBeChecked()
  expect(sectionStates()).toEqual([false, true, true, true, false])
})

test('the registry shows model tags and endpoint types off for options it does not know', async () => {
  const settings = {
    'model_square_setting.variant_grouping_enabled': true,
    'model_square_setting.variant_rules': '',
    'model_square_setting.family_bases': '[]',
  } as unknown as ModelSettings
  await renderSettings(true, undefined, undefined, {}, settings)
  expect(screen.getByRole('switch', VERSION_FOLD_SWITCH)).toBeChecked()
  expect(screen.getByRole('switch', SNAPSHOT_FOLD_SWITCH)).toBeChecked()
  expect(sectionStates()).toEqual([true, true, false, true, false])
})

test('one save sends every changed option in one request in a fixed order', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success')
  await renderSettings(true)
  // Changed in another order than they are saved in.
  await user.type(screen.getByRole('textbox', BASES), 'claude-opus-4-5')
  await user.click(screen.getByRole('switch', { name: 'Model Tags' }))
  await user.click(screen.getByRole('switch', { name: 'Vendors' }))
  await user.click(screen.getByRole('switch', VERSION_FOLD_SWITCH))
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() => expect(success).toHaveBeenCalledOnce())
  expect(api.put).toHaveBeenCalledTimes(1)
  expect(bulkSaves()).toEqual([
    {
      'model_square_setting.version_spelling_fold_enabled': false,
      'model_square_setting.show_vendor_filter': false,
      'model_square_setting.show_tag_filter': true,
      'model_square_setting.family_bases': '["claude-opus-4-5"]',
    },
  ])
  expect(Object.keys(bulkSaves()[0])).toEqual([
    'model_square_setting.version_spelling_fold_enabled',
    'model_square_setting.show_vendor_filter',
    'model_square_setting.show_tag_filter',
    'model_square_setting.family_bases',
  ])
  expect(success).toHaveBeenCalledWith('Setting updated successfully')
})

test('a refused save shows the error, keeps the form and still refetches the pricing', async () => {
  const user = userEvent.setup()
  vi.mocked(api.put).mockResolvedValue({
    data: {
      success: false,
      message: '是否显示供应商筛选必须是 true 或 false',
    },
  })
  const error = vi.spyOn(toast, 'error')
  const success = vi.spyOn(toast, 'success')
  await renderSettings(true)
  client.setQueryData(['pricing'], { success: true, data: [] })
  const vendors = screen.getByRole('switch', { name: 'Vendors' })
  await user.click(vendors)
  await user.click(screen.getByRole('button', SAVE))
  await waitFor(() =>
    expect(error).toHaveBeenCalledWith('是否显示供应商筛选必须是 true 或 false')
  )
  await waitFor(() =>
    expect(client.getQueryState(['pricing'])?.isInvalidated).toBe(true)
  )
  expect(success).not.toHaveBeenCalled()
  expect(vendors).not.toBeChecked()
})

test('the preview shows the spelling as typed and the card it is merged into', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  const preview = screen.getByRole('textbox', { name: 'Preview' })
  await user.clear(preview)
  await user.type(preview, 'claude-opus-4-5-thinking')
  // The result list is rendered anew whenever the input is cleared.
  const value = (label: string) => {
    const result = preview.parentElement?.querySelector('dl')
    if (!result) throw new Error('Preview result is missing')
    return within(result).getByText(label).nextElementSibling?.textContent
  }
  expect(value('Base model')).toBe('claude-opus-4-5')
  expect(value('Merged as')).toBe('claude-opus-4.5')
  expect(value('Variant')).toBe('thinking')
  await user.click(screen.getByRole('switch', VERSION_FOLD_SWITCH))
  expect(value('Merged as')).toBe('claude-opus-4-5')
  // Without the catalogue a dated name still shows its snapshot.
  await user.clear(preview)
  await user.type(preview, 'gpt-4o-mini-2024-07-18')
  expect(value('Base model')).toBe('gpt-4o-mini')
  expect(value('Variant')).toBe('2024-07-18')
  await user.click(screen.getByRole('switch', SNAPSHOT_FOLD_SWITCH))
  expect(value('Base model')).toBe('gpt-4o-mini-2024-07-18')
  expect(value('Variant')).toBe('Standard')
})
