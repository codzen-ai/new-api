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
import type { Decorator, Preview } from 'storybook-react-rsbuild'

import { INTERFACE_LANGUAGE_OPTIONS } from '@/i18n/languages'

import { StoryProviders } from './story-providers'

import '@/styles/index.css'

const withAppProviders: Decorator = (Story, context) => (
  <StoryProviders
    theme={context.globals.theme === 'dark' ? 'dark' : 'light'}
    locale={String(context.globals.locale ?? 'en')}
  >
    <Story />
  </StoryProviders>
)

const preview: Preview = {
  decorators: [withAppProviders],
  globalTypes: {
    theme: {
      description: 'Color theme applied to the story',
      toolbar: {
        title: 'Theme',
        icon: 'circlehollow',
        items: [
          { value: 'light', title: 'Light', icon: 'sun' },
          { value: 'dark', title: 'Dark', icon: 'moon' },
        ],
        dynamicTitle: true,
      },
    },
    locale: {
      description: 'Interface language passed to i18next',
      toolbar: {
        title: 'Language',
        icon: 'globe',
        items: INTERFACE_LANGUAGE_OPTIONS.map((language) => ({
          value: language.code,
          title: language.label,
        })),
        dynamicTitle: true,
      },
    },
  },
  initialGlobals: {
    theme: 'light',
    locale: 'en',
  },
  parameters: {
    // The decorator paints `bg-background`, so the addon's own backgrounds
    // would only ever be visible as a border around the story.
    backgrounds: { disable: true },
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
  },
}

export default preview
