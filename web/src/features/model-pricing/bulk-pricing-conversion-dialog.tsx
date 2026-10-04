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
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'

import { convertibleModelCount, type ModelPricingBulkConversion } from './api'

export function BulkPricingConversionDialog(props: {
  preview: ModelPricingBulkConversion
  isConverting: boolean
  discardsUnsavedChanges: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const count = convertibleModelCount(props.preview)

  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open && !props.isConverting) props.onCancel()
      }}
      title={t('Convert all to expressions')}
      desc={t(
        'After conversion, expression reservation and rounding rules apply. Effective unit prices are preserved; individual rounded charges may differ.'
      )}
      confirmText={t('Convert {{count}} models', { count })}
      disabled={count === 0}
      isLoading={props.isConverting}
      handleConfirm={props.onConfirm}
      className='max-h-[min(90dvh,var(--dialog-available-height))] overflow-y-auto data-[size=default]:max-w-[calc(100%-2rem)] data-[size=default]:sm:max-w-3xl'
    >
      {props.discardsUnsavedChanges && (
        <Alert variant='destructive'>
          <AlertDescription className='text-xs'>
            {t(
              'Unsaved price edits on this page will be discarded after conversion.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <section
        aria-label={t('Models to convert')}
        className='min-w-0 space-y-2'
      >
        <h3 className='text-sm font-semibold'>
          {t('Models to convert')} ({props.preview.converted.length})
        </h3>
        {props.preview.converted.length === 0 ? (
          <p className='text-muted-foreground text-sm'>
            {t('No legacy prices to convert.')}
          </p>
        ) : (
          <ul className='divide-y rounded-lg border'>
            {props.preview.converted.map((item) => (
              <li key={item.model} className='min-w-0 space-y-1 px-3 py-2'>
                <p className='font-mono text-sm break-all'>{item.model}</p>
                <code className='text-muted-foreground block text-xs break-all'>
                  {item.expression}
                </code>
              </li>
            ))}
          </ul>
        )}
      </section>
      {props.preview.suspicious.length > 0 && (
        <section
          aria-label={t('Free expressions with legacy prices')}
          className='min-w-0 space-y-2'
        >
          <h3 className='text-sm font-semibold'>
            {t('Free expressions with legacy prices')} (
            {props.preview.suspicious.length})
          </h3>
          <p className='text-muted-foreground text-xs'>
            {t(
              'These expressions charge nothing for any usage while legacy prices are still stored. They are replaced with the conversion of the stored legacy prices.'
            )}
          </p>
          <ul className='divide-y rounded-lg border border-amber-500/40'>
            {props.preview.suspicious.map((item) => (
              <li key={item.model} className='min-w-0 space-y-1 px-3 py-2'>
                <p className='font-mono text-sm break-all'>{item.model}</p>
                <p className='text-muted-foreground text-xs break-all'>
                  {t('Current expression')}: <code>{item.expression}</code>
                </p>
                <p className='text-muted-foreground text-xs break-all'>
                  {t('Legacy prices')}:{' '}
                  <code>
                    {Object.entries(item.legacy_pricing)
                      .map(([key, value]) => `${key} ${value}`)
                      .join(', ')}
                  </code>
                </p>
                {item.replacement ? (
                  <p className='text-xs break-all'>
                    {t('Replacement')}: <code>{item.replacement}</code>
                  </p>
                ) : (
                  <p className='text-destructive text-xs'>
                    {t(item.reason ?? '')}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {props.preview.skipped.length > 0 && (
        <section aria-label={t('Skipped models')} className='min-w-0 space-y-2'>
          <h3 className='text-sm font-semibold'>
            {t('Skipped models')} ({props.preview.skipped.length})
          </h3>
          <ul className='divide-y rounded-lg border'>
            {props.preview.skipped.map((item) => (
              <li key={item.model} className='min-w-0 space-y-1 px-3 py-2'>
                <p className='font-mono text-sm break-all'>{item.model}</p>
                <p className='text-muted-foreground text-xs'>
                  {t(item.reason ?? '')}
                </p>
              </li>
            ))}
          </ul>
        </section>
      )}
    </ConfirmDialog>
  )
}
