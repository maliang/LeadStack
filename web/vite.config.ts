import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const API_TARGET = 'http://127.0.0.1:8080'

// 构建产物会通过 //go:embed 打进控制端二进制，与 API 同端口在子路径下提供，
// 因此必须使用相对路径资源（base: './'）。
export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: API_TARGET, changeOrigin: true, ws: true },
      '/healthz': { target: API_TARGET, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
})
