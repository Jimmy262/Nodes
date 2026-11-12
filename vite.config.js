import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
export default defineConfig({
  root: 'web',
  plugins: [svelte()],
  server: { proxy: { '/api': { target: 'http://127.0.0.1:8080', ws: true } } },
  build: { outDir: 'dist' },
});
