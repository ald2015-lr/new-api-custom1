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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { InviteAbusePreview, UpdateOptionRequest } from '../../types'
import { BasicAuthSection } from '../basic-auth-section'
import { InviteAbuseSection } from '../invite-abuse-section'

const clients: QueryClient[] = []

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

async function renderSection(section: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Fixture>{section}</Fixture>,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

function savedOptions() {
  return vi
    .mocked(api.put)
    .mock.calls.map(([, body]) => body as UpdateOptionRequest)
}

const inviteAbuseDefaults = {
  InviteAbuseBanEnabled: false,
  InviteAbuseBanThreshold: 5,
  InviteAbuseBanMinRedeemQuota: 500000,
  InviteAbuseBanExemptPaidInviter: true,
}

const preview: InviteAbusePreview = {
  threshold: 8,
  min_redeem_quota: 1000000,
  exempt_paid_inviter: true,
  abusive_inviters: 12,
  inviters_to_disable: 10,
  invitees_to_disable: 340,
  items: [
    {
      id: 88996,
      username: 'farmer',
      display_name: 'Farm owner',
      status: 1,
      invite_count: 57,
      invitees_to_disable: 55,
    },
  ],
  truncated: true,
}

function mockPreview(data: InviteAbusePreview) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/invite_abuse/preview') {
      return { data: { success: true, message: '', data } }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
}

/** The preview totals shown in a dialog, as [label, value] pairs. */
function totalsIn(dialog: HTMLElement) {
  return within(dialog)
    .getAllByRole('term')
    .map((term) => [term.textContent, term.nextElementSibling?.textContent])
}

const previewTotals = [
  ['Inviters matching the rule', '12'],
  ['Inviters to disable', '10'],
  ['Invited users to disable', '340'],
]

async function replaceNumber(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  value: string
) {
  const input = screen.getByRole('spinbutton', { name })
  await user.clear(input)
  await user.type(input, value)
  return input
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, message: '' },
  })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
})

describe('QQ mail rule in basic authentication', () => {
  it('saves turning the rule off and drops the cached sign-up status', async () => {
    const user = userEvent.setup()
    localStorage.setItem(
      'status',
      JSON.stringify({ email_qq_numeric_only: true })
    )
    await renderSection(
      <BasicAuthSection
        defaultValues={{
          PasswordLoginEnabled: true,
          PasswordRegisterEnabled: true,
          EmailVerificationEnabled: true,
          RegisterEnabled: true,
          EmailDomainRestrictionEnabled: true,
          EmailAliasRestrictionEnabled: true,
          EmailQQNumericOnlyEnabled: true,
          EmailDomainWhitelist: 'qq.com',
        }}
      />
    )

    await user.click(
      screen.getByRole('switch', { name: 'QQ Mail: QQ number only' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'EmailQQNumericOnlyEnabled', value: false },
      ])
    )
    await waitFor(() => expect(localStorage.getItem('status')).toBeNull())
  })
})

