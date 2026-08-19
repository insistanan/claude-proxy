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
  successRate: number // 0-100
  errorRate: number // 0-100
  consecutiveFailures: number
  latency: number // ms
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
  id?: string
  poolId?: string
  name: string
  serviceType: 'openai' | 'gemini' | 'claude' | 'responses' | 'chat'
  baseUrl: string
  baseUrls?: string[] // 多 BaseURL 支持（failover 模式）
  apiKeys: string[]
  description?: string
  website?: string
  insecureSkipVerify?: boolean
  proxyMode?: 'inherit' | 'direct' | 'custom'
  proxyUrl?: string
  modelMapping?: Record<string, string[]> // 模型重定向：源模型 -> 目标模型列表（支持多个备选）
  defaultModel?: string
  latency?: number
  status?: ChannelStatus | 'healthy' | 'error' | 'unknown' | ''
  index: number
  pinned?: boolean
  // 多渠道调度相关字段
  priority?: number // 渠道优先级（数字越小优先级越高）
  metrics?: ChannelMetrics // 实时指标
  suspendReason?: string // 熔断原因
  promotionUntil?: string // 促销期截止时间（ISO 格式）
  promotionCount?: number // 促销期剩余请求次数
  latencyTestTime?: number // 延迟测试时间戳（用于 5 分钟后自动清除显示）
  lowQuality?: boolean // 低质量渠道标记：启用后强制本地估算 token，偏差>5%时使用本地值
  visionCapable?: boolean // 渠道是否原生支持图片理解
  excludeFromConversation?: boolean // 不参与常规对话调度，仅作为图片理解渠道使用
  disablePromptCacheKey?: boolean // 不向上游发送 prompt_cache_key
  visionLayerEnabled?: boolean // 是否为当前渠道启用图片理解层
  visionLayerChannelId?: string // 图片理解层指定调用的稳定渠道标识
  visionLayerModel?: string // 可选：覆盖透传给图片理解渠道的模型名
  temporary?: boolean // 临时渠道：一天后自动移入弃用池
  temporaryUntil?: string // 临时渠道到期时间
  deprecatedAt?: string // 移入弃用池时间
  injectDummyThoughtSignature?: boolean // Gemini 特定：为 functionCall 注入 dummy thought_signature（兼容第三方 API）
  stripThoughtSignature?: boolean // Gemini 特定：移除 thought_signature 字段（兼容旧版 Gemini API）
}

export interface SkillLocation {
  key: string
  agent: string
  path: string
  sourceType: string
  readOnly: boolean
  exists: boolean
}

export interface ManagedSkill {
  locationKey: string
  agent: string
  name: string
  translatedName?: string
  note?: string
  description: string
  path: string
  size: number
  modifiedAt: string
  valid: boolean
  issue?: string
  sourceType: string
  readOnly: boolean
}

export interface SkillsResponse {
  locations: SkillLocation[]
  skills: ManagedSkill[]
}

export interface SkillBackup {
  found: boolean
  translated?: string
  path?: string
  createdAt?: string
  model?: string
  channelName?: string
}

export interface SkillSearchResult {
  id: string
  skillId: string
  name: string
  source: string
  installs: number
  repositoryUrl?: string
  skillsUrl?: string
}

export interface SkillSearchResponse {
  query: string
  searchType: string
  skills: SkillSearchResult[]
}

export interface RemoteSkillFile {
  path: string
  size: number
}

export interface RemoteSkillPreview {
  id: string
  name: string
  description: string
  source: string
  repositoryUrl: string
  skillUrl: string
  license?: string
  files: RemoteSkillFile[]
  containsScripts: boolean
}

export interface AppSettings {
  network: {
    upstreamProxyUrl: string
    upstreamProxyEnabled: boolean
  }
  contentSafety: ContentSafetySettings
}

export interface ContentSafetySettings {
  sensitiveWord: {
    enabled: boolean
    pornographyEnabled: boolean
    gamblingEnabled: boolean
    drugsEnabled: boolean
    violenceTerrorEnabled: boolean
    politicalEnabled: boolean
    illegalCrimeEnabled: boolean
    customWords: string[]
  }
  sensitiveInfo: {
    enabled: boolean
    mode: ContentSafetyMode
    enabledRules: SensitiveInfoRule[]
  }
  credential: {
    enabled: boolean
    userInputMode: ContentSafetyMode
    toolResultMode: Exclude<ContentSafetyMode, 'mask'>
    toolArgumentMode: Exclude<ContentSafetyMode, 'mask'>
    enabledRules: CredentialRule[]
  }
  dangerousCmd: {
    enabled: boolean
    enabledRules: DangerousCommandRule[]
  }
}

export type ContentSafetyMode = 'audit' | 'block' | 'mask'
export type SensitiveInfoRule = 'phone' | 'id_card' | 'email' | 'ip_address'
export type CredentialRule = 'api_key' | 'named_secret' | 'private_key' | 'connection_string' | 'high_entropy'
export type DangerousCommandRule =
  | 'destructive'
  | 'download_execute'
  | 'reverse_shell'
  | 'privilege_escalation'
  | 'environment_tampering'

