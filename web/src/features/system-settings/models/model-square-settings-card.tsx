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
import { useEffect, useId, useMemo } from 'react'
import { useForm, useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  useFormField,
} from '@/components/ui/form'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  splitFamilyBases,
  validateFamilyBases,
} from '@/features/pricing/lib/model-variants'

import {
  SettingsControlChildren,
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateModelSquareOptions } from '../hooks/use-update-option'
import {
  modelSquareSettingsSchema,
  serializeFamilyBases,
  serializeVariantRules,
  toFamilyBasesFormValue,
  toVariantRuleFormValues,
  type ModelSquareSettingsFormValues,
} from './model-square-settings-schema'
import { ModelVariantRulesEditor } from './model-variant-rules-editor'

const RULES_KEY = 'model_square_setting.variant_rules'
const FAMILY_BASES_KEY = 'model_square_setting.family_bases'

type SwitchName =
  | 'variant_grouping_enabled'
  | 'version_spelling_fold_enabled'
  | 'snapshot_fold_enabled'
  | 'show_group_filter'
  | 'show_vendor_filter'
  | 'show_tag_filter'
  | 'show_quota_type_filter'
  | 'show_endpoint_type_filter'

/** The switches in save order, with their option keys. */
const SWITCH_KEYS: [SwitchName, string][] = [
  ['variant_grouping_enabled', 'model_square_setting.variant_grouping_enabled'],
  [
    'version_spelling_fold_enabled',
    'model_square_setting.version_spelling_fold_enabled',
  ],
  ['snapshot_fold_enabled', 'model_square_setting.snapshot_fold_enabled'],
  ['show_group_filter', 'model_square_setting.show_group_filter'],
  ['show_vendor_filter', 'model_square_setting.show_vendor_filter'],
  ['show_tag_filter', 'model_square_setting.show_tag_filter'],
  ['show_quota_type_filter', 'model_square_setting.show_quota_type_filter'],
  [
    'show_endpoint_type_filter',
    'model_square_setting.show_endpoint_type_filter',
  ],
]

/** Filter sidebar sections in sidebar order, labelled like the sidebar. */
const FILTER_SECTION_SWITCHES: { name: SwitchName; label: string }[] = [
  { name: 'show_group_filter', label: 'Groups' },
  { name: 'show_vendor_filter', label: 'Vendors' },
  { name: 'show_tag_filter', label: 'Model Tags' },
  { name: 'show_quota_type_filter', label: 'Pricing Type' },
  { name: 'show_endpoint_type_filter', label: 'Endpoint Type' },
]

type ModelSquareSettingsCardProps = {
  defaultValues: {
    variantGroupingEnabled: boolean
    /** Missing on older backends: on. */
    versionSpellingFoldEnabled?: boolean
    /** Missing on older backends: on. */
    snapshotFoldEnabled?: boolean
    /** Raw JSON option value; empty or invalid falls back to the defaults. */
    variantRules: string
    /** Raw JSON option value; empty or invalid means no base models. */
    familyBases?: string
    showGroupFilter?: boolean
    showVendorFilter?: boolean
    /** Missing: hidden, like the backend default. */
    showTagFilter?: boolean
    showQuotaTypeFilter?: boolean
    /** Missing: hidden, like the backend default. */
    showEndpointTypeFilter?: boolean
  }
}

/**
 * Each invalid base model on its own line, under the textarea. It takes the
 * form message id, so the textarea's aria-describedby points at the list.
 */
function FamilyBaseIssues(props: { text: string }) {
  const { t } = useTranslation()
  const { formMessageId } = useFormField()
  const issues = validateFamilyBases(splitFamilyBases(props.text))
  if (issues.length === 0) return null
  return (
    <ul
      id={formMessageId}
      data-slot='form-message'
      // A long name without break points must wrap, not widen the form.
      className='text-destructive flex min-w-0 flex-col gap-0.5 text-sm [overflow-wrap:anywhere]'
    >
      {issues.map((issue) => (
        <li key={`${issue.index}:${issue.message}`}>
          {t(issue.message, issue.values)}
        </li>
      ))}
    </ul>
  )
}

function SwitchField(props: {
  control: Control<ModelSquareSettingsFormValues>
  name: SwitchName
  label: string
  description?: string
  disabled?: boolean
}) {
  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <SettingsSwitchItem>
          <SettingsSwitchContent>
            <FormLabel>{props.label}</FormLabel>
            {props.description && (
              <FormDescription>{props.description}</FormDescription>
            )}
          </SettingsSwitchContent>
          <FormControl>
            <Switch
              checked={field.value}
              onCheckedChange={field.onChange}
              disabled={props.disabled}
            />
          </FormControl>
        </SettingsSwitchItem>
      )}
    />
  )
}

