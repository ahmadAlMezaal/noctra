import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'
import { viteSingleFile } from 'vite-plugin-singlefile'

// The dashboard ships as ONE self-contained index.html embedded in the Go
// binary (//go:embed static). The current deployment serves the page behind a
// token while carving /fonts/ out of auth — @font-face subrequests can't carry
// the page's ?token= query param. Separate /assets/*.js|css chunks would hit
// the same wall and 401, so viteSingleFile() inlines all JS + CSS into the HTML.
// Fonts stay external (web/public/fonts → static/fonts), referenced by the
// absolute /fonts/ URL so Vite leaves them untouched and they keep loading
// unauthenticated.
export default defineConfig({
  plugins: [preact(), viteSingleFile()],
  build: {
    outDir: '../static',
    emptyOutDir: true,
    cssCodeSplit: false,
    assetsInlineLimit: 100_000_000,
    chunkSizeWarningLimit: 100_000_000,
  },
})
