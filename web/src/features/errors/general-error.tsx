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
  useNavigate,
  useRouter,
  type ErrorComponentProps,
} from '@tanstack/react-router'
import { useLayoutEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  isChunkLoadError,
  reloadOnceForChunkError,
} from '@/lib/chunk-load-error'
import { cn } from '@/lib/utils'

const FEEDBACK_URL = 'https://github.com/QuantumNous/new-api/issues'

type GeneralErrorProps = React.HTMLAttributes<HTMLDivElement> &
  Partial<ErrorComponentProps<unknown>> & {
    minimal?: boolean
  }

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error !== 'object' || error === null) return undefined
  const response = (error as Record<string, unknown>).response
  if (typeof response !== 'object' || response === null) return undefined
  const status = (response as Record<string, unknown>).status
  return typeof status === 'number' ? status : undefined
}

export function GeneralError({
  className,
  minimal = false,
  error,
}: GeneralErrorProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { history } = useRouter()
  const [reloading, setReloading] = useState(false)
  const status = getHttpStatus(error)
  const isRateLimited = status === 429
  const isChunkError = isChunkLoadError(error)

  // Layout effect: the state update is flushed before paint, so a page that
  // is about to reload never flashes the error UI first.
  useLayoutEffect(() => {
    if (!isChunkError) return
    if (reloadOnceForChunkError(error)) setReloading(true)
  }, [error, isChunkError])

  if (reloading) return null

  let title: string
  let description: string
  if (isRateLimited) {
    title = t('Too many requests')
    description = t('Please wait a moment before trying again.')
  } else if (isChunkError) {
    title = t('Failed to load part of the page')
    description = t(
      'A new version may have been deployed. Please reload the page.'
    )
  } else {
    title = `${t('Oops! Something went wrong')} :')`
    description = t('Please try again later.')
  }

  return (
    <div className={cn('h-svh w-full', className)}>
      <div className='m-auto flex h-full w-full flex-col items-center justify-center gap-2'>
        {!minimal && !isChunkError && (
          <h1 className='text-[7rem] leading-tight font-bold'>
            {status ?? 500}
          </h1>
        )}
        <span className='font-medium'>{title}</span>
        <p className='text-muted-foreground text-center'>
          {t('We apologize for the inconvenience.')} <br /> {description}
        </p>
        {!minimal && (
          <p className='text-muted-foreground text-center text-sm'>
            {t('If this keeps happening, please report it on GitHub Issues.')}
          </p>
        )}
        {!minimal && (
          <div className='mt-6 flex flex-wrap justify-center gap-4'>
            <Button variant='outline' onClick={() => history.go(-1)}>
              {t('Go Back')}
            </Button>
            <Button variant='outline' onClick={() => window.location.reload()}>
              {t('Reload page')}
            </Button>
            <Button
              variant='outline'
              render={
                <a
                  href={FEEDBACK_URL}
                  target='_blank'
                  rel='noopener noreferrer'
                />
              }
            >
              {t('Report an issue')}
            </Button>
            <Button onClick={() => navigate({ to: '/' })}>
              {t('Back to Home')}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
