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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Combobox } from '@/components/ui/combobox'
import { createServerError } from '@/lib/server-error-message'

import { createVendor } from '../api'
import { vendorsQueryKeys } from '../lib'
import type { BuiltinVendor, Vendor } from '../types'
import { vendorErrorMessage } from '../vendor-api'

const BUILTIN_VALUE_PREFIX = 'builtin:'

type VendorSelectProps = {
  vendors: readonly Vendor[]
  builtinVendors: readonly BuiltinVendor[]
  value: number
  onChange: (vendorId: number) => void
  onCreateError: (message: string) => void
  id?: string
  'aria-describedby'?: string
  'aria-invalid'?: React.AriaAttributes['aria-invalid']
}

/**
 * Picks a saved vendor, or saves one first: a built-in vendor with its default
 * icon, or a new name typed into the search. Models only reference saved
 * vendors, so a built-in vendor's display ID is never selected.
 */
export function VendorSelect(props: VendorSelectProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const savedNames = new Set(
    props.vendors.map((vendor) => vendor.name.trim().toLowerCase())
  )
  const unsavedBuiltins = props.builtinVendors.filter(
    (vendor) => !savedNames.has(vendor.name.trim().toLowerCase())
  )
  const options = [
    ...props.vendors.map((vendor) => ({
      value: String(vendor.id),
      label: vendor.name,
    })),
    ...unsavedBuiltins.map((vendor) => ({
      value: BUILTIN_VALUE_PREFIX + vendor.name,
      label: vendor.name,
      description: t('Built-in'),
    })),
  ]
  const create = useMutation({
    meta: { errorToast: false },
    mutationFn: async (vendor: { name: string; icon?: string }) => {
      const response = await createVendor(vendor)
      if (!response.success || !response.data) {
        throw createServerError(response, t('Operation failed'))
      }
      return response.data
    },
    onSuccess: async (vendor) => {
      // Refresh the options first so the new vendor shows by name.
      await queryClient.invalidateQueries({ queryKey: vendorsQueryKeys.all })
      props.onChange(vendor.id)
      toast.success(t('Vendor created successfully'))
    },
    onError: (error) => props.onCreateError(vendorErrorMessage(error)),
  })

  return (
    <Combobox
      id={props.id}
      aria-describedby={props['aria-describedby']}
      aria-invalid={props['aria-invalid']}
      options={options}
      value={props.value ? String(props.value) : null}
      onValueChange={(value) => {
        if (!value?.startsWith(BUILTIN_VALUE_PREFIX)) {
          props.onChange(value ? Number.parseInt(value) : 0)
          return
        }
        const builtin = unsavedBuiltins.find(
          (vendor) => BUILTIN_VALUE_PREFIX + vendor.name === value
        )
        if (builtin) create.mutate({ name: builtin.name, icon: builtin.icon })
      }}
      onCreate={(name: string) => create.mutate({ name })}
      createLabel='Create vendor "{{value}}"'
      disabled={create.isPending}
      className='w-full'
      placeholder={t('Select vendor')}
    />
  )
}
