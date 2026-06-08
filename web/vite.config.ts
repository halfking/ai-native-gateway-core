import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5780,
    proxy: {
      '/api': { target: 'http://localhost:8781', changeOrigin: true },
      '/v1':  { target: 'http://localhost:8781', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8781', changeOrigin: true },
    },
  },
})
