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
import { Component, lazy, Suspense, type ReactNode } from 'react'

import { loadMarkdown } from '@/components/markdown-loader'
import { Skeleton } from '@/components/ui/skeleton'
import { reloadOnceForChunkError } from '@/lib/chunk-load-error'
import { cn } from '@/lib/utils'

type LazyMarkdownProps = {
  /** Resolve relative document links against their source, never the dashboard URL. */
  baseUrl?: string
  breaks?: boolean
  children: string
  className?: string
}

const Markdown = lazy(() =>
  loadMarkdown().then((module) => ({ default: module.Markdown }))
)

function PlainText(props: { children: string; className?: string }) {
  return (
    <div
      className={cn(
        'text-sm whitespace-pre-wrap [overflow-wrap:anywhere]',
        props.className
      )}
    >
      {props.children}
    </div>
  )
}

type MarkdownBoundaryProps = {
  children: ReactNode
  fallback: ReactNode
}

class MarkdownBoundary extends Component<
  MarkdownBoundaryProps,
  { failed: boolean }
> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  componentDidCatch(error: unknown) {
    // A renderer chunk that a deploy replaced gets the app's one automatic
    // reload; the text stays readable until then, and when no reload is due.
    reloadOnceForChunkError(error)
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

/**
 * Markdown that loads its renderer on first use. While the renderer
 * downloads it shows a skeleton; if it cannot load or render, the text is
 * shown as plain text, so the content is never lost.
 */
export function LazyMarkdown(props: LazyMarkdownProps) {
  return (
    <MarkdownBoundary
      fallback={
        <PlainText className={props.className}>{props.children}</PlainText>
      }
    >
      <Suspense
        fallback={
          <div className='flex flex-col gap-2 py-1' aria-busy='true'>
            <Skeleton className='h-4 w-full' />
            <Skeleton className='h-4 w-2/3' />
          </div>
        }
      >
        <Markdown
          baseUrl={props.baseUrl}
          breaks={props.breaks}
          className={props.className}
        >
          {props.children}
        </Markdown>
      </Suspense>
    </MarkdownBoundary>
  )
}
