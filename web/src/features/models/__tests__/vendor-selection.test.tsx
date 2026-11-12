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
import { AxiosError } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelMutateDrawer } from '../components/drawers/model-mutate-drawer'
import type { BuiltinVendor, Vendor } from '../types'

const builtinVendors: BuiltinVendor[] = [
  { name: 'OpenAI', icon: 'OpenAI' },
  { name: 'Google', icon: 'Gemini.Color' },
  { name: 'NovelAI', icon: 'NovelAI' },
]

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

const duplicateName = {
  success: false,
  message: 'vendor name already exists',
} as const

// Serves the vendor list and saves vendors and models the way the API does.
function mockVendorApi(createFailure?: 'business' | 'http') {
  let saved: Partial<Vendor>[] = [{ id: 3, name: 'OpenAI', icon: 'OpenAI' }]
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url !== '/api/vendors/') throw new Error(`Unexpected GET ${url}`)
    return {
      data: {
        success: true,
        data: {
          items: saved,
          total: saved.length,
          builtin_vendors: builtinVendors,
        },
      },
    }
  })
  return vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    if (url === '/api/vendors/') {
      if (createFailure === 'business') return { data: duplicateName }
      if (createFailure === 'http') {
        // The vendor API answers a rejected name with HTTP 400.
        const error = new AxiosError('Request failed with status code 400')
        error.response = {
          data: duplicateName,
          status: 400,
          statusText: 'Bad Request',
          headers: {},
          config: { headers: {} },
        } as typeof error.response
        throw error
      }
      const vendor = { id: 12, ...(body as Partial<Vendor>) }
      saved = [...saved, vendor]
      return { data: { success: true, data: vendor } }
    }
    if (url === '/api/models/') {
      return { data: { success: true, data: { ...(body as object), id: 30 } } }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
}

async function renderNewModel(name: string) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelMutateDrawer open onOpenChange={() => {}} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Model Name *'), name)
  const vendor = screen.getByRole('combobox', { name: 'Vendor' })
  await user.click(vendor)
  return { user, vendor }
}

describe('vendor selection in the model editor', () => {
  it('offers the built-in NovelAI vendor without a saved record, saves it with its icon when chosen, and stores the saved ID', async () => {
    const post = mockVendorApi()
    const { user, vendor } = await renderNewModel('nai-diffusion')

    // A saved vendor hides the built-in entry of the same name.
    expect(
      await screen.findAllByRole('option', { name: /^OpenAI/ })
    ).toHaveLength(1)
    await user.click(screen.getByRole('option', { name: /^NovelAI/ }))
    await waitFor(() => expect(vendor).toHaveValue('NovelAI'))
    expect(post).toHaveBeenCalledWith('/api/vendors/', {
      name: 'NovelAI',
      icon: 'NovelAI',
    })

    await user.click(screen.getByRole('button', { name: 'Save metadata' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/models/',
        expect.objectContaining({ model_name: 'nai-diffusion', vendor_id: 12 }),
        { skipBusinessError: true, skipErrorHandler: true }
      )
    )
  })

  it('narrows the built-in vendors to the names that contain the typed text', async () => {
    mockVendorApi()
    const { user, vendor } = await renderNewModel('nai-diffusion')

    await user.type(vendor, 'n')
    expect(
      await screen.findByRole('option', { name: /^NovelAI/ })
    ).toBeVisible()
    expect(
      screen.queryByRole('option', { name: /^Google/ })
    ).not.toBeInTheDocument()
  })

  it('creates a typed vendor name that no vendor has in any case and selects it', async () => {
    const post = mockVendorApi()
    const { user, vendor } = await renderNewModel('acme-model')

    for (const existing of ['openai', ' NOVELAI ']) {
      await user.clear(vendor)
      await user.type(vendor, existing)
      expect(
        screen.queryByRole('option', { name: /^Create vendor/ })
      ).not.toBeInTheDocument()
    }
    await user.clear(vendor)
    await user.type(vendor, ' Acme AI ')
    await user.click(
      screen.getByRole('option', { name: 'Create vendor "Acme AI"' })
    )
    await waitFor(() => expect(vendor).toHaveValue('Acme AI'))
    expect(post).toHaveBeenCalledExactlyOnceWith('/api/vendors/', {
      name: 'Acme AI',
    })

    await user.click(screen.getByRole('button', { name: 'Save metadata' }))
    await waitFor(() =>
      expect(post).toHaveBeenLastCalledWith(
        '/api/models/',
        expect.objectContaining({ model_name: 'acme-model', vendor_id: 12 }),
        { skipBusinessError: true, skipErrorHandler: true }
      )
    )
  })

  it.each(['business', 'http'] as const)(
    'shows the server reason under the vendor field after a %s rejection and keeps the vendor unset',
    async (failure) => {
      const post = mockVendorApi(failure)
      const { user, vendor } = await renderNewModel('acme-model')

      await user.type(vendor, 'Acme AI')
      await user.click(
        screen.getByRole('option', { name: 'Create vendor "Acme AI"' })
      )
      expect(
        await screen.findByText('A vendor with this name already exists.')
      ).toBeVisible()
      expect(vendor).toHaveAttribute('aria-invalid', 'true')
      expect(vendor).toHaveValue('')
      expect(post).not.toHaveBeenCalledWith(
        '/api/models/',
        expect.anything(),
        expect.anything()
      )
    }
  )
})
