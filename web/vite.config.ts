import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  base: '/_ui/',
  server: {
    proxy: {
      '/': 'http://localhost:9000',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
