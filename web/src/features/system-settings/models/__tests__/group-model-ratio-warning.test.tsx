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
import { render, screen, fireEvent } from '@testing-library/react'
import i18next from 'i18next'
import { useForm } from 'react-hook-form'
import { beforeAll, describe, expect, test } from 'vitest'

import { GroupRatioForm } from '../group-ratio-form'

const WARNING_KEY =
  'These models are billed by fixed price or expression, so the ratio set here will not take effect: {{models}}'

const GROUP_DEFAULTS = {
  GroupRatio: '{}',
  TopupGroupRatio: '{}',
  UserUsableGroups: '{}',
  GroupGroupRatio: '{}',
  GroupModelRatio: '{}',
  AutoGroups: '[]',
  MaxTokenAutoGroups: 5,
  DefaultUseAutoGroup: false,
  GroupSpecialUsableGroup: '{}',
}

function Harness(props: {
  groupModelRatio: string
  modelPrice: string
  billingMode: string
}) {
  const form = useForm({
    defaultValues: {
      ...GROUP_DEFAULTS,
      GroupModelRatio: props.groupModelRatio,
    },
  })

  return (
    <GroupRatioForm
      form={form}
      onSave={async () => {}}
      isSaving={false}
      modelPrice={props.modelPrice}
      billingMode={props.billingMode}
    />
  )
}

/** 警告只渲染在 JSON 模式下，表单默认是可视化模式。 */
function renderInJsonMode(props: {
  groupModelRatio: string
  modelPrice?: string
  billingMode?: string
}) {
  render(
    <Harness
      groupModelRatio={props.groupModelRatio}
      modelPrice={props.modelPrice ?? '{}'}
      billingMode={props.billingMode ?? '{}'}
    />
  )
  fireEvent.click(screen.getByText('Switch to JSON'))
}

/** 匹配警告文案并取出其中列出的模型名部分。 */
function warningText(): string | null {
  const node = screen.queryByText(/will not take effect:/)
  return node ? (node.textContent ?? '') : null
}

describe('分组模型倍率静默失效提示', () => {
  beforeAll(() => {
    i18next.addResourceBundle(
      'en',
      'translation',
      {
        [WARNING_KEY]: WARNING_KEY,
        'Switch to JSON': 'Switch to JSON',
      },
      true,
      true
    )
  })

  test('固定价模型被标出', () => {
    renderInJsonMode({
      groupModelRatio: '{"vip":{"draw-model":5}}',
      modelPrice: '{"draw-model":0.05}',
    })

    expect(warningText()).toContain('draw-model')
  })

  test('表达式计费模型被标出', () => {
    renderInJsonMode({
      groupModelRatio: '{"vip":{"expr-model":5}}',
      billingMode: '{"expr-model":"tiered_expr"}',
    })

    expect(warningText()).toContain('expr-model')
  })

  test('按量倍率计费模型不触发提示', () => {
    renderInJsonMode({
      groupModelRatio: '{"vip":{"ratio-model":5}}',
      modelPrice: '{"other-model":0.05}',
      billingMode: '{"other-model":"tiered_expr"}',
    })

    expect(warningText()).toBeNull()
  })

  test('billing_mode 为 ratio 的模型不触发提示', () => {
    renderInJsonMode({
      groupModelRatio: '{"vip":{"ratio-model":5}}',
      billingMode: '{"ratio-model":"ratio"}',
    })

    expect(warningText()).toBeNull()
  })

  test('跨分组去重后按名字排序列出', () => {
    renderInJsonMode({
      groupModelRatio:
        '{"vip":{"b-model":5,"ok-model":2},"svip":{"b-model":3,"a-model":1}}',
      modelPrice: '{"a-model":0.05,"b-model":0.05}',
    })

    const text = warningText() ?? ''
    expect(text).toContain('a-model, b-model')
    expect(text).not.toContain('ok-model')
  })

  test('GroupModelRatio 为非法 JSON 时不渲染提示也不抛错', () => {
    renderInJsonMode({
      groupModelRatio: '{"vip":',
      modelPrice: '{"draw-model":0.05}',
    })

    expect(warningText()).toBeNull()
  })

  test('覆盖表为空时不渲染提示', () => {
    renderInJsonMode({
      groupModelRatio: '{}',
      modelPrice: '{"draw-model":0.05}',
    })

    expect(warningText()).toBeNull()
  })
})
