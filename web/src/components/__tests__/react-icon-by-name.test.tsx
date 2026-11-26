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
import { render, waitFor } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import type { IconType } from 'react-icons'
import { expect, test, vi } from 'vitest'

import { ReactIconByName } from '../react-icon-by-name'

// Count how often the component itself loads the two packs the payment
// presets come from; the tests below read the real packs with importActual,
// which does not run these factories.
const packLoads = vi.hoisted(() => ({ si: 0, lu: 0 }))
vi.mock('react-icons/si', async (importOriginal) => {
  packLoads.si += 1
  return importOriginal()
})
vi.mock('react-icons/lu', async (importOriginal) => {
  packLoads.lu += 1
  return importOriginal()
})

const PRESET_NAMES = ['SiAlipay', 'SiWechat', 'SiStripe', 'LuCreditCard']

test.each(PRESET_NAMES)(
  'the payment icon %s renders in the first render without loading its pack',
  (name) => {
    const { container } = render(
      <ReactIconByName name={name} className='h-4 w-4' title={name} />
    )

    expect(container.querySelector('svg')).not.toBeNull()
    expect(packLoads).toEqual({ si: 0, lu: 0 })
  }
)

test.each(PRESET_NAMES)(
  'the payment icon %s renders the same SVG as the react-icons component',
  async (name) => {
    const pack = await vi.importActual<Record<string, IconType>>(
      name.startsWith('Si') ? 'react-icons/si' : 'react-icons/lu'
    )
    const PackIcon = pack[name]

    expect(
      renderToStaticMarkup(
        <ReactIconByName name={` ${name} `} className='size-5' title='Pay' />
      )
    ).toBe(renderToStaticMarkup(<PackIcon className='size-5' title='Pay' />))
  }
)

test('an icon name outside the presets still renders after its pack loads', async () => {
  const { container } = render(<ReactIconByName name='FiCreditCard' />)

  expect(container.querySelector('svg')).toBeNull()
  await waitFor(() => expect(container.querySelector('svg')).not.toBeNull())
})
