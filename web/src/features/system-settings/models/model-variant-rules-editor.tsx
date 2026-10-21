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
import { ArrowDown, ArrowUp, Plus, RotateCcw, X } from 'lucide-react'
import { useId, useState } from 'react'
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
  FormLabel,
  useFormField,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  DEFAULT_MODEL_VARIANT_RULES,
  MODEL_VARIANT_RULE_LIMITS,
  parseModelName,
  splitFamilyBases,
} from '@/features/pricing/lib/model-variants'

import type { ModelSquareSettingsFormValues } from './model-square-settings-schema'

type RuleField = 'suffix' | 'label' | 'description'

const FIELD_MAX_LENGTH: Record<RuleField, number> = {
  suffix: MODEL_VARIANT_RULE_LIMITS.maxSuffixLength,
  label: MODEL_VARIANT_RULE_LIMITS.maxLabelLength,
  description: MODEL_VARIANT_RULE_LIMITS.maxDescriptionLength,
}

const EMPTY_RULE = { suffix: '', label: '', description: '' }

/** Field error of one rule cell, with the length limit interpolated. */
function RuleFieldMessage(props: { field: RuleField }) {
  const { t } = useTranslation()
  const { error, formMessageId } = useFormField()
  if (!error?.message) return null
  return (
    <p
      data-slot='form-message'
      id={formMessageId}
      className='text-destructive text-xs font-normal whitespace-normal'
    >
      {t(error.message, { max: FIELD_MAX_LENGTH[props.field] })}
    </p>
  )
}

function RuleInput(props: {
  control: Control<ModelSquareSettingsFormValues>
  index: number
  field: RuleField
  label: string
}) {
  return (
    <FormField
      control={props.control}
      name={`variant_rules.${props.index}.${props.field}`}
      render={({ field }) => (
        <FormItem className='gap-1'>
          <FormLabel className='sr-only'>
            {`${props.label} #${props.index + 1}`}
          </FormLabel>
          <FormControl>
            <Input
              {...field}
              className='h-8'
              autoComplete='off'
              spellCheck={false}
            />
          </FormControl>
          <RuleFieldMessage field={props.field} />
        </FormItem>
      )}
    />
  )
}

/** Type a model name to see how the current rules and bases split it. */
function RulesPreview(props: {
  rules: ModelSquareSettingsFormValues['variant_rules']
  familyBases: string
}) {
  const { t } = useTranslation()
  const inputId = useId()
  const [name, setName] = useState('[按次]gemini-3-pro-preview-128-nothinking')
  const parsed = parseModelName(
    name.trim(),
    props.rules.filter((rule) => rule.suffix && rule.label),
    splitFamilyBases(props.familyBases)
  )
  const items = [
    { label: t('Base model'), value: parsed.base },
    { label: t('Line'), value: parsed.line || t('Default') },
    { label: t('Variant'), value: parsed.label || t('Standard') },
  ]
  return (
    <div className='bg-muted/30 flex flex-col gap-2 rounded-lg border p-3'>
      <Label htmlFor={inputId}>{t('Preview')}</Label>
      <Input
        id={inputId}
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder={t('Type a model name')}
        autoComplete='off'
        spellCheck={false}
        className='h-8'
      />
      {name.trim() && (
        <dl
          aria-live='polite'
          className='grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_minmax(0,1fr)]'
        >
          {items.map((item) => (
            <div key={item.label} className='contents'>
              <dt className='text-muted-foreground'>{item.label}</dt>
              <dd className='font-mono break-all'>{item.value}</dd>
            </div>
          ))}
          {parsed.description && (
            <div className='contents'>
              <dt className='text-muted-foreground'>{t('Description')}</dt>
              <dd className='break-words'>{parsed.description}</dd>
            </div>
          )}
        </dl>
      )}
    </div>
  )
}

