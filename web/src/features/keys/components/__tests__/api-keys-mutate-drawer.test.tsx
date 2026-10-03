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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { ApiKey } from '../../types'

const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { ApiKeysProvider } = await import('../api-keys-provider')
const { ApiKeysMutateDrawer } = await import('../api-keys-mutate-drawer')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = {
  get: ApiMethod
  post: ApiMethod
  put: ApiMethod
}
type RenderedDrawer = {
  queryClient: InstanceType<typeof QueryClient>
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post
const originalPut = apiClient.put
let renderedDrawer: RenderedDrawer | null = null

function installApiFixtures(createdPayloads: Array<Record<string, unknown>>) {
  apiClient.get = async (url) => {
    switch (url) {
      case '/api/status':
        return { data: { data: { default_use_auto_group: true } } }
      case '/api/user/models':
        return { data: { success: true, data: [] } }
      case '/api/user/self/groups':
        return {
          data: {
            success: true,
            data: {
              auto: { desc: 'Automatic routing', ratio: 'auto' },
              default: { desc: 'Standard access', ratio: 1 },
              vip: { desc: 'Priority access', ratio: 2 },
            },
          },
        }
      case '/api/token/auto-groups':
        return {
          data: {
            success: true,
            data: { groups: ['vip', 'default'], max_count: 3 },
          },
        }
      default:
        throw new Error(`Unexpected GET ${url}`)
    }
  }
  apiClient.post = async (url, data) => {
    expect(url).toBe('/api/token/')
    expect(data && typeof data === 'object').toBeTruthy()
    createdPayloads.push(data as Record<string, unknown>)
    return { data: { success: true, data: {} } }
  }
}

function installUpdateFixture(
  updatedPayloads: Array<Record<string, unknown>>
): void {
  apiClient.put = async (url, data) => {
    expect(url).toBe('/api/token/')
    updatedPayloads.push(data as Record<string, unknown>)
    return { data: { success: true, data: {} } }
  }
}

function buildExistingKey(overrides: Partial<ApiKey>): ApiKey {
  return {
    id: 7,
    name: 'legacy',
    key: 'sk-legacy',
    status: 1,
    remain_quota: 0,
    used_quota: 0,
    unlimited_quota: true,
    expired_time: -1,
    created_time: 0,
    accessed_time: 0,
    group: 'default',
    auto_groups: null,
    cross_group_retry: false,
    model_limits_enabled: false,
    model_limits: '',
    allow_ips: '',
    ...overrides,
  }
}

const DEFAULT_USER_GROUPS = {
  success: true,
  data: {
    auto: { desc: 'Automatic routing', ratio: 'auto' },
    default: { desc: 'Standard access', ratio: 1 },
    vip: { desc: 'Priority access', ratio: 2 },
  },
}

const USER_GROUPS_WITH_LOCKED = {
  ...DEFAULT_USER_GROUPS,
  locked: {
    svip: {
      desc: 'Gated access',
      ratio: 0.8,
      min_topup: 50,
      current_topup: 20,
      whitelist_only: false,
    },
  },
}

async function renderDrawer(
  options: {
    userGroups?: Record<string, unknown>
    currentRow?: ApiKey
  } = {}
): Promise<void> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(
    ['status'],
    { default_use_auto_group: true },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['user-models'],
    { success: true, data: [] },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['user-groups'],
    options.userGroups ?? DEFAULT_USER_GROUPS,
    { updatedAt: freshAt }
  )
  if (options.currentRow) {
    queryClient.setQueryData(
      ['api-key', options.currentRow.id],
      { success: true, data: options.currentRow },
      { updatedAt: freshAt }
    )
  }
  queryClient.setQueryData(
    ['token-auto-groups'],
    {
      success: true,
      data: { groups: ['vip', 'default'], max_count: 3 },
    },
    { updatedAt: freshAt }
  )
  renderedDrawer = { queryClient }

  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ApiKeysProvider>
          <ApiKeysMutateDrawer
            open
            onOpenChange={() => undefined}
            currentRow={options.currentRow}
          />
        </ApiKeysProvider>
      </I18nextProvider>
    </QueryClientProvider>
  )
  await waitFor(
    () => {
      const saveButton = findButton('Save changes', false)
      expect(saveButton).toBeEnabled()
    },
    { timeout: 1500 }
  )
}

