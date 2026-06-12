// API服务模块
import { useAuthStore } from '@/stores/auth'

export class ApiError extends Error {
  readonly status: number
  readonly details?: unknown

  constructor(message: string, status: number, details?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.details = details
  }
}

// 从环境变量读取配置
const getApiBase = () => {
  // 在生产环境中，API调用会直接请求当前域名
  if (import.meta.env.PROD) {
    return '/api'
  }

  // 在开发环境中，支持从环境变量配置后端地址
  const backendUrl = import.meta.env.VITE_BACKEND_URL
  const apiBasePath = import.meta.env.VITE_API_BASE_PATH || '/api'

  if (backendUrl) {
    return `${backendUrl}${apiBasePath}`
  }

  // fallback到默认配置
  return '/api'
}

const API_BASE = getApiBase()

// 打印当前API配置（仅开发环境）
if (import.meta.env.DEV) {
  console.log('🔗 API Configuration:', {
    API_BASE,
    BACKEND_URL: import.meta.env.VITE_BACKEND_URL,
    IS_DEV: import.meta.env.DEV,
    IS_PROD: import.meta.env.PROD
  })
}

// 渠道状态枚举
export type ChannelStatus = 'active' | 'suspended' | 'disabled' | 'deprecated' | 'deleted'

// 渠道指标
// 分时段统计
export interface TimeWindowStats {
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number
  inputTokens?: number
  outputTokens?: number
  cacheCreationTokens?: number
  cacheReadTokens?: number
  cacheHitRate?: number
}

export interface ChannelMetrics {
  channelIndex: number
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number       // 0-100
  errorRate: number         // 0-100
  consecutiveFailures: number
  latency: number           // ms
  lastSuccessAt?: string
  lastFailureAt?: string
  // 分时段统计 (15m, 1h, 6h, 24h)
  timeWindows?: {
    '15m': TimeWindowStats
    '1h': TimeWindowStats
    '6h': TimeWindowStats
    '24h': TimeWindowStats
  }
}

export interface Channel {
  name: string
  serviceType: 'openai' | 'gemini' | 'claude' | 'responses' | 'chat'
  baseUrl: string
  baseUrls?: string[]                // 多 BaseURL 支持（failover 模式）
  apiKeys: string[]
  description?: string
  website?: string
  insecureSkipVerify?: boolean
  modelMapping?: Record<string, string>
  defaultModel?: string
  latency?: number
  status?: ChannelStatus | 'healthy' | 'error' | 'unknown' | ''
  index: number
  pinned?: boolean
  // 多渠道调度相关字段
  priority?: number          // 渠道优先级（数字越小优先级越高）
  metrics?: ChannelMetrics   // 实时指标
  suspendReason?: string     // 熔断原因
  promotionUntil?: string    // 促销期截止时间（ISO 格式）
  promotionCount?: number    // 促销期剩余请求次数
  latencyTestTime?: number   // 延迟测试时间戳（用于 5 分钟后自动清除显示）
  lowQuality?: boolean       // 低质量渠道标记：启用后强制本地估算 token，偏差>5%时使用本地值
  visionCapable?: boolean    // 是否为图片理解默认模型
  temporary?: boolean        // 临时渠道：一天后自动移入弃用池
  temporaryUntil?: string    // 临时渠道到期时间
  deprecatedAt?: string      // 移入弃用池时间
  injectDummyThoughtSignature?: boolean  // Gemini 特定：为 functionCall 注入 dummy thought_signature（兼容第三方 API）
  stripThoughtSignature?: boolean        // Gemini 特定：移除 thought_signature 字段（兼容旧版 Gemini API）
}

export interface ChannelsResponse {
  channels: Channel[]
  current: number
  loadBalance: string
}

// 渠道仪表盘响应（合并 channels + metrics + stats）
export interface ChannelDashboardResponse {
  channels: Channel[]
  loadBalance: string
  metrics: ChannelMetrics[]
  stats: {
    multiChannelMode: boolean
    activeChannelCount: number
    traceAffinityCount: number
    traceAffinityTTL: string
    failureThreshold: number
    windowSize: number
    circuitRecoveryTime: string
  }
  recentActivity?: ChannelRecentActivity[]  // 最近 15 分钟分段活跃度
}

export interface PingResult {
  success: boolean
  latency: number
  status: string
  error?: string
}

// 历史数据点（用于时间序列图表）
export interface HistoryDataPoint {
  timestamp: string
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number
}

// 渠道历史指标响应
export interface MetricsHistoryResponse {
  channelIndex: number
  channelName: string
  dataPoints: HistoryDataPoint[]
}

// Key 级别历史数据点（包含 Token 数据）
export interface KeyHistoryDataPoint {
  timestamp: string
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number
  inputTokens: number
  outputTokens: number
  cacheCreationTokens: number
  cacheReadTokens: number
  model?: string
  cacheHitRate?: number
}

// 单个 Key 的历史数据
export interface KeyHistoryData {
  keyMask: string
  color: string
  dataPoints: KeyHistoryDataPoint[]
}

// 渠道 Key 级别历史指标响应
export interface ChannelKeyMetricsHistoryResponse {
  channelIndex: number
  channelName: string
  keys: KeyHistoryData[]
}

export interface ChannelLogEntry {
  requestId: string
  attemptId: string
  timestamp: string
  status: string
  statusCode?: number
  success: boolean
  durationMs: number
  apiType: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  cacheCreationTokens?: number
  cacheReadTokens?: number
  cacheCreation5mTokens?: number
  cacheCreation1hTokens?: number
  channelIndex: number
  channelName?: string
  baseUrl: string
  keyMask: string
  errorType?: string
  errorMessage?: string
  retried: boolean
  stream: boolean
}

export interface ChannelLogsResponse {
  channelIndex: number
  channelName?: string
  logs: ChannelLogEntry[]
}

