import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import vuetify from 'vite-plugin-vuetify'
import { resolve } from 'path'

export default defineConfig(({ mode }) => {
  // 加载环境变量
  const env = loadEnv(mode, process.cwd(), '')

  const frontendPort = parseInt(env.VITE_FRONTEND_PORT || '5173')
  const backendUrl = env.VITE_PROXY_TARGET || 'http://localhost:3000'

  return {
    // 使用绝对路径，适配 Go 嵌入式部署
    base: '/',

    plugins: [
      vue(),
      vuetify({
        autoImport: false, // 禁用自动导入，使用手动配置的图标
        styles: {
          configFile: 'src/styles/settings.scss'
        }
      })
    ],
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src')
      }
    },
    server: {
      port: frontendPort,
      proxy: {
        '/api': {
          target: backendUrl,
          changeOrigin: true
        },
        '/v1': {
          target: backendUrl,
          changeOrigin: true
        },
        '/health': {
          target: backendUrl,
          changeOrigin: true
        }
      }
    },
    css: {
      preprocessorOptions: {
        scss: {
          silenceDeprecations: ['import', 'global-builtin', 'if-function']
        }
      }
    },
	build: {
	  outDir: 'dist',
	  emptyOutDir: true,
	  // ApexCharts 与 Vuetify 已拆为独立 vendor chunk；两者压缩后约 520-590 kB。
	  chunkSizeWarningLimit: 600,
      // 确保资源路径正确
      assetsDir: 'assets',
      // 优化代码分割
      rollupOptions: {
		output: {
		  manualChunks(id) {
			if (id.includes('/node_modules/apexcharts/') || id.includes('/node_modules/vue3-apexcharts/')) {
			  return 'charts-vendor'
			}
			if (id.includes('/node_modules/vuetify/')) {
			  return 'vuetify-vendor'
			}
			if (id.includes('/node_modules/@mdi/js/')) {
			  return 'mdi-icons'
			}
			if (id.includes('/node_modules/vue/') || id.includes('/node_modules/vue-router/') || id.includes('/node_modules/pinia/')) {
			  return 'vue-vendor'
			}
			return undefined
		  }
        }
      }
    }
  }
})
