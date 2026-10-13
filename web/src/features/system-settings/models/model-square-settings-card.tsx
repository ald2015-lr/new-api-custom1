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
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormLabel,
} from '@/components/ui/form'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  modelSquareSettingsSchema,
  serializeVariantRules,
  toVariantRuleFormValues,
  type ModelSquareSettingsFormValues,
} from './model-square-settings-schema'
import { ModelVariantRulesEditor } from './model-variant-rules-editor'

const GROUPING_KEY = 'model_square_setting.variant_grouping_enabled'
const RULES_KEY = 'model_square_setting.variant_rules'

type ModelSquareSettingsCardProps = {
  defaultValues: {
    variantGroupingEnabled: boolean
    /** Raw JSON option value; empty or invalid falls back to the defaults. */
    variantRules: string
  }
}

export function ModelSquareSettingsCard(props: ModelSquareSettingsCardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()
  const enabled = props.defaultValues.variantGroupingEnabled
  const storedRules = props.defaultValues.variantRules
  const initialValues = useMemo<ModelSquareSettingsFormValues>(
    () => ({
      variant_grouping_enabled: enabled,
      variant_rules: toVariantRuleFormValues(storedRules),
    }),
    [enabled, storedRules]
  )

  const form = useForm<ModelSquareSettingsFormValues>({
    resolver: zodResolver(modelSquareSettingsSchema),
    defaultValues: initialValues,
  })

  useEffect(() => {
    form.reset(initialValues)
  }, [initialValues, form])

  const onSubmit = async (values: ModelSquareSettingsFormValues) => {
    const updates: { key: string; value: string | boolean }[] = []
    if (
      values.variant_grouping_enabled !== initialValues.variant_grouping_enabled
    ) {
      updates.push({
        key: GROUPING_KEY,
        value: values.variant_grouping_enabled,
      })
    }
    const rules = serializeVariantRules(values.variant_rules)
    if (rules !== serializeVariantRules(initialValues.variant_rules)) {
      updates.push({ key: RULES_KEY, value: rules })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    try {
      for (const update of updates) {
        await updateOption.mutateAsync(update)
      }
    } finally {
      // The Model Square reads these settings from the cached pricing
      // payload; refetch it even when only the first of two saves went through.
      await queryClient.invalidateQueries({ queryKey: ['pricing'] })
    }
  }

  return (
    <SettingsSection title={t('Model Square Display')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <FormField
            control={form.control}
            name='variant_grouping_enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Group model variants')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Show one card per base model in the Model Square. Suffix variants (such as -thinking-high) and lines (the leading [...] tag) are listed inside it. Display only: the model names used for calls and billing do not change.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <Separator />

          <ModelVariantRulesEditor control={form.control} />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