function findButton(text: string, required: true): HTMLButtonElement
function findButton(text: string, required: false): HTMLButtonElement | null
function findButton(text: string, required = true): HTMLButtonElement | null {
  const button = screen
    .queryAllByRole<HTMLButtonElement>('button')
    .find((candidate) => candidate.textContent?.includes(text))
  if (required && !button) {
    throw new Error(`Expected button containing "${text}"`)
  }
  return button ?? null
}

function getControlByLabel(labelText: 'Name' | 'Quantity'): HTMLInputElement
function getControlByLabel(labelText: 'Group'): HTMLButtonElement
function getControlByLabel(labelText: 'Auto group order'): HTMLElement
function getControlByLabel(labelText: string): HTMLElement {
  const label = [...document.querySelectorAll<HTMLLabelElement>('label')].find(
    (candidate) => candidate.textContent?.trim() === labelText
  )
  if (!label) {
    throw new Error(`Expected label "${labelText}"`)
  }

  const control =
    label.control ??
    label
      .closest('[data-slot="form-item"]')
      ?.querySelector<HTMLElement>(
        '[data-slot="form-control"], input, textarea, button[role="combobox"], [role="group"]'
      )
  if (!control) {
    throw new Error(`Expected control for label "${labelText}"`)
  }
  return control
}

function changeInput(input: HTMLInputElement, value: string): void {
  fireEvent.input(input, { target: { value } })
}

function selectComboboxOption(
  trigger: HTMLButtonElement,
  optionDescription: string
): void {
  fireEvent.click(trigger)
  const option = [
    ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
  ].find((candidate) => candidate.textContent?.includes(optionDescription))
  if (!option) {
    throw new Error(`Expected option containing "${optionDescription}"`)
  }
  fireEvent.click(option)
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  apiClient.put = originalPut
  localStorage.clear()
  if (renderedDrawer) {
    renderedDrawer.queryClient.clear()
    renderedDrawer = null
  }
})

describe('API keys mutate drawer Auto group integration', () => {
  test('inherits the root Auto order and sends an empty override for every batch-created key', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderDrawer()

    const groupTrigger = getControlByLabel('Group')
    expect(groupTrigger.textContent?.includes('auto')).toBe(true)
    expect(
      document.body.textContent?.includes(
        'Using the complete global Auto order (2 groups)'
      )
    ).toBe(true)
    expect(
      [
        ...document.querySelectorAll('[data-slot="global-auto-order-name"]'),
      ].map((item) => item.textContent)
    ).toEqual(['vip', 'default'])
    expect(findButton('Restore global Auto', true).disabled).toBe(true)

    changeInput(getControlByLabel('Name'), 'batch')
    changeInput(getControlByLabel('Quantity'), '2')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(2))

    expect(createdPayloads.length).toBe(2)
    expect(createdPayloads[0]?.name).toBe('batch')
    for (const payload of createdPayloads) {
      expect(payload.group).toBe('auto')
      expect(payload.auto_groups).toEqual([])
      expect(payload.cross_group_retry).toBe(true)
    }
  })

  test('preserves an unsaved custom order and mode after Auto to ordinary to Auto changes', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderDrawer()

    const autoOrderControl = getControlByLabel('Auto group order')
    const addGroupTrigger = autoOrderControl.querySelector<HTMLButtonElement>(
      'button[role="combobox"]'
    )
    if (!addGroupTrigger) {
      throw new Error('Expected Auto group order combobox')
    }
    selectComboboxOption(addGroupTrigger, 'Priority access')

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Restore global Auto', true).disabled).toBe(false)

    const groupTrigger = getControlByLabel('Group')
    selectComboboxOption(groupTrigger, 'Standard access')
    expect(document.querySelector('button[aria-label="Remove vip"]')).toBe(null)
    selectComboboxOption(groupTrigger, 'Automatic routing')

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Restore global Auto', true).disabled).toBe(false)

    changeInput(getControlByLabel('Name'), 'custom')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]?.auto_groups).toEqual(['vip'])
  })
})

