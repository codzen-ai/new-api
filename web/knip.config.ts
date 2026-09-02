import type { KnipConfig } from 'knip'

const config: KnipConfig = {
  ignore: [
    'src/components/ui/**',
    'src/routeTree.gen.ts',
    // Referenced by `.storybook/main.ts` as a string path
    // (`framework.options.builder.rsbuildConfigPath`), so knip cannot see the edge.
    '.storybook/rsbuild.config.ts',
  ],
  ignoreDependencies: ['tailwindcss', 'tw-animate-css'],
}

export default config