export type OpenCodeProtocol = 'chat' | 'responses' | 'messages' | 'gemini' | 'custom'

export interface OpenCodeVariant {
  reasoningEffort: string
}

export interface OpenCodeModel {
  key: string
  apiModelId: string
  name: string
  contextLimit: number
  inputLimit: number
  outputLimit: number
  options: Record<string, unknown>
  variants?: Record<string, OpenCodeVariant>
}

export interface OpenCodeProvider {
  id: string
  name: string
  protocol: OpenCodeProtocol
  npm: string
  baseUrl: string
  apiKeyMasked: string
  apiKeyPresent: boolean
  headers: Record<string, string>
  options: Record<string, unknown>
  models: OpenCodeModel[]
}

export interface OpenCodeConfig {
  path: string
  exists: boolean
  jsonc: boolean
  model: string
  smallModel: string
  providers: OpenCodeProvider[]
}

export interface SaveOpenCodeProvider extends Omit<OpenCodeProvider, 'apiKeyMasked' | 'apiKeyPresent'> {
  apiKeyAction: 'keep' | 'replace' | 'remove'
  apiKey?: string
}

export interface ClaudeCodeModelDefault {
  family: string
  model: string
  name: string
}

export interface ClaudeCodeSettings {
  path: string
  exists: boolean
  jsonc: boolean
  baseUrl: string
  credentialKind: 'authToken' | 'apiKey'
  credentialMasked: string
  credentialPresent: boolean
  model: string
  reasoningModel: string
  modelDefaults: ClaudeCodeModelDefault[]
}

// ============== pi-agent 配置管理类型 ==============

export type PiAgentFileKind = 'models.json' | 'auth.json' | 'settings.json'

export interface PiAgentFileStatus {
  path: string
  exists: boolean
  writable: boolean
  revision: string
}

export interface PiAgentStatus {
  configDir: string
  exists: boolean
  writable: boolean
  files: Record<PiAgentFileKind, PiAgentFileStatus>
}

export interface PiAgentModelCost {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
}

export interface PiAgentModel {
  id: string
  name?: string
  api?: string
  baseUrl?: string
  reasoning?: boolean | null
  thinkingLevelMap?: Record<string, string | null>
  input?: string[]
  contextWindow?: number | null
  maxTokens?: number | null
  cost?: PiAgentModelCost
  headers?: Record<string, string>
  compat?: Record<string, unknown>
}

export interface PiAgentProvider {
  id: string
  name?: string
  api?: string
  baseUrl?: string
  authHeader?: boolean | null
  headers?: Record<string, string>
  compat?: Record<string, unknown>
  models?: PiAgentModel[]
  modelOverrides?: Record<string, unknown>
  hasOAuth?: boolean
  apiKeyMasked?: string
  apiKeyPresent?: boolean
}

export interface PiAgentProvidersResponse {
  revision: string
  providers: PiAgentProvider[]
  rawExists: boolean
}

export interface PiAgentCredential {
  id: string
  type: 'api_key' | 'oauth' | 'unknown'
  keyMasked: string
  keyPresent: boolean
  hasOAuth: boolean
  envMasked: Record<string, string>
  hasEnvValues: boolean
}

export interface PiAgentCredentialsResponse {
  revision: string
  credentials: PiAgentCredential[]
}

export interface PiAgentModelSettings {
  defaultProvider: string
  defaultModel: string
  defaultThinkingLevel: string
  enabledModels: string[]
}

export interface PiAgentModelSettingsResponse {
  revision: string
  settings: PiAgentModelSettings
}

export interface PiAgentBackup {
  id: string
  file: string
  size: number
  created: string
  revision: string
  sensitive: boolean
}

export interface PiAgentBackupsResponse {
  backups: PiAgentBackup[]
}

export interface PiAgentProbeResult {
  success: boolean
  latencyMs?: number
  statusCode?: number
  error?: string
}

export interface PiAgentDiscoverResult {
  success: boolean
  models?: string[]
  method?: string
  error?: string
}

export interface SaveClaudeCodeSettings extends Pick<
  ClaudeCodeSettings,
  'baseUrl' | 'credentialKind' | 'model' | 'reasoningModel' | 'modelDefaults'
> {
  credentialAction: 'keep' | 'replace' | 'remove'
  credential?: string
}

// ============== DSH 配置类型 ==============

export interface DSHModel {
  id: string
  name?: string
  input?: string[]
  reasoningEfforts?: Record<string, string | null> | false
  contextWindow?: number | null
  maxTokens?: number | null
}

export interface DSHProvider {
  apiKeyEnv?: string
  api?: string
  baseURL?: string
  displayName?: string
  models?: DSHModel[]
}

export interface DSHDefaultModel {
  provider: string
  model: string
}

export interface DSHSettings {
  dshHome: string
  path: string
  exists: boolean
  writable: boolean
  providers: Record<string, DSHProvider>
  providerKeys: string[]
  defaultModel: DSHDefaultModel | null
  theme: string
}

