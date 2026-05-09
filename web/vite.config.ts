import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Output is consumed by Go via embed.FS — keep it inside web/dist so
// `//go:embed all:dist` picks it up.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    proxy: {
      '/__router': {
        target: 'http://localhost:80',
        ws: true,
        changeOrigin: false,
      },
    },
  },
});
