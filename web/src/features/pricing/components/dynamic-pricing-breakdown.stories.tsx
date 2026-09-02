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

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import {
  MULTIMODAL_EXPR,
  REQUEST_RULE_TRACE,
  SPECIAL_EXPR,
  TIERED_CACHE_EXPR,
  TIERED_WITH_RULES_EXPR,
} from './__fixtures__/tiered-pricing'
import { DynamicPricingBreakdown } from './dynamic-pricing-breakdown'

const meta = {
  title: 'Pricing/DynamicPricingBreakdown',
  component: DynamicPricingBreakdown,
  args: { billingExpr: TIERED_CACHE_EXPR },
  argTypes: {
    billingExpr: { control: 'text' },
    matchedTierLabel: { control: 'text' },
    hideCacheColumns: { control: 'boolean' },
    compact: { control: 'boolean' },
  },
  parameters: {
    docs: {
      description: {
        component:
          'Tier table rendered from the stored billing expression. The model square shows it in the details drawer under Pricing; the usage-log details dialog reuses the same component in `compact` mode with the tier that actually fired highlighted. Price columns appear only for the variables the expression sets, so a cache-less expression shows no cache columns. Prices follow the site currency configuration.',
      },
    },
  },
} satisfies Meta<typeof DynamicPricingBreakdown>

export default meta

type Story = StoryObj<typeof meta>

/** Two length-keyed tiers, each with cache read / write / 1h-write prices. */
export const TieredWithCache: Story = {}

/** One tier carrying image and audio prices — different columns, same table. */
export const Multimodal: Story = {
  args: { billingExpr: MULTIMODAL_EXPR },
}

/** Header and time conditions parsed out of the expression as multipliers. */
export const WithConditionalMultipliers: Story = {
  args: { billingExpr: TIERED_WITH_RULES_EXPR },
}

/** How the model square hides cache columns for a request that used no cache. */
export const CacheColumnsHidden: Story = {
  args: { hideCacheColumns: true },
}

/**
 * The usage-log view: dense layout, the tier the engine picked highlighted, and
 * the settlement trace showing which multipliers actually fired.
 */
export const SettlementTrace: Story = {
  args: {
    billingExpr: TIERED_WITH_RULES_EXPR,
    matchedTierLabel: 'long_context',
    requestRules: REQUEST_RULE_TRACE,
    compact: true,
  },
}

/** No tier structure to expand, so the raw expression is shown instead. */
export const SpecialExpression: Story = {
  args: { billingExpr: SPECIAL_EXPR },
}

/** Same tiers converted with the site's CNY exchange rate. */
export const LocalCurrency: Story = {
  beforeEach: () => {
    const previous = useSystemConfigStore.getState().config.currency
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...previous,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7.3,
      },
    })
    return () => {
      useSystemConfigStore
        .getState()
        .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
    }
  },
}
