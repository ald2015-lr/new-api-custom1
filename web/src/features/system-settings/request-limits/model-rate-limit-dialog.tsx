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

import {
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

const modelRateLimitDialogSchema = z.object({
  modelName: z
    .string()
    .trim()
    .min(1, 'Model name is required')
    .refine(
      (name) => [...name].length <= MODEL_RATE_LIMIT_MAX_NAME_LENGTH,
      'Model name must be at most 255 characters'
    ),
  durationMinutes: z
    .number()
    .int('Must be a whole number')
    .min(1, 'Must be from 1 to 1,440')
    .max(MODEL_RATE_LIMIT_MAX_MINUTES, 'Must be from 1 to 1,440'),
  totalCount: countSchema,
  successCount: countSchema,
})

type ModelRateLimitDialogFormValues = z.infer<typeof modelRateLimitDialogSchema>

const MODEL_RATE_LIMIT_FORM_ID = 'model-rate-limit-form'

const emptyRule: ModelRateLimitRule = {
  modelName: '',
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

  const handleSubmit = (values: ModelRateLimitDialogFormValues) => {
    if (!isEditMode && props.existingModelNames.includes(values.modelName)) {
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
            name='modelName'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Model Name')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('e.g. gpt-4o, gemini-2.5-flash')}
                    {...field}
                    disabled={isEditMode}
                  />
                </FormControl>
                <FormDescription>
                  {isEditMode
                    ? t('Model name cannot be changed when editing.')
                    : t(
                        'The exact model name the client sends, including any [...] prefix.'
                      )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

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
