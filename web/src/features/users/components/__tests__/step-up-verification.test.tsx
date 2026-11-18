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
import type { Row } from '@tanstack/react-table'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { DataTableRowActions } from '../data-table-row-actions'
import { UsersDeleteDialog } from '../users-delete-dialog'
import { UsersProvider, useUsers } from '../users-provider'

const target: User = {
  id: 2,
  username: 'managed-user',
  display_name: 'Managed user',
  role: 1,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
}

function mockVerification(scope: string, proofToken: string) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope,
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    return { data: { success: true, data: [] } }
  })
  return {
    data: {
      success: true,
      data: {
        proof_token: proofToken,
        method: '2fa',
        scope,
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  }
}

function OpenDeleteDialog() {
  const users = useUsers()
  useEffect(() => {
    users.setCurrentRow(target)
    users.setOpen('delete')
    // The harness only seeds provider state once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return null
}

function renderInProvider(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <UsersProvider>{children}</UsersProvider>
    </QueryClientProvider>
  )
}

async function completeTwoFactorVerification() {
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('deleting a user sends the request only with a single-use proof for that user', async () => {
  const proof = mockVerification('admin.user.delete', 'delete-proof')
  const post = vi.spyOn(api, 'post').mockResolvedValue(proof)
  const del = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true } })
  renderInProvider(
    <>
      <OpenDeleteDialog />
      <UsersDeleteDialog />
    </>
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await screen.findByLabelText('Authenticator code or backup code')
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(del).not.toHaveBeenCalled()
  await completeTwoFactorVerification()
  await waitFor(() =>
    expect(del).toHaveBeenCalledWith(
      '/api/user/2/',
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'delete-proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.delete',
      context: { user_id: 2 },
    }),
    expect.anything()
  )
})

it('cancelling verification leaves the user untouched and returns to the confirmation', async () => {
  mockVerification('admin.user.delete', 'delete-proof')
  const del = vi.spyOn(api, 'delete')
  renderInProvider(
    <>
      <OpenDeleteDialog />
      <UsersDeleteDialog />
    </>
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  await screen.findByLabelText('Authenticator code or backup code')
  await userEvent.keyboard('{Escape}')
  await screen.findByRole('alertdialog')
  expect(del).not.toHaveBeenCalled()
})

it('disabling a user from the row menu binds the proof to the user and action', async () => {
  const proof = mockVerification('admin.user.manage', 'manage-proof')
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') return proof
    if (url === '/api/user/manage') return { data: { success: true } }
    throw new Error(`Unexpected POST ${url}`)
  })
  renderInProvider(
    <DataTableRowActions row={{ original: target } as Row<User>} />
  )
  await userEvent.click(screen.getByRole('button', { name: 'Open menu' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'Disable' })
  )
  expect(post).not.toHaveBeenCalledWith(
    '/api/user/manage',
    expect.anything(),
    expect.anything()
  )
  await completeTwoFactorVerification()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/manage',
      { id: 2, action: 'disable' },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'manage-proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.manage',
      context: { user_id: 2, action: 'disable' },
    }),
    expect.anything()
  )
})

const rootViewer: AuthUser = { id: 1, username: 'root', role: ROLE.SUPER_ADMIN }

// The admin kind and the recharge capabilities vary independently: root can
// untick recharge permissions on a full admin, and the menu follows them.
function adminViewer(
  adminRole: 'admin' | 'deputy',
  granted = adminRole === 'admin'
): AuthUser {
  return {
    id: 5,
    username: adminRole,
    role: ROLE.ADMIN,
    admin_role: adminRole,
    permissions: {
      admin_permissions: {
        billing: { read: granted, write: granted },
        redemption: { read: granted, write: granted },
      },
    },
  }
}

const withoutRecharge = [
  ['a deputy admin', adminViewer('deputy')],
  [
    'an admin whose recharge permissions root removed',
    adminViewer('admin', false),
  ],
] as const

const fullAdminTarget: User = { ...target, role: 10, admin_role: 'admin' }
const deputyTarget: User = { ...target, role: 10, admin_role: 'deputy' }

async function openRowMenu(viewer: AuthUser, row: User) {
  useAuthStore.getState().auth.setUser(viewer)
  renderInProvider(<DataTableRowActions row={{ original: row } as Row<User>} />)
  await userEvent.click(screen.getByRole('button', { name: 'Open menu' }))
  await screen.findByRole('menuitem', { name: 'Manage Bindings' })
}

it.each([
  ['a common user', 'set_deputy', target, 'Set as deputy admin'],
  ['a full admin', 'set_deputy', fullAdminTarget, 'Set as deputy admin'],
  ['a deputy admin', 'set_admin', deputyTarget, 'Set as full admin'],
] as const)(
  'root converting %s from the row menu binds the proof to the user and the %s action',
  async (_kind, action, row, label) => {
    const proof = mockVerification('admin.user.manage', 'manage-proof')
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/verify') return proof
      if (url === '/api/user/manage') return { data: { success: true } }
      throw new Error(`Unexpected POST ${url}`)
    })
    await openRowMenu(rootViewer, row)
    const other =
      action === 'set_deputy' ? 'Set as full admin' : 'Set as deputy admin'
    expect(
      screen.queryByRole('menuitem', { name: other })
    ).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('menuitem', { name: label }))
    await completeTwoFactorVerification()

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/user/manage',
        { id: 2, action },
        expect.objectContaining({
          headers: { 'X-Security-Proof': 'manage-proof' },
          singleUseAuthorization: true,
        })
      )
    )
    expect(post).toHaveBeenCalledWith(
      '/api/verify',
      expect.objectContaining({
        scope: 'admin.user.manage',
        context: { user_id: 2, action },
      }),
      expect.anything()
    )
  }
)

it('root is not offered a deputy conversion for another root account', async () => {
  mockVerification('admin.user.manage', 'manage-proof')
  await openRowMenu(rootViewer, { ...target, role: 100 })

  expect(
    screen.queryByRole('menuitem', { name: 'Set as deputy admin' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('menuitem', { name: 'Set as full admin' })
  ).not.toBeInTheDocument()
})

it.each([
  ['a common user', target],
  ['a deputy admin', deputyTarget],
])('an admin is not offered deputy conversions for %s', async (_kind, row) => {
  mockVerification('admin.user.manage', 'manage-proof')
  await openRowMenu(adminViewer('admin'), row)

  expect(
    screen.queryByRole('menuitem', { name: 'Set as deputy admin' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('menuitem', { name: 'Set as full admin' })
  ).not.toBeInTheDocument()
})

it.each(withoutRecharge)(
  '%s is not offered the subscriptions of a user',
  async (_kind, viewer) => {
    mockVerification('admin.user.manage', 'manage-proof')
    await openRowMenu(viewer, target)

    expect(
      screen.queryByRole('menuitem', { name: 'Manage Subscriptions' })
    ).not.toBeInTheDocument()
  }
)

it('an admin with billing read opens the subscriptions of a user', async () => {
  mockVerification('admin.user.manage', 'manage-proof')
  await openRowMenu(adminViewer('admin'), target)

  await userEvent.click(
    screen.getByRole('menuitem', { name: 'Manage Subscriptions' })
  )
  expect(
    await screen.findByRole('dialog', { name: 'User Subscription Management' })
  ).toBeInTheDocument()
})
