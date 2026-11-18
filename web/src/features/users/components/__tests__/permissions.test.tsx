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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { UsersMutateDrawer } from '../users-mutate-drawer'
import { UsersProvider } from '../users-provider'

const target: User = {
  id: 2,
  username: 'managed-admin',
  display_name: 'Managed admin',
  role: 10,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
}
const label = "View other accounts' audit logs"
const description =
  'View audit records from user and admin roles. Root records are always excluded.'

function renderPermissions(viewerRole: number, allowed?: boolean) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: viewerRole })
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/authz/catalog') {
      return {
        data: {
          success: true,
          data: {
            resources: [
              {
                resource: 'audit',
                label_key: 'Audit Logs',
                actions: [
                  {
                    action: 'read',
                    label_key: label,
                    description_key: description,
                  },
                ],
              },
            ],
            roles: [{ key: 'admin', grants: { audit: { read: false } } }],
          },
        },
      }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'admin.user.update',
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          ...target,
          admin_permissions:
            allowed === undefined ? {} : { audit: { read: allowed } },
        },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <UsersMutateDrawer
          open
          onOpenChange={() => undefined}
          currentRow={target}
        />
      </UsersProvider>
    </QueryClientProvider>
  )
  return get
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it.each([undefined, true])(
  'root can save an audit grant or revocation after step-up verification (previous=%s)',
  async (allowed) => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          proof_token: 'update-proof',
          method: '2fa',
          scope: 'admin.user.update',
          expires_at: Math.floor(Date.now() / 1000) + 60,
        },
      },
    })
    renderPermissions(100, allowed)
    await screen.findByDisplayValue('Managed admin')
    const toggle = await screen.findByRole('button', { name: label })
    await waitFor(() =>
      expect(toggle).toHaveAttribute('aria-pressed', String(!!allowed))
    )
    expect(toggle).toHaveAccessibleDescription(description)
    await userEvent.click(toggle)
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    // The permission matrix changed, so the save waits for verification.
    expect(put).not.toHaveBeenCalled()
    await userEvent.type(
      await screen.findByLabelText('Authenticator code or backup code'),
      '123456'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/user/',
        expect.objectContaining({
          id: 2,
          admin_permissions: { audit: { read: !allowed } },
        }),
        expect.objectContaining({
          headers: { 'X-Security-Proof': 'update-proof' },
          singleUseAuthorization: true,
        })
      )
    )
  }
)

it('root saving an administrator without changing permissions or password does not verify', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const get = renderPermissions(100, true)
  const displayName = await screen.findByDisplayValue('Managed admin')
  await userEvent.clear(displayName)
  await userEvent.type(displayName, 'Renamed admin')
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/user/',
      expect.objectContaining({ id: 2, display_name: 'Renamed admin' }),
      {}
    )
  )
  expect(put.mock.calls[0][1]).not.toHaveProperty('admin_permissions')
  expect(get).not.toHaveBeenCalledWith('/api/verify/methods', expect.anything())
})

it('admin cannot edit the audit permission even when the catalog is available', async () => {
  renderPermissions(10)
  await screen.findByDisplayValue('Managed admin')
  expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument()
})

// Catalog as the backend serves it: billing and redemption are recharge
// resources, and the deputy baseline is the admin one without them.
const rechargeCatalog = {
  resources: [
    {
      resource: 'audit',
      label_key: 'Audit Logs',
      actions: [
        { action: 'read', label_key: label, description_key: description },
      ],
    },
    {
      resource: 'billing',
      label_key: 'Billing management',
      recharge: true,
      actions: [
        {
          action: 'read',
          label_key: 'View recharge records',
          description_key: '',
        },
        {
          action: 'write',
          label_key: 'Recharge users and manage plans',
          description_key: '',
        },
      ],
    },
    {
      resource: 'redemption',
      label_key: 'Redemption codes',
      recharge: true,
      actions: [
        {
          action: 'read',
          label_key: 'View redemption codes',
          description_key: '',
        },
        {
          action: 'write',
          label_key: 'Manage redemption codes',
          description_key: '',
        },
      ],
    },
  ],
  roles: [
    {
      key: 'admin',
      grants: {
        audit: { read: false },
        billing: { read: true, write: true },
        redemption: { read: true, write: true },
      },
    },
    {
      key: 'deputy',
      grants: {
        audit: { read: false },
        billing: { read: false, write: false },
        redemption: { read: false, write: false },
      },
    },
  ],
}
const deputyNote =
  'Deputy admins can never view or manage recharge information.'

function renderRechargeDrawer(currentRow?: User) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/authz/catalog') {
      return { data: { success: true, data: rechargeCatalog } }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: currentRow ? 'admin.user.update' : 'admin.user.create',
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    // GET /api/user/:id returns the effective capabilities of the target.
    const deputy = currentRow?.admin_role === 'deputy'
    return {
      data: {
        success: true,
        data: {
          ...currentRow,
          admin_permissions: {
            audit: { read: true },
            billing: { read: !deputy, write: !deputy },
            redemption: { read: !deputy, write: !deputy },
          },
        },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <UsersMutateDrawer
          open
          onOpenChange={() => undefined}
          currentRow={currentRow}
        />
      </UsersProvider>
    </QueryClientProvider>
  )
  return get
}

