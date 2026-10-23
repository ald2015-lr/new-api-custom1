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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import {
  hasModelRateLimitPatternStar,
  MODEL_RATE_LIMIT_MATCHES,
  MODEL_RATE_LIMIT_MAX_COUNT,
  MODEL_RATE_LIMIT_MAX_MINUTES,
  MODEL_RATE_LIMIT_MAX_NAME_LENGTH,
  type ModelRateLimitRule,
} from './model-rate-limit-rules'

const countSchema = z
  .number()
  .int('Must be a whole number')
  .min(0, 'Must be from 0 to 100,000,000')
  .max(MODEL_RATE_LIMIT_MAX_COUNT, 'Must be from 0 to 100,000,000')

const modelRateLimitDialogSchema = z
  .object({
    modelName: z
      .string()
      .trim()
      .min(1, 'Model name is required')
      .refine(
        (name) => [...name].length <= MODEL_RATE_LIMIT_MAX_NAME_LENGTH,
        'Model name must be at most 255 characters'
      ),
    match: z.enum(MODEL_RATE_LIMIT_MATCHES),
    shared: z.boolean(),
    durationMinutes: z
      .number()
      .int('Must be a whole number')
      .min(1, 'Must be from 1 to 1,440')
      .max(MODEL_RATE_LIMIT_MAX_MINUTES, 'Must be from 1 to 1,440'),
    totalCount: countSchema,
    successCount: countSchema,
  })
  // "[按次]*" or "*-thinking", copied from the table, would match no model.
  .superRefine((values, ctx) => {
    if (!hasModelRateLimitPatternStar(values.modelName, values.match)) return
    let message =
      'Leave out the *: Suffix Match already matches every model whose name ends with the suffix.'
    if (values.match === 'prefix') {
      message =
        'Leave out the *: Prefix Match already matches every model whose name starts with the prefix.'
    }
    ctx.addIssue({ code: 'custom', path: ['modelName'], message })
  })

type ModelRateLimitDialogFormValues = z.infer<typeof modelRateLimitDialogSchema>

const MODEL_RATE_LIMIT_FORM_ID = 'model-rate-limit-form'

// A name that is only a [...] line tag, such as "[按次]" or "[按次]*": as an
// exact rule it matches no model of the line, which a prefix rule does.
const BARE_LINE_TAG = /^\[[^\]]*\]\*?$/

const emptyRule: ModelRateLimitRule = {
  modelName: '',
  match: 'exact',
  shared: false,
  durationMinutes: 1,
  totalCount: 0,
  successCount: 0,
}

type ModelRateLimitDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (rule: ModelRateLimitRule) => void
  editData?: ModelRateLimitRule | null
  existingModelNames: string[]
}

