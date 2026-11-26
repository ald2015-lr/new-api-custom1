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
import { Loader2 } from 'lucide-react'
import { Suspense, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { LazyChunkBoundary } from '@/components/lazy-chunk-boundary'

function LazyDialogLoading() {
  const { t } = useTranslation()
  return (
    <div
      role='status'
      aria-live='polite'
      className='bg-popover text-popover-foreground fixed top-4 left-1/2 z-50 flex -translate-x-1/2 items-center gap-2 rounded-md border px-3 py-2 text-sm shadow-md'
    >
      <Loader2 aria-hidden className='size-4 animate-spin' />
      {t('Loading...')}
    </div>
  )
}

type LazyDialogProps = {
  /** Whether the dialog is open. */
  open: boolean
  /** Closes the dialog after its code could not be loaded. */
  onLoadError: () => void
  /** The dialog, a React.lazy component. */
  children: ReactNode
}

/**
 * Mounts a dialog whose code is a chunk of its own: nothing is loaded before
 * it first opens, and from then on it stays mounted like a dialog that was
 * always there, so its closing animation and the state it keeps between
 * openings are unchanged. While the code downloads a small loading notice
 * shows. When the code cannot be loaded, the page gets its one automatic
 * reload; when no reload is due, the dialog closes and a toast offers one.
 */
export function LazyDialog(props: LazyDialogProps) {
  const { t } = useTranslation()
  const [mounted, setMounted] = useState(props.open)
  const [wasOpen, setWasOpen] = useState(props.open)
  // Every opening gets a fresh attempt; closing does not retry a failure.
  const [attempt, setAttempt] = useState(0)
  if (props.open !== wasOpen) {
    setWasOpen(props.open)
    if (props.open) {
      setMounted(true)
      setAttempt((value) => value + 1)
    }
  }
  if (!mounted) return null

  return (
    <LazyChunkBoundary
      resetKey={attempt}
      onChunkError={() => {
        toast.error(t('Failed to load part of the page'), {
          description: t(
            'A new version may have been deployed. Please reload the page.'
          ),
          action: {
            label: t('Reload page'),
            onClick: () => window.location.reload(),
          },
        })
        props.onLoadError()
      }}
    >
      <Suspense fallback={props.open ? <LazyDialogLoading /> : null}>
        {props.children}
      </Suspense>
    </LazyChunkBoundary>
  )
}
