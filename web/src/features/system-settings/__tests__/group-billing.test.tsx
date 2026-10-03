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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { api } from '@/lib/api'

import {
  buildGroupBillingRows,
  parseFreeGroups,
  serializeFreeGroups,
} from '../billing/free-groups'
import { GroupBillingSection } from '../billing/group-billing-section'
import { SettingsPageProvider } from '../components/settings-page-context'

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

let savedOptions: Array<{ key: string; value: string }> = []
let saveResponse: { success: boolean; message?: string } = { success: true }

beforeEach(() => {
  savedOptions = []
  saveResponse = { success: true }
  apiClient.get = async (url) => {
    if (url !== '/api/group/') throw new Error(`Unexpected GET ${url}`)
    return {
      data: { success: true, data: ['vip', 'auto', 'default', 'welfare'] },
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

function SectionFixture(props: { freeGroups: string }) {
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
        <GroupBillingSection defaultValues={{ freeGroups: props.freeGroups }} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

function chargeSwitch(group: string) {
  return screen.findByRole('switch', {
    name: `Charge users in group ${group}`,
  })
}

describe('free groups option value', () => {
  it('reads stored names sorted without auto, blanks or non-strings', () => {
    expect(parseFreeGroups('["vip"," welfare ","auto","",3,"vip"]')).toEqual([
      'vip',
      'welfare',
    ])
  })

  it('reads a malformed stored value as no free groups', () => {
    expect(parseFreeGroups('{"vip":true}')).toEqual([])
    expect(parseFreeGroups('not json')).toEqual([])
  })

  it('serializes free groups as a sorted JSON array string', () => {
    expect(serializeFreeGroups(['welfare', 'beta', 'welfare'])).toBe(
      '["beta","welfare"]'
    )
  })

  it('keeps a stored free group that is missing from the group list', () => {
    expect(buildGroupBillingRows(['default', 'auto'], ['legacy'])).toEqual([
      { group: 'default', free: false, missing: false },
      { group: 'legacy', free: true, missing: true },
    ])
  })
})

describe('group billing settings section', () => {
  it('lists every group except auto with charging switched on by default', async () => {
    render(<SectionFixture freeGroups='[]' />)

    await chargeSwitch('default')
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining('default'),
      expect.stringContaining('vip'),
      expect.stringContaining('welfare'),
    ])
    expect(screen.queryByText('auto')).not.toBeInTheDocument()
    for (const row of rows) {
      expect(within(row).getByRole('switch')).toHaveAttribute(
        'aria-checked',
        'true'
      )
      expect(row).toHaveTextContent('Charge users')
    }
  })

  it('marks a stored free group with a not charged badge and an off switch', async () => {
    render(<SectionFixture freeGroups='["welfare"]' />)

    const toggle = await chargeSwitch('welfare')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    const row = screen.getByRole('row', { name: /welfare/ })
    expect(within(row).getByText('Not charged')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Save group billing' })
    ).toBeDisabled()
  })

  it('saves a group switched to free as the sorted JSON string of free groups', async () => {
    const user = userEvent.setup()
    render(<SectionFixture freeGroups='["welfare"]' />)

    await user.click(await chargeSwitch('vip'))
    expect(await chargeSwitch('vip')).toHaveAttribute('aria-checked', 'false')
    await user.click(screen.getByRole('button', { name: 'Save group billing' }))

    await waitFor(() => expect(savedOptions).toHaveLength(1))
    expect(savedOptions[0]).toEqual({
      key: 'group_billing_setting.free_groups',
      value: '["vip","welfare"]',
    })
  })

  it('switches the toggle with the keyboard', async () => {
    const user = userEvent.setup()
    render(<SectionFixture freeGroups='[]' />)

    const toggle = await chargeSwitch('default')
    toggle.focus()
    await user.keyboard(' ')

    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(
      screen.getByRole('button', { name: 'Save group billing' })
    ).toBeEnabled()
  })

  it('leaves nothing to save after a group is switched to free and back', async () => {
    const user = userEvent.setup()
    render(<SectionFixture freeGroups='["welfare"]' />)

    const saveButton = screen.getByRole('button', {
      name: 'Save group billing',
    })
    await user.click(await chargeSwitch('default'))
    expect(saveButton).toBeEnabled()
    await user.click(await chargeSwitch('default'))

    expect(saveButton).toBeDisabled()
  })

  it('lists a stored free group missing from the group list so it can be charged again', async () => {
    const user = userEvent.setup()
    render(<SectionFixture freeGroups='["legacy","welfare"]' />)

    const toggle = await chargeSwitch('legacy')
    const row = screen.getByRole('row', { name: /legacy/ })
    expect(within(row).getByText('Group no longer exists')).toBeVisible()
    expect(toggle).toHaveAttribute('aria-checked', 'false')

    await user.click(toggle)
    await user.click(screen.getByRole('button', { name: 'Save group billing' }))

    await waitFor(() => expect(savedOptions).toHaveLength(1))
    expect(savedOptions[0].value).toBe('["welfare"]')
  })

  it('keeps the unsaved switch when the server rejects the save', async () => {
    saveResponse = { success: false, message: 'invalid group' }
    const user = userEvent.setup()
    render(<SectionFixture freeGroups='[]' />)

    await user.click(await chargeSwitch('vip'))
    const saveButton = screen.getByRole('button', {
      name: 'Save group billing',
    })
    await user.click(saveButton)

    await waitFor(() => expect(savedOptions).toHaveLength(1))
    await waitFor(() => expect(saveButton).toBeEnabled())
    expect(await chargeSwitch('vip')).toHaveAttribute('aria-checked', 'false')
  })
})
