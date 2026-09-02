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
import { useEffect } from 'react'

import { DirectionProvider } from '@/context/direction-provider'
import { FontProvider } from '@/context/font-provider'
import { ThemeProvider } from '@/context/theme-provider'
import i18n from '@/i18n/config'

/**
 * Cookie name the story-level ThemeProvider reads from. Deliberately different
 * from the app's `vite-ui-theme` cookie: the toolbar owns the theme in
 * Storybook, and reusing the app cookie would let a previously visited page of
 * the real app pin every story to one theme.
 */
const STORYBOOK_THEME_COOKIE = 'storybook-ui-theme'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false, staleTime: Infinity },
  },
})

/**
 * Mirrors the provider stack `src/main.tsx` mounts around the router, minus the
 * router itself, so stories see the same theme tokens, fonts, direction and
 * query client the real app gives their component.
 */
export function StoryProviders(props: {
  theme: 'light' | 'dark'
  locale: string
  children: React.ReactNode
}) {
  useEffect(() => {
    if (i18n.language !== props.locale) void i18n.changeLanguage(props.locale)
  }, [props.locale])

  return (
    <QueryClientProvider client={queryClient}>
      {/* ThemeProvider seeds its state once from `defaultTheme`, so remounting
          on `key` is what makes the toolbar toggle take effect. */}
      <ThemeProvider
        key={props.theme}
        defaultTheme={props.theme}
        storageKey={STORYBOOK_THEME_COOKIE}
      >
        <FontProvider>
          <DirectionProvider>
            <div className='bg-background text-foreground min-h-dvh p-6'>
              {props.children}
            </div>
          </DirectionProvider>
        </FontProvider>
      </ThemeProvider>
    </QueryClientProvider>
  )
}
