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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import { afterEach, describe, expect, it } from 'vitest'

import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  DEPUTY_ROLE_KEY,
  hasPermission,
  normalizeAdminPermissions,
  type PermissionCatalog,
} from '@/lib/admin-permissions'
import { getRoleLabelKey, ROLE } from '@/lib/roles'
import { Route as RedemptionCodesRoute } from '@/routes/_authenticated/redemption-codes/index'
import { Route as SubscriptionsRoute } from '@/routes/_authenticated/subscriptions/index'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'

const { BILLING, REDEMPTION } = ADMIN_PERMISSION_RESOURCES
const { READ, WRITE } = ADMIN_PERMISSION_ACTIONS

function rechargeCapabilities(granted: boolean) {
  return {
    billing: { read: granted, write: granted },
    redemption: { read: granted, write: granted },
  }
}

// Capabilities as the server reports them for each kind of account.
const viewers: Record<string, AuthUser> = {
  deputy: {
    id: 3,
    username: 'deputy',
    role: ROLE.ADMIN,
    admin_role: 'deputy',
    permissions: { admin_permissions: rechargeCapabilities(false) },
  },
  admin: {
    id: 2,
    username: 'admin',
    role: ROLE.ADMIN,
    admin_role: 'admin',
    permissions: { admin_permissions: rechargeCapabilities(true) },
  },
  root: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
  common: {
    id: 4,
    username: 'common',
    role: ROLE.USER,
    permissions: { admin_permissions: rechargeCapabilities(false) },
  },
}

afterEach(() => {
  useAuthStore.getState().auth.reset()
})

describe('recharge permissions', () => {
  it.each([
    ['deputy', false],
    ['admin', true],
    ['root', true],
    ['common', false],
  ])(
    'a %s holds billing and redemption permissions only when the server grants them (%s)',
    (kind, expected) => {
      for (const resource of [BILLING, REDEMPTION]) {
        for (const action of [READ, WRITE]) {
          expect(hasPermission(viewers[kind], resource, action)).toBe(expected)
        }
      }
    }
  )

  it('a role-10 account without capabilities holds no recharge permission', () => {
    expect(
      hasPermission(
        { id: 5, username: 'stale', role: ROLE.ADMIN },
        BILLING,
        READ
      )
    ).toBe(false)
  })
})

describe('normalizeAdminPermissions', () => {
  const catalog: PermissionCatalog = {
    resources: [
      {
        resource: 'channel',
        label_key: 'Channel Management',
        actions: [
          { action: 'read', label_key: 'Read', description_key: '' },
          { action: 'write', label_key: 'Write', description_key: '' },
        ],
      },
      {
        resource: 'billing',
        label_key: 'Billing management',
        recharge: true,
        actions: [
          { action: 'read', label_key: 'Read', description_key: '' },
          { action: 'write', label_key: 'Write', description_key: '' },
        ],
      },
    ],
    roles: [
      {
        key: 'admin',
        name: 'Admin',
        built_in: true,
        superuser: false,
        grants: {
          channel: { read: true, write: true },
          billing: { read: true, write: true },
        },
      },
      {
        key: 'deputy',
        name: 'Deputy Admin',
        built_in: true,
        superuser: false,
        grants: {
          channel: { read: true, write: true },
          billing: { read: false, write: false },
        },
      },
    ],
  }

  it('fills missing cells of a deputy from the deputy baseline, which holds no recharge permission', () => {
    expect(
      normalizeAdminPermissions(
        { channel: { write: false } },
        catalog,
        DEPUTY_ROLE_KEY
      )
    ).toEqual({
      channel: { read: true, write: false },
      billing: { read: false, write: false },
    })
  })

  it('fills missing cells from the admin baseline by default', () => {
    expect(
      normalizeAdminPermissions({ channel: { write: false } }, catalog)
    ).toEqual({
      channel: { read: true, write: false },
      billing: { read: true, write: true },
    })
  })
})

describe('getRoleLabelKey', () => {
  it.each([
    [ROLE.ADMIN, 'deputy', 'Deputy Admin'],
    [ROLE.ADMIN, 'admin', 'Admin'],
    [ROLE.ADMIN, undefined, 'Admin'],
    // The deputy marker only means something on role 10.
    [ROLE.USER, 'deputy', 'User'],
    [ROLE.SUPER_ADMIN, 'deputy', 'Super Admin'],
  ] as const)(
    'role %s with admin_role %s is labelled %s',
    (role, adminRole, label) => {
      expect(getRoleLabelKey(role, adminRole)).toBe(label)
    }
  )
})

describe('recharge route guards', () => {
  async function landingPath(path: string) {
    const root = createRootRoute()
    const routes = [
      createRoute({
        getParentRoute: () => root,
        path: '/redemption-codes/',
        beforeLoad: RedemptionCodesRoute.options.beforeLoad as () => void,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/subscriptions/',
        beforeLoad: SubscriptionsRoute.options.beforeLoad as () => void,
      }),
      createRoute({ getParentRoute: () => root, path: '/403' }),
    ]
    const router = createRouter({
      routeTree: root.addChildren(routes),
      history: createMemoryHistory({ initialEntries: [path] }),
    })
    await router.load()
    return router.state.location.pathname
  }

  it.each(['/redemption-codes/', '/subscriptions/'])(
    'a deputy admin opening %s directly is sent to 403',
    async (path) => {
      useAuthStore.getState().auth.setUser(viewers.deputy)
      expect(await landingPath(path)).toBe('/403')
    }
  )

  it.each([
    ['admin', '/redemption-codes/'],
    ['admin', '/subscriptions/'],
    ['root', '/redemption-codes/'],
    ['root', '/subscriptions/'],
  ])('a %s may open %s', async (kind, path) => {
    useAuthStore.getState().auth.setUser(viewers[kind])
    expect(await landingPath(path)).toBe(path)
  })

  it('an admin whose redemption read was unticked cannot open redemption codes but can open subscriptions', async () => {
    useAuthStore.getState().auth.setUser({
      ...viewers.admin,
      permissions: {
        admin_permissions: {
          ...rechargeCapabilities(true),
          redemption: { read: false, write: false },
        },
      },
    })
    expect(await landingPath('/redemption-codes/')).toBe('/403')
    expect(await landingPath('/subscriptions/')).toBe('/subscriptions/')
  })
})
