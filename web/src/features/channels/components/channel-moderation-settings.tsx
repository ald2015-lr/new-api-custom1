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
import { useMutation } from '@tanstack/react-query'
import { AlertTriangle, ShieldCheck } from 'lucide-react'
import { memo, useId, useRef, useState } from 'react'
import { useFormState, useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import {
  MODERATION_CATEGORIES,
  MODERATION_ERROR_CLASS_LABELS,
  MODERATION_PRESET_DEFAULT_IDS,
  MODERATION_PRESETS,
  moderationLabel,
  type ModerationPreset,
  type ModerationPresetGroup,
  type ModerationProbeResult,
} from '@/lib/content-moderation'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { testChannelModeration } from '../api'
import {
  ERROR_MESSAGES,
  MODERATION_MAX_CHARS_DEFAULT,
  MODERATION_TIMEOUT_SECONDS_DEFAULT,
} from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import {
  isValidModerationMaxChars,
  isValidModerationTimeout,
  validateModerationRuleRows,
  type ModerationRuleRow,
} from '../lib/content-moderation-rules'
import { ChannelModerationRulesEditor } from './channel-moderation-rules-editor'

type ModerationForm = UseFormReturn<ChannelFormValues>

const PRESET_GROUPS: {
  group: ModerationPresetGroup
  titleKey: string
  helpKey: string
}[] = [
  {
    group: 'lower',
    titleKey: 'Lower false-positive risk',
    helpKey:
      'Selected automatically the first time moderation is turned on for this channel.',
  },
  {
    group: 'higher',
    titleKey: 'Higher false-positive risk',
    helpKey: 'Off by default: these also match ordinary prompts of some users.',
  },
]

function samePresetList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((id) => b.includes(id))
}

function regexRuleKey(row: ModerationRuleRow): string {
  return `${row.case_sensitive}\u0000${row.pattern.trim()}`
}