export type ConversationKind = 'messages' | 'responses' | 'gemini' | 'chat'

export interface RequestLogEntry extends ChannelLogEntry {
  apiType: ConversationKind
  entry?: 'claude' | 'codex' | 'gemini' | 'chat'
  firstTokenMs?: number
  resolvedModel?: string
  transform?: string
  cacheTTL?: string
  tpm?: number
  conversationId?: string
}

export interface RequestLogsResponse {
  logs: RequestLogEntry[]
  limit: number
}

export interface ConversationRouteOverride {
  kind: ConversationKind
  channelIndex: number
  channelName?: string
  updatedAt: string
}

export interface ConversationResolvedChannel {
  kind: ConversationKind
  channelIndex: number
  channelName?: string
  updatedAt: string
}

export interface ConversationEntry {
  id: string
  apiKind: ConversationKind
  lastModel?: string
  lastResolvedModel?: string
  firstPrompt?: string
  prompts?: string[]
  stream: boolean
  isSending?: boolean
  firstSeenAt: string
  lastSeenAt: string
  lastRequestAt?: string
  lastCompletedAt?: string
  requestCount: number
  errorCount: number
  lastError?: string
  routeOverride?: ConversationRouteOverride
  lastResolved?: ConversationResolvedChannel
}

export interface ConversationsResponse {
  conversations: ConversationEntry[]
}

export interface ConversationRouteOptionChannel {
  kind: ConversationKind
  channelIndex: number
  channelName: string
  serviceType?: Channel['serviceType']
  status: string
  defaultModel?: string
  modelMapping?: Record<string, string>
}

export interface ConversationRouteOptionGroup {
  kind: ConversationKind
  label: string
  channels: ConversationRouteOptionChannel[]
}

export interface ConversationRouteOptionsResponse {
  kinds: ConversationRouteOptionGroup[]
}

export interface ConversationRouteUpdateForm {
  kind: ConversationKind
  channelIndex: number
}

// ============== 全局统计类型 ==============

// 全局历史数据点（包含 Token 数据）
export interface GlobalHistoryDataPoint {
  timestamp: string
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number
  inputTokens: number
  outputTokens: number
  cacheCreationTokens: number
  cacheReadTokens: number
}

// 全局统计汇总
export interface GlobalStatsSummary {
  totalRequests: number
  totalSuccess: number
  totalFailure: number
  totalInputTokens: number
  totalOutputTokens: number
  totalCacheCreationTokens: number
  totalCacheReadTokens: number
  avgSuccessRate: number
  duration: string
}

// 全局统计响应
export interface GlobalStatsHistoryResponse {
  dataPoints: GlobalHistoryDataPoint[]
  summary: GlobalStatsSummary
}

// ============== 渠道实时活跃度类型 ==============

// 活跃度分段数据（每 6 秒一段）
export interface ActivitySegment {
  requestCount: number
  successCount: number
  failureCount: number
  inputTokens: number
  outputTokens: number
}

// 渠道最近活跃度数据
export interface ChannelRecentActivity {
  channelIndex: number
  segments: ActivitySegment[]  // 150 段，每段 6 秒，从旧到新（共 15 分钟）
  rpm: number                  // 15分钟平均 RPM
  tpm: number                  // 15分钟平均 TPM
}

// ============== 上游模型列表类型 ==============

export interface ModelEntry {
  id: string
  object: string
  created: number
  owned_by: string
}

export interface ModelsResponse {
  object: string
  data: ModelEntry[]
}

/**
 * 构建上游的 /v1/models 端点 URL
 * 参考：backend-go/internal/handlers/messages/models.go:240-257
 */
function buildModelsURL(baseURL: string): string {
  // 处理 # 后缀（跳过版本前缀）
  const skipVersionPrefix = baseURL.endsWith('#')
  if (skipVersionPrefix) {
    baseURL = baseURL.slice(0, -1)
  }
  baseURL = baseURL.replace(/\/$/, '')

  // 检查是否已有版本后缀（如 /v1, /v2）
  const versionPattern = /\/v\d+[a-z]*$/
  const hasVersionSuffix = versionPattern.test(baseURL)

  // 构建端点
  let endpoint = '/models'
  if (!hasVersionSuffix && !skipVersionPrefix) {
    endpoint = '/v1' + endpoint
  }

  return baseURL + endpoint
}

/**
 * 直接从上游获取模型列表（前端直连）
 */
export async function fetchUpstreamModels(
  baseUrl: string,
  apiKey: string
): Promise<ModelsResponse> {
  const url = buildModelsURL(baseUrl)

  const response = await fetch(url, {
    method: 'GET',
    headers: {
      'Authorization': `Bearer ${apiKey}`
    },
    signal: AbortSignal.timeout(10000) // 10秒超时
  })

  if (!response.ok) {
    let errorMessage = `${response.status} ${response.statusText}`
    let errorDetails: unknown = null

    try {
      const errorText = await response.text()
      if (errorText) {
        const errorJson = JSON.parse(errorText)
        // 解析上游错误格式: { "error": { "code": "", "message": "...", "type": "..." } }
        if (errorJson.error && errorJson.error.message) {
          errorMessage = errorJson.error.message
          errorDetails = errorJson.error
        } else if (errorJson.message) {
          errorMessage = errorJson.message
          errorDetails = errorJson
        }
      }
    } catch {
      // 解析失败,使用默认错误消息
    }

    throw new ApiError(errorMessage, response.status, errorDetails)
  }

  return await response.json()
}

class ApiService {
  // 获取当前 API Key（从 AuthStore）
  private getApiKey(): string | null {
    const authStore = useAuthStore()
    return authStore.apiKey
  }

  private normalizePromotionValue(value: unknown): number {
    const parsed = typeof value === 'number'
      ? value
      : Number(String(value ?? '').trim())

    if (!Number.isFinite(parsed) || parsed <= 0) {
      return 0
    }

    return Math.floor(parsed)
  }

