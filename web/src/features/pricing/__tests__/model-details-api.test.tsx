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
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { ModelDetailsApi } from '../components/model-details-api'
import type { PricingModel } from '../types'

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({
    status: { server_address: 'https://gateway.example.com' },
    loading: false,
    error: null,
  }),
}))

const imageModel: PricingModel = {
  id: 1,
  model_name: 'nai-diffusion-4-5-full',
  vendor_name: 'NovelAI',
  quota_type: 1,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  tags: 'Image',
  supported_endpoint_types: ['image-generation'],
}

const endpointMap = {
  'image-generation': { path: '/v1/images/generations', method: 'POST' },
}

// Providers such as NovelAI and gpt-image models answer with b64_json and no
// url, so a sample that only reads data[0].url prints nothing useful.
describe('model details image code sample', () => {
  it.each([
    {
      tab: 'cURL',
      b64: `jq -r '.data[0] | .b64_json // .url'`,
      url: `.url'`,
    },
    {
      tab: 'Python',
      b64: 'f.write(base64.b64decode(image.b64_json))',
      url: 'print(image.url)',
    },
    {
      tab: 'TypeScript',
      b64: `writeFileSync('image.png', Buffer.from(image.b64_json, 'base64'))`,
      url: 'console.log(image?.url)',
    },
    {
      tab: 'JavaScript',
      b64: `writeFileSync('image.png', Buffer.from(image.b64_json, 'base64'))`,
      url: 'console.log(image.url)',
    },
  ])(
    'copying the $tab sample saves b64_json when present and falls back to url',
    async (sample) => {
      const user = userEvent.setup()
      render(<ModelDetailsApi model={imageModel} endpointMap={endpointMap} />)

      await user.click(screen.getByRole('tab', { name: sample.tab }))
      await user.click(screen.getByRole('button', { name: 'Copy code' }))
      const code = await navigator.clipboard.readText()

      expect(code).toContain('https://gateway.example.com')
      expect(code).toContain('nai-diffusion-4-5-full')
      expect(code).toContain(sample.b64)
      expect(code).toContain(sample.url)
      expect(code.indexOf('b64_json')).toBeLessThan(code.lastIndexOf('url'))
      expect(code).not.toContain('data[0].url')
    }
  )
})
