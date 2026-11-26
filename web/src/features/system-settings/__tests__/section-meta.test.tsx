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
import { readFileSync, readdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import type { TFunction } from 'i18next'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SYSTEM_SETTINGS_VIEW } from '@/components/layout/config/system-settings.config'
import { useSidebarView } from '@/hooks/use-sidebar-view'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { Route as AuthSectionRoute } from '@/routes/_authenticated/system-settings/auth/$section'
import { Route as AuthIndexRoute } from '@/routes/_authenticated/system-settings/auth/index'
import { Route as BillingSectionRoute } from '@/routes/_authenticated/system-settings/billing/$section'
import { Route as BillingIndexRoute } from '@/routes/_authenticated/system-settings/billing/index'
import { Route as ContentSectionRoute } from '@/routes/_authenticated/system-settings/content/$section'
import { Route as ContentIndexRoute } from '@/routes/_authenticated/system-settings/content/index'
import { Route as SettingsIndexRoute } from '@/routes/_authenticated/system-settings/index'
import { Route as ModelsSectionRoute } from '@/routes/_authenticated/system-settings/models/$section'
import { Route as ModelsIndexRoute } from '@/routes/_authenticated/system-settings/models/index'
import { Route as OperationsSectionRoute } from '@/routes/_authenticated/system-settings/operations/$section'
import { Route as OperationsIndexRoute } from '@/routes/_authenticated/system-settings/operations/index'
import { Route as PolicySectionRoute } from '@/routes/_authenticated/system-settings/request-policies/$section'
import { Route as PolicyIndexRoute } from '@/routes/_authenticated/system-settings/request-policies/index'
import { Route as SettingsRoute } from '@/routes/_authenticated/system-settings/route'
import { Route as SecuritySectionRoute } from '@/routes/_authenticated/system-settings/security/$section'
import { Route as SecurityIndexRoute } from '@/routes/_authenticated/system-settings/security/index'
import { Route as SiteSectionRoute } from '@/routes/_authenticated/system-settings/site/$section'
import { Route as SiteIndexRoute } from '@/routes/_authenticated/system-settings/site/index'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'

import * as authMeta from '../auth/section-meta'
import * as authRegistry from '../auth/section-registry'
import * as billingMeta from '../billing/section-meta'
import * as billingRegistry from '../billing/section-registry'
import * as contentMeta from '../content/section-meta'
import * as contentRegistry from '../content/section-registry'
import * as modelsMeta from '../models/section-meta'
import * as modelsRegistry from '../models/section-registry'
import * as operationsMeta from '../operations/section-meta'
import * as operationsRegistry from '../operations/section-registry'
import * as policyMeta from '../request-policies/section-meta'
import * as policyRegistry from '../request-policies/section-registry'
import * as securityMeta from '../security/section-meta'
import * as securityRegistry from '../security/section-registry'
import * as siteMeta from '../site/section-meta'
import * as siteRegistry from '../site/section-registry'

// The route guards and the settings sidebar read each page's section-meta.ts,
// which carries no components, so System Settings stays out of the entry and
// the layout chunk. The registries keep the components. These tests hold the
// two in step and check that every section is reached exactly as before, for
// root and for the admin kinds who must not reach System Settings at all.

type NavItem = { title: string; url: string; badge?: string }
type NavItems = (
  t: TFunction,
  badges?: Partial<Record<string, string>>
) => NavItem[]
type SectionGuard = (context: { params: { section: string } }) => void

type SettingsPage = {
  name: string
  meta: {
    ids: readonly string[]
    defaultSection: string
    navItems: NavItems
  }
  registry: {
    ids: readonly string[]
    /** Not exported by every registry; the index route is checked instead. */
    defaultSection?: string
    navItems: NavItems
    sectionOf: (id: string) => { id: string }
  }
  indexRoute: { options: { beforeLoad?: unknown } }
  sectionRoute: { options: { beforeLoad?: unknown } }
  badges?: Record<string, string>
}

