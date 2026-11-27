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
import { describe, expect, test } from 'vitest'

import {
  findStaticImports,
  staticImportClosure,
} from '@/test-utils/static-import-graph'

const MODEL_DETAILS = /^features\/pricing\/components\/model-details\.tsx$/
const CHARTS =
  /^(features\/pricing\/components\/model-details-charts\.tsx|package:@visactor\/)/
const CODE_EDITOR =
  /^(components\/ai-elements\/code-block\.tsx|package:@codemirror\/)/

describe('model square chunks', () => {
  test('the model list loads neither the model details nor their charts and editor', () => {
    const closure = staticImportClosure('routes/pricing/index.tsx')

    expect(findStaticImports(closure, MODEL_DETAILS)).toEqual([])
    expect(findStaticImports(closure, CHARTS)).toEqual([])
    expect(findStaticImports(closure, CODE_EDITOR)).toEqual([])
  })

  test('the model details page loads its charts and editor only with their tabs', () => {
    const closure = staticImportClosure('routes/pricing/$modelId/index.tsx')

    expect(closure.modules).toContain(
      'features/pricing/components/model-details.tsx'
    )
    expect(findStaticImports(closure, CHARTS)).toEqual([])
    expect(findStaticImports(closure, CODE_EDITOR)).toEqual([])
  })
})
