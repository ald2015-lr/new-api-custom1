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
import { Loader2, RefreshCw } from 'lucide-react'
import { useId, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

import type { RegisterCaptchaState } from '../hooks/use-register-captcha'

type RegisterCaptchaFieldProps = {
  captcha: RegisterCaptchaState
  disabled?: boolean
  /**
   * Load the first challenge when the answer is focused, for a page that
   * does not load it on mount. Pages that do keep a failed load failed until
   * the image is clicked, so the focus does not retry a refused request.
   */
  loadOnFocus?: boolean
}

/** Answer input, image and refresh control of the arithmetic captcha. */
export function RegisterCaptchaField(props: RegisterCaptchaFieldProps) {
  const { t } = useTranslation()
  const inputId = useId()
  const challenge = props.captcha.challenge
  const isLoading = props.captcha.isLoading

  // Wraps where the language makes it wider than the image.
  let preview: ReactNode = (
    <span className='text-muted-foreground px-1 text-center text-xs leading-tight whitespace-normal'>
      {t('Click to refresh')}
    </span>
  )
  if (isLoading) {
    preview = <Loader2 className='size-4 animate-spin' aria-hidden='true' />
  } else if (challenge && props.captcha.expired) {
    // One word fits the image in every language; the button's title says
    // what a click does.
    preview = (
      <span className='text-muted-foreground text-xs'>{t('Expired')}</span>
    )
  } else if (challenge) {
    preview = (
      <img
        src={challenge.image}
        alt={t('Captcha')}
        width={120}
        height={40}
        className='h-10 w-[120px] object-contain'
      />
    )
  }

  return (
    <div className='grid gap-2'>
      <Label htmlFor={inputId}>{t('Captcha')}</Label>
      <div className='flex items-center gap-2'>
        <Input
          ref={props.captcha.answerRef}
          id={inputId}
          inputMode='numeric'
          maxLength={3}
          autoComplete='off'
          placeholder={t('Enter the result')}
          value={props.captcha.answer}
          onChange={(event) => props.captcha.setAnswer(event.target.value)}
          onFocus={() => {
            if (props.loadOnFocus && props.captcha.ensureLoaded(true)) return
            props.captcha.refreshIfExpired()
          }}
          // Nothing typed while a new image loads can belong to it.
          disabled={props.disabled || isLoading}
          className='h-10 min-w-0 flex-1'
        />
        <Button
          type='button'
          variant='outline'
          title={t('Click to refresh')}
          onClick={() => void props.captcha.refresh()}
          disabled={props.disabled}
          className='h-10 w-[120px] shrink-0 overflow-hidden p-0'
        >
          {preview}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-lg'
          aria-label={t('Refresh captcha')}
          title={t('Refresh captcha')}
          onClick={() => void props.captcha.refresh()}
          disabled={props.disabled}
        >
          <RefreshCw
            className={cn(isLoading && 'animate-spin')}
            aria-hidden='true'
          />
        </Button>
      </div>
    </div>
  )
}