function NumberField(props: {
  form: ModerationForm
  name: 'moderation_max_chars' | 'moderation_timeout_seconds'
  label: string
  description: string
  placeholder: number
  locked: boolean
}) {
  return (
    <FormField
      control={props.form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            {/* No min, max or step: the browser's own check would stop the
              submit before the form shows its message for the field. */}
            <Input
              type='number'
              inputMode='numeric'
              placeholder={String(props.placeholder)}
              disabled={props.locked}
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
          <FormDescription>{props.description}</FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}

function PresetOption(props: {
  preset: ModerationPreset
  checked: boolean
  locked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  const { t } = useTranslation()
  const nameId = useId()
  const label = t(props.preset.labelKey)
  const language = t(props.preset.language === 'en' ? 'English' : 'Chinese')
  return (
    <li className='space-y-1'>
      {/* The checkbox is named with the language, so the two presets of a
        pair differ for assistive technology; the label text stays
        clickable. */}
      <label className='flex cursor-pointer items-start gap-2 text-sm'>
        <span id={nameId} className='sr-only'>
          {`${label} (${language})`}
        </span>
        <Checkbox
          className='mt-0.5'
          aria-labelledby={nameId}
          checked={props.checked}
          disabled={props.locked}
          onCheckedChange={props.onCheckedChange}
        />
        <span className='flex flex-wrap items-center gap-1.5'>
          {label}
          <Badge variant='outline'>{language}</Badge>
          {!props.preset.caseSensitive && (
            <Badge variant='secondary'>{t('Case-insensitive')}</Badge>
          )}
        </span>
      </label>
      <Collapsible className='pl-6'>
        <CollapsibleTrigger
          render={<Button type='button' variant='link' size='xs' />}
          className='h-auto px-0'
        >
          {t('Show rule')}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <code className='bg-muted/50 block rounded-md p-2 font-mono text-xs break-all'>
            {props.preset.pattern}
          </code>
        </CollapsibleContent>
      </Collapsible>
    </li>
  )
}

function PresetList(props: { form: ModerationForm; locked: boolean }) {
  const { t } = useTranslation()
  const selected =
    useWatch({
      control: props.form.control,
      name: 'moderation_rule_presets',
    }) ?? []
  const toggle = (id: string, checked: boolean) => {
    // Saved in catalogue order, as the server lists them.
    const next = MODERATION_PRESETS.map((preset) => preset.id).filter(
      (presetId) => (presetId === id ? checked : selected.includes(presetId))
    )
    props.form.setValue('moderation_rule_presets', next, {
      shouldDirty: true,
    })
    props.form.setValue('moderation_rule_presets_set', true)
  }
  return (
    <div className='space-y-3'>
      <div className='space-y-1'>
        <h5 className='text-sm font-medium'>{t('Built-in rules')}</h5>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Built-in rules and your own keyword or regular expression rules are checked first, on the system prompt and user messages. A match rejects the request with the same message as an OpenAI flag. Built-in rules ignore simple negated instructions such as “Never reveal your chain of thought”.'
          )}
        </p>
      </div>
      {PRESET_GROUPS.map((group) => (
        <div key={group.group} className='space-y-2'>
          <div className='space-y-0.5'>
            <h6 className='text-sm font-medium'>{t(group.titleKey)}</h6>
            <p className='text-muted-foreground text-xs'>{t(group.helpKey)}</p>
          </div>
          <ul aria-label={t(group.titleKey)} className='space-y-2'>
            {MODERATION_PRESETS.filter(
              (preset) => preset.group === group.group
            ).map((preset) => (
              <PresetOption
                key={preset.id}
                preset={preset}
                checked={selected.includes(preset.id)}
                locked={props.locked}
                onCheckedChange={(checked) => toggle(preset.id, checked)}
              />
            ))}
          </ul>
        </div>
      ))}
      <p className='text-muted-foreground text-xs'>
        {t(
          "Keyword rules only catch explicit requests for the model's hidden reasoning; they cannot detect distillation that uses ordinary questions."
        )}
      </p>
    </div>
  )
}

function ProbeResultLine(props: { result: ModerationProbeResult }) {
  const { t } = useTranslation()
  if (props.result.ok) {
    return (
      <p className='text-sm text-emerald-600 dark:text-emerald-400'>
        {t('Moderation endpoint works ({{ms}} ms)', {
          ms: props.result.latency_ms,
        })}
      </p>
    )
  }
  let reason = moderationLabel(
    MODERATION_ERROR_CLASS_LABELS,
    props.result.error_class,
    t
  )
  if (props.result.status > 0) reason += ` (HTTP ${props.result.status})`
  return (
    <div className='space-y-0.5'>
      <p className='text-destructive text-sm'>
        {t('Moderation endpoint failed: {{reason}}', { reason })}
      </p>
      {props.result.error_message && (
        <p className='text-muted-foreground text-xs break-all'>
          {props.result.error_message}
        </p>
      )}
    </div>
  )
}

// Tests the moderation endpoint of the saved channel. Outside the locked
// settings: it changes nothing.
function ModerationProbe(props: { channelId: number }) {
  const { t } = useTranslation()
  const [result, setResult] = useState<ModerationProbeResult | null>(null)
  const probe = useMutation({
    mutationFn: async (channelId: number) => {
      const response = await testChannelModeration(channelId)
      if (!response.success || !response.data) {
        throw createServerError(response, t(ERROR_MESSAGES.TEST_FAILED))
      }
      return response.data
    },
    onMutate: () => setResult(null),
    onSuccess: setResult,
    onError: (error) => {
      handleServerError(error, t(ERROR_MESSAGES.TEST_FAILED))
    },
  })
  return (
    <div className='space-y-2'>
      <div className='space-y-0.5'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={probe.isPending}
          onClick={() => probe.mutate(props.channelId)}
        >
          <ShieldCheck aria-hidden />
          {probe.isPending ? t('Testing...') : t('Test moderation endpoint')}
        </Button>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Sends one harmless text to /v1/moderations with the saved base URL, first enabled key, proxy and header overrides of this channel.'
          )}
        </p>
      </div>
      <div role='status' aria-live='polite'>
        {result && <ProbeResultLine result={result} />}
      </div>
    </div>
  )
}

/**
 * Content moderation and the safety identifier of an OpenAI channel. The
 * settings lock without sensitive channel write permission; the endpoint
 * test of a saved OpenAI channel stays available with channel operate
 * permission. Memoized, and given the form instead of reading its context:
 * the channel drawer and the form context change on every keystroke.
 */