describe('Invite abuse protection settings', () => {
  it('previews the unsaved threshold and quota without saving them', async () => {
    const user = userEvent.setup()
    const get = mockPreview(preview)
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    await replaceNumber(user, 'Invite count threshold', '8')
    await replaceNumber(user, 'Minimum redeemed quota', '1000000')
    await user.click(screen.getByRole('button', { name: 'Preview' }))

    const dialog = await screen.findByRole('dialog', {
      name: 'Invite abuse preview',
    })
    expect(get).toHaveBeenCalledWith('/api/user/invite_abuse/preview', {
      params: {
        threshold: 8,
        min_redeem_quota: 1000000,
        exempt_paid_inviter: true,
      },
      disableDuplicate: true,
    })
    expect(api.put).not.toHaveBeenCalled()
    expect(totalsIn(dialog)).toEqual(previewTotals)
    const row = within(dialog).getByRole('row', { name: /farmer/ })
    expect(within(row).getByText('88996')).toBeInTheDocument()
    expect(within(row).getByText('Farm owner')).toBeInTheDocument()
    expect(within(row).getByText('57')).toBeInTheDocument()
    expect(within(row).getByText('55')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Only the 1 inviters with the most invites are listed.'
      )
    ).toBeInTheDocument()
  })

  it('previews with the unsaved paid inviter exemption and shows the one applied', async () => {
    const user = userEvent.setup()
    const get = mockPreview({ ...preview, exempt_paid_inviter: false })
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    await user.click(
      screen.getByRole('switch', { name: 'Exempt inviters who recharged' })
    )
    await user.click(screen.getByRole('button', { name: 'Preview' }))

    const dialog = await screen.findByRole('dialog', {
      name: 'Invite abuse preview',
    })
    expect(get).toHaveBeenCalledWith('/api/user/invite_abuse/preview', {
      params: {
        threshold: 5,
        min_redeem_quota: 500000,
        exempt_paid_inviter: false,
      },
      disableDuplicate: true,
    })
    expect(dialog).toHaveTextContent('Inviters who recharged are not exempt')
    expect(api.put).not.toHaveBeenCalled()
  })

  it('says so when no inviter matches the previewed rule', async () => {
    const user = userEvent.setup()
    mockPreview({
      ...preview,
      abusive_inviters: 0,
      inviters_to_disable: 0,
      invitees_to_disable: 0,
      items: [],
      truncated: false,
    })
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    await user.click(screen.getByRole('button', { name: 'Preview' }))

    const dialog = await screen.findByRole('dialog', {
      name: 'Invite abuse preview',
    })
    expect(
      within(dialog).getByText('No inviter matches the rule.')
    ).toBeInTheDocument()
    expect(within(dialog).queryByRole('table')).not.toBeInTheDocument()
  })

  it('refuses a threshold of 0 without previewing or saving', async () => {
    const user = userEvent.setup()
    const get = mockPreview(preview)
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    const input = await replaceNumber(user, 'Invite count threshold', '0')
    await user.click(screen.getByRole('button', { name: 'Preview' }))

    expect(
      await screen.findByText(
        'Invite count threshold must be an integer between 1 and 100000'
      )
    ).toBeInTheDocument()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(get).not.toHaveBeenCalled()
  })

  it('shows what turning the ban on disables before it can be confirmed, then saves the new threshold first', async () => {
    const user = userEvent.setup()
    let releasePreview = () => {}
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url !== '/api/user/invite_abuse/preview') {
        throw new Error(`Unexpected GET ${url}`)
      }
      await new Promise<void>((resolve) => {
        releasePreview = resolve
      })
      return { data: { success: true, message: '', data: preview } }
    })
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    await replaceNumber(user, 'Invite count threshold', '10')
    await user.click(
      screen.getByRole('switch', { name: 'Ban invite abuse automatically' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    const confirm = await screen.findByRole('alertdialog', {
      name: 'Turn on automatic invite abuse bans?',
    })
    expect(get).toHaveBeenCalledWith('/api/user/invite_abuse/preview', {
      params: {
        threshold: 10,
        min_redeem_quota: 500000,
        exempt_paid_inviter: true,
      },
      disableDuplicate: true,
    })
    const turnOn = within(confirm).getByRole('button', { name: 'Turn on' })
    expect(turnOn).toBeDisabled()
    releasePreview()
    await waitFor(() => expect(totalsIn(confirm)).toEqual(previewTotals))
    expect(turnOn).toBeEnabled()
    expect(api.put).not.toHaveBeenCalled()

    await user.click(turnOn)

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'InviteAbuseBanThreshold', value: 10 },
        { key: 'InviteAbuseBanEnabled', value: true },
      ])
    )
  })

  it('saves nothing when turning the ban on is cancelled', async () => {
    const user = userEvent.setup()
    mockPreview(preview)
    await renderSection(
      <InviteAbuseSection defaultValues={inviteAbuseDefaults} />
    )

    await user.click(
      screen.getByRole('switch', { name: 'Ban invite abuse automatically' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Cancel' }))

    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(api.put).not.toHaveBeenCalled()
  })

  it.each([
    {
      change: 'a lower threshold',
      edit: (user: ReturnType<typeof userEvent.setup>) =>
        replaceNumber(user, 'Invite count threshold', '3'),
      params: { threshold: 3 },
      saved: { key: 'InviteAbuseBanThreshold', value: 3 },
    },
    {
      change: 'a higher minimum redeemed quota',
      edit: (user: ReturnType<typeof userEvent.setup>) =>
        replaceNumber(user, 'Minimum redeemed quota', '2000000'),
      params: { min_redeem_quota: 2000000 },
      saved: { key: 'InviteAbuseBanMinRedeemQuota', value: 2000000 },
    },
    {
      change: 'the paid inviter exemption turned off',
      edit: (user: ReturnType<typeof userEvent.setup>) =>
        user.click(
          screen.getByRole('switch', { name: 'Exempt inviters who recharged' })
        ),
      params: { exempt_paid_inviter: false },
      saved: { key: 'InviteAbuseBanExemptPaidInviter', value: false },
    },
  ])(
    'asks before saving $change while the ban is on and shows what the save disables',
    async ({ edit, params, saved }) => {
      const user = userEvent.setup()
      const get = mockPreview(preview)
      await renderSection(
        <InviteAbuseSection
          defaultValues={{
            ...inviteAbuseDefaults,
            InviteAbuseBanEnabled: true,
          }}
        />
      )

      await edit(user)
      await user.click(screen.getByRole('button', { name: 'Save Changes' }))

      const confirm = await screen.findByRole('alertdialog', {
        name: 'Save the changed invite abuse rule?',
      })
      expect(get).toHaveBeenCalledWith('/api/user/invite_abuse/preview', {
        params: {
          threshold: 5,
          min_redeem_quota: 500000,
          exempt_paid_inviter: true,
          ...params,
        },
        disableDuplicate: true,
      })
      await waitFor(() => expect(totalsIn(confirm)).toEqual(previewTotals))
      expect(api.put).not.toHaveBeenCalled()

      await user.click(within(confirm).getByRole('button', { name: 'Save' }))

      await waitFor(() => expect(savedOptions()).toEqual([saved]))
    }
  )

  it('lets the admin confirm with a warning when the preview of the save fails', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'get').mockRejectedValue(new Error('Network Error'))
    const error = vi.spyOn(toast, 'error')
    await renderSection(
      <InviteAbuseSection
        defaultValues={{ ...inviteAbuseDefaults, InviteAbuseBanEnabled: true }}
      />
    )

    await replaceNumber(user, 'Invite count threshold', '3')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    const confirm = await screen.findByRole('alertdialog', {
      name: 'Save the changed invite abuse rule?',
    })
    expect(
      await within(confirm).findByText(
        'The preview failed, so it is unknown how many accounts this disables. Saving can still disable many accounts at once.'
      )
    ).toBeInTheDocument()
    expect(error).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'InviteAbuseBanThreshold', value: 3 },
      ])
    )
  })

  it('turns the ban off before saving a lower threshold in the same save', async () => {
    const user = userEvent.setup()
    await renderSection(
      <InviteAbuseSection
        defaultValues={{
          ...inviteAbuseDefaults,
          InviteAbuseBanEnabled: true,
          InviteAbuseBanThreshold: 50,
        }}
      />
    )

    await replaceNumber(user, 'Invite count threshold', '5')
    await user.click(
      screen.getByRole('switch', { name: 'Ban invite abuse automatically' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'InviteAbuseBanEnabled', value: false },
        { key: 'InviteAbuseBanThreshold', value: 5 },
      ])
    )
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('shows the error of a failed second save once and leaves no unhandled rejection', async () => {
    const user = userEvent.setup()
    vi.mocked(api.put)
      .mockResolvedValueOnce({ data: { success: true, message: '' } })
      .mockRejectedValueOnce(new Error('Network Error'))
    const error = vi.spyOn(toast, 'error')
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    try {
      await renderSection(
        <InviteAbuseSection
          defaultValues={{
            ...inviteAbuseDefaults,
            InviteAbuseBanEnabled: true,
            InviteAbuseBanThreshold: 50,
          }}
        />
      )

      await replaceNumber(user, 'Invite count threshold', '5')
      await user.click(
        screen.getByRole('switch', { name: 'Ban invite abuse automatically' })
      )
      await user.click(screen.getByRole('button', { name: 'Save Changes' }))

      await waitFor(() => expect(error).toHaveBeenCalledWith('Network Error'))
      // Node reports a rejection nobody handled after the current task.
      await new Promise((resolve) => setImmediate(resolve))
      expect(unhandled).not.toHaveBeenCalled()
      expect(error).toHaveBeenCalledTimes(1)
      expect(savedOptions()).toEqual([
        { key: 'InviteAbuseBanEnabled', value: false },
        { key: 'InviteAbuseBanThreshold', value: 5 },
      ])
    } finally {
      process.off('unhandledRejection', unhandled)
    }
  })
})
