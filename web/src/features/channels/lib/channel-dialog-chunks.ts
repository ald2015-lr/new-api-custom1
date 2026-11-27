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
import { prefetchWhenIdle } from '@/lib/idle-prefetch'

// The dialogs and drawers of the channel list are chunks of their own (see
// components/channels-dialogs.tsx): the list does not download them, nor the
// editors inside them (the code editor alone is about 175 KB), before one
// opens. The channel editor is the one most visits open, so it is also
// fetched ahead: when the pointer reaches a button that opens it, and once
// the list has settled.
export const loadChannelMutateDrawer = () =>
  import('../components/drawers/channel-mutate-drawer')

let editorPrefetch: Promise<unknown> | null = null

/**
 * Start downloading the channel editor ahead of opening it. Cheap to call on
 * every pointer move: only the first call (or the first after a failure)
 * starts a download; the editor loads it again when it opens.
 */
export function prefetchChannelMutateDrawer(): void {
  editorPrefetch ??= loadChannelMutateDrawer().catch(() => {
    editorPrefetch = null
  })
}

/**
 * Prefetch the channel editor once the channel list has settled. Returns a
 * function that cancels the prefetch if it has not started yet.
 */
export function prefetchChannelMutateDrawerWhenIdle(): () => void {
  return prefetchWhenIdle(prefetchChannelMutateDrawer)
}
