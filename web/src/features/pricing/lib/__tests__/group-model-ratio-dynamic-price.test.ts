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

import { QUOTA_TYPE_VALUES } from '../../constants'
import type { PricingModel } from '../../types'
import {
  getDynamicDisplayGroupRatio,
  getDynamicGroupRatio,
} from '../dynamic-price'

/** 阶梯（tiered_expr）计费模型：分组模型倍率同样是「该模型在该分组的分组倍率」。 */
function makeModel(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    id: 1,
    model_name: 'expr-model',
    quota_type: QUOTA_TYPE_VALUES.TOKEN,
    model_ratio: 0,
    completion_ratio: 1,
    enable_groups: ['vip'],
    group_ratio: { vip: 0.8 },
    billing_mode: 'tiered_expr',
    billing_expr: 'tier("base", p * 3 + c * 15)',
    ...overrides,
  }
}

describe('阶梯模型的分组模型倍率（单个分组）', () => {
  test('命中时取代分组倍率', () => {
    const model = makeModel({
      group_model_ratio: { vip: { 'expr-model': 0.5 } },
    })

    // 0.5 直接作用在表达式结果上，而不是 0.5 × 0.8
    expect(getDynamicGroupRatio(model, 'vip', { vip: 0.8 })).toBe(0.5)
  })

  test('未命中时回退分组倍率', () => {
    const model = makeModel({
      group_model_ratio: { svip: { 'expr-model': 0.5 } },
    })

    expect(getDynamicGroupRatio(model, 'vip', { vip: 0.8 })).toBe(0.8)
  })

  test('倍率为 0 不被当作未配置', () => {
    const model = makeModel({
      group_model_ratio: { vip: { 'expr-model': 0 } },
    })

    expect(getDynamicGroupRatio(model, 'vip', { vip: 0.8 })).toBe(0)
  })

  test('分组倍率缺失时回退 1', () => {
    expect(getDynamicGroupRatio(makeModel(), 'vip', {})).toBe(1)
  })
})

describe('阶梯模型的模型广场汇总倍率', () => {
  test('指定分组时用该分组的倍率', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'expr-model': 0.2 } },
    })

    expect(getDynamicDisplayGroupRatio(model, 'vip')).toBe(0.2)
    expect(getDynamicDisplayGroupRatio(model, 'svip')).toBe(0.5)
  })

  test('未指定分组时取最低倍率，分组模型倍率参与比较', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'expr-model': 0.2 } },
    })

    expect(getDynamicDisplayGroupRatio(model)).toBe(0.2)
  })

  test('分组模型倍率更贵时最低倍率仍取其他分组', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'expr-model': 2 } },
    })

    expect(getDynamicDisplayGroupRatio(model)).toBe(0.5)
  })

  test('只配了分组模型倍率、没有分组倍率的分组参与比较', () => {
    const model = makeModel({
      enable_groups: ['vip', 'ghost'],
      group_ratio: { vip: 0.8 },
      group_model_ratio: { ghost: { 'expr-model': 0.3 } },
    })

    expect(getDynamicDisplayGroupRatio(model)).toBe(0.3)
  })

  test('两者都没有的分组不参与比较', () => {
    const model = makeModel({
      enable_groups: ['vip', 'ghost'],
      group_ratio: { vip: 0.8 },
    })

    expect(getDynamicDisplayGroupRatio(model)).toBe(0.8)
  })

  test('筛选到未启用该模型的分组时回退最低倍率', () => {
    const model = makeModel({
      enable_groups: ['vip'],
      group_ratio: { vip: 0.8, other: 0.1 },
    })

    expect(getDynamicDisplayGroupRatio(model, 'other')).toBe(0.8)
  })
})
