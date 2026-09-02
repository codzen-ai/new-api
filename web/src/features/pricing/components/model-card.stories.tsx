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
import type { Meta, StoryObj } from 'storybook-react-rsbuild'
import { fn } from 'storybook/test'

import {
  SPECIAL_EXPR_MODEL,
  TIERED_MODEL,
  TIERED_MODEL_WITH_GROUP_OVERRIDE,
  TIERED_MODEL_WITH_RULES,
  TOKEN_RATIO_MODEL,
} from './__fixtures__/tiered-pricing'
import { ModelCard } from './model-card'

const meta = {
  title: 'Pricing/ModelCard (tiered)',
  component: ModelCard,
  args: {
    model: TIERED_MODEL,
    onClick: fn(),
    tokenUnit: 'M',
    priceRate: 1,
    usdExchangeRate: 1,
  },
  argTypes: {
    tokenUnit: { control: 'inline-radio', options: ['M', 'K'] },
    selectedGroup: { control: 'text' },
    showRechargePrice: { control: 'boolean' },
  },
  decorators: [
    (Story, context) => (
      <div className={context.parameters.wide ? 'max-w-3xl' : 'max-w-sm'}>
        <Story />
      </div>
    ),
  ],
  parameters: {
    docs: {
      description: {
        component:
          'Grid card in the model square. For a tiered model the summary price comes from the **first** tier of the expression, multiplied by the group ratio in effect, so the card stays comparable with token-ratio cards. An expression the client cannot decompose degrades to a warning plus the raw text rather than a wrong number.',
      },
    },
  },
} satisfies Meta<typeof ModelCard>

export default meta

type Story = StoryObj<typeof meta>

/** First-tier prices, no group filter, so the cheapest available group wins. */
export const TieredPricing: Story = {}

/** Conditional multipliers do not change the card summary, only the details. */
export const TieredWithRequestRules: Story = {
  args: { model: TIERED_MODEL_WITH_RULES },
}

/**
 * With the `vip` filter active the card prices that group. Here `vip` carries a
 * per-group model ratio of 0.5, which overrides its 0.8 group ratio.
 */
export const GroupOverrideApplied: Story = {
  args: { model: TIERED_MODEL_WITH_GROUP_OVERRIDE, selectedGroup: 'vip' },
}

/** Per-1K prices instead of per-1M. */
export const PerThousandTokens: Story = {
  args: { tokenUnit: 'K' },
}

/** Unparseable expression: warning plus the raw expression, no invented price. */
export const SpecialExpression: Story = {
  args: { model: SPECIAL_EXPR_MODEL },
}

/** A tiered card next to an ordinary token-ratio card, for visual comparison. */
export const ComparedWithTokenRatio: Story = {
  parameters: { wide: true },
  render: (args) => (
    <div className='grid gap-3 sm:grid-cols-2'>
      <ModelCard {...args} model={TIERED_MODEL} />
      <ModelCard {...args} model={TOKEN_RATIO_MODEL} />
    </div>
  ),
}
