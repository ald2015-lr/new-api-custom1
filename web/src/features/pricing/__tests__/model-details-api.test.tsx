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
// url, so a sample that only reads data[0].url prints nothing useful, and an
// error answer carries no image at all. Each sample's handling of the answer
// is pinned line by line: b64_json is decoded to image.png, otherwise the url
// is printed, otherwise (cURL, fetch) the answer is printed as received; the
// SDKs throw on an error answer themselves. The cURL sample pipes with
// printf: zsh, dash and bash with xpg_echo turn the \n escapes of JSON
// strings in an echo into newlines, which jq rejects.
describe('model details image code sample', () => {
  it.each([
    {
      tab: 'cURL',
      request: `response=$(curl -sS https://gateway.example.com/v1/images/generations \\`,
      handling: [
        String.raw`if image=$(printf '%s\n' "$response" | jq -er '.data[0].b64_json'); then`,
        `  printf '%s' "$image" | base64 --decode > image.png`,
        `else`,
        String.raw`  printf '%s\n' "$response" | jq -r '.data[0].url // .'`,
        `fi`,
      ],
    },
    {
      tab: 'Python',
      request: `client = OpenAI(base_url="https://gateway.example.com/v1", api_key="<YOUR_API_KEY>")`,
      handling: [
        'image = response.data[0]',
        'if image.b64_json:',
        '    with open("image.png", "wb") as f:',
        '        f.write(base64.b64decode(image.b64_json))',
        'else:',
        '    print(image.url)',
      ],
    },
    {
      tab: 'TypeScript',
      request: `  baseURL: 'https://gateway.example.com/v1',`,
      handling: [
        `const image = response.data?.[0]`,
        `if (image?.b64_json) {`,
        `  writeFileSync('image.png', Buffer.from(image.b64_json, 'base64'))`,
        `} else {`,
        `  console.log(image?.url)`,
        `}`,
      ],
    },
    {
      tab: 'JavaScript',
      request: `const response = await fetch('https://gateway.example.com/v1/images/generations', {`,
      handling: [
        `const body = await response.text()`,
        `const image = response.ok ? JSON.parse(body).data?.[0] : undefined`,
        `if (image?.b64_json) {`,
        `  writeFileSync('image.png', Buffer.from(image.b64_json, 'base64'))`,
        `} else if (image?.url) {`,
        `  console.log(image.url)`,
        `} else {`,
        `  console.error(response.status, body)`,
        `}`,
      ],
    },
  ])(
    'the copied $tab sample saves b64_json to image.png, else prints the url or the error answer',
    async (sample) => {
      const user = userEvent.setup()
      render(<ModelDetailsApi model={imageModel} endpointMap={endpointMap} />)

      await user.click(screen.getByRole('tab', { name: sample.tab }))
      await user.click(screen.getByRole('button', { name: 'Copy code' }))
      const code = await navigator.clipboard.readText()
      const lines = code.split('\n')

      expect(lines).toContain(sample.request)
      expect(code).toContain('nai-diffusion-4-5-full')
      expect(lines.slice(-sample.handling.length)).toEqual(sample.handling)
    }
  )
})
