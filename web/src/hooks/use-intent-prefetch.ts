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
import { useEffect, useState } from 'react'

import { createIntentPrefetch, type IntentPrefetch } from '@/lib/idle-prefetch'

/**
 * Handlers for a group of controls (the tabs of one tab list) that prefetch
 * what a control opens once the pointer or focus rests on it (see
 * createIntentPrefetch). Spread the result of `handlers` on each control.
 */
export function useIntentPrefetch(): IntentPrefetch & {
  handlers: (prefetch: () => void) => {
    onPointerEnter: () => void
    onPointerLeave: () => void
    onFocus: () => void
    onBlur: () => void
  }
} {
  const [intent] = useState(createIntentPrefetch)
  useEffect(() => intent.cancel, [intent])
  return {
    ...intent,
    handlers: (prefetch) => ({
      onPointerEnter: () => intent.start(prefetch),
      onPointerLeave: intent.cancel,
      onFocus: () => intent.start(prefetch),
      onBlur: intent.cancel,
    }),
  }
}
