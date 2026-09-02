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

import { ModelBillingModeBadge } from '@/features/pricing/components/model-billing-mode-badge'
import { QUOTA_TYPE_VALUES } from '@/features/pricing/constants'
import type { PricingModel } from '@/features/pricing/types'

const BASE_MODEL: PricingModel = {
  id: 1,
  model_name: 'gpt-5',
  quota_type: QUOTA_TYPE_VALUES.TOKEN,
  model_ratio: 1,
  completion_ratio: 4,
  enable_groups: ['default'],
}

const meta = {
  title: 'Pricing/ModelBillingModeBadge',
  component: ModelBillingModeBadge,
  args: { model: BASE_MODEL },
  parameters: {
    docs: {
      description: {
        component:
          'Badge shown on the model square. The label is driven purely by the pricing payload: `billing_mode` + `billing_expr` mean tiered/dynamic pricing, `quota_type` distinguishes per-token from per-request models. Switch the Language toolbar to check the translations.',
      },
    },
  },
} satisfies Meta<typeof ModelBillingModeBadge>

export default meta

type Story = StoryObj<typeof meta>

/** `quota_type: 0` — priced per token. */
export const TokenBased: Story = {}

/** `quota_type: 1` — a flat charge per call. */
export const PerRequest: Story = {
  args: {
    model: { ...BASE_MODEL, quota_type: QUOTA_TYPE_VALUES.REQUEST },
  },
}

/** `billing_mode: 'tiered_expr'` with a parseable expression. */
export const DynamicPricing: Story = {
  args: {
    model: {
      ...BASE_MODEL,
      billing_mode: 'tiered_expr',
      billing_expr:
        'p<=200000 ? {in: 1.25, out: 10} : {in: 2.5, out: 15} @ "long context"',
    },
  },
}

/**
 * `billing_mode` set but no expression: the model square falls back to the
 * plain token-based badge rather than claiming dynamic pricing.
 */
export const TieredModeWithoutExpression: Story = {
  args: {
    model: { ...BASE_MODEL, billing_mode: 'tiered_expr' },
  },
}
