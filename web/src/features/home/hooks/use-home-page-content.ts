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
import i18next from 'i18next'
import { useEffect, useState } from 'react'

import { preloadMarkdown } from '@/components/markdown-loader'
import { isHttpUrl, isLikelyHtml } from '@/lib/content-format'
import { handleServerError } from '@/lib/handle-server-error'

import { getHomePageContent } from '../api'
import type { HomePageContentResult } from '../types'

const STORAGE_KEY = 'home_page_content'
/** `'none'` once the server said there is no custom content, `'content'` otherwise. */
const STATE_STORAGE_KEY = 'home_page_content_state'

/** A first visit shows the default home if the custom content takes longer. */
export const FIRST_VISIT_CONTENT_WAIT_MS = 5000

// A markdown home page renders through LazyMarkdown: start loading the
// renderer together with the content, so the page is not slower than before.
function preloadRendererFor(content: string) {
  if (content && !isHttpUrl(content) && !isLikelyHtml(content)) {
    preloadMarkdown()
  }
}

interface CachedHomePageContent {
  content: string
  /** Whether the cache answers what to show (content, or none) */
  known: boolean
}

function readCachedContent(): CachedHomePageContent {
  try {
    const content = localStorage.getItem(STORAGE_KEY) || ''
    if (content) return { content, known: true }
    return {
      content: '',
      known: localStorage.getItem(STATE_STORAGE_KEY) === 'none',
    }
  } catch {
    return { content: '', known: false }
  }
}

function writeCachedContent(content: string): void {
  try {
    if (content) {
      localStorage.setItem(STORAGE_KEY, content)
      localStorage.setItem(STATE_STORAGE_KEY, 'content')
    } else {
      localStorage.removeItem(STORAGE_KEY)
      localStorage.setItem(STATE_STORAGE_KEY, 'none')
    }
  } catch {
    // Storage can be unavailable in private mode; the next visit just waits.
  }
}

/**
 * Hook to load and manage custom home page content
 * Supports both Markdown/HTML content and iframe URLs
 *
 * A returning visitor renders the cached answer at once and refreshes it in
 * the background; a first visit waits for the response, at most
 * FIRST_VISIT_CONTENT_WAIT_MS, before the default home.
 */
export function useHomePageContent(): HomePageContentResult {
  const [cached] = useState(readCachedContent)
  const [content, setContent] = useState<string>(cached.content)
  const [isLoaded, setIsLoaded] = useState(cached.known)

  useEffect(() => {
    let mounted = true
    preloadRendererFor(cached.content)
    const firstVisitTimer = cached.known
      ? undefined
      : setTimeout(() => setIsLoaded(true), FIRST_VISIT_CONTENT_WAIT_MS)

    const loadContent = async () => {
      try {
        const response = await getHomePageContent()
        const { success, data } = response

        if (!mounted) return

        if (success && data) {
          preloadRendererFor(data)
          setContent(data)
          writeCachedContent(data)
        } else {
          // Clear content if API returns empty
          setContent('')
          writeCachedContent('')
        }
      } catch (error) {
        if (!mounted) return
        handleServerError(error, i18next.t('Failed to load home page content'))
      } finally {
        if (mounted) {
          clearTimeout(firstVisitTimer)
          setIsLoaded(true)
        }
      }
    }

    loadContent()

    return () => {
      mounted = false
      clearTimeout(firstVisitTimer)
    }
  }, [cached.content, cached.known])

  const isUrl = isHttpUrl(content)

  return { content, isLoaded, isUrl }
}
