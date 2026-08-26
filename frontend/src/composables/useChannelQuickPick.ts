import { ref, watch } from 'vue'
import { fetchHealth } from '@/services/api'
import type { ApiTab } from '@/services/api'

/**
 * "连接本代理"共享逻辑。
 *
 * 客户端配置页的核心交互重构：不再按渠道选择，而是直接选协议（messages/responses/gemini/chat）。
 * 选定协议后：
 * 1. Base URL 默认填本代理地址 http://localhost:{port}（端口从 /health 获取），可手动改
 * 2. 模型列表从本代理 /api/proxy-models 按协议分组过滤导入
 *
 * 端口获取失败时回退 3000（后端默认端口），不静默吞错——通过 lastPortError 暴露给调用方可选展示。
 */

export interface QuickPickTypeOption {
  title: string
  value: ApiTab
}

/** 默认四协议选项 */
export const defaultQuickPickTypeOptions: QuickPickTypeOption[] = [
  { title: 'Messages', value: 'messages' },
  { title: 'Responses', value: 'responses' },
  { title: 'Gemini', value: 'gemini' },
  { title: 'Chat', value: 'chat' }
]

/** 端口缓存（模块级，多个配置页共享一次 /health 请求） */
let cachedPort: number | null = null
let portFetchPromise: Promise<number | null> | null = null

/**
 * 获取本代理监听端口（带模块级缓存）。
 * /health 是公开端点无需鉴权；失败返回 null（调用方回退默认端口）。
 */
export function fetchProxyPort(): Promise<number | null> {
  if (cachedPort !== null) return Promise.resolve(cachedPort)
  if (portFetchPromise) return portFetchPromise
  portFetchPromise = fetchHealth()
    .then(health => {
      if (typeof health.port === 'number' && health.port > 0) {
        cachedPort = health.port
        return health.port
      }
      return null
    })
    .catch(() => null)
  return portFetchPromise
}

/**
 * 获取本代理默认 Base URL（http://localhost:{port}）。
 * 端口获取失败时回退 3000（后端 env.go 默认端口）。
 */
export async function fetchProxyBaseUrl(): Promise<string> {
  const port = (await fetchProxyPort()) ?? 3000
  return `http://localhost:${port}`
}

/**
 * 协议选择 composable。
 *
 * @param onTypeChange 选定协议后的回调（由调用方实现协议映射与 baseUrl 填充）
 */
export function useProxyProtocolPick(
  onTypeChange?: (type: ApiTab, defaultBaseUrl: Promise<string>) => void
) {
  const selectedType = ref<ApiTab | null>(null)
  const defaultBaseUrl = ref('')

  // 初始化默认 Base URL
  fetchProxyBaseUrl().then(url => {
    defaultBaseUrl.value = url
  })

  watch(selectedType, (type) => {
    if (!type) return
    onTypeChange?.(type, fetchProxyBaseUrl())
  })

  return {
    selectedType,
    defaultBaseUrl
  }
}
