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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { createInstance } from 'i18next'
import { useState } from 'react'
import { I18nextProvider } from 'react-i18next'
import { describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { formatQuota } from '@/lib/format'

import { AffiliateRewardsCard } from '../components/affiliate-rewards-card'
import { BonusGrantsCard } from '../components/bonus-grants-card'
import { TransferDialog } from '../components/dialogs/transfer-dialog'
import { transferThenRefresh } from '../lib'
import type {
  BonusGrant,
  BonusGrantTotals,
  SelfBonusSummary,
  UserWalletData,
} from '../types'

// 2026-06-21 UTC, mid-year so the rendered year does not depend on time zone.
const EXPIRES_AT = 1782000000

function grant(overrides: Partial<BonusGrant>): BonusGrant {
  return {
    id: 1,
    source: 'checkin_free',
    amount: 10000,
    remaining: 4000,
    created_at: EXPIRES_AT - 30 * 86400,
    expires_at: EXPIRES_AT,
    expire_days: 30,
    expire_only_when_unused: false,
    ...overrides,
  }
}

const user: UserWalletData = {
  id: 1,
  username: 'alice',
  quota: 0,
  used_quota: 0,
  request_count: 0,
  aff_quota: 250000,
  aff_history_quota: 250000,
  aff_count: 3,
  group: 'default',
}

function totalsOf(grants: BonusGrant[]): BonusGrantTotals {
  return {
    count: grants.length,
    remaining: grants.reduce((sum, item) => sum + item.remaining, 0),
  }
}

/** The server lists at most 100 rewards and totals every one of them. */
function cappedGrants(total: number): SelfBonusSummary {
  const rows = Array.from({ length: 100 }, (_, index) =>
    grant({ id: index + 1, remaining: 1000 })
  )
  return summary({
    wallet_grants: rows,
    wallet_totals: { count: total, remaining: total * 1000 },
  })
}

function summary(overrides: Partial<SelfBonusSummary>): SelfBonusSummary {
  const walletGrants = overrides.wallet_grants ?? []
  const affGrants = overrides.aff_grants ?? []
  return {
    recharged_quota: 0,
    checkin: {
      enabled: true,
      paid_threshold_quota: 1000000,
      paid_multiplier: 2,
      is_paid_user: false,
      free_expire_days: 30,
      paid_expire_days: 30,
      expire_only_when_unused: false,
    },
    invite: {
      threshold_quota: 0,
      expire_days: 30,
      expire_only_when_unused: false,
    },
    wallet_grants: walletGrants,
    wallet_totals: totalsOf(walletGrants),
    aff_grants: affGrants,
    aff_totals: totalsOf(affGrants),
    pending_invites: { count: 0, items: [] },
    own_invite_reward: null,
    ...overrides,
  }
}

function renderAffiliateCard(bonus: SelfBonusSummary) {
  render(
    <AffiliateRewardsCard
      user={user}
      affiliateLink='https://example.com/register?aff=abcd'
      onTransfer={vi.fn()}
      bonus={bonus}
    />
  )
}

describe('expiring rewards card', () => {
  test('two active grants render one row each', () => {
    render(
      <BonusGrantsCard
        bonus={summary({
          wallet_grants: [
            grant({ id: 1, source: 'checkin_paid' }),
            grant({ id: 2, source: 'invitee' }),
          ],
        })}
      />
    )
    const rows = within(screen.getByRole('list')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Paid check-in reward')).toBeVisible()
    expect(within(rows[1]).getByText('Invitation bonus')).toBeVisible()
  })

  test('no active grants show the empty state instead of a list', () => {
    render(<BonusGrantsCard bonus={summary({})} />)
    expect(screen.getByText('No expiring rewards')).toBeVisible()
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
  })

  test('no grants while every reward is permanent hides the card', () => {
    const { container } = render(
      <BonusGrantsCard
        bonus={summary({
          checkin: { ...summary({}).checkin, enabled: false },
          invite: {
            threshold_quota: 0,
            expire_days: 0,
            expire_only_when_unused: false,
          },
        })}
      />
    )
    expect(container).toBeEmptyDOMElement()
  })

  test('more than five grants show only the first five rows', () => {
    render(
      <BonusGrantsCard
        bonus={summary({
          wallet_grants: Array.from({ length: 7 }, (_, index) =>
            grant({ id: index + 1 })
          ),
        })}
      />
    )
    const rows = within(screen.getByRole('list')).getAllByRole('listitem')
    expect(rows).toHaveLength(5)
    expect(
      screen.getByRole('button', { name: 'Show all 7 rewards' })
    ).toHaveAttribute('aria-expanded', 'false')
  })

  test('the show-all toggle lists every grant', () => {
    render(
      <BonusGrantsCard
        bonus={summary({
          wallet_grants: Array.from({ length: 7 }, (_, index) =>
            grant({ id: index + 1 })
          ),
        })}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: 'Show all 7 rewards' }))
    expect(
      within(screen.getByRole('list')).getAllByRole('listitem')
    ).toHaveLength(7)
    expect(screen.getByRole('button', { name: 'Collapse' })).toHaveAttribute(
      'aria-expanded',
      'true'
    )
  })

  test('five grants or fewer show no toggle', () => {
    render(
      <BonusGrantsCard
        bonus={summary({
          wallet_grants: Array.from({ length: 5 }, (_, index) =>
            grant({ id: index + 1 })
          ),
        })}
      />
    )
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  test('the unspent total covers rewards beyond the listed rows', () => {
    render(<BonusGrantsCard bonus={cappedGrants(121)} />)
    expect(screen.getByText(formatQuota(121000))).toBeVisible()
  })

  test('a list cut short by the server says how many rewards it holds', () => {
    render(<BonusGrantsCard bonus={cappedGrants(121)} />)
    expect(
      screen.getByText('Only the first 100 of 121 rewards are listed.')
    ).toBeVisible()
  })

  test('the toggle of a list cut short does not promise every reward', () => {
    render(<BonusGrantsCard bonus={cappedGrants(121)} />)
    expect(
      screen.getByRole('button', { name: 'Show the first 100 rewards' })
    ).toBeVisible()
  })

  test('a failed load renders nothing', () => {
    const { container } = render(<BonusGrantsCard bonus={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('grant source labels are translated', async () => {
    // Provided through context only, so the shared test instance stays English.
    const i18n = createInstance()
    await i18n.init({
      lng: 'zh',
      fallbackLng: false,
      nsSeparator: false,
      resources: { en, zh },
      interpolation: { escapeValue: false },
    })
    render(
      <I18nextProvider i18n={i18n}>
        <BonusGrantsCard
          bonus={summary({
            wallet_grants: [grant({ source: 'checkin_paid' })],
          })}
        />
      </I18nextProvider>
    )
    expect(screen.getByText('付费签到奖励')).toBeVisible()
  })
})

describe('referral card invite rewards', () => {
  test('pending invites show how many invitees are below the threshold', () => {
    renderAffiliateCard(
      summary({
        invite: {
          threshold_quota: 500000,
          expire_days: 30,
          expire_only_when_unused: false,
        },
        pending_invites: {
          count: 3,
          items: [{ id: 7, created_at: EXPIRES_AT, inviter_quota: 250000 }],
        },
      })
    )
    expect(
      screen.getByText(
        /^3 invited users have not reached the recharge threshold yet/
      )
    ).toBeVisible()
  })

  test('pending invites without a recharge threshold do not mention one', () => {
    renderAffiliateCard(
      summary({
        pending_invites: {
          count: 2,
          items: [{ id: 7, created_at: EXPIRES_AT, inviter_quota: 250000 }],
        },
      })
    )
    expect(
      screen.getByText('2 invite rewards are waiting to be granted')
    ).toBeVisible()
    expect(screen.queryByText(/recharge threshold/)).not.toBeInTheDocument()
  })

  test('the own pending invitation bonus shows its recharge threshold', () => {
    renderAffiliateCard(
      summary({
        own_invite_reward: {
          status: 'pending',
          invitee_quota: 250000,
          threshold_quota: 500000,
        },
      })
    )
    expect(
      screen.getByText(
        /^Your invitation bonus of .+ is granted once your total recharge reaches .+\.$/
      )
    ).toBeVisible()
  })

  test('an inviter reward that expires only when unused counts transfers as use', () => {
    renderAffiliateCard(
      summary({
        aff_grants: [
          grant({ source: 'inviter', expire_only_when_unused: true }),
        ],
      })
    )
    expect(
      screen.getByText('Expires if not transferred for 30 days')
    ).toBeVisible()
  })

  test('no pending or expiring invite rewards add no invite details', () => {
    renderAffiliateCard(summary({}))
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
    expect(screen.queryByText(/invited users/)).not.toBeInTheDocument()
  })
})

function renderTransferDialog(
  availableQuota: number,
  onConfirm = vi.fn().mockResolvedValue(true)
) {
  render(
    <TransferDialog
      open
      onOpenChange={vi.fn()}
      onConfirm={onConfirm}
      availableQuota={availableQuota}
      transferring={false}
    />
  )
  return onConfirm
}

describe('transfer dialog', () => {
  test('expiring invite rewards add the transferred-first note', () => {
    render(
      <TransferDialog
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
        availableQuota={1000000}
        transferring={false}
        hasExpiringRewards
      />
    )
    expect(
      screen.getByText('Expiring rewards are transferred first')
    ).toBeVisible()
  })

  test('a balance below the transfer minimum is transferred in full', () => {
    // The default minimum is one unit: 500000 quota.
    const onConfirm = renderTransferDialog(250000)
    fireEvent.click(screen.getByRole('button', { name: 'Transfer' }))
    expect(onConfirm).toHaveBeenCalledWith(250000)
  })

  test('a balance below the transfer minimum explains the whole-balance rule', () => {
    renderTransferDialog(250000)
    expect(
      screen.getByText(
        /^Below the .+ minimum, the whole invitation balance is transferred at once\.$/
      )
    ).toBeVisible()
  })

  test('a balance below the transfer minimum cannot be edited to a part of it', () => {
    renderTransferDialog(250000)
    expect(screen.getByRole('spinbutton')).toHaveAttribute('readonly')
  })

  test('a rejected transfer re-reads the balance for the next attempt', async () => {
    const transfer = vi.fn().mockResolvedValue(false)
    function StaleBalanceDialog() {
      // The page loaded 250000; another invite reward was granted since.
      const [availableQuota, setAvailableQuota] = useState(250000)
      return (
        <TransferDialog
          open
          onOpenChange={vi.fn()}
          onConfirm={(quota) =>
            transferThenRefresh(
              transfer,
              async () => setAvailableQuota(350000),
              quota
            )
          }
          availableQuota={availableQuota}
          transferring={false}
        />
      )
    }
    render(<StaleBalanceDialog />)
    fireEvent.click(screen.getByRole('button', { name: 'Transfer' }))
    await waitFor(() => expect(transfer).toHaveBeenCalledWith(250000))
    fireEvent.click(screen.getByRole('button', { name: 'Transfer' }))
    await waitFor(() => expect(transfer).toHaveBeenLastCalledWith(350000))
  })

  test('a balance above the transfer minimum starts at the minimum', () => {
    const onConfirm = renderTransferDialog(1000000)
    fireEvent.click(screen.getByRole('button', { name: 'Transfer' }))
    expect(onConfirm).toHaveBeenCalledWith(500000)
  })
})
