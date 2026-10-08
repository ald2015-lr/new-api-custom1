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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { GlobalSettingsCard } from '../global-settings-card'

const NON_STREAM_SWITCH = { name: 'Keep-alive for non-streaming requests' }
const MASTER_SWITCH = { name: 'Keep-alive Ping' }
const INTERVAL_INPUT = { name: 'Ping Interval (seconds)' }

const PROXY_TIMEOUT_WARNING =
  'Proxies such as nginx and AWS load balancers close requests that stay idle for 60 seconds by default, so an interval of 60 seconds or more fires too late. Use 10–30 seconds.'

function Fixture(props: { pingEnabled: boolean; pingIntervalSeconds: number }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <GlobalSettingsCard
          defaultValues={{
            global: {
              pass_through_request_enabled: false,
              thinking_model_blacklist: '[]',
              chat_completions_to_responses_policy: '{}',
            },
            general_setting: {
              ping_interval_enabled: props.pingEnabled,
              ping_interval_seconds: props.pingIntervalSeconds,
              non_stream_ping_enabled: false,
            },
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

async function renderSettings(pingEnabled: boolean, pingIntervalSeconds = 10) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <Fixture
          pingEnabled={pingEnabled}
          pingIntervalSeconds={pingIntervalSeconds}
        />
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('switch', NON_STREAM_SWITCH)
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('non-stream keep-alive switch is disabled while the master ping switch is off', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings(false)
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
})

test('turning the master ping switch on enables the non-stream keep-alive switch', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings(false)
  await user.click(screen.getByRole('switch', MASTER_SWITCH))
  await waitFor(() => expect(toggle).not.toHaveAttribute('aria-disabled'))
})

test('enabling non-stream keep-alive saves only general_setting.non_stream_ping_enabled', async () => {
  const user = userEvent.setup()
  const toggle = await renderSettings(true)
  expect(toggle).not.toBeChecked()
  await user.click(toggle)
  expect(toggle).toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.non_stream_ping_enabled',
      value: true,
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test.each([
  ['3601', 'Ping interval must be between 1 and 3600 seconds'],
  ['0', 'Ping interval must be between 1 and 3600 seconds'],
  ['2.5', 'Must be a whole number'],
])(
  'invalid ping interval "%s" shows a field error and prevents saving',
  async (value, message) => {
    await renderSettings(true)
    const input = screen.getByRole('spinbutton', INTERVAL_INPUT)
    fireEvent.change(input, { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.getByText(message)).toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('ping interval of 3600 seconds is accepted and saved', async () => {
  await renderSettings(true)
  const input = screen.getByRole('spinbutton', INTERVAL_INPUT)
  fireEvent.change(input, { target: { value: '3600' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.ping_interval_seconds',
      value: 3600,
    })
  )
})

test('a stored out-of-range interval does not block saving while ping is off', async () => {
  const user = userEvent.setup()
  await renderSettings(false, 7200)
  await user.click(
    screen.getByRole('switch', { name: 'Enable Request Passthrough' })
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'global.pass_through_request_enabled',
      value: true,
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('an invalid interval typed while ping was on is left unsaved after ping is turned off', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  fireEvent.change(screen.getByRole('spinbutton', INTERVAL_INPUT), {
    target: { value: '0' },
  })
  await user.click(screen.getByRole('switch', MASTER_SWITCH))
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'general_setting.ping_interval_enabled',
      value: false,
    })
  )
  expect(api.put).toHaveBeenCalledTimes(1)
})

test('warns when the interval is as long as common proxy idle timeouts', async () => {
  await renderSettings(true, 60)
  expect(screen.getByText(PROXY_TIMEOUT_WARNING)).toBeInTheDocument()
  fireEvent.change(screen.getByRole('spinbutton', INTERVAL_INPUT), {
    target: { value: '30' },
  })
  await waitFor(() =>
    expect(screen.queryByText(PROXY_TIMEOUT_WARNING)).not.toBeInTheDocument()
  )
})

test('turning ping off clears an interval error it no longer applies to', async () => {
  const user = userEvent.setup()
  await renderSettings(true)
  const input = screen.getByRole('spinbutton', INTERVAL_INPUT)
  fireEvent.change(input, { target: { value: '0' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
  await user.click(screen.getByRole('switch', MASTER_SWITCH))
  await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'false'))
  expect(
    screen.queryByText('Ping interval must be between 1 and 3600 seconds')
  ).not.toBeInTheDocument()
})
