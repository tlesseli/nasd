import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { readFileSync } from 'node:fs'

// The release (../VERSION), shown on the sign-in page before any API call.
const version = readFileSync(new URL('../VERSION', import.meta.url), 'utf8').trim()

// `npm run dev` gives you hot reload on http://localhost:5173 while API
// calls are forwarded to nasd running in the VM (https://localhost:8443).
export default defineConfig({
  plugins: [svelte()],
  define: { __APP_VERSION__: JSON.stringify(version) },
  server: {
    proxy: { '/api': { target: 'https://localhost:8443', secure: false } },
  },
  build: { outDir: 'dist', emptyOutDir: false },
})