  private async parseResponseBody(response: Response): Promise<unknown> {
    const text = await response.text()
    if (!text) return null
    try {
      return JSON.parse(text)
    } catch {
      return text
    }
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private async request(url: string, options: RequestInit = {}): Promise<any> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...(options.headers as Record<string, string>)
    }

    // 从 AuthStore 获取 API 密钥并添加到请求头
    const apiKey = this.getApiKey()
    if (apiKey) {
      headers['x-api-key'] = apiKey
    }

    const response = await fetch(`${API_BASE}${url}`, {
      ...options,
      headers
    })

    if (!response.ok) {
      const errorBody = await this.parseResponseBody(response)
      const errorMessage =
        (typeof errorBody === 'object' && errorBody && 'error' in errorBody && typeof (errorBody as { error?: unknown }).error === 'string'
          ? (errorBody as { error: string }).error
          : typeof errorBody === 'object' && errorBody && 'message' in errorBody && typeof (errorBody as { message?: unknown }).message === 'string'
            ? (errorBody as { message: string }).message
            : typeof errorBody === 'string'
              ? errorBody
              : null) || `Request failed (${response.status})`

      // 如果是401错误，清除认证信息并提示用户重新登录
      if (response.status === 401) {
        const authStore = useAuthStore()
        authStore.clearAuth()
        // 记录认证失败(前端日志)
        if (import.meta.env.DEV) {
          console.warn('🔒 认证失败 - 时间:', new Date().toISOString())
        }
        throw new ApiError('认证失败，请重新输入访问密钥', response.status, errorBody)
      }

      throw new ApiError(errorMessage, response.status, errorBody)
    }