export function ModelVariantRulesEditor(props: {
  control: Control<ModelSquareSettingsFormValues>
}) {
  const { t } = useTranslation()
  const { fields, append, remove, move, replace } = useFieldArray({
    control: props.control,
    name: 'variant_rules',
  })
  const rules = useWatch({ control: props.control, name: 'variant_rules' })
  const familyBases = useWatch({ control: props.control, name: 'family_bases' })
  const { errors } = useFormState({
    control: props.control,
    name: 'variant_rules',
  })
  const listError =
    errors.variant_rules?.root?.message ?? errors.variant_rules?.message
  const atLimit = fields.length >= MODEL_VARIANT_RULE_LIMITS.maxRules

  return (
    <div className='flex min-w-0 flex-col gap-3'>
      <div className='flex flex-col gap-1'>
        <h4 className='text-sm font-medium'>{t('Variant suffix rules')}</h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Suffixes are removed from the end of a model name, longest match first, and can stack (for example -128-nothinking). The rule order is the display order of variants. Labels and descriptions are shown as entered.'
          )}
        </p>
      </div>

      <StaticDataTable
        data={fields}
        getRowKey={(field) => field.id}
        emptyContent={t(
          'No rules: only the lines of the same model are grouped.'
        )}
        emptyClassName='text-muted-foreground py-6 text-center text-sm'
        columns={[
          {
            id: 'index',
            header: '#',
            className: 'w-10',
            cellClassName: 'text-muted-foreground align-top pt-3.5',
            cell: (_field, index) => <span>{index + 1}</span>,
          },
          {
            id: 'suffix',
            header: t('Suffix'),
            className: 'min-w-36',
            cellClassName: 'overflow-visible align-top',
            cell: (_field, index) => (
              <RuleInput
                control={props.control}
                index={index}
                field='suffix'
                label={t('Suffix')}
              />
            ),
          },
          {
            id: 'label',
            header: t('Label'),
            className: 'min-w-32',
            cellClassName: 'overflow-visible align-top',
            cell: (_field, index) => (
              <RuleInput
                control={props.control}
                index={index}
                field='label'
                label={t('Label')}
              />
            ),
          },
          {
            id: 'description',
            header: t('Description'),
            className: 'min-w-48',
            cellClassName: 'overflow-visible align-top',
            cell: (_field, index) => (
              <RuleInput
                control={props.control}
                index={index}
                field='description'
                label={t('Description')}
              />
            ),
          },
          {
            id: 'actions',
            header: <span className='sr-only'>{t('Actions')}</span>,
            className: 'w-28',
            cellClassName: 'align-top',
            cell: (_field, index) => {
              const name = rules[index]?.suffix || `#${index + 1}`
              return (
                <div className='flex justify-end gap-0.5'>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    disabled={index === 0}
                    aria-label={t('Move {{group}} up', { group: name })}
                    onClick={() => move(index, index - 1)}
                  >
                    <ArrowUp aria-hidden />
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    disabled={index === fields.length - 1}
                    aria-label={t('Move {{group}} down', { group: name })}
                    onClick={() => move(index, index + 1)}
                  >
                    <ArrowDown aria-hidden />
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('Remove {{group}}', { group: name })}
                    onClick={() => remove(index)}
                  >
                    <X aria-hidden />
                  </Button>
                </div>
              )
            },
          },
        ]}
      />

      {listError && (
        <p className='text-destructive text-sm'>
          {t(listError, { max: MODEL_VARIANT_RULE_LIMITS.maxRules })}
        </p>
      )}

      <div className='flex flex-wrap gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={atLimit}
          onClick={() => append({ ...EMPTY_RULE })}
        >
          <Plus aria-hidden />
          {t('Add rule')}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={() =>
            replace(
              DEFAULT_MODEL_VARIANT_RULES.map((rule) => ({
                ...EMPTY_RULE,
                ...rule,
              }))
            )
          }
        >
          <RotateCcw aria-hidden />
          {t('Restore defaults')}
        </Button>
      </div>

      <RulesPreview rules={rules ?? []} familyBases={familyBases ?? ''} />
    </div>
  )
}
