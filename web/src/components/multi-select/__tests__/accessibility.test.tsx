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
import { render, screen } from '@testing-library/react'
import { useForm } from 'react-hook-form'
import { describe, expect, it } from 'vitest'

import { MultiSelect } from '@/components/multi-select'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'

const options = [
  { value: 'a', label: 'a' },
  { value: 'b', label: 'b' },
]

function FormFixture(props: { labelledByLabel: boolean }) {
  const form = useForm<{ values: string[] }>({
    defaultValues: { values: [] },
  })
  return (
    <Form {...form}>
      <FormField
        control={form.control}
        name='values'
        render={({ field }) => (
          <FormItem>
            <FormLabel id='values-label'>Values to keep</FormLabel>
            <FormControl>
              <MultiSelect
                options={options}
                selected={field.value}
                onChange={field.onChange}
                placeholder='Pick values'
                aria-labelledby={
                  props.labelledByLabel ? 'values-label' : undefined
                }
              />
            </FormControl>
            <FormDescription>Empty keeps every value.</FormDescription>
          </FormItem>
        )}
      />
    </Form>
  )
}

describe('multi-select accessible name and description', () => {
  it('inside a form field with aria-labelledby, is named by its label and described by its help text', () => {
    render(<FormFixture labelledByLabel />)
    const input = screen.getByRole('combobox', { name: 'Values to keep' })
    expect(input).toHaveAccessibleDescription('Empty keeps every value.')
    expect(input).toHaveAttribute('aria-invalid', 'false')
  })

  it('inside a form field without aria-labelledby, keeps its placeholder as the name and gets the help text', () => {
    render(<FormFixture labelledByLabel={false} />)
    const input = screen.getByRole('combobox', { name: 'Pick values' })
    expect(input).toHaveAccessibleDescription('Empty keeps every value.')
  })
})
