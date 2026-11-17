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
import { Plus, RotateCcw, Trash2 } from 'lucide-react'
import { useId } from 'react'
import {
  useFieldArray,
  useFormState,
  useWatch,
  type Control,
} from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormField,
  FormItem,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import {
  GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS,
  GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS,
} from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import {
  toGeminiFreeKeyModelLimitRows,
  type GeminiFreeKeyModelLimitRow,
} from '../lib/gemini-free-key'

type CellProps<TField> = {
  control: Control<ChannelFormValues>
  index: number
  field: TField
  label: string
  disabled: boolean
}

const EMPTY_ROW: GeminiFreeKeyModelLimitRow = {
  model: '',
  aliases: '',
  rpm: null,
  tpm: null,
  rpd: null,
}

const cellMessageClassName = 'text-xs font-normal whitespace-normal'

function NameCell(props: CellProps<'model' | 'aliases'>) {
  return (
    <FormField
      control={props.control}
      name={`gemini_free_key_model_limits.${props.index}.${props.field}`}
      render={({ field }) => (
        <FormItem className='gap-1'>
          <FormControl>
            <Input
              {...field}
              aria-label={`${props.label} #${props.index + 1}`}
              value={field.value ?? ''}
              disabled={props.disabled}
              className='h-8 font-mono text-xs'
              autoComplete='off'
              spellCheck={false}
            />
          </FormControl>
          <FormMessage className={cellMessageClassName} />
        </FormItem>
      )}
    />
  )
}

function LimitCell(props: CellProps<'rpm' | 'tpm' | 'rpd'>) {
  return (
    <FormField
      control={props.control}
      name={`gemini_free_key_model_limits.${props.index}.${props.field}`}
      render={({ field }) => (
        <FormItem className='gap-1'>
          <FormControl>
            {/* No min, max or step: the browser's own check would stop the
                submit before the form shows its message for the cell. */}
            <Input
              type='number'
              inputMode='numeric'
              aria-label={`${props.label} #${props.index + 1}`}
              className='h-8 text-xs'
              disabled={props.disabled}
              name={field.name}
              ref={field.ref}
              onBlur={field.onBlur}
              value={field.value ?? ''}
              onChange={(e) =>
                field.onChange(
                  e.target.value === '' ? null : Number(e.target.value)
                )
              }
            />
          </FormControl>
          <FormMessage className={cellMessageClassName} />
        </FormItem>
      )}
    />
  )
}

// The error of the whole table, such as an empty one. Its own component: a
// form state subscription re-renders on every change inside the table.
function ModelLimitsListError(props: { control: Control<ChannelFormValues> }) {
  const { t } = useTranslation()
  const { errors } = useFormState({
    control: props.control,
    name: 'gemini_free_key_model_limits',
  })
  const message =
    errors.gemini_free_key_model_limits?.root?.message ??
    errors.gemini_free_key_model_limits?.message
  if (!message) return null
  return (
    <p role='alert' className='text-destructive text-sm'>
      {t(message)}
    </p>
  )
}

// Watches only its own row's model, so typing in one cell does not render
// the whole table again.
function RemoveRowButton(props: {
  control: Control<ChannelFormValues>
  index: number
  disabled: boolean
  onRemove: (index: number) => void
}) {
  const { t } = useTranslation()
  const model = useWatch({
    control: props.control,
    name: `gemini_free_key_model_limits.${props.index}.model`,
  })
  return (
    <Button
      type='button'
      variant='ghost'
      size='icon-sm'
      disabled={props.disabled}
      aria-label={t('Remove {{group}}', {
        group: model?.trim() || `#${props.index + 1}`,
      })}
      onClick={() => props.onRemove(props.index)}
    >
      <Trash2 aria-hidden />
    </Button>
  )
}

/** The editable per-model RPM / TPM / RPD table of a Gemini free key channel. */
export function GeminiFreeKeyModelLimitsEditor(props: {
  control: Control<ChannelFormValues>
  disabled: boolean
}) {
  const { t } = useTranslation()
  const headingId = useId()
  const { fields, append, remove, replace } = useFieldArray({
    control: props.control,
    name: 'gemini_free_key_model_limits',
  })
  const limitColumn = (field: 'rpm' | 'tpm' | 'rpd', label: string) => ({
    id: field,
    header: label,
    className: 'w-32 min-w-28',
    cellClassName: 'overflow-visible align-top',
    cell: (_row: unknown, index: number) => (
      <LimitCell
        control={props.control}
        index={index}
        field={field}
        label={label}
        disabled={props.disabled}
      />
    ),
  })

  return (
    <div
      role='group'
      aria-labelledby={headingId}
      className='flex min-w-0 flex-col gap-3'
    >
      <div className='space-y-1'>
        <h5 id={headingId} className='text-sm font-medium'>
          {t('Model limits')}
        </h5>
        <p className='text-muted-foreground text-sm'>
          {t(
            '-1 means unlimited; 0 means the free tier does not offer the model (requests get 429 at once and go to other channels). Models not listed have no local limits; a key is paused for them only when the upstream answers 429.'
          )}
        </p>
      </div>
      <StaticDataTable
        className='max-h-[32rem] overflow-auto'
        data={fields}
        getRowKey={(row) => row.id}
        emptyContent={t('No models configured. Use Add model to get started.')}
        emptyClassName='text-muted-foreground py-6 text-center text-sm'
        columns={[
          {
            id: 'model',
            header: t('Model'),
            className: 'min-w-56',
            cellClassName: 'overflow-visible align-top',
            cell: (_row, index) => (
              <NameCell
                control={props.control}
                index={index}
                field='model'
                label={t('Model')}
                disabled={props.disabled}
              />
            ),
          },
          {
            id: 'aliases',
            header: t('Aliases (comma-separated)'),
            className: 'min-w-56',
            cellClassName: 'overflow-visible align-top',
            cell: (_row, index) => (
              <NameCell
                control={props.control}
                index={index}
                field='aliases'
                label={t('Aliases')}
                disabled={props.disabled}
              />
            ),
          },
          limitColumn('rpm', 'RPM'),
          limitColumn('tpm', 'TPM'),
          limitColumn('rpd', 'RPD'),
          {
            id: 'actions',
            header: <span className='sr-only'>{t('Actions')}</span>,
            className: 'w-12',
            cellClassName: 'align-top',
            cell: (_row, index) => (
              <RemoveRowButton
                control={props.control}
                index={index}
                disabled={props.disabled}
                onRemove={remove}
              />
            ),
          },
        ]}
      />
      <ModelLimitsListError control={props.control} />
      <div className='flex flex-wrap gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={
            props.disabled ||
            fields.length >= GEMINI_FREE_KEY_MODEL_LIMITS_MAX_ROWS
          }
          onClick={() => append({ ...EMPTY_ROW })}
        >
          <Plus aria-hidden />
          {t('Add model')}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={props.disabled}
          onClick={() =>
            replace(
              toGeminiFreeKeyModelLimitRows(
                GEMINI_FREE_KEY_DEFAULT_MODEL_LIMITS
              )
            )
          }
        >
          <RotateCcw aria-hidden />
          {t('Restore defaults')}
        </Button>
      </div>
    </div>
  )
}
