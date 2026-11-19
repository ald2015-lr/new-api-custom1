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
import { Plus, Trash2 } from 'lucide-react'
import { useId } from 'react'
import { useFieldArray, useFormState, type Control } from 'react-hook-form'
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
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { MODERATION_RULES_MAX } from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import type { ModerationRuleRow } from '../lib/content-moderation-rules'

type CellProps = {
  control: Control<ChannelFormValues>
  index: number
  label: string
  disabled: boolean
}

const EMPTY_RULE: ModerationRuleRow = {
  name: '',
  type: 'keyword',
  pattern: '',
  case_sensitive: false,
}

const cellMessageClassName = 'text-xs font-normal whitespace-normal'

function TextCell(
  props: CellProps & { field: 'name' | 'pattern'; placeholder?: string }
) {
  return (
    <FormField
      control={props.control}
      name={`moderation_rules.${props.index}.${props.field}`}
      render={({ field }) => (
        <FormItem className='gap-1'>
          <FormControl>
            <Input
              {...field}
              aria-label={`${props.label} #${props.index + 1}`}
              placeholder={props.placeholder}
              value={field.value ?? ''}
              disabled={props.disabled}
              className={
                props.field === 'pattern' ? 'h-8 font-mono text-xs' : 'h-8'
              }
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

function TypeCell(props: CellProps) {
  const { t } = useTranslation()
  const items = [
    { value: 'keyword', label: t('Keyword') },
    { value: 'regex', label: t('Regular expression') },
  ]
  return (
    <FormField
      control={props.control}
      name={`moderation_rules.${props.index}.type`}
      render={({ field }) => (
        <FormItem className='gap-1'>
          <Select
            disabled={props.disabled}
            items={items}
            onValueChange={field.onChange}
            value={field.value}
          >
            <FormControl>
              <SelectTrigger
                size='sm'
                aria-label={`${props.label} #${props.index + 1}`}
              >
                <SelectValue />
              </SelectTrigger>
            </FormControl>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                {items.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <FormMessage className={cellMessageClassName} />
        </FormItem>
      )}
    />
  )
}

function CaseSensitiveCell(props: CellProps) {
  return (
    <FormField
      control={props.control}
      name={`moderation_rules.${props.index}.case_sensitive`}
      render={({ field }) => (
        // As tall as the row inputs, so the shorter switch sits on their
        // middle in the top-aligned row.
        <FormItem className='h-8 content-center gap-1'>
          <FormControl>
            <Switch
              aria-label={`${props.label} #${props.index + 1}`}
              disabled={props.disabled}
              checked={field.value === true}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )
}

// The error of the whole table, such as too many rules. Its own component:
// a form state subscription re-renders on every change inside the table.
function RulesListError(props: { control: Control<ChannelFormValues> }) {
  const { t } = useTranslation()
  const { errors } = useFormState({
    control: props.control,
    name: 'moderation_rules',
  })
  const message =
    errors.moderation_rules?.root?.message ?? errors.moderation_rules?.message
  if (!message) return null
  return (
    <p role='alert' className='text-destructive text-sm'>
      {t(message)}
    </p>
  )
}

/** The editable custom keyword and regular expression rules of a channel. */
export function ChannelModerationRulesEditor(props: {
  control: Control<ChannelFormValues>
  disabled: boolean
}) {
  const { t } = useTranslation()
  const headingId = useId()
  const { fields, append, remove } = useFieldArray({
    control: props.control,
    name: 'moderation_rules',
  })

  return (
    <div
      role='group'
      aria-labelledby={headingId}
      className='flex min-w-0 flex-col gap-3'
    >
      <div className='space-y-1'>
        <h5 id={headingId} className='text-sm font-medium'>
          {t('Custom rules')}
        </h5>
        {/* Two paragraphs: a space between the two sentences would follow a
          Chinese full stop. */}
        <p className='text-muted-foreground text-sm'>
          {t(
            'Keywords match anywhere in the text. Regular expressions use RE2 syntax (no look-around or back-references), are checked when saved and should stay simple: each one runs on up to 256 KB of every request. Up to 100 rules, at most 30 regular expressions.'
          )}
        </p>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Text is normalised first (full-width letters and punctuation become half-width, curly quotes and dashes become \' " -, repeated spaces become one, and spaces and line breaks between Chinese characters or Chinese punctuation are removed), so write regular expressions in that form. Keywords treat a line break as a space, so a keyword still matches when the text breaks it across two lines; regular expressions see any other line break as \\n.'
          )}
        </p>
      </div>
      {fields.length > 0 && (
        <StaticDataTable
          className='max-h-[32rem] overflow-auto'
          data={fields}
          getRowKey={(row) => row.id}
          columns={[
            {
              id: 'index',
              header: '#',
              className: 'w-10',
              cellClassName: 'text-muted-foreground align-top pt-3 text-xs',
              cell: (_row, index) => index + 1,
            },
            {
              id: 'name',
              header: t('Name'),
              className: 'min-w-36',
              cellClassName: 'overflow-visible align-top',
              cell: (_row, index) => (
                <TextCell
                  control={props.control}
                  index={index}
                  field='name'
                  label={t('Name')}
                  placeholder={t('Name (optional)')}
                  disabled={props.disabled}
                />
              ),
            },
            {
              id: 'type',
              header: t('Type'),
              className: 'w-40 min-w-36',
              cellClassName: 'overflow-visible align-top',
              cell: (_row, index) => (
                <TypeCell
                  control={props.control}
                  index={index}
                  label={t('Type')}
                  disabled={props.disabled}
                />
              ),
            },
            {
              id: 'pattern',
              header: t('Pattern'),
              className: 'min-w-56',
              cellClassName: 'overflow-visible align-top',
              cell: (_row, index) => (
                <TextCell
                  control={props.control}
                  index={index}
                  field='pattern'
                  label={t('Pattern')}
                  disabled={props.disabled}
                />
              ),
            },
            {
              id: 'case_sensitive',
              header: t('Case sensitive'),
              className: 'w-28',
              cellClassName: 'align-top pt-2',
              cell: (_row, index) => (
                <CaseSensitiveCell
                  control={props.control}
                  index={index}
                  label={t('Case sensitive')}
                  disabled={props.disabled}
                />
              ),
            },
            {
              id: 'actions',
              header: <span className='sr-only'>{t('Actions')}</span>,
              className: 'w-12',
              cellClassName: 'align-top',
              cell: (_row, index) => (
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  disabled={props.disabled}
                  aria-label={`${t('Remove')} #${index + 1}`}
                  onClick={() => remove(index)}
                >
                  <Trash2 aria-hidden />
                </Button>
              ),
            },
          ]}
        />
      )}
      <RulesListError control={props.control} />
      <div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled || fields.length >= MODERATION_RULES_MAX}
          onClick={() => append({ ...EMPTY_RULE })}
        >
          <Plus aria-hidden />
          {t('Add rule')}
        </Button>
      </div>
    </div>
  )
}
