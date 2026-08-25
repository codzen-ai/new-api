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
import { EXCLUDED_GROUPS, FILTER_ALL, QUOTA_TYPE_VALUES } from '../constants'
import type { PricingModel } from '../types'
// ----------------------------------------------------------------------------
// Model Helper Utilities
// ----------------------------------------------------------------------------

/**
 * Get available groups for a model
 */
export function getAvailableGroups(
  model: PricingModel,
  usableGroup: Record<string, { desc: string; ratio: number }>
): string[] {
  const modelEnableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []

  return Object.keys(usableGroup)
    .filter((g) => !EXCLUDED_GROUPS.includes(g))
    .filter((g) => modelEnableGroups.includes(g))
}

/**
 * Read a configured group ratio while preserving valid zero ratios.
 */
export function getConfiguredGroupRatio(
  groupRatio: Record<string, number>,
  group: string
): number {
  const ratio = groupRatio[group]
  return typeof ratio === 'number' && Number.isFinite(ratio) ? ratio : 1
}

/**
 * Per-group model ratio for a model, or undefined when none is configured.
 *
 * This ratio IS that model's group ratio in that group: it replaces the group
 * ratio (and any inter-group override) while the model keeps its own pricing —
 * the global model ratio, the fixed price, or the billing expression. All three
 * billing modes honor it, so callers never branch on billing mode.
 */
export function getGroupModelRatio(
  model: PricingModel,
  group: string
): number | undefined {
  const ratio = model.group_model_ratio?.[group]?.[model.model_name]
  return typeof ratio === 'number' && Number.isFinite(ratio) ? ratio : undefined
}

/**
 * Group ratio actually applied to a model in one group: the per-group model
 * ratio when configured, otherwise the plain group ratio.
 */
export function getEffectiveGroupRatio(
  model: PricingModel,
  group: string,
  groupRatio: Record<string, number>
): number {
  return (
    getGroupModelRatio(model, group) ??
    getConfiguredGroupRatio(groupRatio, group)
  )
}

/**
 * Resolve the group ratio used by model square summary prices.
 *
 * When a group filter is active it shows that group's ratio, otherwise the best
 * one available to the viewer. Per-group model ratios participate in that
 * comparison, so a model priced down for one group shows that price.
 *
 * Shared by every billing mode — token ratio (× model_ratio), fixed price
 * (× model_price) and tiered (× expression output) — so they stay in sync by
 * construction.
 */
export function getDisplayEffectiveGroupRatio(
  model: PricingModel,
  selectedGroup?: string
): number {
  const enableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []
  const groupRatio = model.group_ratio || {}

  if (
    selectedGroup &&
    selectedGroup !== FILTER_ALL &&
    enableGroups.includes(selectedGroup)
  ) {
    return getEffectiveGroupRatio(model, selectedGroup, groupRatio)
  }

  if (enableGroups.length === 0) return 1

  let minRatio = Number.POSITIVE_INFINITY
  for (const group of enableGroups) {
    const perModel = getGroupModelRatio(model, group)
    const ratio = groupRatio[group]
    // Skip groups with neither a per-group model ratio nor a group ratio.
    if (
      perModel === undefined &&
      !(typeof ratio === 'number' && Number.isFinite(ratio))
    ) {
      continue
    }
    const effective = perModel ?? ratio
    if (effective < minRatio) minRatio = effective
  }

  return minRatio === Number.POSITIVE_INFINITY ? 1 : minRatio
}

/**
 * Replace model placeholder in endpoint path
 */
export function replaceModelInPath(path: string, modelName: string): string {
  return path.replaceAll('{model}', modelName)
}

/**
 * Check if model is token-based pricing
 */
export function isTokenBasedModel(model: PricingModel): boolean {
  return model.quota_type === QUOTA_TYPE_VALUES.TOKEN
}
