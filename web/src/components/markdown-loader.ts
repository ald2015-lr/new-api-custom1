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
// The markdown renderer (marked, KaTeX and its stylesheet, DOMPurify) is its
// own chunk. It downloads the first time LazyMarkdown renders, or earlier
// through preloadMarkdown(), instead of with every page whose header could
// show a notice.
export const loadMarkdown = () => import('@/components/ui/markdown')

/**
 * Start downloading the markdown renderer ahead of showing markdown (for
 * example when the pointer reaches the button that opens it). Failures are
 * left to LazyMarkdown, which loads it again when it renders.
 */
export function preloadMarkdown(): void {
  loadMarkdown().catch(() => undefined)
}
