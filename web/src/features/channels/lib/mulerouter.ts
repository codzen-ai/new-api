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
 * MuleRouter channel route table.
 *
 * Each route maps one upstream endpoint
 * (/vendors/{vendor}/v1/{model}/{action}) to the internal model name
 * "{vendor}/{model}/{action}", and declares every request field that
 * participates in billing. A field can only become a billing multiplier by
 * first being bounded, which is why bounds and multipliers live in the same
 * declaration. The backend enforces the same rules on save and per request.
 */

export interface MuleRouterBillingVar {
  name: string
  source?: string
  kind: 'int' | 'enum'
  default: string
  min?: number
  max?: number
  enum?: number[]
  values?: Record<string, number>
  divisor?: number
}

export interface MuleRouterRoute {
  vendor: string
  model: string
  action?: string
  billing_vars?: MuleRouterBillingVar[]
}

export interface MuleRouterConfig {
  routes: MuleRouterRoute[]
}

/** Sample route table shown as the editor placeholder (not user-facing prose). */
export const MULEROUTER_ROUTES_PLACEHOLDER = `{
  "routes": [
    {
      "vendor": "carrothub",
      "model": "wan2.2-i2v-spicy",
      "billing_vars": [
        { "name": "seconds", "source": "duration", "kind": "int",
          "enum": [5, 8], "default": "5", "divisor": 5 },
        { "name": "resolution", "source": "resolution", "kind": "enum",
          "values": { "480p": 1.0, "720p": 2.0 }, "default": "480p" }
      ]
    }
  ]
}`

export function muleRouterModelName(route: MuleRouterRoute): string {
  return `${route.vendor}/${route.model}/${route.action?.trim() || 'generation'}`
}

export function parseMuleRouterConfig(
  value: string | undefined
): MuleRouterConfig | null {
  const raw = (value || '').trim()
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return null
    }
    return parsed as MuleRouterConfig
  } catch {
    return null
  }
}

export function stringifyMuleRouterConfig(config: unknown): string {
  if (!config) return ''
  try {
    return JSON.stringify(config, null, 2)
  } catch {
    return ''
  }
}

/**
 * Mirrors the backend's save-time validation closely enough to catch the
 * common mistakes before a round trip; the backend remains the authority and
 * reports the offending index. Messages are static so they can be i18n keys.
 */
export function validateMuleRouterConfig(value: string | undefined): string {
  const raw = (value || '').trim()
  if (!raw) return 'MuleRouter route table is required'

  const config = parseMuleRouterConfig(raw)
  if (!config) return 'MuleRouter route table must be a JSON object'
  if (!Array.isArray(config.routes) || config.routes.length === 0) {
    return 'MuleRouter route table requires at least one route'
  }

  const seen = new Set<string>()
  for (const route of config.routes) {
    if (!route?.vendor?.trim() || !route?.model?.trim()) {
      return 'Every MuleRouter route requires vendor and model'
    }
    const name = muleRouterModelName(route)
    if (seen.has(name)) return 'MuleRouter routes must map to distinct models'
    seen.add(name)

    for (const billingVar of route.billing_vars || []) {
      if (!billingVar?.name?.trim()) {
        return 'Every billing var requires a name'
      }
      if (billingVar.kind !== 'int' && billingVar.kind !== 'enum') {
        return 'Every billing var must be of kind int or enum'
      }
      if (!String(billingVar.default ?? '').trim()) {
        return 'Every billing var requires a default'
      }
      if (
        billingVar.kind === 'enum' &&
        Object.keys(billingVar.values || {}).length === 0
      ) {
        return 'Every enum billing var requires values'
      }
      if (
        billingVar.kind === 'int' &&
        (billingVar.divisor || 0) > 0 &&
        billingVar.max === undefined &&
        !(billingVar.enum || []).length
      ) {
        return 'A billing var that contributes a multiplier must declare max or enum'
      }
    }
  }
  return ''
}
