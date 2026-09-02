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
import { CircleCheck } from 'lucide-react'
import type { Meta, StoryObj } from 'storybook-react-rsbuild'

import {
  StatusBadge,
  dotColorMap,
  type StatusBadgeType,
} from '@/components/status-badge'

const VARIANTS = Object.keys(dotColorMap) as (keyof typeof dotColorMap)[]
const TYPES: StatusBadgeType[] = ['badge', 'text', 'underline']

const meta = {
  title: 'Components/StatusBadge',
  component: StatusBadge,
  args: {
    label: 'Active',
    variant: 'success',
    size: 'sm',
    copyable: false,
  },
  argTypes: {
    variant: { control: 'select', options: VARIANTS },
    size: { control: 'inline-radio', options: ['sm', 'md', 'lg'] },
    type: { control: 'inline-radio', options: TYPES },
  },
} satisfies Meta<typeof StatusBadge>

export default meta

type Story = StoryObj<typeof meta>

export const Default: Story = {}

export const WithIcon: Story = {
  args: { icon: CircleCheck, label: 'Verified' },
}

export const Copyable: Story = {
  args: { label: 'sk-1a2b3c4d', variant: 'neutral', copyable: true },
}

/** Every semantic variant, so palette regressions show up in one screen. */
export const AllVariants: Story = {
  render: (args) => (
    <div className='flex max-w-2xl flex-wrap gap-2'>
      {VARIANTS.map((variant) => (
        <StatusBadge
          key={variant}
          {...args}
          variant={variant}
          label={variant}
        />
      ))}
    </div>
  ),
}

/** The three visual styles side by side at each size. */
export const TypesAndSizes: Story = {
  render: (args) => (
    <div className='space-y-3'>
      {TYPES.map((type) => (
        <div key={type} className='flex items-center gap-3'>
          <span className='text-muted-foreground w-20 text-xs'>{type}</span>
          {(['sm', 'md', 'lg'] as const).map((size) => (
            <StatusBadge
              key={size}
              {...args}
              type={type}
              size={size}
              label={`${type} / ${size}`}
            />
          ))}
        </div>
      ))}
    </div>
  ),
}
