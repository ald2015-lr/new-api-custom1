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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'

import { BillingHistoryDialog } from '../components/dialogs/billing-history-dialog'

function viewer(billing: { read: boolean; write: boolean }): AuthUser {
  return {
    id: 5,
    username: 'operator',
    role: ROLE.ADMIN,
    admin_role: billing.read ? 'admin' : 'deputy',
    permissions: { admin_permissions: { billing } },
  }
}

const deputy = viewer({ read: false, write: false })
const admin = viewer({ read: true, write: true })

function renderHistory(user: AuthUser) {
  useAuthStore.getState().auth.setUser(user)
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            id: 1,
            user_id: 9,
            status: 'pending',
            amount: 10,
            money: 10,
            trade_no: 'order-pending',
            payment_method: 'stripe',
            create_time: 1,
          },
        ],
      },
    },
  })
  render(<BillingHistoryDialog open onOpenChange={vi.fn()} />)
  return get
}

function requestedUrls(get: ReturnType<typeof renderHistory>) {
  return get.mock.calls.map(([url]) => url)
}

afterEach(() => {
  useAuthStore.getState().auth.reset()
})

it('a deputy admin lists only their own orders, without user IDs or Complete Order on a pending order', async () => {
  const get = renderHistory(deputy)

  await screen.findByText('order-pending')
  expect(requestedUrls(get)).toEqual(['/api/user/topup/self?p=1&page_size=10'])
  expect(screen.getByText('Pending')).toBeVisible()
  expect(screen.queryByText(/User ID/)).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Complete Order' })
  ).not.toBeInTheDocument()
})

it('an admin lists every order with user IDs and completes a pending order after confirming', async () => {
  const get = renderHistory(admin)
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })

  await screen.findByText('order-pending')
  expect(requestedUrls(get)).toEqual(['/api/user/topup?p=1&page_size=10'])
  expect(screen.getByText('User ID: 9')).toBeVisible()

  await userEvent.click(screen.getByRole('button', { name: 'Complete Order' }))
  await userEvent.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Confirm',
    })
  )

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/api/user/topup/complete', {
      trade_no: 'order-pending',
    })
  )
})

it('an admin who may view but not complete orders sees every order without Complete Order', async () => {
  const get = renderHistory(viewer({ read: true, write: false }))

  await screen.findByText('order-pending')
  expect(requestedUrls(get)).toEqual(['/api/user/topup?p=1&page_size=10'])
  expect(screen.getByText('User ID: 9')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Complete Order' })
  ).not.toBeInTheDocument()
})
