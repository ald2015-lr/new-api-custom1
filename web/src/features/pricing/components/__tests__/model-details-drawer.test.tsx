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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import type { PricingModel } from '../../types'

vi.mock('../../lib/model-details-chunks', () => ({
  loadModelDetails: vi.fn(),
}))

/** A fresh drawer module: its lazy details keep what they loaded. */
async function importDrawer() {
  vi.resetModules()
  const chunks = await import('../../lib/model-details-chunks')
  const { ModelDetailsDrawer } = await import('../model-details-drawer')
  return {
    ModelDetailsDrawer,
    loadModelDetails: vi.mocked(chunks.loadModelDetails),
  }
}

const CHUNK_URL = 'http://localhost/static/js/async/66.js'

function chunkLoadError(): Error {
  const error = new Error(`Loading chunk 66 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

const detailsChunk = {
  ModelDetailsContent: (props: { model: PricingModel }) => (
    <p>details of {props.model.model_name}</p>
  ),
}

const model = {
  id: 1,
  model_name: 'alpha-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
} as PricingModel

function drawer(
  ModelDetailsDrawer: Awaited<
    ReturnType<typeof importDrawer>
  >['ModelDetailsDrawer'],
  open: boolean
) {
  return (
    <ModelDetailsDrawer
      open={open}
      onOpenChange={() => undefined}
      model={model}
      groupRatio={{ default: 1 }}
      usableGroup={{}}
      endpointMap={{}}
      autoGroups={[]}
      priceRate={1}
      usdExchangeRate={1}
      tokenUnit='M'
    />
  )
}

beforeEach(() => {
  // This tab already spent its one automatic reload on the details chunk.
  window.sessionStorage.setItem(`newapi:chunk-reload:${CHUNK_URL}`, '1')
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  window.sessionStorage.clear()
  vi.restoreAllMocks()
})

test('details whose chunk failed to load offer a retry that loads them', async () => {
  const user = userEvent.setup()
  const { ModelDetailsDrawer, loadModelDetails } = await importDrawer()
  loadModelDetails
    .mockRejectedValueOnce(chunkLoadError())
    .mockResolvedValue(detailsChunk as never)
  render(drawer(ModelDetailsDrawer, true))

  expect(
    await screen.findByText('Failed to load part of the page')
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Retry' }))

  expect(await screen.findByText('details of alpha-model')).toBeVisible()
  expect(loadModelDetails).toHaveBeenCalledTimes(2)
})

test('details whose chunk failed to load load again when the drawer opens again', async () => {
  const { ModelDetailsDrawer, loadModelDetails } = await importDrawer()
  loadModelDetails
    .mockRejectedValueOnce(chunkLoadError())
    .mockResolvedValue(detailsChunk as never)
  const { rerender } = render(drawer(ModelDetailsDrawer, true))
  expect(
    await screen.findByText('Failed to load part of the page')
  ).toBeVisible()

  rerender(drawer(ModelDetailsDrawer, false))
  await waitFor(() =>
    expect(
      screen.queryByText('Failed to load part of the page')
    ).not.toBeInTheDocument()
  )
  rerender(drawer(ModelDetailsDrawer, true))

  expect(await screen.findByText('details of alpha-model')).toBeVisible()
  expect(loadModelDetails).toHaveBeenCalledTimes(2)
})
