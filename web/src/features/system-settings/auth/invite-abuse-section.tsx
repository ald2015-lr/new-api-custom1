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
import { useMutation } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useState, type ChangeEvent, type FormEvent } from 'react'
import type { Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import {
  createServerError,
  requireServerSuccess,
} from '@/lib/server-error-message'

import { previewInviteAbuseBan } from '../api'
import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateInviteAbuseOptions } from '../hooks/use-update-option'
import type { InviteAbusePreviewParams } from '../types'
import { integerRangeSchema } from '../utils/numeric-field'
import { InviteAbusePreviewDialog } from './invite-abuse-preview-dialog'

const inviteAbuseSchema = z.object({
  InviteAbuseBanEnabled: z.boolean(),
  InviteAbuseBanThreshold: integerRangeSchema(
    1,
    100000,
    'Invite count threshold must be an integer between 1 and 100000'
  ),
  // The wallet ceiling, common.MaxWalletQuota, is 2^53 - 1.
  InviteAbuseBanMinRedeemQuota: integerRangeSchema(
    0,
    Number.MAX_SAFE_INTEGER,
    'Minimum redeemed quota must be a non-negative integer'
  ),
  InviteAbuseBanExemptPaidInviter: z.boolean(),
})

type InviteAbuseFormValues = z.infer<typeof inviteAbuseSchema>

type InviteAbuseSectionProps = {
  defaultValues: InviteAbuseFormValues
}

/** An emptied number input stays empty instead of becoming NaN. */
function numberInputValue(event: ChangeEvent<HTMLInputElement>): number | '' {
  const value = event.currentTarget.valueAsNumber
  return Number.isNaN(value) ? '' : value
}

