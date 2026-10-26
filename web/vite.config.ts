import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({plugins:[react()],server:{port:42784,strictPort:true,proxy:{'/api':'http://127.0.0.1:42783'}},build:{outDir:'../internal/webassets/dist',emptyOutDir:true}});
