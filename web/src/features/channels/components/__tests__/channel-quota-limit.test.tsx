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
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  channelSchema,
  type Channel,
  type ChannelQuotaLimit,
} from '../../types'
import { useChannelsColumns } from '../channels-columns'
import { ChannelsProvider } from '../channels-provider'
import { ChannelQuotaLimitDialog } from '../dialogs/channel-quota-limit-dialog'

const CHANNEL_ID = 7
// 500,000 quota = 1 USD; at 7 CNY per USD, 5,000,000 quota = ¥70.
const QUOTA_PER_UNIT = 500_000
const CNY_PER_USD = 7

const clients: QueryClient[] = []

function channel(overrides: Partial<Channel> = {}): Channel {
  return channelSchema.parse({
    id: CHANNEL_ID,
    type: 1,
    key: '',
    name: 'prod-openai',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    ...overrides,
  })
}

function quotaLimit(
  overrides: Partial<ChannelQuotaLimit> = {}
): ChannelQuotaLimit {
  return {
    channel_id: CHANNEL_ID,
    limit_quota: 5_000_000,
    daily_reset: false,
    message: '',
    used_quota: 1_000_000,
    current_used: 1_000_000,
    exhausted: false,
    period_start: 0,
    updated_at: 1,
    ...overrides,
  }
}

function mockQuotaLimits(limits: ChannelQuotaLimit[]) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/quota_limits') {
      return { data: { success: true, data: limits } }
    }
    return { data: { success: true, data: [] } }
  })
}

function createClient(): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return client
}

function renderDialog(onOpenChange = vi.fn()) {
  render(
    <QueryClientProvider client={createClient()}>
      <ChannelQuotaLimitDialog
        channel={channel()}
        onOpenChange={onOpenChange}
      />
    </QueryClientProvider>
  )
  return onOpenChange
}

async function findLimitInput(): Promise<HTMLInputElement> {
  return (await screen.findByLabelText('Usage limit (CNY)')) as HTMLInputElement
}

function ChannelCells(props: { channel: Channel; columnIds: string[] }) {
  const table = useReactTable({
    data: [props.channel],
    columns: useChannelsColumns({ enableSelection: false }),
    getCoreRowModel: getCoreRowModel(),
  })
  const cells =
    table
      .getRowModel()
      .rows[0]?.getAllCells()
      .filter((item) => props.columnIds.includes(item.column.id)) ?? []
  return (
    <>
      {cells.map((cell) => (
        <div key={cell.id} data-testid={`cell-${cell.column.id}`}>
          {flexRender(cell.column.columnDef.cell, cell.getContext())}
        </div>
      ))}
    </>
  )
}

function renderChannelCells(columnIds: string[]) {
  render(
    <QueryClientProvider client={createClient()}>
      <ChannelsProvider>
        <ChannelCells channel={channel()} columnIds={columnIds} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: 'CNY',
      quotaPerUnit: QUOTA_PER_UNIT,
      usdExchangeRate: CNY_PER_USD,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('channel quota limit dialog', () => {
  test('shows the stored limit in the display currency and saves the edited amount as quota', async () => {
    mockQuotaLimits([quotaLimit()])
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: quotaLimit() } })
    const user = userEvent.setup()
    const onOpenChange = renderDialog()

    const input = await findLimitInput()
    expect(input).toHaveValue(70)
    await user.clear(input)
    await user.type(input, '140')
    await user.click(screen.getByRole('switch', { name: 'Reset daily' }))
    await user.type(
      screen.getByLabelText('Error message when the limit is reached'),
      '  Group quota is used up  '
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0][0]).toBe(`/api/channel/${CHANNEL_ID}/quota_limit`)
    expect(put.mock.calls[0][1]).toEqual({
      limit_quota: 10_000_000,
      daily_reset: true,
      message: 'Group quota is used up',
    })
  })

  test('sends limit_quota 0 to remove the limit when the amount is set to 0', async () => {
    mockQuotaLimits([quotaLimit({ daily_reset: true, message: 'custom' })])
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: null } })
    const user = userEvent.setup()
    const onOpenChange = renderDialog()

    const input = await findLimitInput()
    await user.clear(input)
    await user.type(input, '0')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(put.mock.calls[0][1]).toEqual({
      limit_quota: 0,
      daily_reset: true,
      message: 'custom',
    })
  })

  test('marks a negative amount invalid and does not save it', async () => {
    mockQuotaLimits([])
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    renderDialog()

    const input = await findLimitInput()
    expect(input).toHaveValue(null)
    await user.type(input, '-5')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(
      screen.getByText('Must be greater than or equal to 0')
    ).toBeInTheDocument()
    expect(put).not.toHaveBeenCalled()
  })

  test('calls the reset endpoint only after the reset is confirmed', async () => {
    mockQuotaLimits([quotaLimit({ current_used: 5_000_000, exhausted: true })])
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: true, data: quotaLimit({ current_used: 0 }) },
    })
    const user = userEvent.setup()
    renderDialog()

    await user.click(await screen.findByRole('button', { name: 'Reset usage' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(post).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Reset' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe(
      `/api/channel/${CHANNEL_ID}/quota_limit/reset`
    )
  })
})

describe('channel table cells', () => {
  test('adds a limit reached badge next to the status when the quota limit is exhausted', async () => {
    mockQuotaLimits([quotaLimit({ current_used: 5_000_000, exhausted: true })])

    renderChannelCells(['status'])

    const status = screen.getByTestId('cell-status')
    expect(await within(status).findByText('Limit Reached')).toBeInTheDocument()
    expect(within(status).getByText('Enabled')).toBeInTheDocument()
  })

  test('keeps only the status badge while the quota limit is not exhausted', async () => {
    mockQuotaLimits([quotaLimit()])

    renderChannelCells(['status', 'balance'])

    expect(
      await within(screen.getByTestId('cell-balance')).findByText(/^Limit /)
    ).toBeInTheDocument()
    const status = screen.getByTestId('cell-status')
    expect(within(status).getByText('Enabled')).toBeInTheDocument()
    expect(within(status).queryByText('Limit Reached')).not.toBeInTheDocument()
  })

  test('shows the limit usage in the display currency with a daily marker', async () => {
    mockQuotaLimits([quotaLimit({ daily_reset: true })])

    renderChannelCells(['balance'])

    const balance = screen.getByTestId('cell-balance')
    expect(
      await within(balance).findByText('Limit ¥14 / ¥70')
    ).toBeInTheDocument()
    expect(within(balance).getByText('Daily')).toBeInTheDocument()
  })
})
