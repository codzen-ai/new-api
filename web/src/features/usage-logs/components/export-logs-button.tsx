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
import { useMutation } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { Table } from '@tanstack/react-table'
import { Download, Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { exportLogsCsv } from '../api'
import { buildApiParams } from '../lib/utils'
import { useLogsViewScope } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

/**
 * Admin-only CSV export for the Common Logs view. Exports exactly the rows the
 * current view resolves to, by reusing the same parameter builder as the table
 * query — including active column filters, so the file matches what is on
 * screen rather than only the submitted filter-bar values.
 *
 * The server streams the whole result set and rejects overly broad ranges, so
 * no page/page_size is sent.
 */
export function ExportLogsButton<TData>(props: { table: Table<TData> }) {
  const { t } = useTranslation()
  const searchParams = route.useSearch()
  const { isAdminView: isAdmin } = useLogsViewScope()

  const { mutate: exportLogs, isPending } = useMutation({
    mutationFn: async () => {
      const {
        p: _p,
        page_size: _pageSize,
        ...params
      } = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: props.table.getState().columnFilters,
        isAdmin,
      })

      const { blob, filename } = await exportLogsCsv(params)

      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filename
      link.click()
      URL.revokeObjectURL(url)
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to export logs'))
    },
  })

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            onClick={() => exportLogs()}
            disabled={isPending}
            aria-label={t('Export CSV')}
            className='text-muted-foreground hover:text-foreground size-7'
          />
        }
      >
        {isPending ? <Loader2 className='animate-spin' /> : <Download />}
      </TooltipTrigger>
      <TooltipContent>{t('Export CSV')}</TooltipContent>
    </Tooltip>
  )
}
