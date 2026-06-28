import esbuild from 'esbuild'
import { readFileSync, writeFileSync, mkdirSync, copyFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
export const defaultOutDir = join(root, '..', 'static')
const template = readFileSync(join(root, 'index.html'), 'utf8')

function assemble(result, outDir) {
  let js = ''
  let css = ''
  for (const f of result.outputFiles) {
    if (f.path.endsWith('.css')) css = f.text
    else if (f.path.endsWith('.js')) js = f.text
  }
  let html = template.replace(/<script type="module" src="\/src\/main\.tsx"><\/script>/, () => `<script>${js}</script>`)
  html = html.replace('</head>', () => `<style>${css}</style>\n</head>`)

  mkdirSync(outDir, { recursive: true })
  writeFileSync(join(outDir, 'index.html'), html)

  const fontsSrc = join(root, 'public', 'fonts')
  const fontsOut = join(outDir, 'fonts')
  mkdirSync(fontsOut, { recursive: true })
  for (const f of readdirSync(fontsSrc)) copyFileSync(join(fontsSrc, f), join(fontsOut, f))

  console.log(`built ${join(outDir, 'index.html')} — ${(Buffer.byteLength(html) / 1024).toFixed(1)} kB`)
}

export async function bundle({ watch = false, outDir = defaultOutDir } = {}) {
  const opts = {
    entryPoints: [join(root, 'src', 'main.tsx')],
    bundle: true,
    format: 'iife',
    jsx: 'automatic',
    jsxImportSource: 'preact',
    target: ['es2020'],
    minify: !watch,
    sourcemap: watch ? 'inline' : false,
    external: ['/fonts/*'],
    outdir: outDir,
    write: false,
    logLevel: 'info',
  }
  if (!watch) return assemble(await esbuild.build(opts), outDir)
  const ctx = await esbuild.context({
    ...opts,
    plugins: [{ name: 'assemble', setup: (b) => b.onEnd((r) => r.errors.length || assemble(r, outDir)) }],
  })
  await ctx.watch()
  return ctx
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const watch = process.argv.includes('--watch')
  await bundle({ watch })
  if (watch) console.log('watching src/ — run the Go binary (DASHBOARD_ADDR=…) and refresh on rebuild')
}
