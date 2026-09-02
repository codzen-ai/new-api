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
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from '@rsbuild/core'
import { pluginReact } from '@rsbuild/plugin-react'
import { pluginTailwindcss } from '@rsbuild/plugin-tailwindcss'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

// Storybook-only Rsbuild config, deliberately not the app's `rsbuild.config.ts`.
// The app config owns the SPA entry, the `index.html` template, the dev proxy
// and the TanStack Router code generator — none of which apply to the Storybook
// iframe, and several of which fight the builder's own entry/HTML handling.
// Only the parts stories actually need are mirrored here: React, Tailwind and
// the `@` alias.
export default defineConfig({
  plugins: [pluginReact(), pluginTailwindcss({ optimize: false })],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, '../src'),
    },
  },
})