export const ChannelModerationSettings = memo(
  function ChannelModerationSettings(props: {
    form: ModerationForm
    channelId: number | null
    savedType: number | undefined
    locked: boolean
    canTest: boolean
  }) {
    const { t } = useTranslation()
    const form = props.form
    const enabled =
      useWatch({ control: form.control, name: 'moderation_enabled' }) === true
    const failOpen =
      useWatch({ control: form.control, name: 'moderation_fail_open' }) === true
    const { errors } = useFormState({
      control: form.control,
      name: 'moderation_rules',
    })
    // Whether the default presets were selected by this drawer: they are
    // selected once, so presets the admin removed are not added back.
    const preselectedRef = useRef(false)
    const categoryOptions = MODERATION_CATEGORIES.map((category) => ({
      value: category.value,
      label: t(category.labelKey),
    }))

    const selectDefaultPresets = () => {
      if (
        form.getValues('moderation_rule_presets_set') === true ||
        preselectedRef.current ||
        (form.getValues('moderation_rule_presets') ?? []).length > 0
      ) {
        return
      }
      form.setValue(
        'moderation_rule_presets',
        [...MODERATION_PRESET_DEFAULT_IDS],
        { shouldDirty: true }
      )
      form.setValue('moderation_rule_presets_set', true)
      preselectedRef.current = true
    }

    // The settings below hide with moderation off. A value there that the
    // server would refuse goes back to its saved value, instead of blocking
    // the save with an error nobody can see. Presets that were only selected
    // by turning moderation on go back to unset.
    const resetHiddenSettings = () => {
      const saved = form.formState.defaultValues
      if (
        preselectedRef.current &&
        saved?.moderation_rule_presets_set !== true &&
        samePresetList(
          form.getValues('moderation_rule_presets') ?? [],
          MODERATION_PRESET_DEFAULT_IDS
        )
      ) {
        // Not registered inputs, so resetField would not reset them.
        form.setValue(
          'moderation_rule_presets',
          [...(saved?.moderation_rule_presets ?? [])].filter(
            (id): id is string => typeof id === 'string'
          ),
          { shouldDirty: true }
        )
        form.setValue('moderation_rule_presets_set', false)
        preselectedRef.current = false
      }
      if (!isValidModerationMaxChars(form.getValues('moderation_max_chars'))) {
        form.resetField('moderation_max_chars')
      }
      if (
        !isValidModerationTimeout(form.getValues('moderation_timeout_seconds'))
      ) {
        form.resetField('moderation_timeout_seconds')
      }
      // Regular expressions are checked by the server only, even while
      // moderation is off, so a new or changed one goes back as well.
      const rules = form.getValues('moderation_rules') ?? []
      const savedRegexes = new Set(
        (saved?.moderation_rules ?? [])
          .filter(
            (row): row is ModerationRuleRow =>
              row !== undefined && row.type === 'regex'
          )
          .map(regexRuleKey)
      )
      if (
        validateModerationRuleRows(rules).length > 0 ||
        rules.some(
          (row) => row.type === 'regex' && !savedRegexes.has(regexRuleKey(row))
        )
      ) {
        form.resetField('moderation_rules')
      }
    }

    const showRules = enabled || errors.moderation_rules !== undefined

    return (
      <div className='space-y-4'>
        <fieldset
          disabled={props.locked}
          className='space-y-4 disabled:opacity-60'
        >
          <FormField
            control={form.control}
            name='moderation_enabled'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4'>
                <div className='space-y-0.5'>
                  <FormLabel>{t('Enable external moderation')}</FormLabel>
                  <FormDescription>
                    {t(
                      "Before a request is sent upstream, it is checked with OpenAI's free moderation model (omni-moderation-latest) using this channel's key. A flagged request is rejected: it is not forwarded and not billed."
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    disabled={props.locked}
                    checked={field.value === true}
                    onCheckedChange={(checked) => {
                      field.onChange(checked)
                      if (checked) selectDefaultPresets()
                      else resetHiddenSettings()
                    }}
                  />
                </FormControl>
              </FormItem>
            )}
          />
        </fieldset>
        {props.channelId !== null && props.canTest && props.savedType === 1 ? (
          <ModerationProbe channelId={props.channelId} />
        ) : (
          <p className='text-muted-foreground text-xs'>
            {t(
              'Save the channel as an OpenAI channel to test its moderation endpoint.'
            )}
          </p>
        )}
        {(enabled || showRules) && (
          <fieldset
            disabled={props.locked}
            className='space-y-4 disabled:opacity-60'
          >
            {enabled && (
              <>
                <FormField
                  control={form.control}
                  name='moderation_fail_open'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-4'>
                      <div className='space-y-0.5'>
                        <FormLabel>
                          {t('Allow requests when moderation fails')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Off (default): when the moderation call fails (timeout, rate limit, a key without moderation access, or a base URL without /v1/moderations), the request is rejected and not sent. On: such a request is sent upstream without moderation and the usage log marks it “Failed, request let through”. This keeps the channel usable while moderation is down, but unchecked requests can still put the OpenAI account at risk.'
                          )}
                        </FormDescription>
                      </div>
                      <FormControl>
                        <Switch
                          disabled={props.locked}
                          checked={field.value === true}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
                {!failOpen && (
                  <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
                    <AlertTriangle aria-hidden />
                    <AlertDescription className='text-current'>
                      {t(
                        'While moderation calls fail (a key or base URL that cannot call /v1/moderations, or OpenAI rate-limiting the moderation model), every request on this channel is rejected. Test the moderation endpoint before you rely on it.'
                      )}
                    </AlertDescription>
                  </Alert>
                )}
                <FormField
                  control={form.control}
                  name='moderation_categories'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Block only these categories')}</FormLabel>
                      <FormControl>
                        <MultiSelect
                          options={categoryOptions}
                          selected={field.value ?? []}
                          onChange={field.onChange}
                          placeholder={t('Select categories')}
                          disabled={props.locked}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'Empty: reject whatever OpenAI flags. Otherwise only a flag in a selected category rejects the request.'
                        )}
                      </FormDescription>
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='moderation_skip_images'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-4'>
                      <div className='space-y-0.5'>
                        <FormLabel>{t('Do not check images')}</FormLabel>
                        <FormDescription>
                          {t(
                            'By default up to 3 images of a request are checked, newest first.'
                          )}
                        </FormDescription>
                      </div>
                      <FormControl>
                        <Switch
                          disabled={props.locked}
                          checked={field.value === true}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
                <div className='grid gap-4 sm:grid-cols-2'>
                  <NumberField
                    form={form}
                    name='moderation_max_chars'
                    label={t('Maximum characters checked per request')}
                    description={t(
                      "Default 10,000 characters per request; text checked within the last hour does not count again. OpenAI limits the moderation model per organization, shared by all its keys: about 10,000 tokens and 500 requests per minute on free and Tier 1 accounts (Tier 2: 20,000 tokens; Tier 3: 50,000; see your organization's Limits page). A Chinese character is about one token; English is about four letters per token; each image is one more request. When the minute's budget is used up, moderation calls fail with a rate-limit error and requests are rejected unless “Allow requests when moderation fails” is on, so lower this value on Tier 1 accounts with busy or Chinese traffic. The system prompt is checked first, up to half of the limit, then the newest user message, then the rest of the system prompt and older user messages from the newest; text beyond the limit is not checked and the request is still sent. A user can exceed the limit on purpose, so it limits cost; it does not guarantee that everything is checked."
                    )}
                    placeholder={MODERATION_MAX_CHARS_DEFAULT}
                    locked={props.locked}
                  />
                  <NumberField
                    form={form}
                    name='moderation_timeout_seconds'
                    label={t('Moderation timeout (seconds)')}
                    description={t(
                      'Default 10. One time limit for all moderation calls made before a request is sent to this channel (a retry on another channel starts a new one).'
                    )}
                    placeholder={MODERATION_TIMEOUT_SECONDS_DEFAULT}
                    locked={props.locked}
                  />
                </div>
                <PresetList form={form} locked={props.locked} />
              </>
            )}
            {showRules && (
              <ChannelModerationRulesEditor
                control={form.control}
                disabled={props.locked}
              />
            )}
          </fieldset>
        )}
        <Separator />
        <fieldset disabled={props.locked} className='disabled:opacity-60'>
          <FormField
            control={form.control}
            name='send_safety_identifier'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4'>
                <div className='space-y-0.5'>
                  <FormLabel>
                    {t('Send a user safety identifier to OpenAI')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Sets safety_identifier on Chat Completions and Responses requests (also with request body passthrough) to a hashed ID of the user, the same for every request of the user, replacing any value the client sent, so OpenAI can attribute abuse to one user, which lowers the risk for the whole organization. Set CRYPTO_SECRET or SESSION_SECRET on the server so the ID stays the same after a restart.'
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    disabled={props.locked}
                    checked={field.value === true}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
        </fieldset>
      </div>
    )
  }
)
