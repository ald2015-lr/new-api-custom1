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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { CHANNEL_TYPE_NOVELAI } from '../../constants'
import { channelSchema, type Channel } from '../../types'
import { BalanceCell } from '../channels-columns'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { BalanceQueryDialog } from '../dialogs/balance-query-dialog'

let client: QueryClient

function channelWithBalance(type: number, balance: number): Channel {
  return channelSchema.parse({
    id: 64,
    type,
    key: '',
    name: 'novelai',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance,
    balance_updated_time: 1,
  })
}

function renderBalance(type: number, balance: number) {
  const channel = channelWithBalance(type, balance)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <BalanceCell channel={channel} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  // A display currency other than USD must not convert Anlas.
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: 'CNY',
      quotaPerUnit: 500_000,
      usdExchangeRate: 7,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: [] } })
})

afterEach(() => {
  client.clear()
  localStorage.clear()
  vi.restoreAllMocks()
})

test('a NovelAI balance shows as Anlas rather than as an amount of money', () => {
  renderBalance(CHANNEL_TYPE_NOVELAI, 10250)

  expect(screen.getByText('10,250 Anlas')).toBeInTheDocument()
  // Only the used quota is money.
  expect(screen.getAllByText(/¥/).map((badge) => badge.textContent)).toEqual([
    '¥0',
  ])
})

test('a USD balance keeps the display currency', () => {
  renderBalance(1, 10)

  expect(screen.queryByText(/Anlas/)).not.toBeInTheDocument()
  expect(screen.getByText('¥70')).toBeInTheDocument()
})

test('a long Anlas balance shows compactly inline', () => {
  renderBalance(CHANNEL_TYPE_NOVELAI, 123_456_789)

  expect(screen.getByText('123.5M Anlas')).toBeInTheDocument()
})

test('updating a NovelAI balance reports the Anlas the account has', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/update_balance/64') {
      return { data: { success: true, message: '', balance: 250 } }
    }
    return { data: { success: true, data: [] } }
  })
  const success = vi.spyOn(toast, 'success')
  const user = userEvent.setup()
  renderBalance(CHANNEL_TYPE_NOVELAI, 10250)

  await user.click(screen.getByText('10,250 Anlas'))

  await waitFor(() =>
    expect(success).toHaveBeenCalledWith('Balance updated: 250 Anlas')
  )
  expect(
    get.mock.calls.some(([url]) => url === '/api/channel/update_balance/64')
  ).toBe(true)
})

function QueryBalance(props: { channel: Channel }) {
  const { setCurrentRow } = useChannels()
  useEffect(() => setCurrentRow(props.channel), [props.channel, setCurrentRow])
  return <BalanceQueryDialog open onOpenChange={vi.fn()} />
}

test('the balance dialog of a NovelAI channel queries and shows Anlas', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/update_balance/64') {
      return { data: { success: true, message: '', balance: 250 } }
    }
    return { data: { success: true, data: [] } }
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <QueryBalance
          channel={channelWithBalance(CHANNEL_TYPE_NOVELAI, 10250)}
        />
      </ChannelsProvider>
    </QueryClientProvider>
  )

  expect(await screen.findByText('10,250 Anlas')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Update Balance' }))

  expect(await screen.findByText('250 Anlas')).toBeInTheDocument()
})
