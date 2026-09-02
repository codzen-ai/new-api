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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Meta, StoryObj } from 'storybook-react-rsbuild'

import type { PerformanceMetricsData } from '@/features/performance-metrics/types'

import {
  ENDPOINT_MAP,
  GROUP_RATIO,
  SPECIAL_EXPR_MODEL,
  TIERED_MODEL_WITH_GROUP_OVERRIDE,
  TIERED_MODEL_WITH_RULES,
  USABLE_GROUP,
} from './__fixtures__/tiered-pricing'
import { ModelDetailsContent } from './model-details'

/**
 * The overview tab reads `['perf-metrics', modelName]`. Seeding it keeps the
 * story deterministic and off the network, since the Storybook iframe has no
 * backend to answer `/api/perf-metrics`.
 */
function seededQueryClient(modelName: string): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const metrics: PerformanceMetricsData = {
    success: true,
    data: {
      model_name: modelName,
      groups: [
        {
          group: 'default',
          avg_ttft_ms: 480,
          avg_latency_ms: 1320,
          success_rate: 99.8,
          avg_tps: 62.4,
          series: [],
        },
      ],
    },
  }
  client.setQueryData(['perf-metrics', modelName], metrics)
  return client
}

const meta = {
  title: 'Pricing/ModelDetails (tiered)',
  component: ModelDetailsContent,
  args: {
    model: TIERED_MODEL_WITH_GROUP_OVERRIDE,
    groupRatio: GROUP_RATIO,
    usableGroup: USABLE_GROUP,
    endpointMap: ENDPOINT_MAP,
    autoGroups: [],
    priceRate: 1,
    usdExchangeRate: 1,
    tokenUnit: 'M',
  },
  argTypes: {
    tokenUnit: { control: 'inline-radio', options: ['M', 'K'] },
    showRechargePrice: { control: 'boolean' },
  },
  render: (args) => (
    <QueryClientProvider client={seededQueryClient(args.model.model_name)}>
      <ModelDetailsContent {...args} />
    </QueryClientProvider>
  ),
  parameters: {
    docs: {
      description: {
        component:
          'Details drawer content for a tiered model. Two tiered surfaces live here: the expression breakdown under Pricing, and Pricing by Group, which re-prices every tier per group. A group carrying a per-group model ratio uses that ratio instead of its group ratio, and the header of each group table states the multiplier applied.',
      },
    },
  },
} satisfies Meta<typeof ModelDetailsContent>

export default meta

type Story = StoryObj<typeof meta>

/**
 * `default` prices at 1x, `vip` at 0.5x — the per-group model ratio override,
 * not the 0.8 group ratio.
 */
export const TieredOverview: Story = {}

/** Adds the conditional multipliers section below the tier table. */
export const WithRequestRules: Story = {
  args: { model: TIERED_MODEL_WITH_RULES },
}

/** Per-1K prices across both the tier table and every group table. */
export const PerThousandTokens: Story = {
  args: { tokenUnit: 'K' },
}

/**
 * An expression with no tier structure: group prices cannot be expanded, so
 * the section explains why and falls back to the raw expression.
 */
export const SpecialExpression: Story = {
  args: { model: SPECIAL_EXPR_MODEL },
}