export function InviteAbuseSection(props: InviteAbuseSectionProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const updateOptions = useUpdateInviteAbuseOptions()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [previewOpen, setPreviewOpen] = useState(false)

  const { form, handleSubmit, isDirty, isSubmitting } =
    useSettingsForm<InviteAbuseFormValues>({
      resolver: zodResolver(inviteAbuseSchema) as Resolver<
        InviteAbuseFormValues,
        unknown,
        InviteAbuseFormValues
      >,
      defaultValues: props.defaultValues,
      onSubmit: async (_data, changedFields) => {
        // One request saves every changed option, the switch included, or
        // none of them. Saved one per request, a save that stopped half-way
        // left the rule with some new values and some old ones, and the rule
        // then disabled accounts that neither the old nor the new settings
        // call abusive.
        await updateOptions.mutateAsync(
          changedFields as Record<string, string | number | boolean>
        )
      },
    })

  const preview = useMutation({
    mutationFn: async (params: InviteAbusePreviewParams) => {
      const response = requireServerSuccess(await previewInviteAbuseBan(params))
      if (!response.data) {
        throw createServerError(response, t('Failed to preview'))
      }
      return response.data
    },
    onSuccess: () => setPreviewOpen(true),
    onError: (error) => handleServerError(error, t('Failed to preview')),
  })

  const threshold = form.watch('InviteAbuseBanThreshold')
  const minRedeemQuota = form.watch('InviteAbuseBanMinRedeemQuota')

  const handlePreview = async () => {
    const valid = await form.trigger([
      'InviteAbuseBanThreshold',
      'InviteAbuseBanMinRedeemQuota',
    ])
    if (!valid) return
    // The unsaved values in the form, so the admin sees their effect first.
    preview.mutate({
      threshold: Number(form.getValues('InviteAbuseBanThreshold')),
      min_redeem_quota: Number(form.getValues('InviteAbuseBanMinRedeemQuota')),
      exempt_paid_inviter: form.getValues('InviteAbuseBanExemptPaidInviter'),
    })
  }

  const save = async () => {
    try {
      await handleSubmit()
    } catch (error) {
      // Nothing was saved and the form keeps the unsaved values; a failed
      // request was already shown.
      handleServerError(error, t('Failed to update setting'))
    }
  }

  const requestSave = async (event?: FormEvent) => {
    event?.preventDefault()
    const enabling =
      form.getValues('InviteAbuseBanEnabled') &&
      Boolean(form.formState.dirtyFields.InviteAbuseBanEnabled)
    if (enabling && (await form.trigger())) {
      setConfirmOpen(true)
      return
    }
    await save()
  }

  const confirmEnable = async () => {
    setConfirmOpen(false)
    await save()
  }

  return (
    <SettingsSection title={t('Invite abuse protection')}>
      <FormNavigationGuard when={isDirty} />
      <Form {...form}>
        <SettingsForm onSubmit={requestSave}>
          <SettingsPageFormActions
            onSave={() => void requestSave()}
            isSaving={updateOptions.isPending || isSubmitting}
          />
          <FormDirtyIndicator isDirty={isDirty} />

          <Alert>
            <AlertTitle>{t('How the rule works')}</AlertTitle>
            <AlertDescription>
              <p>
                {t(
                  'When a user has invited at least {{threshold}} users and none of them has a successful top-up or used redemption codes worth at least {{amount}}, that user and the users they invited are disabled automatically.',
                  {
                    threshold:
                      typeof threshold === 'number'
                        ? formatNumber(threshold, locale)
                        : '—',
                    amount:
                      typeof minRedeemQuota === 'number'
                        ? formatQuota(minRedeemQuota)
                        : '—',
                  }
                )}
              </p>
              <p>
                {t(
                  'Administrators are never disabled. Users that an administrator enables again are not disabled automatically again.'
                )}
              </p>
            </AlertDescription>
          </Alert>

          <FormField
            control={form.control}
            name='InviteAbuseBanEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Ban invite abuse automatically')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Checks an inviter when an invited user registers and every 10 minutes. Turning it on scans existing users immediately and can disable many accounts at once, so use Preview first.'
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

          <FormField
            control={form.control}
            name='InviteAbuseBanThreshold'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Invite count threshold')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={100000}
                    step={1}
                    value={field.value ?? ''}
                    onChange={(event) =>
                      field.onChange(numberInputValue(event))
                    }
                    name={field.name}
                    onBlur={field.onBlur}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Invited users, deleted ones included, from which an inviter is checked'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='InviteAbuseBanMinRedeemQuota'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Minimum redeemed quota')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    step={1}
                    value={field.value ?? ''}
                    onChange={(event) =>
                      field.onChange(numberInputValue(event))
                    }
                    name={field.name}
                    onBlur={field.onBlur}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'An invited user whose used redemption codes add up to at least this quota counts as recharged ({{formattedQuota}})',
                    {
                      formattedQuota: formatQuota(
                        typeof field.value === 'number' ? field.value : 0
                      ),
                    }
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='InviteAbuseBanExemptPaidInviter'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Exempt inviters who recharged')}</FormLabel>
                  <FormDescription>
                    {t(
                      'An inviter who has a successful top-up or enough used redemption codes is never disabled by this rule.'
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

          <div className='flex flex-wrap items-center gap-3'>
            <Button
              type='button'
              variant='outline'
              onClick={() => void handlePreview()}
              disabled={preview.isPending}
            >
              {preview.isPending ? (
                <Loader2 className='animate-spin' aria-hidden='true' />
              ) : null}
              {t('Preview')}
            </Button>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Counts the users the rule would disable with the settings above, before they are saved. Nobody is disabled by a preview.'
              )}
            </p>
          </div>
        </SettingsForm>
      </Form>

      <InviteAbusePreviewDialog
        open={previewOpen}
        onOpenChange={setPreviewOpen}
        preview={preview.data}
      />

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Turn on automatic invite abuse bans?')}
        desc={t(
          'Existing users are scanned right after saving, and every inviter matching the rule is disabled together with the users they invited. Use Preview to see how many accounts that is.'
        )}
        confirmText={t('Turn on')}
        destructive
        handleConfirm={() => void confirmEnable()}
      />
    </SettingsSection>
  )
}
