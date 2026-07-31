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
import { t } from 'i18next'

import { api, getFreshAuthHeaders, type ApiRequestConfig } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type { UsageLog } from './data/schema'
import { buildQueryParams } from './lib/query-params'
import { parseTaskArtifactsResponse } from './lib/task-artifacts'
import type {
  GetLogsParams,
  GetLogsResponse,
  GetLogStatsParams,
  GetLogStatsResponse,
  GetMidjourneyLogsParams,
  GetTaskLogsParams,
  TaskArtifactsResponse,
  UserInfo,
} from './types'

// ============================================================================
// Generic API Helpers
// ============================================================================

function buildApiPath(endpoint: string, isAdmin: boolean): string {
  return isAdmin ? endpoint : `${endpoint}/self`
}

async function fetchLogs<T>(
  endpoint: string,
  params: T,
  isAdmin: boolean
): Promise<GetLogsResponse> {
  const paramRecord = params as unknown as Record<string, unknown>
  const queryParams = buildQueryParams({
    p: paramRecord.p || 1,
    page_size: paramRecord.page_size || 20,
    ...params,
  })
  const path = buildApiPath(endpoint, isAdmin)
  const res = await api.get(`${path}?${queryParams}`)
  return res.data
}

async function fetchLogStats<T>(
  endpoint: string,
  params: T,
  isAdmin: boolean
): Promise<GetLogStatsResponse> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const path = buildApiPath(endpoint, isAdmin)
  const res = await api.get(`${path}/stat?${queryParams}`)
  return res.data
}

// ============================================================================
// Common Log APIs
// ============================================================================

export const getAllLogs = (params: GetLogsParams = {}) =>
  fetchLogs('/api/log', params, true)

export const getUserLogs = (
  params: Omit<GetLogsParams, 'username' | 'channel'> = {}
) => fetchLogs('/api/log', params, false)

export const getLogStats = (params: GetLogStatsParams = {}) =>
  fetchLogStats('/api/log', params, true)

export const getUserLogStats = (
  params: Omit<GetLogStatsParams, 'username' | 'channel'> = {}
) => fetchLogStats('/api/log', params, false)

/**
 * Stream the logs matching params, newest first. The server sends one JSON
 * log per line and ends with an API response object; a stream that ends
 * without a successful one is a failed export. Resolves with the log count.
 */
export async function exportLogs(
  params: GetLogsParams,
  isAdmin: boolean,
  options: { signal: AbortSignal; onLog: (log: UsageLog) => void }
): Promise<number> {
  const queryParams = buildQueryParams(
    params as unknown as Record<string, unknown>
  )
  const response = await fetch(
    `${buildApiPath('/api/log', isAdmin)}/export?${queryParams}`,
    {
      credentials: 'include',
      headers: await getFreshAuthHeaders(),
      signal: options.signal,
    }
  )
  if (!response.ok || !response.body) {
    const body: unknown = await response.json().catch(() => null)
    throw createServerError(body ?? { status: response.status })
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let result: { success?: unknown; data?: { count?: number } } | null = null
  for (;;) {
    const { done, value } = await reader.read()
    buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = done ? '' : (lines.pop() ?? '')
    for (const line of lines) {
      if (!line.trim()) continue
      let parsed
      try {
        parsed = JSON.parse(line)
      } catch {
        throw createServerError(null)
      }
      if (typeof parsed.success === 'boolean') result = parsed
      else options.onLog(parsed as UsageLog)
    }
    if (done) break
  }
  if (result?.success !== true) {
    throw createServerError(result)
  }
  return result.data?.count ?? 0
}

export async function getUserInfo(
  userId: number
): Promise<{ success: boolean; message?: string; data?: UserInfo }> {
  const res = await api.get(`/api/user/${userId}`)
  return res.data
}

/**
 * Downloads the admin log export as a CSV blob, together with the filename the
 * server assigned via Content-Disposition.
 *
 * The endpoint reports failures as HTTP 200 with a JSON body, which the shared
 * response interceptor cannot detect here: under `responseType: 'blob'` the
 * payload is a Blob, so its `success` flag is never visible. Sniff the blob's
 * content type and surface the server message instead, otherwise a failed
 * export would be saved as a .csv file containing an error object.
 */
export async function exportLogsCsv(
  params: GetLogsParams
): Promise<{ blob: Blob; filename: string }> {
  const res = await api.get(
    `/api/log/export/csv?${buildQueryParams(
      params as unknown as Record<string, unknown>
    )}`,
    {
      responseType: 'blob',
      disableDuplicate: true,
    }
  )

  const blob = res.data as Blob
  if (blob.type.includes('json') || blob.type.includes('text/plain')) {
    const raw = await blob.text()
    let message = ''
    try {
      message = (JSON.parse(raw) as { message?: string }).message || ''
    } catch {
      message = ''
    }
    throw new Error(message || t('Failed to export logs'))
  }

  const disposition = String(res.headers['content-disposition'] || '')
  const filename = /filename="?([^";]+)"?/.exec(disposition)?.[1]

  return { blob, filename: filename || 'logs.csv' }
}

// ============================================================================
// MjProxy (Drawing) Logs API
// ============================================================================

export const getAllMidjourneyLogs = (params: GetMidjourneyLogsParams) =>
  fetchLogs('/api/mj', params, true)

export const getUserMidjourneyLogs = (params: GetMidjourneyLogsParams) =>
  fetchLogs('/api/mj', params, false)

// ============================================================================
// Task Logs API
// ============================================================================

export const getAllTaskLogs = (params: GetTaskLogsParams) =>
  fetchLogs('/api/task', params, true)

export const getUserTaskLogs = (params: GetTaskLogsParams) =>
  fetchLogs('/api/task', params, false)

const taskArtifactRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} satisfies ApiRequestConfig

export async function getTaskArtifacts(taskId: string) {
  const response = await api.get<TaskArtifactsResponse>(
    `/api/task/${encodeURIComponent(taskId)}/artifacts`,
    taskArtifactRequestConfig
  )
  return parseTaskArtifactsResponse(response.data)
}
