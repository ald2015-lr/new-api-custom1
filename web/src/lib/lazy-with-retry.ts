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
import {
  createElement,
  lazy,
  type ComponentProps,
  type ComponentType,
  type LazyExoticComponent,
} from 'react'

// React.lazy keeps a failed import for the life of the page: every later
// render throws the same error and never asks for the chunk again, even once
// the network is back. A part made with lazyWithRetry swaps in a new
// React.lazy after a failure, so that its next render imports again (the
// chunk loader asks the server again after a failure).
//
// The swap waits for the error boundary that caught the failure (see
// retryLazyAfterError, called by LazyChunkBoundary): swapping as soon as the
// import fails would let React render the new lazy at once, which would fail
// again and again in a loop while the chunk stays unreachable.

const retriesByError = new WeakMap<object, Set<() => void>>()

/**
 * React.lazy for a part of a page whose code is a chunk of its own, with a
 * new attempt at the next render after an error boundary caught a failed
 * load (a dialog opened again, a drawer reopened, a Retry). A part that
 * loaded is never loaded again.
 */
// The same constraint as React.lazy's own.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function lazyWithRetry<T extends ComponentType<any>>(
  load: () => Promise<{ default: T }>
): ComponentType<ComponentProps<T>> {
  const attempt = (): LazyExoticComponent<T> => {
    const component = lazy(() =>
      load().catch((error: unknown) => {
        if (typeof error === 'object' && error !== null) {
          let retries = retriesByError.get(error)
          if (!retries) {
            retries = new Set()
            retriesByError.set(error, retries)
          }
          retries.add(() => {
            if (current === component) current = attempt()
          })
        }
        throw error
      })
    )
    return component
  }
  let current = attempt()

  function LazyWithRetry(props: ComponentProps<T>) {
    return createElement(current, props)
  }
  return LazyWithRetry
}

/**
 * Let every part whose load failed with `error` import again the next time it
 * renders. For the error boundary that caught `error`, once it shows its
 * fallback in place of the part.
 */
export function retryLazyAfterError(error: unknown): void {
  if (typeof error !== 'object' || error === null) return
  const retries = retriesByError.get(error)
  if (!retries) return
  retriesByError.delete(error)
  for (const retry of retries) retry()
}
