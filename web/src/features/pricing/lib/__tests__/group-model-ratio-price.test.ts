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
  formatFixedPrice,
  formatGroupPrice,
  formatPrice,
  formatRequestPrice,
} from '../price'

/**
 * 只声明定价所需字段；其余按 PricingModel 的可选字段留空。
 * 断言一律用「等价配置产出同一价格」的形式，避免依赖全局货币显示配置。
 */
function makeModel(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    id: 1,
    model_name: 'gmr-model',
    quota_type: QUOTA_TYPE_VALUES.TOKEN,
    model_ratio: 10,
    completion_ratio: 1,
    enable_groups: ['vip'],
    group_ratio: { vip: 0.8 },
    ...overrides,
  }
}

/** 与被测模型等价的参照：直接把最终倍率写进 model_ratio，分组倍率为 1。 */
function priceAtFinalRatio(finalRatio: number): string {
  return formatGroupPrice(
    makeModel({ model_ratio: finalRatio, group_ratio: { vip: 1 } }),
    'vip',
    'input',
    'M',
    false,
    1,
    1,
    { vip: 1 }
  )
}

function groupPrice(
  model: PricingModel,
  group: string,
  groupRatio: Record<string, number>
): string {
  return formatGroupPrice(model, group, 'input', 'M', false, 1, 1, groupRatio)
}

describe('分组模型倍率对定价展示的影响', () => {
  test('取代分组倍率，但全局模型倍率照常相乘', () => {
    const model = makeModel({
      group_model_ratio: { vip: { 'gmr-model': 0.5 } },
    })

    // 最终倍率 = 全局 10 × 0.5 = 5，而不是 10 × 0.8 × 0.5 = 4（不叠加分组倍率）。
    expect(groupPrice(model, 'vip', { vip: 0.8 })).toBe(priceAtFinalRatio(5))
    expect(groupPrice(model, 'vip', { vip: 0.8 })).not.toBe(
      priceAtFinalRatio(4)
    )
    expect(groupPrice(model, 'vip', { vip: 0.8 })).not.toBe(
      priceAtFinalRatio(8)
    )
  })

  test('未命中时回退为 model_ratio × group_ratio', () => {
    const model = makeModel({
      group_model_ratio: { svip: { 'gmr-model': 0.5 } },
    })

    expect(groupPrice(model, 'vip', { vip: 0.8 })).toBe(priceAtFinalRatio(8))
  })

  test('分组倍率为 0 不被当作未配置', () => {
    // effectiveRatioProduct 若用 `groupRatio[group] || 1` 会把 0 吃成 1，
    // 免费分组会被按原价展示。
    const model = makeModel({ group_ratio: { vip: 0 } })

    expect(groupPrice(model, 'vip', { vip: 0 })).toBe(priceAtFinalRatio(0))
    expect(groupPrice(model, 'vip', { vip: 0 })).not.toBe(priceAtFinalRatio(10))
  })

  test('倍率为 0 表示该分组免费', () => {
    const model = makeModel({
      group_model_ratio: { vip: { 'gmr-model': 0 } },
    })

    expect(groupPrice(model, 'vip', { vip: 0.8 })).toBe(priceAtFinalRatio(0))
  })
})

describe('模型广场汇总价（formatPrice）', () => {
  const summaryPrice = (model: PricingModel, selectedGroup?: string) =>
    formatPrice(model, 'input', 'M', false, 1, 1, selectedGroup)

  test('指定分组时展示该分组的价格，且应用分组模型倍率', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'gmr-model': 0.2 } },
    })

    expect(summaryPrice(model, 'vip')).toBe(priceAtFinalRatio(2))
    expect(summaryPrice(model, 'svip')).toBe(priceAtFinalRatio(5))
  })

  test('未指定分组时取跨分组最低价，分组模型倍率参与比较', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'gmr-model': 0.2 } },
    })

    // vip = 10 × 0.2 = 2；svip = 10 × 0.5 = 5。忽略该倍率则最低价会是 5。
    expect(summaryPrice(model)).toBe(priceAtFinalRatio(2))
    expect(summaryPrice(model)).not.toBe(priceAtFinalRatio(5))
  })

  test('分组模型倍率更贵时最低价仍取其他分组', () => {
    const model = makeModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'gmr-model': 0.9 } },
    })

    // vip = 10 × 0.9 = 9，svip 仍为 5，最低价应取 svip。
    expect(summaryPrice(model)).toBe(priceAtFinalRatio(5))
  })

  test('既无分组模型倍率又无分组倍率的分组不参与最低价比较', () => {
    const model = makeModel({
      enable_groups: ['vip', 'ghost'],
      group_ratio: { vip: 0.8 },
      group_model_ratio: {},
    })

    expect(summaryPrice(model)).toBe(priceAtFinalRatio(8))
  })

  test('筛选到未启用该模型的分组时回退为最低价', () => {
    const model = makeModel({
      enable_groups: ['vip'],
      group_ratio: { vip: 0.8, other: 0.1 },
      group_model_ratio: {},
    })

    expect(summaryPrice(model, 'other')).toBe(priceAtFinalRatio(8))
  })
})

describe('固定价模型的分组模型倍率', () => {
  /** 按次计费模型：系数乘在固定价上，同样取代分组倍率。 */
  function makeFixedModel(overrides: Partial<PricingModel> = {}): PricingModel {
    return {
      id: 2,
      model_name: 'gmr-draw',
      quota_type: QUOTA_TYPE_VALUES.REQUEST,
      model_ratio: 0,
      completion_ratio: 1,
      model_price: 0.04,
      enable_groups: ['vip'],
      group_ratio: { vip: 0.8 },
      ...overrides,
    }
  }

  /** 参照：把最终价直接写进 model_price，分组倍率为 1。 */
  function priceAtFinalPrice(finalPrice: number): string {
    return formatFixedPrice(
      makeFixedModel({ model_price: finalPrice, group_ratio: { vip: 1 } }),
      'vip',
      false,
      1,
      1,
      { vip: 1 }
    )
  }

  test('取代分组倍率，乘在固定价上', () => {
    const model = makeFixedModel({
      group_model_ratio: { vip: { 'gmr-draw': 0.5 } },
    })

    // 0.04 × 0.5 = 0.02，而不是 0.04 × 0.8 = 0.032，也不是 0.04 × 0.8 × 0.5 = 0.016
    expect(formatFixedPrice(model, 'vip', false, 1, 1, { vip: 0.8 })).toBe(
      priceAtFinalPrice(0.02)
    )
  })

  test('未命中时回退分组倍率', () => {
    const model = makeFixedModel()

    expect(formatFixedPrice(model, 'vip', false, 1, 1, { vip: 0.8 })).toBe(
      priceAtFinalPrice(0.032)
    )
  })

  test('汇总价取最低，分组模型倍率参与比较', () => {
    const model = makeFixedModel({
      enable_groups: ['vip', 'svip'],
      group_ratio: { vip: 0.8, svip: 0.5 },
      group_model_ratio: { vip: { 'gmr-draw': 0.2 } },
    })

    // vip = 0.04 × 0.2 = 0.008；svip = 0.04 × 0.5 = 0.02
    expect(formatRequestPrice(model, false, 1, 1)).toBe(
      priceAtFinalPrice(0.008)
    )
    expect(formatRequestPrice(model, false, 1, 1, 'svip')).toBe(
      priceAtFinalPrice(0.02)
    )
  })
})
