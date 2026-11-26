import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig, loadEnv, type RsbuildPlugin } from '@rsbuild/core'
import { pluginReact } from '@rsbuild/plugin-react'
import { pluginTailwindcss } from '@rsbuild/plugin-tailwindcss'
import { tanstackRouter } from '@tanstack/router-plugin/rspack'

import { slimTranslationSource } from './scripts/slim-locale.mjs'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

// Production bundles get en.json without its identity entries (key === value);
// see scripts/slim-locale.mjs. Development keeps the full file.
const pluginSlimEnglishLocale: RsbuildPlugin = {
  name: 'newapi:slim-english-locale',
  setup(api) {
    api.transform(
      { test: /[\\/]src[\\/]i18n[\\/]locales[\\/]en\.json$/ },
      ({ code }) => slimTranslationSource(code)
    )
  },
}

export default defineConfig(({ envMode }) => {
  const env = loadEnv({ mode: envMode, prefixes: ['VITE_'] })
  const serverUrl =
    process.env.VITE_REACT_APP_SERVER_URL ||
    env.rawPublicVars.VITE_REACT_APP_SERVER_URL ||
    'http://localhost:3000'

  const isProd = envMode === 'production'
  const devProxy = Object.fromEntries(
    (['/api', '/v1', '/mj', '/pg'] as const).map((key) => [
      key,
      { target: serverUrl, changeOrigin: true },
    ])
  ) as Record<string, { target: string; changeOrigin: boolean }>

  return {
    plugins: [
      pluginReact(),
      pluginTailwindcss({ optimize: false }),
      ...(isProd ? [pluginSlimEnglishLocale] : []),
    ],
    // Rsbuild 2: replaces deprecated `performance.chunkSplit` (RSPack 2 aligned)
    // React itself is in the preset's `lib-react` chunk.
    splitChunks: {
      preset: 'default',
      // Fewer, larger lazy chunks, so that a cold load of a console page
      // stays at about 20 JS/CSS files: over HTTP/1.1 a browser opens only six
      // connections per host and queues the rest. Sizes are before
      // minification. Shared vendor code gets a chunk of its own from 200 KB;
      // shared app code (the `default` group below) only from 600 KB, below
      // that it stays in the chunks of the pages that use it.
      minSize: 200_000,
      cacheGroups: {
        // Rspack's built-in `default` group with a higher minSize. A group
        // given here replaces the built-in one, so all its fields are listed.
        default: {
          idHint: '',
          minChunks: 2,
          priority: -20,
          reuseExistingChunk: true,
          minSize: 600_000,
        },
        // Only what the entry needs: with `chunks: 'all'` every module of
        // these packages used anywhere (settings, tables, editors) would be
        // forced into the first load. Modules used only by lazy pages go to
        // those pages' chunks.
        'vendor-ui-primitives': {
          test: /node_modules[\\/](@base-ui|@radix-ui)[\\/]/,
          name: 'vendor-ui-primitives',
          chunks: 'initial',
          priority: 0,
          enforce: true,
        },
        'vendor-tanstack': {
          test: /node_modules[\\/]@tanstack[\\/]/,
          name: 'vendor-tanstack',
          chunks: 'initial',
          priority: 0,
          enforce: true,
        },
      },
    },
    source: {
      entry: {
        index: './src/main.tsx',
      },
      // loadEnv only reads VITE_* vars; without this they never reach
      // import.meta.env in the bundle (VITE_REACT_APP_VERSION is compared
      // against the server's X-New-Api-Version header at runtime).
      define: env.publicVars,
    },
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    html: {
      template: './index.html',
      favicon: './public/favicon.ico',
      // The app stylesheets go to the end of <body>, after the boot shell of
      // index.html: a stylesheet in <head> holds back the first paint, so a
      // slow or stalled one left the page blank. The deferred scripts still
      // run only once they have loaded, so the app never renders unstyled.
      tags: [
        (tags) =>
          tags.map((tag) =>
            tag.tag === 'link' && tag.attrs?.rel === 'stylesheet'
              ? { ...tag, head: false }
              : tag
          ),
      ],
    },
    server: {
      host: '0.0.0.0',
      strictPort: false,
      proxy: devProxy,
    },
    output: {
      // Production optimizations
      minify: isProd,
      target: 'web',
      distPath: {
        root: 'dist',
      },
      // Rely on Rsbuild default legalComments ("linked" → per-chunk *.LICENSE.txt) in all modes.
      // Do not set "none" in production: that strips minifier-preserved third-party notices and
      // extracted license files, which some distributions require for open-source compliance.
    },
    performance: {
      // Remove console in production
      removeConsole: isProd ? ['log'] : false,
      buildCache: false,
    },
    tools: {
      rspack: {
        plugins: [
          tanstackRouter({
            target: 'react',
            // Dev: avoid per-route async chunks (reduces white flash on navigation + faster HMR feedback).
            // Prod: keep route-based code splitting.
            autoCodeSplitting: isProd,
          }),
        ],
        optimization: {
          // The chunk loader and its map of every chunk's file name in a file
          // of its own: the map changes with any chunk, so inside index.js it
          // would make every deploy re-download the whole entry.
          runtimeChunk: { name: 'runtime' },
        },
      },
    },
  }
})
