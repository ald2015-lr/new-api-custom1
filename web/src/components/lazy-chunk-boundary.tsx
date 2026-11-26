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
import { Component, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import {
  isChunkLoadError,
  reloadOnceForChunkError,
} from '@/lib/chunk-load-error'

type LazyChunkBoundaryProps = {
  children: ReactNode
  /**
   * Stands in for the children when their code could not be loaded and no
   * automatic reload is due. Nothing by default.
   */
  fallback?: ReactNode
  /** Called when the code could not be loaded and no automatic reload is due. */
  onChunkError?: (error: unknown) => void
  /** A new value clears a failure, so the next render loads the code again. */
  resetKey?: unknown
}

type LazyChunkBoundaryState = {
  error: unknown
  failed: boolean
  reloading: boolean
  resetKey: unknown
}

/**
 * Error boundary for a lazily loaded part of a page. A chunk that cannot be
 * loaded (a deploy replaced it, or the network failed) gets the app's one
 * automatic reload, as a route chunk does; when no reload is due, `fallback`
 * stands in for that part and the rest of the page stays usable. Any other
 * error is passed on unchanged to the next boundary up.
 */
export class LazyChunkBoundary extends Component<
  LazyChunkBoundaryProps,
  LazyChunkBoundaryState
> {
  state: LazyChunkBoundaryState = {
    error: null,
    failed: false,
    reloading: false,
    resetKey: this.props.resetKey,
  }

  static getDerivedStateFromError(
    error: unknown
  ): Partial<LazyChunkBoundaryState> {
    return { error, failed: true }
  }

  static getDerivedStateFromProps(
    props: LazyChunkBoundaryProps,
    state: LazyChunkBoundaryState
  ): Partial<LazyChunkBoundaryState> | null {
    if (Object.is(props.resetKey, state.resetKey)) return null
    return {
      error: null,
      failed: false,
      reloading: false,
      resetKey: props.resetKey,
    }
  }

  componentDidCatch(error: unknown) {
    if (!isChunkLoadError(error)) return
    // Commit phase: the page that is about to reload never paints a fallback.
    if (reloadOnceForChunkError(error)) {
      this.setState({ reloading: true })
      return
    }
    this.props.onChunkError?.(error)
  }

  render() {
    if (!this.state.failed) return this.props.children
    if (!isChunkLoadError(this.state.error)) throw this.state.error
    if (this.state.reloading) return null
    return this.props.fallback ?? null
  }
}

/** The usual `fallback` of a lazily loaded part: what failed, and a reload. */
export function ChunkLoadErrorNotice(props: { className?: string }) {
  const { t } = useTranslation()
  return (
    <ErrorState
      className={props.className}
      title={t('Failed to load part of the page')}
      description={t(
        'A new version may have been deployed. Please reload the page.'
      )}
      action={
        <Button
          variant='outline'
          size='sm'
          onClick={() => window.location.reload()}
        >
          {t('Reload page')}
        </Button>
      }
    />
  )
}