export function ModelRateLimitDialog(props: ModelRateLimitDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!props.editData

  const form = useForm<ModelRateLimitDialogFormValues>({
    resolver: zodResolver(modelRateLimitDialogSchema),
    defaultValues: emptyRule,
  })

  useEffect(() => {
    form.reset(props.editData ?? emptyRule)
  }, [props.editData, form, props.open])

  const match = form.watch('match')
  const modelName = form.watch('modelName')
  const showLineTagHint =
    match === 'exact' && BARE_LINE_TAG.test(modelName.trim())
  let nameLabel = t('Model Name')
  let namePlaceholder = t('e.g. gpt-4o, gemini-2.5-flash')
  let nameHint = t(
    'The model name the client sends, including any [...] prefix. Variants with @ modifiers or a suffix such as -thinking also count under it.'
  )
  if (match === 'prefix') {
    nameLabel = t('Prefix')
    namePlaceholder = '[按次]'
    nameHint = t(
      'e.g. [按次] matches every model whose name starts with [按次].'
    )
  } else if (match === 'suffix') {
    nameLabel = t('Suffix')
    namePlaceholder = '-thinking'
    nameHint = t(
      'e.g. -thinking matches every model whose name ends with -thinking.'
    )
  }

  // An exact rule saves without its counting (see upsertModelRateLimitRule),
  // so a rule switched from prefix or suffix to exact drops it.
  const handleSubmit = (values: ModelRateLimitDialogFormValues) => {
    if (props.editData) {
      // The name is locked while editing: save under the key of the rule
      // being edited, not its trimmed form, which can be another rule's key.
      props.onSave({ ...values, modelName: props.editData.modelName })
      props.onOpenChange(false)
      return
    }
    // One key is one rule, whatever its match mode.
    if (props.existingModelNames.includes(values.modelName)) {
      form.setError('modelName', {
        message: 'This model already has a rate limit',
      })
      return
    }
    props.onSave(values)
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        isEditMode ? t('Edit model rate limit') : t('Add model rate limit')
      }
      description={t(
        'Limit how many requests each user can send to one model.'
      )}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={MODEL_RATE_LIMIT_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        {/* noValidate: the schema reports out-of-range numbers in the
            interface language instead of the browser's own bubble. The
            dialog is portaled, but its submit still bubbles through the
            React tree to the settings form, which must not save on it. */}
        <form
          id={MODEL_RATE_LIMIT_FORM_ID}
          noValidate
          onSubmit={(event) => {
            event.stopPropagation()
            void form.handleSubmit(handleSubmit)(event)
          }}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='match'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Match mode')}</FormLabel>
                <FormControl>
                  <NativeSelect className='w-full' {...field}>
                    <NativeSelectOption value='exact'>
                      {t('Exact Match')}
                    </NativeSelectOption>
                    <NativeSelectOption value='prefix'>
                      {t('Prefix Match')}
                    </NativeSelectOption>
                    <NativeSelectOption value='suffix'>
                      {t('Suffix Match')}
                    </NativeSelectOption>
                  </NativeSelect>
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='modelName'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{nameLabel}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={namePlaceholder}
                    {...field}
                    disabled={isEditMode}
                  />
                </FormControl>
                {/* One description, which the input's aria-describedby
                    names, so the line tag hint is read with the field. */}
                <FormDescription>
                  {isEditMode
                    ? t('Model name cannot be changed when editing.')
                    : nameHint}
                  {showLineTagHint && (
                    <span className='text-warning mt-1 block'>
                      {t(
                        'This looks like a line tag. An exact rule only limits the model named exactly this; to limit every model whose name starts with it, choose Prefix Match.'
                      )}
                    </span>
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {match !== 'exact' && (
            <FormField
              control={form.control}
              name='shared'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Counting')}</FormLabel>
                  <FormControl>
                    <NativeSelect
                      className='w-full'
                      name={field.name}
                      ref={field.ref}
                      onBlur={field.onBlur}
                      value={field.value ? 'shared' : 'separate'}
                      onChange={(e) =>
                        field.onChange(e.target.value === 'shared')
                      }
                    >
                      <NativeSelectOption value='separate'>
                        {t('Count each model separately')}
                      </NativeSelectOption>
                      <NativeSelectOption value='shared'>
                        {t('Count all matching models together')}
                      </NativeSelectOption>
                    </NativeSelect>
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

          <FormField
            control={form.control}
            name='durationMinutes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Time window (minutes)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={MODEL_RATE_LIMIT_MAX_MINUTES}
                    step={1}
                    {...field}
                    onChange={(e) =>
                      field.onChange(Number.parseInt(e.target.value) || 0)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t('Counted per user over this many minutes, at most 1,440.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='totalCount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Max Requests (including failures)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    max={MODEL_RATE_LIMIT_MAX_COUNT}
                    step={1}
                    {...field}
                    onChange={(e) =>
                      field.onChange(Number.parseInt(e.target.value) || 0)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t('Total requests allowed per period. 0 = unlimited.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='successCount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Max Successful Requests')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    max={MODEL_RATE_LIMIT_MAX_COUNT}
                    step={1}
                    {...field}
                    onChange={(e) =>
                      field.onChange(Number.parseInt(e.target.value) || 0)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t('Successful requests allowed per period. 0 = unlimited.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
