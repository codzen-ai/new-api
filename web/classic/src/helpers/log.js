/*
Copyright (C) 2025 QuantumNous

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

import { API } from './api';

export function getLogOther(otherStr) {
  if (otherStr === undefined || otherStr === null || otherStr === '') {
    return {};
  }
  if (typeof otherStr === 'object') {
    return otherStr;
  }
  try {
    return JSON.parse(otherStr);
  } catch (e) {
    console.error(`Failed to parse record.other: "${otherStr}".`, e);
    return null;
  }
}

/**
 * Fetches the log CSV export endpoint with the given filters and triggers a
 * browser file download. Throws an Error on failure.
 *
 * @param {object} filters - query parameters matching the /api/log/export/csv API
 * @returns {Promise<void>}
 */
export async function downloadLogsCsv(filters) {
  const {
    type = 0,
    username = '',
    token_name = '',
    model_name = '',
    start_timestamp = '',
    end_timestamp = '',
    channel = '',
    group = '',
    request_id = '',
  } = filters;

  const url = encodeURI(
    `/api/log/export/csv?type=${type}&username=${username}&token_name=${token_name}&model_name=${model_name}&start_timestamp=${start_timestamp}&end_timestamp=${end_timestamp}&channel=${channel}&group=${group}&request_id=${request_id}`,
  );

  const res = await API.get(url, { responseType: 'blob' });

  const contentType = res.headers['content-type'] || '';
  if (contentType.includes('application/json') || contentType.includes('text/plain')) {
    const text = await res.data.text();
    try {
      const json = JSON.parse(text);
      throw new Error(json.message || '导出日志失败');
    } catch (e) {
      if (e instanceof SyntaxError) throw new Error('导出日志失败');
      throw e;
    }
  }

  const blob = new Blob([res.data], { type: 'text/csv;charset=utf-8;' });
  const link = document.createElement('a');
  link.href = URL.createObjectURL(blob);
  const now = new Date();
  const ts = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}_${String(now.getHours()).padStart(2, '0')}${String(now.getMinutes()).padStart(2, '0')}${String(now.getSeconds()).padStart(2, '0')}`;
  link.download = `logs_${ts}.csv`;
  link.click();
  URL.revokeObjectURL(link.href);
}