export function ModelSquareSettingsCard(props: ModelSquareSettingsCardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOptions = useUpdateModelSquareOptions()
  const filterHeadingId = useId()
  const stored = props.defaultValues
  const enabled = stored.variantGroupingEnabled
  const versionFold = stored.versionSpellingFoldEnabled ?? true
  const snapshotFold = stored.snapshotFoldEnabled ?? true
  const showGroups = stored.showGroupFilter ?? true
  const showVendors = stored.showVendorFilter ?? true
  const showTags = stored.showTagFilter ?? false
  const showQuotaTypes = stored.showQuotaTypeFilter ?? true
  const showEndpointTypes = stored.showEndpointTypeFilter ?? false
  const storedRules = stored.variantRules
  const storedBases = stored.familyBases ?? ''
  const initialValues = useMemo<ModelSquareSettingsFormValues>(
    () => ({
      variant_grouping_enabled: enabled,
      version_spelling_fold_enabled: versionFold,
      snapshot_fold_enabled: snapshotFold,
      show_group_filter: showGroups,
      show_vendor_filter: showVendors,
      show_tag_filter: showTags,
      show_quota_type_filter: showQuotaTypes,
      show_endpoint_type_filter: showEndpointTypes,
      family_bases: toFamilyBasesFormValue(storedBases),
      variant_rules: toVariantRuleFormValues(storedRules),
    }),
    [
      enabled,
      versionFold,
      snapshotFold,
      showGroups,
      showVendors,
      showTags,
      showQuotaTypes,
      showEndpointTypes,
      storedBases,
      storedRules,
    ]
  )

  const form = useForm<ModelSquareSettingsFormValues>({
    resolver: zodResolver(modelSquareSettingsSchema),
    defaultValues: initialValues,
  })
  // The folds only act on grouped cards; their values are kept while off.
  const grouping = useWatch({
    control: form.control,
    name: 'variant_grouping_enabled',
  })

  useEffect(() => {
    form.reset(initialValues)
  }, [initialValues, form])

  const onSubmit = async (values: ModelSquareSettingsFormValues) => {
    // Every changed option in one request, saved all or none: one toast, and
    // the refetched options never reset a switch that was not saved yet.
    const options: Record<string, string | boolean> = {}
    for (const [name, key] of SWITCH_KEYS) {
      if (values[name] !== initialValues[name]) options[key] = values[name]
    }
    const bases = serializeFamilyBases(values.family_bases)
    if (bases !== serializeFamilyBases(initialValues.family_bases)) {
      options[FAMILY_BASES_KEY] = bases
    }
    const rules = serializeVariantRules(values.variant_rules)
    if (rules !== serializeVariantRules(initialValues.variant_rules)) {
      options[RULES_KEY] = rules
    }

    if (Object.keys(options).length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    try {
      await updateOptions.mutateAsync(options)
    } catch {
      // The mutation shows the error; the form keeps the unsaved values.
    } finally {
      // The Model Square reads these settings from the cached pricing
      // payload; refetch it whatever the outcome.
      await queryClient.invalidateQueries({ queryKey: ['pricing'] })
    }
  }

  return (
    <SettingsSection title={t('Model Square Display')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOptions.isPending}
          />
          <SwitchField
            control={form.control}
            name='variant_grouping_enabled'
            label={t('Group model variants')}
            description={t(
              'Show one card per base model in the Model Square. Suffix variants (such as -thinking-high) and lines (the leading [...] tag) are listed inside it. Display only: the model names used for calls and billing do not change.'
            )}
          />
          <SettingsControlChildren>
            <SwitchField
              control={form.control}
              name='version_spelling_fold_enabled'
              label={t('Merge version spellings')}
              description={t(
                'Treat a version written with "-" between short numbers as the dotted spelling (claude-opus-4-5 and claude-opus-4.5), and ignore letter case and one leading "org/" prefix, so these names share one card.'
              )}
              disabled={!grouping}
            />
            <SwitchField
              control={form.control}
              name='snapshot_fold_enabled'
              label={t('Merge dated snapshots')}
              description={t(
                "Fold a date at the end of a name (-2024-07-18, -20251101, -0731 …) into the base model's card. The date is shown as the variant; snapshots are listed after the named variants, newest first."
              )}
              disabled={!grouping}
            />
          </SettingsControlChildren>

          <Separator />

          <div
            role='group'
            aria-labelledby={filterHeadingId}
            className='flex min-w-0 flex-col gap-1'
          >
            <h4 id={filterHeadingId} className='text-sm font-medium'>
              {t('Filter sidebar')}
            </h4>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Choose which filter sections the Model Square shows on the left and in the mobile filter drawer. A hidden section is not shown and its filter is not applied.'
              )}
            </p>
            {FILTER_SECTION_SWITCHES.map((section) => (
              <SwitchField
                key={section.name}
                control={form.control}
                name={section.name}
                label={t(section.label)}
              />
            ))}
          </div>

          <Separator />

          <FormField
            control={form.control}
            name='family_bases'
            render={({ field, fieldState }) => (
              <FormItem>
                <FormLabel>{t('Merged base models')}</FormLabel>
                <FormDescription>
                  {t(
                    'One base model per line, without the leading [...] line tag. Every model named after it plus "-" and anything else (for example gemini-4-argon-low and gemini-4-argon-medium for gemini-4-argon) is shown in that base model\'s card, and the rest of its name becomes the variant. Use it for suffixes the rules below do not cover. While "Merge version spellings" is on, base models are compared with the same spelling rules (claude-opus-4-5 also matches claude-opus-4.5-thinking-max and claude-opus-4-5-20251101).'
                  )}
                </FormDescription>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={3}
                    placeholder={t('e.g. gemini-4-argon')}
                    autoComplete='off'
                    spellCheck={false}
                    className='font-mono'
                  />
                </FormControl>
                {fieldState.error && <FamilyBaseIssues text={field.value} />}
              </FormItem>
            )}
          />

          <Separator />

          <ModelVariantRulesEditor control={form.control} />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
