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
import type { AnyRoute, AnyRouter } from '@tanstack/react-router'

/**
 * The routes whose code the page at `href` needs: every route its path
 * matches, layouts included. A section index route without a page of its own
 * (`/dashboard`, `/usage-logs`, ...) only redirects to its default section, so
 * the `$section` route next to it, the page the user ends on, is added. An
 * href that matches no page needs nothing.
 */
function routesForHref(router: AnyRouter, href: string): AnyRoute[] {
  const pathname = href.split(/[?#]/, 1)[0] || '/'
  const [matched, , page] = router.getMatchedRoutes(pathname)
  if (!page) return []
  const section = page.options.component
    ? undefined
    : router.routesById[`${page.id}$section`]
  return section ? [...matched, section] : [...matched]
}

/**
 * Download the code of the page at `href` without opening it: the lazy chunks
 * of its routes, through the router's loadRouteChunk. Unlike preloadRoute and
 * the router's preload on intent, this runs no beforeLoad and no loader, so it
 * sends no API request, never redirects or navigates and leaves the session
 * alone. Never rejects: a chunk that fails to download is left to the
 * navigation, which loads it again and handles a failure as before (no toast,
 * no reload from here).
 */
export async function prefetchRouteChunks(
  router: AnyRouter,
  href: string
): Promise<void> {
  try {
    await Promise.allSettled(
      routesForHref(router, href).map((route) => router.loadRouteChunk(route))
    )
  } catch {
    // Left to the navigation, see above.
  }
}