function mockProof(scope: string, proofToken: string) {
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

async function verifyWithTwoFactor() {
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

const deputyTarget: User = { ...target, admin_role: 'deputy' }

it('root editing a deputy admin sees no recharge permissions and a note why', async () => {
  renderRechargeDrawer(deputyTarget)
  await screen.findByDisplayValue('Managed admin')

  expect(await screen.findByRole('button', { name: label })).toBeVisible()
  expect(screen.getByText(deputyNote)).toBeVisible()
  for (const name of [
    'View recharge records',
    'Recharge users and manage plans',
    'View redemption codes',
    'Manage redemption codes',
  ]) {
    expect(screen.queryByRole('button', { name })).not.toBeInTheDocument()
  }
})

it('root editing a full admin still sees the recharge permissions', async () => {
  renderRechargeDrawer({ ...target, admin_role: 'admin' })
  await screen.findByDisplayValue('Managed admin')

  expect(
    await screen.findByRole('button', { name: 'View recharge records' })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(
    screen.getByRole('button', { name: 'Manage redemption codes' })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(screen.queryByText(deputyNote)).not.toBeInTheDocument()
})

it('root saving a changed deputy matrix sends no recharge resource', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  vi.spyOn(api, 'post').mockResolvedValue(
    mockProof('admin.user.update', 'update-proof')
  )
  renderRechargeDrawer(deputyTarget)
  await screen.findByDisplayValue('Managed admin')
  const toggle = await screen.findByRole('button', { name: label })
  await waitFor(() => expect(toggle).toHaveAttribute('aria-pressed', 'true'))

  await userEvent.click(toggle)
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await verifyWithTwoFactor()

  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  expect(put.mock.calls[0][1]).toMatchObject({
    id: 2,
    admin_permissions: { audit: { read: false } },
  })
  expect(put.mock.calls[0][1]).not.toHaveProperty('admin_role')
  expect(
    Object.keys(
      (put.mock.calls[0][1] as { admin_permissions: object }).admin_permissions
    )
  ).toEqual(['audit'])
})

it('root saving a deputy without changing permissions or password does not verify', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const get = renderRechargeDrawer(deputyTarget)
  const displayName = await screen.findByDisplayValue('Managed admin')
  await screen.findByRole('button', { name: label })

  await userEvent.clear(displayName)
  await userEvent.type(displayName, 'Renamed deputy')
  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))

  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/user/',
      expect.objectContaining({ id: 2, display_name: 'Renamed deputy' }),
      {}
    )
  )
  expect(put.mock.calls[0][1]).not.toHaveProperty('admin_permissions')
  expect(get).not.toHaveBeenCalledWith('/api/verify/methods', expect.anything())
})

it('root creating a deputy admin sends the deputy kind, its proof context and no recharge resource', async () => {
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return mockProof('admin.user.create', 'create-proof')
    }
    if (url === '/api/user/') return { data: { success: true } }
    throw new Error(`Unexpected POST ${url}`)
  })
  renderRechargeDrawer()
  await userEvent.type(
    await screen.findByPlaceholderText('Enter username'),
    'new-deputy'
  )
  await userEvent.type(
    screen.getByPlaceholderText('Enter password (8–128 characters)'),
    'a-strong-password'
  )
  await userEvent.click(screen.getByRole('combobox', { name: 'Role' }))
  await userEvent.click(
    await screen.findByRole('option', { name: 'Deputy Admin' })
  )

  expect(await screen.findByText(deputyNote)).toBeVisible()
  expect(screen.getByRole('button', { name: label })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'View recharge records' })
  ).not.toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await verifyWithTwoFactor()

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/',
      {
        username: 'new-deputy',
        display_name: 'new-deputy',
        password: 'a-strong-password',
        role: 10,
        admin_role: 'deputy',
        admin_permissions: { audit: { read: false } },
      },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'create-proof' },
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.create',
      context: { role: 10, admin_role: 'deputy' },
    }),
    expect.anything()
  )
})

it('root creating a full admin keeps the recharge permissions and the role-only proof context', async () => {
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return mockProof('admin.user.create', 'create-proof')
    }
    if (url === '/api/user/') return { data: { success: true } }
    throw new Error(`Unexpected POST ${url}`)
  })
  renderRechargeDrawer()
  await userEvent.type(
    await screen.findByPlaceholderText('Enter username'),
    'new-admin'
  )
  await userEvent.type(
    screen.getByPlaceholderText('Enter password (8–128 characters)'),
    'a-strong-password'
  )
  await userEvent.click(screen.getByRole('combobox', { name: 'Role' }))
  await userEvent.click(await screen.findByRole('option', { name: 'Admin' }))
  expect(
    await screen.findByRole('button', { name: 'View recharge records' })
  ).toBeVisible()

  await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await verifyWithTwoFactor()

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/',
      expect.objectContaining({
        role: 10,
        admin_permissions: {
          audit: { read: false },
          billing: { read: true, write: true },
          redemption: { read: true, write: true },
        },
      }),
      expect.anything()
    )
  )
  const created = post.mock.calls.find(([url]) => url === '/api/user/')
  expect(created?.[1]).not.toHaveProperty('admin_role')
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.create',
      context: { role: 10 },
    }),
    expect.anything()
  )
})
