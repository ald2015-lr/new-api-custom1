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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { api } from '@/lib/api'

import {
  parseGroupAccessRules,
  parseGroupAccessUsers,
  serializeGroupAccessRules,
} from '../billing/group-access-rules'
import { GroupAccessSection } from '../billing/group-access-section'
import { SettingsPageProvider } from '../components/settings-page-context'

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

const STORED_RULES =
  '[{"group":"svip","min_topup":50,"users":[{"id":12,"username":"alice"}]},' +
  '{"group":"partner","min_topup":0,"users":[]}]'

let savedOptions: Array<{ key: string; value: string }> = []
let saveResponse: { success: boolean; message?: string } = { success: true }
/** Rules value the server returns after a save, as it would store it. */
let storedRulesAfterSave = '[]'

beforeEach(() => {
  savedOptions = []
  saveResponse = { success: true }
  storedRulesAfterSave = '[]'
  apiClient.get = async (url) => {
    if (url === '/api/option/') {
      return {
        data: {
          success: true,
          data: [
            { key: 'group_access_setting.rules', value: storedRulesAfterSave },
            { key: 'group_access_setting.count_redemption', value: 'true' },
          ],
        },
      }
    }
    if (url !== '/api/group/') throw new Error(`Unexpected GET ${url}`)
    return {
      data: { success: true, data: ['vip', 'auto', 'svip', 'default'] },
    }
  }
  apiClient.put = async (url, data) => {
    if (url !== '/api/option/') throw new Error(`Unexpected PUT ${url}`)
    savedOptions.push(data as { key: string; value: string })
    return { data: saveResponse }
  }
})

afterEach(() => {
  apiClient.get = originalGet
  apiClient.put = originalPut
})

function SectionFixture(props: { rules: string }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false },
        },
      })
  )
  const [actions, setActions] = useState<HTMLDivElement | null>(null)

  return (
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actions}>
        <div ref={setActions} />
        <GroupAccessSection
          defaultValues={{ rules: props.rules, countRedemption: true }}
        />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

async function addRule(
  user: ReturnType<typeof userEvent.setup>,
  values: { group: string; minTopup: string; users: string }
) {
  await user.click(screen.getByRole('button', { name: 'Add rule' }))
  const dialog = await screen.findByRole('dialog')
  await user.click(within(dialog).getByLabelText('Group'))
  await user.click(await screen.findByRole('option', { name: values.group }))
  // jsdom drops a typed leading "-" from number inputs, so set the value.
  fireEvent.change(within(dialog).getByLabelText('Minimum cumulative top-up'), {
    target: { value: values.minTopup },
  })
  if (values.users) {
    await user.type(
      within(dialog).getByLabelText('Whitelisted users'),
      values.users
    )
  }
  return dialog
}

describe('group access whitelist parsing', () => {
  it('reads digit-only tokens as IDs and @ as a forced username', () => {
    const parsed = parseGroupAccessUsers('12, alice\n@123  bob,12')

    expect(parsed).toEqual({
      users: [
        { id: 12, username: '' },
        { id: 0, username: 'alice' },
        { id: 0, username: '123' },
        { id: 0, username: 'bob' },
      ],
      invalidIds: [],
    })
  })

  it('reports a zero user ID as invalid instead of whitelisting it', () => {
    expect(parseGroupAccessUsers('0 7').invalidIds).toEqual(['0'])
  })
})

describe('group access rules option value', () => {
  it('serializes rules into the stored JSON array shape', () => {
    const value = serializeGroupAccessRules([
      {
        group: 'svip',
        min_topup: 50,
        users: [
          { id: 12, username: 'alice' },
          { id: 0, username: 'bob' },
        ],
      },
    ])

    expect(value).toBe(
      '[{"group":"svip","min_topup":50,"users":[{"id":12,"username":"alice"},{"id":0,"username":"bob"}]}]'
    )
  })

  it('keeps valid stored rules and drops malformed entries', () => {
    const rules = parseGroupAccessRules(
      '[{"group":"svip","min_topup":5,"users":[{"id":3,"username":"c"},{"id":-1}]},{"min_topup":1},"x"]'
    )

    expect(rules).toEqual([
      { group: 'svip', min_topup: 5, users: [{ id: 3, username: 'c' }] },
    ])
  })

  it('reads a non-array stored value as no rules', () => {
    expect(parseGroupAccessRules('{"group":"svip"}')).toEqual([])
  })
})

