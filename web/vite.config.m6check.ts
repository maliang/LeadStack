import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  server: {
    port: 5176,
    strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:18081', changeOrigin: true, ws: true },
    },
  },
})
