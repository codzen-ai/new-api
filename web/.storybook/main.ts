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
import type { StorybookConfig } from 'storybook-react-rsbuild'

const config: StorybookConfig = {
  stories: ['../src/**/*.stories.@(ts|tsx)'],
  addons: ['@storybook/addon-docs'],
  framework: {
    name: 'storybook-react-rsbuild',
    options: {
      builder: {
        rsbuildConfigPath: '.storybook/rsbuild.config.ts',
      },
    },
  },
  typescript: {
    // NOT `react-docgen-typescript`: that parser needs the classic
    // `typescript` JS API (`ts.JsxEmit`), and this project typechecks with
    // `@typescript/native-preview` (tsgo). The only `typescript` in the tree is
    // the 7.x native preview pulled in as a peer, which no longer exposes that
    // API, so the TS docgen parser crashes at preview build time. `react-docgen`
    // reads prop types straight from the source AST and needs no TS install.
    reactDocgen: 'react-docgen',
  },
}

export default config
