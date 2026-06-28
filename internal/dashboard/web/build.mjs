import esbuild from 'esbuild'
import { readFileSync, writeFileSync, mkdirSync, copyFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
const outDir = join(root, '..', 'static')
const watch = process.argv.includes('--watch')

const template = readFileSync(join(root, 'index.html'), 'utf8')

function assemble(result) {
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

  console.log(`built ${join('static', 'index.html')} — ${(Buffer.byteLength(html) / 1024).toFixed(1)} kB`)
}

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

if (watch) {
  const ctx = await esbuild.context({
    ...opts,
    plugins: [
      {
        name: 'assemble',
        setup(b) {
          b.onEnd((r) => {
            if (!r.errors.length) assemble(r)
          })
        },
      },
    ],
  })
  await ctx.watch()
  console.log('watching src/ — run the Go binary (DASHBOARD_ADDR=…) and refresh on rebuild')
} else {
  assemble(await esbuild.build(opts))
}