    if (response.status === 204) return null
    return this.parseResponseBody(response)
  }

  async getChannels(): Promise<ChannelsResponse> {
    return this.request('/messages/channels')
  }

  async addChannel(channel: Omit<Channel, 'index' | 'latency' | 'status'>): Promise<void> {
    await this.request('/messages/channels', {
      method: 'POST',
      body: JSON.stringify(channel)
    })
  }

  async updateChannel(id: number, channel: Partial<Channel>): Promise<void> {
    await this.request(`/messages/channels/${id}`, {
      method: 'PUT',
      body: JSON.stringify(channel)
    })
  }

  async deleteChannel(id: number): Promise<void> {
    await this.request(`/messages/channels/${id}`, {
      method: 'DELETE'
    })
  }

  async addApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/messages/channels/${channelId}/keys`, {
      method: 'POST',
      body: JSON.stringify({ apiKey })
    })
  }

  async removeApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/messages/channels/${channelId}/keys/${encodeURIComponent(apiKey)}`, {
      method: 'DELETE'
    })
  }

  async pingChannel(id: number): Promise<PingResult> {
    return this.request(`/messages/ping/${id}`)
  }

  async pingAllChannels(): Promise<Array<{ id: number; name: string; latency: number; status: string }>> {
    return this.request('/messages/ping')
  }

  async pingResponsesChannel(id: number): Promise<PingResult> {
    return this.request(`/responses/ping/${id}`)
  }

  async pingAllResponsesChannels(): Promise<Array<{ id: number; name: string; latency: number; status: string }>> {
    return this.request('/responses/ping')
  }

  async updateLoadBalance(strategy: string): Promise<void> {
    await this.request('/loadbalance', {
      method: 'PUT',
      body: JSON.stringify({ strategy })
    })
  }

  async updateResponsesLoadBalance(strategy: string): Promise<void> {
    await this.request('/responses/loadbalance', {
      method: 'PUT',
      body: JSON.stringify({ strategy })
    })
  }

  async updateChatLoadBalance(strategy: string): Promise<void> {
    await this.request('/chat/loadbalance', {
      method: 'PUT',
      body: JSON.stringify({ strategy })
    })
  }

  // ============== Responses 渠道管理 API ==============

  async getResponsesChannels(): Promise<ChannelsResponse> {
    return this.request('/responses/channels')
  }

  async addResponsesChannel(channel: Omit<Channel, 'index' | 'latency' | 'status'>): Promise<void> {
    await this.request('/responses/channels', {
      method: 'POST',
      body: JSON.stringify(channel)
    })
  }

  async updateResponsesChannel(id: number, channel: Partial<Channel>): Promise<void> {
    await this.request(`/responses/channels/${id}`, {
      method: 'PUT',
      body: JSON.stringify(channel)
    })
  }

  async deleteResponsesChannel(id: number): Promise<void> {
    await this.request(`/responses/channels/${id}`, {
      method: 'DELETE'
    })
  }

  async addResponsesApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/responses/channels/${channelId}/keys`, {
      method: 'POST',
      body: JSON.stringify({ apiKey })
    })
  }

  async removeResponsesApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/responses/channels/${channelId}/keys/${encodeURIComponent(apiKey)}`, {
      method: 'DELETE'
    })
  }

  // ============== Chat 渠道管理 API ==============

  async getChatChannels(): Promise<ChannelsResponse> {
    return this.request('/chat/channels')
  }

  async addChatChannel(channel: Omit<Channel, 'index' | 'latency' | 'status'>): Promise<void> {
    await this.request('/chat/channels', {
      method: 'POST',
      body: JSON.stringify(channel)
    })
  }

  async updateChatChannel(id: number, channel: Partial<Channel>): Promise<void> {
    await this.request(`/chat/channels/${id}`, {
      method: 'PUT',
      body: JSON.stringify(channel)
    })
  }

  async deleteChatChannel(id: number): Promise<void> {
    await this.request(`/chat/channels/${id}`, {
      method: 'DELETE'
    })
  }

  async addChatApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/chat/channels/${channelId}/keys`, {
      method: 'POST',
      body: JSON.stringify({ apiKey })
    })
  }

  async removeChatApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/chat/channels/${channelId}/keys/${encodeURIComponent(apiKey)}`, {
      method: 'DELETE'
    })
  }

  async moveApiKeyToTop(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/messages/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/top`, {
      method: 'POST'
    })
  }

  async moveApiKeyToBottom(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/messages/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/bottom`, {
      method: 'POST'
    })
  }

  async moveResponsesApiKeyToTop(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/responses/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/top`, {
      method: 'POST'
    })
  }

  async moveResponsesApiKeyToBottom(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/responses/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/bottom`, {
      method: 'POST'
    })
  }

  async moveChatApiKeyToTop(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/chat/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/top`, {
      method: 'POST'
    })
  }

  async moveChatApiKeyToBottom(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/chat/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/bottom`, {
      method: 'POST'
    })
  }

  // ============== 多渠道调度 API ==============

  // 重新排序渠道优先级
  async reorderChannels(order: number[]): Promise<void> {
    await this.request('/messages/channels/reorder', {
      method: 'POST',
      body: JSON.stringify({ order })
    })
  }

  async duplicateChannel(type: 'messages' | 'responses' | 'gemini' | 'chat', channelId: number): Promise<void> {
    await this.request(`/${type}/channels/${channelId}/duplicate`, {
      method: 'POST'
    })
  }

  async tidyProblemChannels(type: 'messages' | 'responses' | 'gemini' | 'chat'): Promise<void> {
    await this.request(`/${type}/channels/tidy`, {
      method: 'POST'
    })
  }

  // 设置渠道状态
  async setChannelStatus(channelId: number, status: ChannelStatus): Promise<void> {
    await this.request(`/messages/channels/${channelId}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status })
    })
  }

  // 恢复熔断渠道（重置错误计数）
  async resumeChannel(channelId: number): Promise<void> {
    await this.request(`/messages/channels/${channelId}/resume`, {
      method: 'POST'
    })
  }

  // 获取渠道指标
  async getChannelMetrics(): Promise<ChannelMetrics[]> {
    return this.request('/messages/channels/metrics')
  }

  // 获取调度器统计信息
  async getSchedulerStats(type?: 'messages' | 'responses' | 'gemini' | 'chat'): Promise<{
    multiChannelMode: boolean
    activeChannelCount: number
    traceAffinityCount: number
    traceAffinityTTL: string
    failureThreshold: number
    windowSize: number
  }> {
    const query = type && type !== 'messages' ? `?type=${type}` : ''
    return this.request(`/messages/channels/scheduler/stats${query}`)
  }

  // 获取渠道仪表盘数据（合并 channels + metrics + stats）
  async getChannelDashboard(type: 'messages' | 'responses' | 'gemini' | 'chat' = 'messages'): Promise<ChannelDashboardResponse> {
    // Gemini 使用降级实现：组合 getChannels + getMetrics
    if (type === 'gemini') {
      return this.getGeminiChannelDashboard()
    }
    if (type === 'chat') {
      return this.getChatChannelDashboard()
    }
    const query = type === 'responses' ? '?type=responses' : ''
    return this.request(`/messages/channels/dashboard${query}`)
  }

  // ============== Responses 多渠道调度 API ==============

  // 重新排序 Responses 渠道优先级
  async reorderResponsesChannels(order: number[]): Promise<void> {
    await this.request('/responses/channels/reorder', {
      method: 'POST',
      body: JSON.stringify({ order })
    })
  }

  // 设置 Responses 渠道状态
  async setResponsesChannelStatus(channelId: number, status: ChannelStatus): Promise<void> {
    await this.request(`/responses/channels/${channelId}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status })
    })
  }

  // 恢复 Responses 熔断渠道
  async resumeResponsesChannel(channelId: number): Promise<void> {
    await this.request(`/responses/channels/${channelId}/resume`, {
      method: 'POST'
    })
  }

  // 获取 Responses 渠道指标
  async getResponsesChannelMetrics(): Promise<ChannelMetrics[]> {
    return this.request('/responses/channels/metrics')
  }

  // ============== Chat 多渠道调度 API ==============

  async reorderChatChannels(order: number[]): Promise<void> {
    await this.request('/chat/channels/reorder', {
      method: 'POST',
      body: JSON.stringify({ order })
    })
  }

  async setChatChannelStatus(channelId: number, status: ChannelStatus): Promise<void> {
    await this.request(`/chat/channels/${channelId}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status })
    })
  }

  async resumeChatChannel(channelId: number): Promise<void> {
    await this.request(`/chat/channels/${channelId}/resume`, {
      method: 'POST'
    })
  }

  async getChatChannelMetrics(): Promise<ChannelMetrics[]> {
    return this.request('/chat/channels/metrics')
  }

  async getChannelLogs(type: 'messages' | 'responses' | 'gemini' | 'chat', channelId: number): Promise<ChannelLogsResponse> {
    return this.request(`/${type}/channels/${channelId}/logs`)
  }

  async getRequestLogs(params: { type?: ConversationKind | ''; limit?: number } = {}): Promise<RequestLogsResponse> {
    const search = new URLSearchParams()
    if (params.type) search.set('type', params.type)
    if (params.limit) search.set('limit', String(params.limit))
    const query = search.toString()
    return this.request(`/request-logs${query ? `?${query}` : ''}`)
  }

  async getConversations(params?: { q?: string; kind?: ConversationKind }): Promise<ConversationsResponse> {
    const search = new URLSearchParams()
    if (params?.q) search.set('q', params.q)
    if (params?.kind) search.set('kind', params.kind)
    const suffix = search.toString() ? `?${search.toString()}` : ''
    return this.request(`/conversations${suffix}`)
  }

  async getConversation(id: string): Promise<ConversationEntry> {
    return this.request(`/conversations/${encodeURIComponent(id)}`)
  }

  async getConversationRouteOptions(): Promise<ConversationRouteOptionsResponse> {
    return this.request('/conversations/route-options')
  }

  async setConversationRoute(id: string, kind: ConversationKind, channelIndex: number): Promise<ConversationEntry> {
    return this.request(`/conversations/${encodeURIComponent(id)}/route`, {
      method: 'PUT',
      body: JSON.stringify({ kind, channelIndex })
    })
  }

  async clearConversationRoute(id: string): Promise<ConversationEntry> {
    return this.request(`/conversations/${encodeURIComponent(id)}/route`, {
      method: 'DELETE'
    })
  }

  // ============== 促销期管理 API ==============

  // 设置 Messages 渠道促销期
  async setChannelPromotion(channelId: number, durationSeconds: number, count?: number): Promise<void> {
    const duration = this.normalizePromotionValue(durationSeconds)
    const normalizedCount = this.normalizePromotionValue(count)

    await this.request(`/messages/channels/${channelId}/promotion`, {
      method: 'POST',
      body: JSON.stringify({ duration, count: normalizedCount })
    })
  }

  // 设置 Responses 渠道促销期
  async setResponsesChannelPromotion(channelId: number, durationSeconds: number, count?: number): Promise<void> {
    const duration = this.normalizePromotionValue(durationSeconds)
    const normalizedCount = this.normalizePromotionValue(count)

    await this.request(`/responses/channels/${channelId}/promotion`, {
      method: 'POST',
      body: JSON.stringify({ duration, count: normalizedCount })
    })
  }

  // 设置 Chat 渠道促销期
  async setChatChannelPromotion(channelId: number, durationSeconds: number, count?: number): Promise<void> {
    const duration = this.normalizePromotionValue(durationSeconds)
    const normalizedCount = this.normalizePromotionValue(count)

    await this.request(`/chat/channels/${channelId}/promotion`, {
      method: 'POST',
      body: JSON.stringify({ duration, count: normalizedCount })
    })
  }

  // ============== Fuzzy 模式 API ==============

  // 获取 Fuzzy 模式状态
  async getFuzzyMode(): Promise<{ fuzzyModeEnabled: boolean }> {
    return this.request('/settings/fuzzy-mode')
  }

  // 设置 Fuzzy 模式状态
  async setFuzzyMode(enabled: boolean): Promise<void> {
    await this.request('/settings/fuzzy-mode', {
      method: 'PUT',
      body: JSON.stringify({ enabled })
    })
  }

  // ============== 历史指标 API ==============

  // 获取 Messages 渠道历史指标（用于时间序列图表）
  async getChannelMetricsHistory(duration: '1h' | '6h' | '24h' = '24h'): Promise<MetricsHistoryResponse[]> {
    return this.request(`/messages/channels/metrics/history?duration=${duration}`)
  }

  // 获取 Responses 渠道历史指标
  async getResponsesChannelMetricsHistory(duration: '1h' | '6h' | '24h' = '24h'): Promise<MetricsHistoryResponse[]> {
    return this.request(`/responses/channels/metrics/history?duration=${duration}`)
  }

  // 获取 Chat 渠道历史指标
  async getChatChannelMetricsHistory(duration: '1h' | '6h' | '24h' = '24h'): Promise<MetricsHistoryResponse[]> {
    return this.request(`/chat/channels/metrics/history?duration=${duration}`)
  }

  // ============== Key 级别历史指标 API ==============

  // 获取 Messages 渠道 Key 级别历史指标（用于 Key 趋势图表）
  async getChannelKeyMetricsHistory(channelId: number, duration: '1h' | '6h' | '24h' | 'today' = '6h'): Promise<ChannelKeyMetricsHistoryResponse> {
    return this.request(`/messages/channels/${channelId}/keys/metrics/history?duration=${duration}`)
  }

  // 获取 Responses 渠道 Key 级别历史指标
  async getResponsesChannelKeyMetricsHistory(channelId: number, duration: '1h' | '6h' | '24h' | 'today' = '6h'): Promise<ChannelKeyMetricsHistoryResponse> {
    return this.request(`/responses/channels/${channelId}/keys/metrics/history?duration=${duration}`)
  }

  // 获取 Chat 渠道 Key 级别历史指标
  async getChatChannelKeyMetricsHistory(channelId: number, duration: '1h' | '6h' | '24h' | 'today' = '6h'): Promise<ChannelKeyMetricsHistoryResponse> {
    return this.request(`/chat/channels/${channelId}/keys/metrics/history?duration=${duration}`)
  }

  // ============== 全局统计 API ==============

  // 获取 Messages 全局统计历史
  async getMessagesGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/messages/global/stats/history?duration=${duration}`)
  }

  // 获取 Responses 全局统计历史
  async getResponsesGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/responses/global/stats/history?duration=${duration}`)
  }

  // 获取 Chat 全局统计历史
  async getChatGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/chat/global/stats/history?duration=${duration}`)
  }

  // ============== Gemini 渠道管理 API ==============

  async getGeminiChannels(): Promise<ChannelsResponse> {
    return this.request('/gemini/channels')
  }

  async addGeminiChannel(channel: Omit<Channel, 'index' | 'latency' | 'status'>): Promise<void> {
    await this.request('/gemini/channels', {
      method: 'POST',
      body: JSON.stringify(channel)
    })
  }

  async updateGeminiChannel(id: number, channel: Partial<Channel>): Promise<void> {
    await this.request(`/gemini/channels/${id}`, {
      method: 'PUT',
      body: JSON.stringify(channel)
    })
  }

  async deleteGeminiChannel(id: number): Promise<void> {
    await this.request(`/gemini/channels/${id}`, {
      method: 'DELETE'
    })
  }

  async addGeminiApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/gemini/channels/${channelId}/keys`, {
      method: 'POST',
      body: JSON.stringify({ apiKey })
    })
  }

  async removeGeminiApiKey(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/gemini/channels/${channelId}/keys/${encodeURIComponent(apiKey)}`, {
      method: 'DELETE'
    })
  }

  async moveGeminiApiKeyToTop(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/gemini/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/top`, {
      method: 'POST'
    })
  }

  async moveGeminiApiKeyToBottom(channelId: number, apiKey: string): Promise<void> {
    await this.request(`/gemini/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/bottom`, {
      method: 'POST'
    })
  }

  // ============== Gemini 多渠道调度 API ==============

  async reorderGeminiChannels(order: number[]): Promise<void> {
    await this.request('/gemini/channels/reorder', {
      method: 'POST',
      body: JSON.stringify({ order })
    })
  }

  async setGeminiChannelStatus(channelId: number, status: ChannelStatus): Promise<void> {
    await this.request(`/gemini/channels/${channelId}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status })
    })
  }

  // Gemini 恢复渠道（降级实现：后端未实现 resume 端点，直接设置状态为 active）
  async resumeGeminiChannel(channelId: number): Promise<void> {
    await this.setGeminiChannelStatus(channelId, 'active')
  }

  async getGeminiChannelMetrics(): Promise<ChannelMetrics[]> {
    return this.request('/gemini/channels/metrics')
  }

  async setGeminiChannelPromotion(channelId: number, durationSeconds: number, count?: number): Promise<void> {
    const duration = this.normalizePromotionValue(durationSeconds)
    const normalizedCount = this.normalizePromotionValue(count)

    await this.request(`/gemini/channels/${channelId}/promotion`, {
      method: 'POST',
      body: JSON.stringify({ duration, count: normalizedCount })
    })
  }

  async updateGeminiLoadBalance(strategy: string): Promise<void> {
    await this.request('/gemini/loadbalance', {
      method: 'PUT',
      body: JSON.stringify({ strategy })
    })
  }

  // ============== Gemini 历史指标 API ==============

  // 获取 Gemini 渠道历史指标
  async getGeminiChannelMetricsHistory(duration: '1h' | '6h' | '24h' = '24h'): Promise<MetricsHistoryResponse[]> {
    return this.request(`/gemini/channels/metrics/history?duration=${duration}`)
  }

  // 获取 Gemini 渠道 Key 级别历史指标
  async getGeminiChannelKeyMetricsHistory(channelId: number, duration: '1h' | '6h' | '24h' | 'today' = '6h'): Promise<ChannelKeyMetricsHistoryResponse> {
    return this.request(`/gemini/channels/${channelId}/keys/metrics/history?duration=${duration}`)
  }

  // 获取 Gemini 全局统计历史
  async getGeminiGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/gemini/global/stats/history?duration=${duration}`)
  }

  async pingGeminiChannel(id: number): Promise<PingResult> {
    return this.request(`/gemini/ping/${id}`)
  }

  async pingAllGeminiChannels(): Promise<Array<{ id: number; name: string; latency: number; status: string }>> {
    const resp = await this.request('/gemini/ping')
    // 后端返回 { channels: [...] }，需要提取并转换字段名
    return (resp.channels || []).map((ch: { index: number; name: string; latency: number; success: boolean }) => ({
      id: ch.index,
      name: ch.name,
      latency: ch.latency,
      status: ch.success ? 'healthy' : 'error'
    }))
  }

  // Gemini Dashboard（使用后端统一接口）
  async getGeminiChannelDashboard(): Promise<ChannelDashboardResponse> {
    return this.request('/gemini/channels/dashboard')
  }

  async pingChatChannel(id: number): Promise<PingResult> {
    return this.request(`/chat/ping/${id}`)
  }

  async pingAllChatChannels(): Promise<Array<{ id: number; name: string; latency: number; status: string }>> {
    return this.request('/chat/ping')
  }

  async getChatChannelDashboard(): Promise<ChannelDashboardResponse> {
    return this.request('/chat/channels/dashboard')
  }
}

// 健康检查响应类型
export interface HealthResponse {
  version?: {
    version: string
    buildTime: string
    gitCommit: string
  }
  timestamp: string
  uptime: number
  mode: string
}

/**
 * 获取健康检查信息（包含版本号）
 * 注意：/health 端点不需要认证，直接请求根路径
 */
export const fetchHealth = async (): Promise<HealthResponse> => {
  const baseUrl = import.meta.env.PROD ? '' : (import.meta.env.VITE_BACKEND_URL || '')
  const response = await fetch(`${baseUrl}/health`)
  if (!response.ok) {
    throw new Error(`Health check failed: ${response.status}`)
  }
  return response.json()
}

export const api = new ApiService()

/**
 * 测试渠道连通性（演练台专用）
 * @param apiType API 协议类型
 * @param channelIndex 渠道索引
 * @param message 测试消息
 * @param onChunk 流式返回回调
 * @param sessionContext 会话上下文（用于模拟客户端）
 */
export const testChannel = async (
  apiType: 'messages' | 'responses' | 'gemini' | 'chat',
  channelIndex: number,
  message: string,
  onChunk: (chunk: string) => void,
  sessionContext?: {
    sessionId?: string
    threadId?: string
    interactionId?: string
    onInteractionId?: (id: string) => void
    responseId?: string
    onResponseId?: (id: string) => void
  }
): Promise<void> => {
  const authStore = useAuthStore()
  const baseUrl = import.meta.env.PROD ? '' : (import.meta.env.VITE_BACKEND_URL || '')
  const accessKey = authStore.apiKey?.trim() || ''

  if (!accessKey) {
    throw new Error('未检测到访问密钥，请先完成登录认证')
  }
  
  // 生成 UUID
  const generateUUID = () => {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
      const r = Math.random() * 16 | 0
      const v = c === 'x' ? r : (r & 0x3 | 0x8)
      return v.toString(16)
    })
  }

  // 检测操作系统
  const detectOS = () => {
    const ua = navigator.userAgent
    if (ua.includes('Win')) return 'Windows'
    if (ua.includes('Mac')) return 'MacOS'
    if (ua.includes('Linux')) return 'Linux'
    return 'Unknown'
  }

  // 检测架构
  const detectArch = () => {
    const ua = navigator.userAgent
    if (ua.includes('ARM') || ua.includes('aarch64')) return 'arm64'
    return 'x64'
  }

  const sessionId = sessionContext?.sessionId || generateUUID()
  const threadId = sessionContext?.threadId || `thread-${generateUUID()}`
  const createConversationMetadata = () => ({
    channel_index: channelIndex,
    user_id: sessionId,
    session_id: sessionId,
    thread_id: threadId
  })

  let endpoint = ''
  let body: any = {}
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'x-api-key': accessKey
  }

  switch (apiType) {
    case 'messages': {
      // 模拟 Claude Code CLI
      endpoint = '/v1/messages'
      headers['User-Agent'] = 'claude-code/2.1.83'
      headers['X-Claude-Code-Session-Id'] = sessionId
      headers['Anthropic-Version'] = '2023-06-01'
      headers['Anthropic-Beta'] = 'interleaved-thinking-2025-05-14'
      headers['X-App'] = 'cli'
      headers['X-Stainless-Lang'] = 'js'
      headers['X-Stainless-Runtime'] = 'node'
      headers['X-Stainless-Runtime-Version'] = 'v24.3.0'
      headers['X-Stainless-Os'] = detectOS()
      headers['X-Stainless-Arch'] = detectArch()
      headers['X-Stainless-Package-Version'] = '0.75.0'
      headers['X-Stainless-Retry-Count'] = '0'
      headers['X-Stainless-Timeout'] = '600'
      
      body = {
        model: 'claude-3-5-sonnet-20241022',
        max_tokens: 1024,
        messages: [{ role: 'user', content: message }],
        metadata: createConversationMetadata(),
        stream: true
      }
      break
    }
    
    case 'responses': {
      // 模拟 Codex CLI
      endpoint = '/v1/responses'
      const requestId = generateUUID()
      const installationId = sessionId
      
      headers['X-Codex-Window-Id'] = `${threadId}:0`
      headers['X-Codex-Installation-Id'] = installationId
      headers['X-Request-Id'] = requestId
      headers['X-Codex-Turn-Metadata'] = JSON.stringify({
        session_id: installationId,
        thread_id: threadId,
        request_kind: 'turn'
      })
      
      body = {
        input: message,
        model: 'claude-3-5-sonnet-20241022',
        metadata: createConversationMetadata(),
        stream: true
      }
      if (sessionContext?.responseId) {
        body.previous_response_id = sessionContext.responseId
      }
      break
    }
    
    case 'gemini': {
      // 模拟 Gemini SDK
      endpoint = '/gemini/v1beta/models/gemini-2.0-flash-exp:streamGenerateContent'
      headers['Api-Revision'] = '2026-05-20'
      headers['User-Agent'] = 'google-genai-sdk/1.71.0 gl-python/3.14.3'
      
      body = {
        contents: [{ role: 'user', parts: [{ text: message }] }],
        metadata: createConversationMetadata()
      }
      
      // 多轮对话支持
      if (sessionContext?.interactionId) {
        body.previous_interaction_id = sessionContext.interactionId
      }
      break
    }
    
    case 'chat': {
      // Chat API 不需要特殊模拟
      endpoint = '/v1/chat/completions'
      body = {
        model: 'gpt-4',
        messages: [{ role: 'user', content: message }],
        metadata: createConversationMetadata(),
        user: sessionId,
        stream: true
      }
      break
    }
  }

  const response = await fetch(`${baseUrl}${endpoint}`, {
    method: 'POST',
    headers,
    body: JSON.stringify(body)
  })

  if (!response.ok) {
    throw new Error(`请求失败: ${response.status} ${response.statusText}`)
  }

  const reader = response.body?.getReader()
  if (!reader) {
    throw new Error('无法读取响应流')
  }

  const decoder = new TextDecoder()
  let buffer = ''

  const processSSELine = (line: string) => {
    if (!line.trim() || !line.startsWith('data:')) return

    const data = line.slice(5).trim()
    if (data === '[DONE]') return

    try {
      const parsed = JSON.parse(data)

      if (apiType === 'gemini' && parsed.id && sessionContext?.onInteractionId) {
        sessionContext.onInteractionId(parsed.id)
      }

      if (
        apiType === 'responses' &&
        parsed.type === 'response.completed' &&
        typeof parsed.response?.id === 'string' &&
        parsed.response.id &&
        sessionContext?.onResponseId
      ) {
        sessionContext.onResponseId(parsed.response.id)
      }

      let content = ''
      if (apiType === 'messages') {
        content = parsed.delta?.text || ''
      } else if (apiType === 'responses') {
        if (parsed.type === 'response.output_text.delta' && typeof parsed.delta === 'string') {
          content = parsed.delta
        } else if (typeof parsed.completion === 'string') {
          content = parsed.completion
        }
      } else if (apiType === 'gemini') {
        content = parsed.candidates?.[0]?.content?.parts?.[0]?.text || ''
      } else if (apiType === 'chat') {
        content = parsed.choices?.[0]?.delta?.content || ''
      }

      if (content) {
        onChunk(content)
      }
    } catch {
      // 忽略非 JSON SSE 行
    }
  }

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''

      for (const line of lines) {
        processSSELine(line)
      }
    }

    buffer += decoder.decode()
    if (buffer) {
      processSSELine(buffer)
    }
  } finally {
    reader.releaseLock()
  }
}
/**
 * 测试渠道连通性（快捷测试专用，可指定模型）
 * @param apiType API 协议类型
 * @param channelIndex 渠道索引
 * @param model 模型名称
 * @param message 测试消息
 * @param onChunk 流式返回回调
 */
export const testChannelWithModel = async (
  apiType: 'messages' | 'responses' | 'gemini' | 'chat',
  channelIndex: number,
  model: string,
  message: string,
  onChunk: (chunk: string) => void
): Promise<void> => {
  const authStore = useAuthStore()
  const baseUrl = import.meta.env.PROD ? '' : (import.meta.env.VITE_BACKEND_URL || '')
  const accessKey = authStore.apiKey?.trim() || ''

  if (!accessKey) {
    throw new Error('未检测到访问密钥，请先完成登录认证')
  }
  
  // 生成 UUID
  const generateUUID = () => {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
      const r = Math.random() * 16 | 0
      const v = c === 'x' ? r : (r & 0x3 | 0x8)
      return v.toString(16)
    })
  }

  // 检测操作系统
  const detectOS = () => {
    const ua = navigator.userAgent
    if (ua.includes('Win')) return 'Windows'
    if (ua.includes('Mac')) return 'MacOS'
    if (ua.includes('Linux')) return 'Linux'
    return 'Unknown'
  }

  // 检测架构
  const detectArch = () => {
    const ua = navigator.userAgent
    if (ua.includes('ARM') || ua.includes('aarch64')) return 'arm64'
    return 'x64'
  }

  const sessionId = generateUUID()
  const threadId = `thread-${generateUUID()}`
  const createConversationMetadata = () => ({
    channel_index: channelIndex,
    user_id: sessionId,
    session_id: sessionId,
    thread_id: threadId
  })

  let endpoint = ''
  let body: any = {}
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'x-api-key': accessKey
  }

  switch (apiType) {
    case 'messages': {
      // 模拟 Claude Code CLI
      endpoint = '/v1/messages'
      headers['User-Agent'] = 'claude-code/2.1.83'
      headers['X-Claude-Code-Session-Id'] = sessionId
      headers['Anthropic-Version'] = '2023-06-01'
      headers['Anthropic-Beta'] = 'interleaved-thinking-2025-05-14'
      headers['X-App'] = 'cli'
      headers['X-Stainless-Lang'] = 'js'
      headers['X-Stainless-Runtime'] = 'node'
      headers['X-Stainless-Runtime-Version'] = 'v24.3.0'
      headers['X-Stainless-Os'] = detectOS()
      headers['X-Stainless-Arch'] = detectArch()
      headers['X-Stainless-Package-Version'] = '0.75.0'
      headers['X-Stainless-Retry-Count'] = '0'
      headers['X-Stainless-Timeout'] = '600'
      
      body = {
        model: model,
        max_tokens: 1024,
        messages: [{ role: 'user', content: message }],
        metadata: createConversationMetadata(),
        stream: true
      }
      break
    }
    
    case 'responses': {
      // 模拟 Codex CLI
      endpoint = '/v1/responses'
      const requestId = generateUUID()
      const installationId = sessionId
      
      headers['X-Codex-Window-Id'] = `${threadId}:0`
      headers['X-Codex-Installation-Id'] = installationId
      headers['X-Request-Id'] = requestId
      headers['X-Codex-Turn-Metadata'] = JSON.stringify({
        session_id: installationId,
        thread_id: threadId,
        request_kind: 'turn'
      })
      
      body = {
        input: message,
        model: model,
        metadata: createConversationMetadata(),
        stream: true
      }
      break
    }
    
    case 'gemini': {
      // Gemini API 的模型在 endpoint 中指定
      endpoint = `/gemini/v1beta/models/${model}:streamGenerateContent`
      headers['Api-Revision'] = '2026-05-20'
      headers['User-Agent'] = 'google-genai-sdk/1.71.0 gl-python/3.14.3'
      
      body = {
        contents: [{ role: 'user', parts: [{ text: message }] }],
        metadata: createConversationMetadata()
      }
      break
    }
    
    case 'chat': {
      // Chat API
      endpoint = '/v1/chat/completions'
      body = {
        model: model,
        messages: [{ role: 'user', content: message }],
        metadata: createConversationMetadata(),
        user: sessionId,
        stream: true
      }
      break
    }
  }

  const response = await fetch(`${baseUrl}${endpoint}`, {
    method: 'POST',
    headers,
    body: JSON.stringify(body)
  })

  if (!response.ok) {
    let errorMessage = `请求失败: ${response.status} ${response.statusText}`
    try {
      const errorBody = await response.text()
      if (errorBody) {
        const errorJson = JSON.parse(errorBody)
        if (errorJson.error?.message) {
          errorMessage = errorJson.error.message
        } else if (errorJson.message) {
          errorMessage = errorJson.message
        }
      }
    } catch {
      // 解析失败，使用默认错误消息
    }
    throw new Error(errorMessage)
  }

  const reader = response.body?.getReader()
  if (!reader) {
    throw new Error('无法读取响应流')
  }

  const decoder = new TextDecoder()
  let buffer = ''

  const processSSELine = (line: string) => {
    if (!line.trim() || !line.startsWith('data:')) return

    const data = line.slice(5).trim()
    if (data === '[DONE]') return

    try {
      const parsed = JSON.parse(data)

      let content = ''
      if (apiType === 'messages') {
        content = parsed.delta?.text || ''
      } else if (apiType === 'responses') {
        if (parsed.type === 'response.output_text.delta' && typeof parsed.delta === 'string') {
          content = parsed.delta
        } else if (typeof parsed.completion === 'string') {
          content = parsed.completion
        }
      } else if (apiType === 'gemini') {
        content = parsed.candidates?.[0]?.content?.parts?.[0]?.text || ''
      } else if (apiType === 'chat') {
        content = parsed.choices?.[0]?.delta?.content || ''
      }

      if (content) {
        onChunk(content)
      }
    } catch {
      // 忽略非 JSON SSE 行
    }
  }

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''

      for (const line of lines) {
        processSSELine(line)
      }
    }

    buffer += decoder.decode()
    if (buffer) {
      processSSELine(buffer)
    }
  } finally {
    reader.releaseLock()
  }
}

export default api