export interface SaveDSHSettings {
  providers: Record<string, DSHProvider>
  providerKeys?: string[]
  defaultModel: DSHDefaultModel | null
}

export interface ChannelPool {
  id: string
  name: string
  modelMatcher: string
  priority: number
}

export interface CreatedChannelResponse {
  channel: {
    id: string
    index: number
  }
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
  recentActivity?: ChannelRecentActivity[] // 最近 15 分钟分段活跃度
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

export type ConversationKind = 'messages' | 'responses' | 'gemini' | 'chat' | 'images'

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

export type ContentSafetyAPIType = 'messages' | 'responses' | 'chat' | 'gemini'
export type BlockedLogType = 'sensitive_word' | 'sensitive_info' | 'credential' | 'dangerous_cmd'

export interface BlockedLogEntry {
  id: number
  timestamp: string
  apiType: ContentSafetyAPIType
  blockType: BlockedLogType
  ruleName?: string
  promptSnippet?: string
  channelName?: string
  model?: string
  requestId?: string
  createdAt: string
}

export interface BlockedLogsResponse {
  logs: BlockedLogEntry[]
  total: number
  page: number
  pageSize: number
}

export interface BlockedLogFilters {
  apiType?: ContentSafetyAPIType | ''
  blockType?: BlockedLogType | ''
  from?: string
  to?: string
  page?: number
  pageSize?: number
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
  name?: string
  apiKind: ConversationKind
  lastModel?: string
  lastResolvedModel?: string
  firstPrompt?: string
  prompts?: string[]
  stream: boolean
  isSending?: boolean
  activeRequests?: number
  firstSeenAt: string
  lastSeenAt: string
  lastRequestAt?: string
  lastCompletedAt?: string
  requestCount: number
  errorCount: number
  lastError?: string
  routeOverride?: ConversationRouteOverride
  lastResolved?: ConversationResolvedChannel
  imageFingerprints?: string[]
  clientFamily?: string
  identitySource?: string
  parentConversationId?: string
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
  modelMapping?: Record<string, string[]>
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
  segments: ActivitySegment[] // 150 段，每段 6 秒，从旧到新（共 15 分钟）
  rpm: number // 15分钟平均 RPM
  tpm: number // 15分钟平均 TPM
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

export interface UpstreamModelsRequest {
  baseUrl: string
  baseUrls?: string[]
  apiKey?: string
  serviceType?: string
  insecureSkipVerify?: boolean
  proxyMode?: Channel['proxyMode']
  proxyUrl?: string
}

class ApiService {
  /**
   * 返回指定渠道类型的统一 API 方法集（前端渠道管理收敛核心）。
   * 收敛前 getChannels/getResponsesChannels/... 五组命名方法各实现一遍，仅 URL 前缀不同；
   * 现在按 ApiTab 查表，一份实现覆盖五种渠道。
   */
  channelApi(type: ApiTab): ChannelApi {
    const p = `/${type}`
    return {
      getChannels: () => this.request(`${p}/channels`),
      addChannel: channel => this.request(`${p}/channels`, { method: 'POST', body: JSON.stringify(channel) }),
      updateChannel: (id, channel) =>
        this.request(`${p}/channels/${id}`, { method: 'PUT', body: JSON.stringify(channel) }),
      deleteChannel: id => this.request(`${p}/channels/${id}`, { method: 'DELETE' }),
      addKey: (channelId, apiKey) =>
        this.request(`${p}/channels/${channelId}/keys`, { method: 'POST', body: JSON.stringify({ apiKey }) }),
      removeKey: (channelId, apiKey) =>
        this.request(`${p}/channels/${channelId}/keys/${encodeURIComponent(apiKey)}`, { method: 'DELETE' }),
      moveKeyToTop: (channelId, apiKey) =>
        this.request(`${p}/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/top`, { method: 'POST' }),
      moveKeyToBottom: (channelId, apiKey) =>
        this.request(`${p}/channels/${channelId}/keys/${encodeURIComponent(apiKey)}/bottom`, { method: 'POST' }),
      reorder: order => this.request(`${p}/channels/reorder`, { method: 'POST', body: JSON.stringify({ order }) }),
      setStatus: (channelId, status) =>
        this.request(`${p}/channels/${channelId}/status`, { method: 'PATCH', body: JSON.stringify({ status }) }),
      setPromotion: (channelId, durationSeconds, count) => {
        const duration = this.normalizePromotionValue(durationSeconds)
        const normalizedCount = this.normalizePromotionValue(count)
        return this.request(`${p}/channels/${channelId}/promotion`, {
          method: 'POST',
          body: JSON.stringify({ duration, count: normalizedCount })
        })
      },
      pingChannel: id => this.request(`${p}/ping/${id}`),
      pingAllChannels: () => this.request(`${p}/ping`),
      updateLoadBalance: strategy =>
        this.request(`${p}/loadbalance`, { method: 'PUT', body: JSON.stringify({ strategy }) }),
      resumeChannel: channelId => this.request(`${p}/channels/${channelId}/resume`, { method: 'POST' }),
      getChannelMetrics: () => this.request(`${p}/channels/metrics`),
      getChannelMetricsHistory: (duration = '24h') =>
        this.request(`${p}/channels/metrics/history?duration=${duration}`),
      getChannelKeyMetricsHistory: (channelId, duration = '6h') =>
        this.request(`${p}/channels/${channelId}/keys/metrics/history?duration=${duration}`),
      getGlobalStats: (duration = '24h') => this.request(`${p}/global/stats/history?duration=${duration}`),
      getChannelDashboard: () => this.request(`${p}/channels/dashboard`)
    }
  }