describe('API keys mutate drawer recharge-gated groups', () => {
  test('leaves a locked group out of the Auto group order candidates', async () => {
    installApiFixtures([])
    await renderDrawer({ userGroups: USER_GROUPS_WITH_LOCKED })
    const addGroupTrigger = getControlByLabel(
      'Auto group order'
    ).querySelector<HTMLButtonElement>('button[role="combobox"]')
    if (!addGroupTrigger) {
      throw new Error('Expected Auto group order combobox')
    }

    fireEvent.click(addGroupTrigger)

    const candidates = [
      ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
    ].map((item) => item.textContent ?? '')
    expect(candidates.some((text) => text.includes('Priority access'))).toBe(
      true
    )
    expect(candidates.some((text) => text.includes('Gated access'))).toBe(false)
  })

  test('keeps the locked group of an existing key selected with a warning', async () => {
    installApiFixtures([])
    await renderDrawer({
      userGroups: USER_GROUPS_WITH_LOCKED,
      currentRow: buildExistingKey({ group: 'svip' }),
    })

    const groupTrigger = getControlByLabel('Group')

    expect(groupTrigger).toHaveTextContent('svip')
    expect(groupTrigger).toHaveTextContent(
      'Requires cumulative top-up of 50 (current 20)'
    )
    expect(
      screen.getByText(
        'Requests with this API key fail until you meet the group requirement. Choose another group to keep using it.'
      )
    ).toBeInTheDocument()
  })

  test('keeps a locked group in the custom Auto order when an existing key is renamed', async () => {
    const updatedPayloads: Array<Record<string, unknown>> = []
    installApiFixtures([])
    installUpdateFixture(updatedPayloads)
    await renderDrawer({
      userGroups: USER_GROUPS_WITH_LOCKED,
      currentRow: buildExistingKey({
        group: 'auto',
        auto_groups: ['svip', 'default'],
        cross_group_retry: true,
      }),
    })

    changeInput(getControlByLabel('Name'), 'renamed')
    fireEvent.click(findButton('Save changes', true))

    await waitFor(() => expect(updatedPayloads).toHaveLength(1))
    expect(updatedPayloads[0]?.auto_groups).toEqual(['svip', 'default'])
  })

  test('saves a rename when the custom Auto order holds only a locked group', async () => {
    const updatedPayloads: Array<Record<string, unknown>> = []
    installApiFixtures([])
    installUpdateFixture(updatedPayloads)
    await renderDrawer({
      userGroups: USER_GROUPS_WITH_LOCKED,
      currentRow: buildExistingKey({
        group: 'auto',
        auto_groups: ['svip'],
        cross_group_retry: true,
      }),
    })

    changeInput(getControlByLabel('Name'), 'renamed')
    fireEvent.click(findButton('Save changes', true))

    await waitFor(() => expect(updatedPayloads).toHaveLength(1))
    expect(updatedPayloads[0]?.auto_groups).toEqual(['svip'])
  })

  test('shows the lock reason on a locked entry of the custom Auto order', async () => {
    installApiFixtures([])
    await renderDrawer({
      userGroups: USER_GROUPS_WITH_LOCKED,
      currentRow: buildExistingKey({
        group: 'auto',
        auto_groups: ['svip', 'default'],
        cross_group_retry: true,
      }),
    })

    const lockedEntry = screen
      .getByRole('button', { name: 'Remove svip' })
      .closest('li')
    const usableEntry = screen
      .getByRole('button', { name: 'Remove default' })
      .closest('li')

    expect(lockedEntry).toHaveTextContent(
      'Requires cumulative top-up of 50 (current 20)'
    )
    expect(usableEntry).not.toHaveTextContent('Requires cumulative top-up')
  })
})