const PAGES: SettingsPage[] = [
  {
    name: 'site',
    meta: {
      ids: siteMeta.SITE_SECTION_IDS,
      defaultSection: siteMeta.SITE_DEFAULT_SECTION,
      navItems: siteMeta.getSiteSectionNavItems as NavItems,
    },
    registry: {
      ids: siteRegistry.SITE_SECTION_IDS,
      defaultSection: siteRegistry.SITE_DEFAULT_SECTION,
      navItems: siteRegistry.getSiteSectionNavItems as NavItems,
      sectionOf: siteRegistry.getSiteSectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: SiteIndexRoute,
    sectionRoute: SiteSectionRoute,
  },
  {
    name: 'auth',
    meta: {
      ids: authMeta.AUTH_SECTION_IDS,
      defaultSection: authMeta.AUTH_DEFAULT_SECTION,
      navItems: authMeta.getAuthSectionNavItems as NavItems,
    },
    registry: {
      ids: authRegistry.AUTH_SECTION_IDS,
      defaultSection: authRegistry.AUTH_DEFAULT_SECTION,
      navItems: authRegistry.getAuthSectionNavItems as NavItems,
      sectionOf: authRegistry.getAuthSectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: AuthIndexRoute,
    sectionRoute: AuthSectionRoute,
  },
  {
    name: 'billing',
    meta: {
      ids: billingMeta.BILLING_SECTION_IDS,
      defaultSection: billingMeta.BILLING_DEFAULT_SECTION,
      navItems: billingMeta.getBillingSectionNavItems as NavItems,
    },
    registry: {
      ids: billingRegistry.BILLING_SECTION_IDS,
      defaultSection: billingRegistry.BILLING_DEFAULT_SECTION,
      navItems: billingRegistry.getBillingSectionNavItems as NavItems,
      sectionOf: billingRegistry.getBillingSectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: BillingIndexRoute,
    sectionRoute: BillingSectionRoute,
    badges: { 'model-pricing': '3' },
  },
  {
    name: 'models',
    meta: {
      ids: modelsMeta.MODELS_SECTION_IDS,
      defaultSection: modelsMeta.MODELS_DEFAULT_SECTION,
      navItems: modelsMeta.getModelsSectionNavItems as NavItems,
    },
    registry: {
      ids: modelsRegistry.MODELS_SECTION_IDS,
      defaultSection: modelsRegistry.MODELS_DEFAULT_SECTION,
      navItems: modelsRegistry.getModelsSectionNavItems as NavItems,
      sectionOf: modelsRegistry.getModelsSectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: ModelsIndexRoute,
    sectionRoute: ModelsSectionRoute,
  },
  {
    name: 'request-policies',
    meta: {
      ids: policyMeta.POLICY_SECTION_IDS,
      defaultSection: policyMeta.POLICY_DEFAULT_SECTION,
      navItems: policyMeta.getPolicySectionNavItems as NavItems,
    },
    registry: {
      ids: policyRegistry.POLICY_SECTION_IDS,
      navItems: policyRegistry.getPolicySectionNavItems as NavItems,
      sectionOf: policyRegistry.getPolicySectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: PolicyIndexRoute,
    sectionRoute: PolicySectionRoute,
  },
  {
    name: 'security',
    meta: {
      ids: securityMeta.SECURITY_SECTION_IDS,
      defaultSection: securityMeta.SECURITY_DEFAULT_SECTION,
      navItems: securityMeta.getSecuritySectionNavItems as NavItems,
    },
    registry: {
      ids: securityRegistry.SECURITY_SECTION_IDS,
      defaultSection: securityRegistry.SECURITY_DEFAULT_SECTION,
      navItems: securityRegistry.getSecuritySectionNavItems as NavItems,
      sectionOf: securityRegistry.getSecuritySectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: SecurityIndexRoute,
    sectionRoute: SecuritySectionRoute,
  },
  {
    name: 'content',
    meta: {
      ids: contentMeta.CONTENT_SECTION_IDS,
      defaultSection: contentMeta.CONTENT_DEFAULT_SECTION,
      navItems: contentMeta.getContentSectionNavItems as NavItems,
    },
    registry: {
      ids: contentRegistry.CONTENT_SECTION_IDS,
      defaultSection: contentRegistry.CONTENT_DEFAULT_SECTION,
      navItems: contentRegistry.getContentSectionNavItems as NavItems,
      sectionOf: contentRegistry.getContentSectionMeta as (id: string) => {
        id: string
      },
    },
    indexRoute: ContentIndexRoute,
    sectionRoute: ContentSectionRoute,
  },
  {
    name: 'operations',
    meta: {
      ids: operationsMeta.OPERATIONS_SECTION_IDS,
      defaultSection: operationsMeta.OPERATIONS_DEFAULT_SECTION,
      navItems: operationsMeta.getOperationsSectionNavItems as NavItems,
    },
    registry: {
      ids: operationsRegistry.OPERATIONS_SECTION_IDS,
      defaultSection: operationsRegistry.OPERATIONS_DEFAULT_SECTION,
      navItems: operationsRegistry.getOperationsSectionNavItems as NavItems,
      sectionOf: operationsRegistry.getOperationsSectionMeta as (
        id: string
      ) => { id: string },
    },
    indexRoute: OperationsIndexRoute,
    sectionRoute: OperationsSectionRoute,
  },
]

const t = ((key: string) => key) as unknown as TFunction

function rechargeCapabilities(granted: boolean) {
  return {
    billing: { read: granted, write: granted },
    redemption: { read: granted, write: granted },
  }
}

const VIEWERS: Record<'root' | 'admin' | 'deputy', AuthUser> = {
  root: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
  admin: {
    id: 2,
    username: 'admin',
    role: ROLE.ADMIN,
    admin_role: 'admin',
    permissions: { admin_permissions: rechargeCapabilities(true) },
  },
  deputy: {
    id: 3,
    username: 'deputy',
    role: ROLE.ADMIN,
    admin_role: 'deputy',
    permissions: { admin_permissions: rechargeCapabilities(false) },
  },
}

/** Every page and section URL, plus an unknown section per page. */
const SECTION_PATHS = PAGES.flatMap((page) =>
  page.registry.ids.map((id) => `/system-settings/${page.name}/${id}`)
)

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

/** Where a hard load of `path` ends up, through the real System Settings guards. */
async function landingPath(path: string) {
  const root = createRootRoute()
  const settings = createRoute({
    getParentRoute: () => root,
    path: '/system-settings',
    beforeLoad: SettingsRoute.options.beforeLoad as () => void,
  })
  const children = [
    createRoute({
      getParentRoute: () => settings,
      path: '/',
      beforeLoad: SettingsIndexRoute.options.beforeLoad as () => void,
    }),
    ...PAGES.flatMap((page) => [
      createRoute({
        getParentRoute: () => settings,
        path: `/${page.name}/`,
        beforeLoad: page.indexRoute.options.beforeLoad as () => void,
      }),
      createRoute({
        getParentRoute: () => settings,
        path: `/${page.name}/$section`,
        beforeLoad: page.sectionRoute.options.beforeLoad as SectionGuard,
      }),
    ]),
  ]
  const router = createRouter({
    routeTree: root.addChildren([
      settings.addChildren(children),
      createRoute({ getParentRoute: () => root, path: '/403' }),
      createRoute({ getParentRoute: () => root, path: '/request-processing' }),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  await router.load()
  return router.state.location.pathname
}

describe('section metadata matches the section registry', () => {
  it.each(PAGES.map((page) => [page.name, page]))(
    '%s: same section ids in the same order',
    (_name, page) => {
      expect([...page.meta.ids]).toEqual([...page.registry.ids])
    }
  )

  it.each(PAGES.map((page) => [page.name, page]))(
    '%s: same default section',
    (_name, page) => {
      if (page.registry.defaultSection !== undefined) {
        expect(page.meta.defaultSection).toBe(page.registry.defaultSection)
      }
      expect(page.registry.ids).toContain(page.meta.defaultSection)
    }
  )

  it.each(PAGES.map((page) => [page.name, page]))(
    '%s: same sidebar titles, URLs and badges',
    (_name, page) => {
      expect(page.meta.navItems(t, page.badges)).toEqual(
        page.registry.navItems(t, page.badges)
      )
    }
  )

  it.each(PAGES.map((page) => [page.name, page]))(
    '%s: every listed section has its own content in the registry',
    (_name, page) => {
      for (const id of page.meta.ids) {
        expect(page.registry.sectionOf(id).id).toBe(id)
      }
    }
  )
})

describe('System Settings routes', () => {
  it('root reaches every section of every page', async () => {
    useAuthStore.getState().auth.setUser(VIEWERS.root)
    for (const path of SECTION_PATHS) {
      expect(await landingPath(path)).toBe(path)
    }
  })

  it.each(PAGES.map((page) => [page.name, page]))(
    'root opening the %s page lands on its default section',
    async (_name, page) => {
      useAuthStore.getState().auth.setUser(VIEWERS.root)
      const expected = `/system-settings/${page.name}/${page.meta.defaultSection}`
      expect(await landingPath(`/system-settings/${page.name}`)).toBe(expected)
      expect(
        await landingPath(`/system-settings/${page.name}/no-such-section`)
      ).toBe(expected)
    }
  )

  it('root opening System Settings lands on the first site section', async () => {
    useAuthStore.getState().auth.setUser(VIEWERS.root)
    expect(await landingPath('/system-settings')).toBe(
      '/system-settings/site/system-info'
    )
  })

  // Recharge and top-up settings live in billing (payment gateway, quota and
  // top-up link, check-in rewards); neither kind of admin opens any section.
  it.each(['admin', 'deputy'] as const)(
    'an %s is sent to 403 from every System Settings URL',
    async (kind) => {
      useAuthStore.getState().auth.setUser(VIEWERS[kind])
      const paths = [
        '/system-settings',
        ...PAGES.map((page) => `/system-settings/${page.name}`),
        ...SECTION_PATHS,
      ]
      expect(paths).toContain('/system-settings/billing/payment')
      for (const path of paths) {
        expect(await landingPath(path)).toBe('/403')
      }
    }
  )
})

describe('System Settings sidebar', () => {
  it('lists every section of every page with the registry titles and URLs', () => {
    const groups = SYSTEM_SETTINGS_VIEW.getNavGroups(t, { modelPricing: '3' })
    const pages = groups.flatMap((group) => group.items)
    expect(pages.map((page) => page.items)).toEqual(
      PAGES.map((page) =>
        page.registry.navItems(
          t,
          page.name === 'billing' ? { 'model-pricing': '3' } : undefined
        )
      )
    )
  })

  function MainSidebarUrls() {
    const { navGroups } = useSidebarView()
    const urls = navGroups
      .flatMap((group) => group.items)
      .flatMap((item) => [
        item.url,
        ...(item.items ?? []).map((sub) => sub.url),
      ])
    return (
      <ul aria-label='Sidebar URLs'>
        {urls.map((url) => (
          <li key={String(url)}>{String(url)}</li>
        ))}
      </ul>
    )
  }

  async function mainSidebarUrls(user: AuthUser) {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: [] },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    client.setQueryData(['status'], { SidebarModulesAdmin: '' })
    useAuthStore.getState().auth.setUser(user)
    const router = createRouter({
      routeTree: createRootRoute({ component: MainSidebarUrls }),
      history: createMemoryHistory({ initialEntries: ['/dashboard/overview'] }),
    })
    await router.load()
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
    const list = await screen.findByRole('list', { name: 'Sidebar URLs' })
    return Array.from(list.querySelectorAll('li'), (item) => item.textContent)
  }

  it('shows the System Settings entry to root', async () => {
    expect(await mainSidebarUrls(VIEWERS.root)).toContain(
      '/system-settings/site'
    )
  })

  it.each(['admin', 'deputy'] as const)(
    'shows no System Settings entry to an %s',
    async (kind) => {
      const urls = await mainSidebarUrls(VIEWERS[kind])
      expect(urls.length).toBeGreaterThan(0)
      expect(urls.some((url) => url?.startsWith('/system-settings'))).toBe(
        false
      )
    }
  )
})

// Importing a registry (it imports every section component) from a route file
// or from the sidebar config would pull all of System Settings back into the
// entry chunk or the layout chunk that every page loads.
describe('imports that keep System Settings out of the first load', () => {
  const SRC = path.resolve(
    path.dirname(fileURLToPath(import.meta.url)),
    '../../..'
  )

  function sourceFiles(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) return sourceFiles(full)
      return /\.(ts|tsx)$/.test(entry.name) ? [full] : []
    })
  }

  it('route files and the sidebar import section metadata, not registries', () => {
    const files = [
      ...sourceFiles(path.join(SRC, 'routes/_authenticated/system-settings')),
      path.join(SRC, 'components/layout/config/system-settings.config.ts'),
    ]
    expect(files.length).toBeGreaterThan(16)
    for (const file of files) {
      expect(readFileSync(file, 'utf8'), file).not.toMatch(
        /from '[^']*section-registry/
      )
    }
  })
})
