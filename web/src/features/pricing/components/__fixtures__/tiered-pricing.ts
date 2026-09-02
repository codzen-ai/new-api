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
/**
 * Fixtures for the tiered-billing stories of the model square.
 *
 * The expressions below are written in the compiled form the backend actually
 * stores and ships in the pricing payload, not the authoring DSL: request rules
 * appear as plain `(condition ? multiplier : 1)` factors multiplied onto the
 * tier expression, which is what `splitBillingExprAndRequestRules` expects.
 * See `pkg/billingexpr/expr.md` for the language itself.
 */
import { QUOTA_TYPE_VALUES } from '../../constants'
import type { RequestRuleTrace } from '../../lib/billing-expr'
import type { PricingModel } from '../../types'

/**
 * Two tiers keyed on input length, each with cache read / cache write /
 * 1h cache write prices. Mirrors the Claude Sonnet example in `expr.md`.
 */
export const TIERED_CACHE_EXPR =
  'len <= 200000' +
  ' ? tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6)' +
  ' : tier("long_context", p * 6 + c * 22.5 + cr * 0.6 + cc * 7.5 + cc1h * 12)'

/** Single tier carrying image and audio prices instead of cache prices. */
export const MULTIMODAL_EXPR =
  'tier("base", p * 0.43 + c * 3.06 + img * 0.78 + ai * 3.81 + ao * 15.11)'

/** Header-driven surcharge factor: `anthropic-beta: fast-mode` costs 6x. */
const FAST_MODE_RULE =
  '(has(header("anthropic-beta"), "fast-mode") ? 6 : 1)'

/** Overnight discount factor: 22:00-06:00 Shanghai time is half price. */
const OFF_PEAK_RULE =
  '(hour("Asia/Shanghai") >= 22 || hour("Asia/Shanghai") < 6 ? 0.5 : 1)'

/** Tiers plus both conditional multipliers, exactly as stored. */
export const TIERED_WITH_RULES_EXPR = `(${TIERED_CACHE_EXPR}) * ${FAST_MODE_RULE} * ${OFF_PEAK_RULE}`

/**
 * An expression the UI cannot decompose into tiers. The model square degrades
 * to showing the raw expression instead of inventing a price table.
 */
export const SPECIAL_EXPR = 'p * 2.5 + c * 15 + max(0, len - 1000) * 0.001'

/**
 * Settlement trace for one request: the fast-mode surcharge fired, the
 * overnight discount did not.
 */
export const REQUEST_RULE_TRACE: RequestRuleTrace[] = [
  {
    cond: 'has(header("anthropic-beta"), "fast-mode")',
    multiplier: 6,
    matched: true,
  },
  {
    cond: 'hour("Asia/Shanghai") >= 22 || hour("Asia/Shanghai") < 6',
    multiplier: 0.5,
    matched: false,
  },
]

const BASE_MODEL: PricingModel = {
  id: 1,
  model_name: 'claude-sonnet-4-5',
  description:
    'Tiered pricing: the long-context tier doubles every unit price once the request exceeds 200K input tokens.',
  icon: 'claude',
  vendor_name: 'Anthropic',
  quota_type: QUOTA_TYPE_VALUES.TOKEN,
  model_ratio: 1.5,
  completion_ratio: 5,
  enable_groups: ['default', 'vip'],
  group_ratio: { default: 1, vip: 0.8 },
  supported_endpoint_types: ['openai', 'anthropic'],
  tags: 'reasoning,vision',
  context_length: 1_000_000,
  max_output_tokens: 64_000,
  input_modalities: ['text', 'image'],
  output_modalities: ['text'],
  capabilities: ['function_calling', 'streaming', 'vision', 'caching'],
}

/** Tiered model with cache pricing and no request rules. */
export const TIERED_MODEL: PricingModel = {
  ...BASE_MODEL,
  billing_mode: 'tiered_expr',
  billing_expr: TIERED_CACHE_EXPR,
}

/** Tiered model whose expression also carries conditional multipliers. */
export const TIERED_MODEL_WITH_RULES: PricingModel = {
  ...BASE_MODEL,
  billing_mode: 'tiered_expr',
  billing_expr: TIERED_WITH_RULES_EXPR,
}

/**
 * Tiered model that also has a per-group model ratio. `vip` is priced by the
 * override (0.5) rather than by its group ratio (0.8), which is the case the
 * group price table has to get right.
 */
export const TIERED_MODEL_WITH_GROUP_OVERRIDE: PricingModel = {
  ...TIERED_MODEL,
  group_model_ratio: { vip: { 'claude-sonnet-4-5': 0.5 } },
}

/** Tiered model whose expression cannot be expanded into a price table. */
export const SPECIAL_EXPR_MODEL: PricingModel = {
  ...BASE_MODEL,
  id: 2,
  model_name: 'custom-metered-model',
  description: 'Bills through a bespoke expression with no tier structure.',
  icon: undefined,
  vendor_name: undefined,
  billing_mode: 'tiered_expr',
  billing_expr: SPECIAL_EXPR,
}

/** Plain token-ratio model, for side-by-side comparison with a tiered card. */
export const TOKEN_RATIO_MODEL: PricingModel = {
  ...BASE_MODEL,
  id: 3,
  model_name: 'gpt-5-mini',
  description: 'Ordinary per-token pricing driven by model_ratio.',
  icon: 'openai',
  vendor_name: 'OpenAI',
  model_ratio: 0.125,
  completion_ratio: 8,
  cache_ratio: 0.1,
  billing_mode: undefined,
  billing_expr: undefined,
}

export const GROUP_RATIO: Record<string, number> = { default: 1, vip: 0.8 }

export const USABLE_GROUP: Record<string, { desc: string; ratio: number }> = {
  default: { desc: 'Default group', ratio: 1 },
  vip: { desc: 'VIP group', ratio: 0.8 },
}

export const ENDPOINT_MAP: Record<string, { path?: string; method?: string }> =
  {
    openai: { path: '/v1/chat/completions', method: 'POST' },
    anthropic: { path: '/v1/messages', method: 'POST' },
  }
