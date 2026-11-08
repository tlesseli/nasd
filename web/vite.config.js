import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// `npm run dev` gives you hot reload on http://localhost:5173 while API
// calls are forwarded to nasd running in the VM (https://localhost:8443).
export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: { '/api': { target: 'https://localhost:8443', secure: false } },
  },
  build: { outDir: 'dist', emptyOutDir: false },
})
