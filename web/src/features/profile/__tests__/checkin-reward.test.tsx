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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { CheckinCalendarCard } from '../components/checkin-calendar-card'
import type { CheckinResponse, CheckinStatusResponse } from '../types'

// 2026-06-21 UTC: mid-year, so the rendered year does not depend on the
// time zone the tests run in.
const EXPIRES_AT = 1782000000

const status: CheckinStatusResponse = {
  enabled: true,
  paid_threshold_quota: 1000000,
  paid_multiplier: 2,
  free_expire_days: 30,
  paid_expire_days: 30,
  expire_only_when_unused: false,
  recharged_quota: 0,
  is_paid_user: false,
  stats: {
    checked_in_today: false,
    total_checkins: 0,
    total_quota: 0,
    checkin_count: 0,
    records: [],
  },
}

const paidCheckin: CheckinResponse = {
  quota_awarded: 10000,
  base_quota: 5000,
  checkin_date: '2026-05-22',
  paid_multiplier_applied: true,
  paid_multiplier: 2,
  is_paid_user: true,
  expire_days: 30,
  expires_at: EXPIRES_AT,
  expire_only_when_unused: false,
}

function renderCard(statusData: CheckinStatusResponse = status) {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: statusData },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <CheckinCalendarCard
        checkinEnabled
        turnstileEnabled={false}
        turnstileSiteKey=''
      />
    </QueryClientProvider>
  )
}

async function checkIn(result: CheckinResponse) {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, message: 'Check-in successful', data: result },
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Check in now' }))
}

describe('check-in reward result', () => {
  test('a check-in with the paid multiplier applied shows the multiplier badge', async () => {
    renderCard()
    await checkIn(paidCheckin)
    expect(await screen.findByText('Paid user ×2')).toBeInTheDocument()
  })

  test('a free check-in shows no multiplier badge', async () => {
    renderCard()
    await checkIn({
      ...paidCheckin,
      quota_awarded: 5000,
      paid_multiplier_applied: false,
      is_paid_user: false,
    })
    expect(await screen.findByText(/^Current expiry: /)).toBeInTheDocument()
    expect(screen.queryByText('Paid user ×2')).not.toBeInTheDocument()
  })

  test('a reward that expires only when unused shows the rolling expiry rule', async () => {
    renderCard()
    await checkIn({ ...paidCheckin, expire_only_when_unused: true })
    expect(
      await screen.findByText('Expires if unused for 30 days')
    ).toBeInTheDocument()
  })

  test('a reward with a fixed expiry shows its expiry date without the rolling rule', async () => {
    renderCard()
    await checkIn(paidCheckin)
    expect(
      await screen.findByText(/^Current expiry: .*2026/)
    ).toBeInTheDocument()
    expect(
      screen.queryByText('Expires if unused for 30 days')
    ).not.toBeInTheDocument()
  })

  test('a reward that never expires says so instead of a date', async () => {
    renderCard()
    await checkIn({ ...paidCheckin, expire_days: 0, expires_at: 0 })
    expect(await screen.findByText('Never expires')).toBeInTheDocument()
    expect(screen.queryByText(/^Current expiry: /)).not.toBeInTheDocument()
  })
})

describe('check-in reward rules', () => {
  test('a paid multiplier above 1 is listed with its recharge threshold', async () => {
    renderCard()
    expect(
      await screen.findByText(
        /^Paid users \(cumulative recharge ≥ .+\) get ×2 check-in rewards$/
      )
    ).toBeInTheDocument()
  })

  test('expire only when unused lists the rolling expiry rule', async () => {
    renderCard({ ...status, expire_only_when_unused: true })
    expect(
      await screen.findByText('Rewards expire if unused for 30 days')
    ).toBeInTheDocument()
  })

  test('rewards that never expire list no expiry rule', async () => {
    renderCard({ ...status, free_expire_days: 0 })
    await screen.findByText(/^Paid users /)
    expect(screen.queryByText(/^Rewards expire /)).not.toBeInTheDocument()
  })
})