describe('group access settings section', () => {
  it('lists stored rules with threshold and whitelisted users', () => {
    render(<SectionFixture rules={STORED_RULES} />)

    const rows = screen.getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('svip')
    expect(rows[0]).toHaveTextContent('50')
    expect(rows[0]).toHaveTextContent('12 (alice)')
    expect(rows[1]).toHaveTextContent('partner')
    expect(rows[1]).toHaveTextContent('Whitelist only')
  })

  it('shows the empty state when no rule is stored', () => {
    render(<SectionFixture rules='[]' />)

    expect(screen.getByText('No group access rules')).toBeInTheDocument()
  })

  it('offers only unruled groups other than auto when adding a rule', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules={STORED_RULES} />)

    await user.click(screen.getByRole('button', { name: 'Add rule' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText('Group'))

    const options = await screen.findAllByRole('option')
    expect(options.map((option) => option.textContent)).toEqual([
      'default',
      'vip',
    ])
  })

  it('previews the parsed whitelist count while typing users', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules='[]' />)

    const dialog = await addRule(user, {
      group: 'vip',
      minTopup: '10',
      users: '12 @123 alice 12',
    })

    expect(within(dialog).getByText('Whitelisted users: 3')).toBeInTheDocument()
  })

  it('marks an empty threshold invalid with an accessible error', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules='[]' />)

    const dialog = await addRule(user, {
      group: 'vip',
      minTopup: '',
      users: '',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Add' }))

    const minTopup = within(dialog).getByLabelText('Minimum cumulative top-up')
    await waitFor(() =>
      expect(minTopup).toHaveAttribute('aria-invalid', 'true')
    )
    expect(
      within(dialog).getByText('Must be greater than or equal to 0')
    ).toBeInTheDocument()
  })

  it('saves an added rule as the rules option JSON string', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules='[]' />)

    const dialog = await addRule(user, {
      group: 'vip',
      minTopup: '25',
      users: '12, @123',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Add' }))
    await user.click(screen.getByRole('button', { name: 'Save group access' }))

    await waitFor(() => expect(savedOptions).toHaveLength(1))
    expect(savedOptions[0]).toEqual({
      key: 'group_access_setting.rules',
      value:
        '[{"group":"vip","min_topup":25,"users":[{"id":12,"username":""},{"id":0,"username":"123"}]}]',
    })
  })

  it('keeps the unsaved rule when the server rejects the whitelist', async () => {
    saveResponse = { success: false, message: 'unknown user: ghost' }
    const user = userEvent.setup()
    render(<SectionFixture rules='[]' />)

    const dialog = await addRule(user, {
      group: 'vip',
      minTopup: '0',
      users: 'ghost',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Add' }))
    const saveButton = screen.getByRole('button', { name: 'Save group access' })
    await user.click(saveButton)

    await waitFor(() => expect(savedOptions).toHaveLength(1))
    expect(screen.getByText('@ghost')).toBeInTheDocument()
    await waitFor(() => expect(saveButton).toBeEnabled())
  })

  it('shows the server-normalized whitelist after a save that leaves the stored value unchanged', async () => {
    storedRulesAfterSave = STORED_RULES
    const user = userEvent.setup()
    render(<SectionFixture rules={STORED_RULES} />)

    await user.click(screen.getAllByRole('button', { name: 'Edit' })[0])
    const dialog = await screen.findByRole('dialog')
    const usersInput = within(dialog).getByLabelText('Whitelisted users')
    await user.clear(usersInput)
    await user.type(usersInput, 'alice')
    await user.click(within(dialog).getByRole('button', { name: 'Update' }))
    expect(screen.getByText('@alice')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save group access' }))

    await waitFor(() => expect(screen.queryByText('@alice')).toBeNull())
    expect(savedOptions).toHaveLength(1)
    expect(screen.getAllByRole('row')[1]).toHaveTextContent('12 (alice)')
  })

  it('names the whitelist textarea and the parsed preview list differently', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules={STORED_RULES} />)

    await user.click(screen.getAllByRole('button', { name: 'Edit' })[0])
    const dialog = await screen.findByRole('dialog')

    expect(within(dialog).getByLabelText('Whitelisted users')).toHaveValue('12')
    expect(
      within(dialog).getByRole('list', { name: 'Parsed users preview' })
    ).toHaveTextContent('12 (alice)')
  })

  it('removes a rule only after the delete is confirmed', async () => {
    const user = userEvent.setup()
    render(<SectionFixture rules={STORED_RULES} />)

    await user.click(screen.getAllByRole('button', { name: 'Open menu' })[1])
    await user.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(screen.getAllByRole('row', { hidden: true })).toHaveLength(3)

    await user.click(within(confirm).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(2))
    expect(screen.queryByText('partner')).not.toBeInTheDocument()
  })
})