  // 获取当前 API Key（从 AuthStore）
  private getApiKey(): string | null {
    const authStore = useAuthStore()
    return authStore.apiKey
  }

  private normalizePromotionValue(value: unknown): number {
    const parsed = typeof value === 'number' ? value : Number(String(value ?? '').trim())

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
        (typeof errorBody === 'object' &&
        errorBody &&
        'error' in errorBody &&
        typeof (errorBody as { error?: unknown }).error === 'string'
          ? (errorBody as { error: string }).error
          : typeof errorBody === 'object' &&
              errorBody &&
              'error' in errorBody &&
              typeof (errorBody as { error?: { message?: unknown } }).error?.message === 'string'
            ? (errorBody as { error: { message: string } }).error.message
            : typeof errorBody === 'object' &&
                errorBody &&
                'message' in errorBody &&
                typeof (errorBody as { message?: unknown }).message === 'string'
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

  async getChannelPools(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
  ): Promise<{ pools: ChannelPool[] }> {
    return this.request(`/${type}/pools`)
  }

  async createChannelPool(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images',
    pool: Pick<ChannelPool, 'name' | 'modelMatcher'>
  ): Promise<{ pool: ChannelPool }> {
    return this.request(`/${type}/pools`, { method: 'POST', body: JSON.stringify(pool) })
  }

  async updateChannelPool(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images',
    id: string,
    pool: Partial<Pick<ChannelPool, 'name' | 'modelMatcher'>>
  ): Promise<void> {
    await this.request(`/${type}/pools/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(pool) })
  }

  async deleteChannelPool(type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images', id: string): Promise<void> {
    await this.request(`/${type}/pools/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }

  async saveChannelPoolLayout(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images',
    pools: Array<{ poolId: string; channelIds: string[] }>
  ): Promise<void> {
    await this.request(`/${type}/pools/layout`, { method: 'PUT', body: JSON.stringify({ pools }) })
  }

  // ============== Responses 渠道管理 API ==============

  // ============== Chat 渠道管理 API ==============

  // ============== 多渠道调度 API ==============

  // 重新排序渠道优先级

  async duplicateChannel(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images',
    channelId: number
  ): Promise<void> {
    await this.request(`/${type}/channels/${channelId}/duplicate`, {
      method: 'POST'
    })
  }

  async tidyProblemChannels(type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'): Promise<void> {
    await this.request(`/${type}/channels/tidy`, {
      method: 'POST'
    })
  }

  // 设置渠道状态

  // 恢复熔断渠道（重置错误计数）

  // 获取渠道指标
  async getChannelMetrics(): Promise<ChannelMetrics[]> {
    return this.request('/messages/channels/metrics')
  }

  // 获取调度器统计信息
  async getSchedulerStats(type?: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'): Promise<{
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
  async getChannelDashboard(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images' = 'messages'
  ): Promise<ChannelDashboardResponse> {
    return this.request(`/${type}/channels/dashboard`)
  }

  // ============== Responses 多渠道调度 API ==============

  // 重新排序 Responses 渠道优先级

  // 设置 Responses 渠道状态

  // 恢复 Responses 熔断渠道

  // 获取 Responses 渠道指标

  // ============== Chat 多渠道调度 API ==============

  async getChannelLogs(
    type: 'messages' | 'responses' | 'gemini' | 'chat' | 'images',
    channelId: number
  ): Promise<ChannelLogsResponse> {
    return this.request(`/${type}/channels/${channelId}/logs`)
  }

  async getRequestLogs(params: { type?: ConversationKind | ''; limit?: number } = {}): Promise<RequestLogsResponse> {
    const search = new URLSearchParams()
    if (params.type) search.set('type', params.type)
    if (params.limit) search.set('limit', String(params.limit))
    const query = search.toString()
    return this.request(`/request-logs${query ? `?${query}` : ''}`)
  }

  async getBlockedLogs(params: BlockedLogFilters = {}): Promise<BlockedLogsResponse> {
    const search = new URLSearchParams()
    if (params.apiType) search.set('apiType', params.apiType)
    if (params.blockType) search.set('blockType', params.blockType)
    if (params.from) search.set('from', params.from)
    if (params.to) search.set('to', params.to)
    if (params.page) search.set('page', String(params.page))
    if (params.pageSize) search.set('pageSize', String(params.pageSize))
    const query = search.toString()
    return this.request(`/blocked-logs${query ? `?${query}` : ''}`)
  }

  async getBlockedLog(id: number): Promise<BlockedLogEntry> {
    return this.request(`/blocked-logs/${id}`)
  }

  async deleteBlockedLog(id: number): Promise<void> {
    await this.request(`/blocked-logs/${id}`, { method: 'DELETE' })
  }

  async clearBlockedLogs(): Promise<{ deleted: number }> {
    return this.request('/blocked-logs', { method: 'DELETE' })
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

  async setConversationName(id: string, name: string): Promise<ConversationEntry> {
    return this.request(`/conversations/${encodeURIComponent(id)}/name`, {
      method: 'PUT',
      body: JSON.stringify({ name })
    })
  }

  async deleteConversation(id: string): Promise<void> {
    await this.request(`/conversations/${encodeURIComponent(id)}`, {
      method: 'DELETE'
    })
  }

  async deleteAllConversations(): Promise<void> {
    await this.request('/conversations', {
      method: 'DELETE'
    })
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

  // 设置 Responses 渠道促销期

  // 设置 Chat 渠道促销期

  // ============== Fuzzy 模式 API ==============

  async getSettings(): Promise<AppSettings> {
    return this.request('/settings')
  }

  async updateSettings(settings: Partial<AppSettings>): Promise<AppSettings> {
    return this.request('/settings', {
      method: 'PUT',
      body: JSON.stringify(settings)
    })
  }

  async discoverUpstreamModels(request: UpstreamModelsRequest): Promise<ModelsResponse> {
    return this.request('/upstream/models', {
      method: 'POST',
      body: JSON.stringify(request)
    })
  }

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

  // 获取 Messages / Responses 客户端伪装状态
  async getClientDisguise(): Promise<{
    claudeCodeDisguiseEnabled: boolean
    codexDisguiseEnabled: boolean
  }> {
    return this.request('/settings/client-disguise')
  }

  // 设置指定协议的客户端伪装状态
  async setClientDisguise(protocol: 'messages' | 'responses', enabled: boolean): Promise<void> {
    await this.request('/settings/client-disguise', {
      method: 'PUT',
      body: JSON.stringify({ protocol, enabled })
    })
  }

  // ============== OpenCode 配置 API ==============

  async getOpenCodeConfig(): Promise<OpenCodeConfig> {
    return this.request('/settings/opencode')
  }

  async saveOpenCodeConfig(config: { providers: SaveOpenCodeProvider[] }): Promise<{ success: boolean; path: string }> {
    return this.request('/settings/opencode', {
      method: 'PUT',
      body: JSON.stringify(config)
    })
  }

  // ============== Claude Code 配置 API ==============

  async getClaudeCodeSettings(): Promise<ClaudeCodeSettings> {
    return this.request('/settings/claude-code')
  }

  async saveClaudeCodeSettings(settings: SaveClaudeCodeSettings): Promise<{ success: boolean; path: string }> {
    return this.request('/settings/claude-code', {
      method: 'PUT',
      body: JSON.stringify(settings)
    })
  }

  // ============== DSH 配置 API ==============

  async getDSHSettings(): Promise<DSHSettings> {
    return this.request('/settings/dsh')
  }

  async saveDSHSettings(settings: SaveDSHSettings): Promise<{ success: boolean; path: string }> {
    return this.request('/settings/dsh', {
      method: 'PUT',
      body: JSON.stringify(settings)
    })
  }

  // ============== pi-agent 配置管理 API ==============

  async getPiAgentStatus(): Promise<PiAgentStatus> {
    return this.request('/settings/pi-agent')
  }

  async getPiAgentProviders(): Promise<PiAgentProvidersResponse> {
    return this.request('/settings/pi-agent/providers')
  }

  async createPiAgentProvider(payload: Record<string, unknown>): Promise<{ success: boolean; revision: string }> {
    return this.request('/settings/pi-agent/providers', {
      method: 'POST',
      body: JSON.stringify(payload)
    })
  }

  async updatePiAgentProvider(
    id: string,
    payload: Record<string, unknown>
  ): Promise<{ success: boolean; revision: string }> {
    return this.request(`/settings/pi-agent/providers/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload)
    })
  }

  async deletePiAgentProvider(id: string, revision: string): Promise<{ success: boolean; revision: string }> {
    return this.request(`/settings/pi-agent/providers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      body: JSON.stringify({ revision })
    })
  }

  async validatePiAgentProvider(payload: {
    id: string
    provider: Partial<PiAgentProvider>
  }): Promise<{ valid: boolean; errors: string[] }> {
    return this.request('/settings/pi-agent/validate', {
      method: 'POST',
      body: JSON.stringify(payload)
    })
  }

  async testPiAgentProvider(
    id: string,
    payload: { baseUrl?: string; apiKey?: string; api?: string }
  ): Promise<PiAgentProbeResult> {
    return this.request(`/settings/pi-agent/providers/${encodeURIComponent(id)}/test`, {
      method: 'POST',
      body: JSON.stringify(payload)
    })
  }

  async discoverPiAgentModels(
    id: string,
    payload: { baseUrl?: string; apiKey?: string; api?: string }
  ): Promise<PiAgentDiscoverResult> {
    return this.request(`/settings/pi-agent/providers/${encodeURIComponent(id)}/discover-models`, {
      method: 'POST',
      body: JSON.stringify(payload)
    })
  }

  async getPiAgentCredentials(): Promise<PiAgentCredentialsResponse> {
    return this.request('/settings/pi-agent/credentials')
  }

  async updatePiAgentCredential(
    id: string,
    payload: { revision: string; action: 'keep' | 'replace' | 'remove'; key?: string }
  ): Promise<{ success: boolean; revision: string }> {
    return this.request(`/settings/pi-agent/credentials/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(payload)
    })
  }

  async deletePiAgentCredential(id: string, revision: string): Promise<{ success: boolean; revision: string }> {
    return this.request(`/settings/pi-agent/credentials/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      body: JSON.stringify({ revision })
    })
  }

  async getPiAgentModelSettings(): Promise<PiAgentModelSettingsResponse> {
    return this.request('/settings/pi-agent/model-settings')
  }

  async updatePiAgentModelSettings(payload: {
    revision: string
    settings: Partial<PiAgentModelSettings>
  }): Promise<{ success: boolean; revision: string }> {
    return this.request('/settings/pi-agent/model-settings', {
      method: 'PATCH',
      body: JSON.stringify(payload)
    })
  }

  async getPiAgentBackups(): Promise<PiAgentBackupsResponse> {
    return this.request('/settings/pi-agent/backups')
  }

  async createPiAgentBackup(file: PiAgentFileKind): Promise<{ success: boolean; backup: PiAgentBackup }> {
    return this.request('/settings/pi-agent/backups', {
      method: 'POST',
      body: JSON.stringify({ file })
    })
  }

  async restorePiAgentBackup(id: string): Promise<{ success: boolean; revision: string; backup: string }> {
    return this.request(`/settings/pi-agent/backups/${encodeURIComponent(id)}/restore`, {
      method: 'POST'
    })
  }

  // ============== 本机 Skills 管理 API ==============

  async getSkills(): Promise<SkillsResponse> {
    return this.request('/skills')
  }

  async getSkillContent(locationKey: string, name: string): Promise<{ content: string; path: string }> {
    return this.request('/skills/content', { method: 'POST', body: JSON.stringify({ locationKey, name }) })
  }

  async getLatestSkillBackup(locationKey: string, name: string): Promise<SkillBackup> {
    return this.request('/skills/backup/latest', { method: 'POST', body: JSON.stringify({ locationKey, name }) })
  }

  async updateSkillNote(name: string, note: string): Promise<{ success: boolean; note: string }> {
    return this.request('/skills/note', { method: 'POST', body: JSON.stringify({ name, note }) })
  }

  async consolidateSkills(): Promise<{ success: boolean; total: number; imported: number; duplicates: number }> {
    return this.request('/skills/consolidate', { method: 'POST' })
  }

  async importSkill(
    fileName: string,
    contentBase64: string,
    targets: string[]
  ): Promise<{ success: boolean; name: string }> {
    return this.request('/skills/import', {
      method: 'POST',
      body: JSON.stringify({ fileName, contentBase64, targets })
    })
  }

  async searchSkills(query: string): Promise<SkillSearchResponse> {
    return this.request(`/skills/search?q=${encodeURIComponent(query)}`)
  }

  async inspectRemoteSkill(id: string): Promise<RemoteSkillPreview> {
    return this.request('/skills/remote/inspect', { method: 'POST', body: JSON.stringify({ id }) })
  }

  async installRemoteSkill(
    id: string,
    targets: string[]
  ): Promise<{ success: boolean; name: string; source: string; repositoryUrl: string }> {
    return this.request('/skills/remote/install', { method: 'POST', body: JSON.stringify({ id, targets }) })
  }

  async copySkill(
    locationKey: string,
    name: string,
    targets: string[]
  ): Promise<{ success: boolean; name: string; targets: string[] }> {
    return this.request('/skills/copy', { method: 'POST', body: JSON.stringify({ locationKey, name, targets }) })
  }

  async deleteSkill(locationKey: string, name: string): Promise<void> {
    await this.request('/skills', { method: 'DELETE', body: JSON.stringify({ locationKey, name }) })
  }

  async backupSkill(payload: {
    locationKey: string
    name: string
    original: string
    translated: string
    model?: string
    channelName?: string
  }): Promise<{ success: boolean; path: string }> {
    return this.request('/skills/backup', { method: 'POST', body: JSON.stringify(payload) })
  }

  // ============== 历史指标 API ==============

  // 获取 Messages 渠道历史指标（用于时间序列图表）
  async getChannelMetricsHistory(duration: '1h' | '6h' | '24h' = '24h'): Promise<MetricsHistoryResponse[]> {
    return this.request(`/messages/channels/metrics/history?duration=${duration}`)
  }

  // 获取 Responses 渠道历史指标

  // 获取 Chat 渠道历史指标

  // ============== Key 级别历史指标 API ==============

  // 获取 Messages 渠道 Key 级别历史指标（用于 Key 趋势图表）
  async getChannelKeyMetricsHistory(
    channelId: number,
    duration: '1h' | '6h' | '24h' | 'today' = '6h'
  ): Promise<ChannelKeyMetricsHistoryResponse> {
    return this.request(`/messages/channels/${channelId}/keys/metrics/history?duration=${duration}`)
  }

  // 获取 Responses 渠道 Key 级别历史指标

  // 获取 Chat 渠道 Key 级别历史指标

  // ============== 全局统计 API ==============

  // 获取 Messages 全局统计历史
  async getMessagesGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/messages/global/stats/history?duration=${duration}`)
  }

  // 获取 Responses 全局统计历史

  // 获取 Chat 全局统计历史

  // 获取 Images 全局统计历史
  async getImagesGlobalStats(duration: '1h' | '6h' | '24h' | 'today' = '24h'): Promise<GlobalStatsHistoryResponse> {
    return this.request(`/images/global/stats/history?duration=${duration}`)
  }

  // ============== Gemini 渠道管理 API ==============

  // ============== Gemini 多渠道调度 API ==============

  // Gemini 恢复渠道（降级实现：后端未实现 resume 端点，直接设置状态为 active）

  // ============== Gemini 历史指标 API ==============

  // 获取 Gemini 渠道历史指标

  // 获取 Gemini 渠道 Key 级别历史指标

  // 获取 Gemini 全局统计历史

  // Gemini Dashboard（使用后端统一接口）

  // ===== Images API =====
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
  const baseUrl = import.meta.env.PROD ? '' : import.meta.env.VITE_BACKEND_URL || ''
  const response = await fetch(`${baseUrl}/health`)
  if (!response.ok) {
    throw new Error(`Health check failed: ${response.status}`)
  }
  return response.json()
}

export const api = new ApiService()

export function fetchUpstreamModels(
  baseUrl: string,
  apiKey: string = '',
  serviceType?: string,
  options: Omit<UpstreamModelsRequest, 'baseUrl' | 'apiKey' | 'serviceType'> = {}
): Promise<ModelsResponse> {
  return api.discoverUpstreamModels({
    baseUrl,
    apiKey,
    serviceType,
    ...options
  })
}

/**
 * 渠道 API 类型（对应后端 scheduler.ChannelKind 与路由前缀）
 */
export type ApiTab = 'messages' | 'responses' | 'gemini' | 'chat' | 'images'

/**
 * 某一种渠道类型的统一 API 方法集。
 * 收敛前：getChannels / getResponsesChannels / getChatChannels / getGeminiChannels / getImagesChannels
 * 五组命名方法各自实现一遍 CRUD，仅 URL 前缀不同。工厂按 ApiTab 查表，一份实现覆盖五种渠道。
 */
export interface ChannelApi {
  getChannels(): Promise<ChannelsResponse>
  addChannel(channel: Omit<Channel, 'id' | 'index' | 'latency' | 'status'>): Promise<CreatedChannelResponse>
  updateChannel(id: number, channel: Partial<Channel>): Promise<void>
  deleteChannel(id: number): Promise<void>
  addKey(channelId: number, apiKey: string): Promise<void>
  removeKey(channelId: number, apiKey: string): Promise<void>
  moveKeyToTop(channelId: number, apiKey: string): Promise<void>
  moveKeyToBottom(channelId: number, apiKey: string): Promise<void>
  reorder(order: number[]): Promise<void>
  setStatus(channelId: number, status: ChannelStatus): Promise<void>
  setPromotion(channelId: number, durationSeconds: number, count?: number): Promise<void>
  pingChannel(id: number): Promise<PingResult>
  pingAllChannels(): Promise<Array<{ id: number; name: string; latency: number; status: string }>>
  updateLoadBalance(strategy: string): Promise<void>
  resumeChannel(channelId: number): Promise<void>
  getChannelMetrics(): Promise<ChannelMetrics[]>
  getChannelMetricsHistory(duration?: '1h' | '6h' | '24h'): Promise<MetricsHistoryResponse[]>
  getChannelKeyMetricsHistory(
    channelId: number,
    duration?: '1h' | '6h' | '24h' | 'today'
  ): Promise<ChannelKeyMetricsHistoryResponse>
  getGlobalStats(duration?: '1h' | '6h' | '24h' | 'today'): Promise<GlobalStatsHistoryResponse>
  getChannelDashboard(): Promise<ChannelDashboardResponse>
}

/**
 * 返回指定渠道类型的统一 API 方法集。
 * 这是前端渠道管理收敛的核心：store/组件按 ApiTab 取用，不再散落 5 组命名方法。
 */
export function channelApiByType(type: ApiTab): ChannelApi {
  return api.channelApi(type)
}

export interface TestChannelContext {
  purpose?: 'quick_test'
  channelIndex?: number
  thinking?: string
  sessionId?: string
  threadId?: string
  interactionId?: string
  onInteractionId?: (_id: string) => void
  responseId?: string
  onResponseId?: (_id: string) => void
  signal?: AbortSignal
  messages?: Array<{ role: 'user' | 'assistant'; content: string }>
}

export const testChannelWithModel = async (
  apiType: ApiTab,
  channelId: string,
  model: string | undefined,
  message: string,
  onChunk: (_chunk: string) => void,
  sessionContext?: TestChannelContext
): Promise<void> => {
  if (!channelId.trim()) throw new Error('渠道缺少稳定 ID，无法执行测试')
  const channelIndex = sessionContext?.channelIndex ?? Number(channelId)
  if (!Number.isInteger(channelIndex) || channelIndex < 0) {
    throw new Error('快捷测试缺少有效的渠道索引')
  }

  const authStore = useAuthStore()
  const accessKey = authStore.apiKey?.trim() || ''
  if (!accessKey) throw new Error('未检测到访问密钥，请先完成登录认证')

  const baseUrl = import.meta.env.PROD ? '' : (import.meta.env.VITE_BACKEND_URL || '')
  const generateUUID = () => crypto.randomUUID()
  const sessionId = sessionContext?.sessionId || generateUUID()
  const threadId = sessionContext?.threadId || `thread-${generateUUID()}`
  const selectedModel = model?.trim() || ''
  if (!selectedModel) throw new Error('请选择或输入用于快捷测试的模型')
  const metadata = {
    channel_index: channelIndex,
    user_id: sessionId,
    session_id: sessionId,
    thread_id: threadId
  }

  let endpoint = ''
  let body: Record<string, unknown> = {}
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'x-api-key': accessKey
  }

  switch (apiType) {
    case 'messages':
      endpoint = '/v1/messages'
      body = { model: selectedModel, max_tokens: 1024, messages: [{ role: 'user', content: message }], metadata, stream: true }
      break
    case 'responses':
      endpoint = '/v1/responses'
      body = { model: selectedModel, input: message, metadata, stream: true }
      if (sessionContext?.responseId) body.previous_response_id = sessionContext.responseId
      break
    case 'gemini':
      endpoint = `/v1beta/models/${encodeURIComponent(selectedModel.replace(/^models\//, ''))}:streamGenerateContent`
      body = { contents: [{ role: 'user', parts: [{ text: message }] }], metadata }
      if (sessionContext?.interactionId) body.previous_interaction_id = sessionContext.interactionId
      break
    case 'chat':
      endpoint = '/v1/chat/completions'
      body = { model: selectedModel, messages: [{ role: 'user', content: message }], metadata, user: sessionId, stream: true }
      break
    case 'images':
      endpoint = '/v1/images/generations'
      body = { model: selectedModel, prompt: message, metadata, n: 1, response_format: 'url' }
      break
  }

  const response = await fetch(`${baseUrl}${endpoint}`, {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
    signal: sessionContext?.signal
  })
  if (!response.ok) {
    const text = await response.text()
    let detail = ''
    try {
      const parsed = JSON.parse(text)
      detail = parsed.error?.message || parsed.error || parsed.message || ''
    } catch {
      detail = text
    }
    throw new Error(detail || `请求失败: ${response.status} ${response.statusText}`)
  }

  if (apiType === 'images') {
    const result = await response.json()
    const outputs = Array.isArray(result.data)
      ? result.data.map((item: any) => item.url || item.revised_prompt || (item.b64_json ? '[base64 image data]' : '')).filter(Boolean)
      : []
    onChunk(outputs.length > 0 ? outputs.join('\n') : JSON.stringify(result, null, 2))
    return
  }

  if (!response.body) throw new Error('上游响应缺少响应体')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  const processLine = (line: string) => {
    if (!line.trim() || !line.startsWith('data:')) return
    const data = line.slice(5).trim()
    if (data === '[DONE]') return
    let parsed: any
    try {
      parsed = JSON.parse(data)
    } catch {
      return
    }
    if (apiType === 'responses' && parsed.type === 'response.completed' && parsed.response?.id) {
      sessionContext?.onResponseId?.(parsed.response.id)
      sessionContext?.onInteractionId?.(parsed.response.id)
    }
    if (apiType === 'gemini' && parsed.id) sessionContext?.onInteractionId?.(parsed.id)
    const content = apiType === 'messages'
      ? parsed.delta?.text
      : apiType === 'responses'
        ? (parsed.type === 'response.output_text.delta' ? parsed.delta : parsed.completion)
        : apiType === 'gemini'
          ? parsed.candidates?.[0]?.content?.parts?.[0]?.text
          : parsed.choices?.[0]?.delta?.content
    if (typeof content === 'string' && content) onChunk(content)
  }

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      lines.forEach(processLine)
    }
    buffer += decoder.decode()
    if (buffer) processLine(buffer)
  } finally {
    reader.releaseLock()
  }
}

/**
 * 测试渠道连通性（旧版接口，使用默认模型，支持多轮会话）
 * 委托给 testChannelWithModel 实现
 */
export const testChannel = async (
  apiType: ApiTab,
  channelId: string,
  message: string,
  onChunk: (_chunk: string) => void,
  sessionContext?: TestChannelContext
): Promise<void> => {
  return testChannelWithModel(apiType, channelId, undefined, message, onChunk, sessionContext)
}

export default api
