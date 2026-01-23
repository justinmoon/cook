import { build } from 'esbuild'
import { mkdir } from 'node:fs/promises'
import path from 'node:path'

const outdir = path.join(process.cwd(), 'internal/server/static/editor')

await mkdir(path.join(outdir, 'workers'), { recursive: true })

await build({
  entryPoints: {
    editor: 'web/editor/main.ts',
  },
  bundle: true,
  outdir,
  entryNames: '[name]',
  chunkNames: 'chunks/[name]-[hash]',
  assetNames: 'assets/[name]-[hash]',
  format: 'esm',
  splitting: true,
  platform: 'browser',
  target: ['es2020'],
  sourcemap: true,
  logLevel: 'info',
  loader: {
    '.ttf': 'file',
    '.png': 'file',
    '.svg': 'file',
  },
  define: {
    'process.env.NODE_ENV': '"production"',
  },
})

await build({
  entryPoints: {
    'workers/editor.worker': 'web/editor/workers/editor.worker.ts',
    'workers/json.worker': 'web/editor/workers/json.worker.ts',
    'workers/css.worker': 'web/editor/workers/css.worker.ts',
    'workers/html.worker': 'web/editor/workers/html.worker.ts',
    'workers/ts.worker': 'web/editor/workers/ts.worker.ts',
  },
  bundle: true,
  outdir,
  entryNames: '[dir]/[name]',
  format: 'iife',
  platform: 'browser',
  target: ['es2020'],
  sourcemap: true,
  logLevel: 'info',
  define: {
    'process.env.NODE_ENV': '"production"',
  },
})
