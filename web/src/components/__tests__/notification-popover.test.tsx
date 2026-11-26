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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

// Counts downloads of the markdown renderer chunk (the real module is used).
// Each test registers the mock again on a fresh module registry, so its
// factory runs on the first import of that test.
const renderer = { loads: 0 }

beforeEach(() => {
  vi.resetModules()
  renderer.loads = 0
  vi.doMock('@/components/ui/markdown', async (importOriginal) => {
    renderer.loads += 1
    return importOriginal()
  })
})

afterEach(() => {
  vi.doUnmock('@/components/ui/markdown')
})

async function renderClosedBell() {
  const { NotificationPopover } = await import('../notification-popover')
  render(
    <NotificationPopover
      open={false}
      onOpenChange={vi.fn()}
      unreadCount={1}
      activeTab='notice'
      onTabChange={vi.fn()}
      notice='**Maintenance** tonight'
      announcements={[]}
      loading={false}
    />
  )
  return screen.getByRole('button', { name: 'Notifications' })
}

test('the closed notification bell does not load the markdown renderer', async () => {
  await renderClosedBell()

  expect(renderer.loads).toBe(0)
})

test.each([
  ['pointer reaches', fireEvent.pointerEnter],
  ['keyboard focus reaches', fireEvent.focus],
  ['touch starts on', fireEvent.pointerDown],
])(
  'the markdown renderer starts loading when the %s the bell',
  async (_label, fire) => {
    const bell = await renderClosedBell()

    fire(bell)

    await waitFor(() => expect(renderer.loads).toBe(1))
  }
)
