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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState, type ReactNode } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { CheckinSettingsSection } from '../checkin-settings-section'
import { QuotaSettingsSection } from '../quota-settings-section'

function Fixture(props: { children: ReactNode }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        {props.children}
      </SettingsPageProvider>
    </>
  )
}

function renderInSettingsPage(section: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Fixture>{section}</Fixture>,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

const checkinDefaults = {
  enabled: true,
  minQuota: 1000,
  maxQuota: 10000,
  paidThresholdQuota: 1000000,
  paidMultiplier: 2,
  freeExpireDays: 30,
  paidExpireDays: 30,
  expireOnlyWhenUnused: false,
}

const quotaDefaults = {
  QuotaForNewUser: 0,
  QuotaForInviter: 0,
  QuotaForInvitee: 0,
  InviteRechargeThresholdQuota: 0,
  InviteRewardExpireDays: 30,
  InviteRewardExpireOnlyWhenUnused: false,
  TopUpLink: '',
  quota_setting: {
    enable_free_model_pre_consume: true,
    trust_quota_usd: 10,
    pre_consume_multiplier: 1,
  },
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

describe('check-in reward settings', () => {
  test('saving a decimal paid multiplier sends checkin_setting.paid_multiplier', async () => {
    renderInSettingsPage(
      <CheckinSettingsSection defaultValues={checkinDefaults} />
    )
    const input = await screen.findByRole('spinbutton', {
      name: 'Paid user reward multiplier',
    })
    fireEvent.change(input, { target: { value: '1.5' } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Save check-in settings' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'checkin_setting.paid_multiplier',
        value: '1.5',
      })
    )
    expect(api.put).toHaveBeenCalledTimes(1)
  })

  test('turning on expire only when unused sends checkin_setting.expire_only_when_unused', async () => {
    renderInSettingsPage(
      <CheckinSettingsSection defaultValues={checkinDefaults} />
    )
    fireEvent.click(
      await screen.findByRole('switch', { name: 'Expire only when unused' })
    )
    fireEvent.click(
      screen.getByRole('button', { name: 'Save check-in settings' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'checkin_setting.expire_only_when_unused',
        value: 'true',
      })
    )
  })

  test('a paid multiplier below 1 shows the range error and saves nothing', async () => {
    renderInSettingsPage(
      <CheckinSettingsSection defaultValues={checkinDefaults} />
    )
    const input = await screen.findByRole('spinbutton', {
      name: 'Paid user reward multiplier',
    })
    fireEvent.change(input, { target: { value: '0.5' } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Save check-in settings' })
    )
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(
      screen.getByText(
        'Paid check-in multiplier must be a number between 1 and 100 with at most 2 decimal places'
      )
    ).toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  })

  test('a paid multiplier with three decimals shows the precision error and saves nothing', async () => {
    renderInSettingsPage(
      <CheckinSettingsSection defaultValues={checkinDefaults} />
    )
    const input = await screen.findByRole('spinbutton', {
      name: 'Paid user reward multiplier',
    })
    fireEvent.change(input, { target: { value: '1.125' } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Save check-in settings' })
    )
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(api.put).not.toHaveBeenCalled()
  })

  test('expiry days above 3650 show the range error and save nothing', async () => {
    renderInSettingsPage(
      <CheckinSettingsSection defaultValues={checkinDefaults} />
    )
    const input = await screen.findByRole('spinbutton', {
      name: 'Free user reward expiry (days)',
    })
    fireEvent.change(input, { target: { value: '3651' } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Save check-in settings' })
    )
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(
      screen.getByText('Expiry days must be an integer between 0 and 3650')
    ).toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  })
})

describe('invitation reward settings', () => {
  test('saving a recharge threshold sends InviteRechargeThresholdQuota', async () => {
    renderInSettingsPage(<QuotaSettingsSection defaultValues={quotaDefaults} />)
    const input = await screen.findByRole('spinbutton', {
      name: 'Invite reward recharge threshold',
    })
    fireEvent.change(input, { target: { value: '500000' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'InviteRechargeThresholdQuota',
        value: 500000,
      })
    )
    expect(api.put).toHaveBeenCalledTimes(1)
  })

  test('turning on expire only when unused sends InviteRewardExpireOnlyWhenUnused', async () => {
    renderInSettingsPage(<QuotaSettingsSection defaultValues={quotaDefaults} />)
    fireEvent.click(
      await screen.findByRole('switch', {
        name: 'Invitation rewards expire only when unused',
      })
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'InviteRewardExpireOnlyWhenUnused',
        value: true,
      })
    )
  })
})
