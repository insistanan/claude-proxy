<template>
  <v-card
    class="channel-card h-100"
    :style="serviceStyle"
    :data-pinned="channel.pinned"
    elevation="0"
    rounded="xl"
    hover
  >
    <!-- 渐变头部背景 -->
    <div class="card-header-gradient">
      <v-card-title class="d-flex align-center justify-space-between pa-4 pb-3 position-relative">
        <div class="d-flex align-center ga-3">
          <!-- 服务类型图标 -->
          <div class="service-icon-wrapper">
            <v-icon 
              :color="getServiceIconColor()"
              size="24"
            >
              {{ getServiceIcon() }}
            </v-icon>
          </div>
          <div class="d-flex align-center ga-2">
            <div>
              <div class="text-h6 font-weight-bold channel-title">
                {{ channel.name }}
              </div>
              <div class="text-caption text-high-emphasis opacity-80">
                {{ getServiceDisplayName() }}
              </div>
            </div>
            <!-- 官网图标按钮（紧贴标题右侧） -->
            <v-tooltip v-if="channel.website" text="打开官网" location="bottom" :open-delay="150">
              <template #activator="{ props: tooltipProps }">
                <v-btn v-bind="tooltipProps" :href="channel.website" target="_blank" rel="noopener" size="small" variant="text" color="primary" icon>
                  <v-icon size="18">mdi-open-in-new</v-icon>
                </v-btn>
              </template>
            </v-tooltip>
          </div>
        </div>
        
        <div class="d-flex align-center ga-2">
          <!-- Pin 按钮 -->
          <v-btn
            size="small"
            :variant="channel.pinned ? 'tonal' : 'text'"
            :color="channel.pinned ? 'warning' : 'grey'"
            class="pin-btn"
            rounded="lg"
            @click="$emit('togglePin', channel.index)"
          >
            <v-icon size="16">
              {{ channel.pinned ? 'mdi-pin' : 'mdi-pin-outline' }}
            </v-icon>
          </v-btn>

          <v-chip
            :color="getServiceChipColor()"
            size="small"
            variant="tonal"
            density="comfortable"
            rounded="pill"
            class="service-chip"
          >
            <span class="font-weight-bold">{{ channel.serviceType.toUpperCase() }}</span>
          </v-chip>
          <v-tooltip v-if="channel.visionCapable" text="支持图片理解" location="bottom" :open-delay="150">
            <template #activator="{ props: tooltipProps }">
              <v-chip
                v-bind="tooltipProps"
                color="primary"
                size="small"
                variant="tonal"
                density="comfortable"
                rounded="pill"
              >
                <v-icon size="16">mdi-image-search-outline</v-icon>
              </v-chip>
            </template>
          </v-tooltip>
          <!-- 渠道状态芯片 -->
          <v-chip
            v-if="channel.status === 'disabled'"
            color="grey"
            size="small"
            variant="flat"
            density="comfortable"
            rounded="lg"
          >
            <v-icon start size="small">mdi-stop-circle</v-icon>
            停用
          </v-chip>
          <v-chip
            v-else-if="channel.status === 'suspended'"
            color="warning"
            size="small"
            variant="flat"
            density="comfortable"
            rounded="lg"
          >
            <v-icon start size="small">mdi-pause-circle</v-icon>
            熔断
          </v-chip>
          <v-chip
            v-else-if="channel.status === 'deprecated'"
            color="grey"
            size="small"
            variant="flat"
            density="comfortable"
            rounded="lg"
          >
            <v-icon start size="small">mdi-archive-clock-outline</v-icon>
            弃用
          </v-chip>
        </div>
      </v-card-title>
    </div>

    <v-card-text class="px-4 py-2">
      <!-- 描述 -->
      <div v-if="channel.description" class="text-body-2 text-medium-emphasis mb-3">
        {{ channel.description }}
      </div>

      <!-- 基本信息 -->
      <div class="mb-4">
        <div class="d-flex align-center ga-2 mb-2">
          <v-icon size="16" color="medium-emphasis">mdi-web</v-icon>
          <span class="text-body-2 font-weight-medium">Base URL:</span>
          <div class="flex-1-1 text-truncate">
            <code class="text-caption bg-surface pa-1 rounded">{{ channel.baseUrl }}</code>
          </div>
        </div>
        
      </div>

      <!-- 状态和延迟（右对齐、间距更紧凑） -->
      <div class="d-flex align-center justify-end ga-4 mb-3">
        <div class="status-indicator">
          <v-tooltip :text="getStatusTooltip()" location="bottom" :open-delay="150">
            <template #activator="{ props: tooltipProps }">
              <div class="status-badge cursor-help" v-bind="tooltipProps" :class="`status-${channel.status || 'unknown'}`">
                <v-icon 
                  :color="getStatusColor()"
                  size="16"
                  class="status-icon"
                >
                  {{ getStatusIcon() }}
                </v-icon>
                <span class="status-text">{{ getStatusText() }}</span>
              </div>
            </template>
          </v-tooltip>
        </div>
        <div v-if="channel.latency !== null" class="latency-indicator">
          <div class="latency-badge" :class="`latency-${getLatencyLevel()}`">
            <v-icon size="14" class="latency-icon">mdi-speedometer</v-icon>
            <span class="latency-text">{{ channel.latency }}ms</span>
          </div>
        </div>
      </div>

      <!-- 实时指标条 — 成功率 + 缓存命中率 -->
      <div v-if="hasMetricsData" class="metrics-bars mb-4">
        <!-- 成功率 -->
        <div class="metric-bar-row">
          <div class="metric-bar-head">
            <span class="metric-bar-label">
              <v-icon size="12" color="medium-emphasis" class="mr-1">mdi-heart-pulse</v-icon>
              成功率 <span class="text-medium-emphasis font-weight-regular">(15分钟)</span>
            </span>
            <span class="metric-bar-value" :class="`rate-${getRateLevel(successRate)}`">
              {{ successRate?.toFixed(0) }}%
            </span>
          </div>
          <div class="metric-bar-track" :class="`track-${getRateLevel(successRate)}`">
            <div
              class="metric-bar-fill"
              :class="`fill-${getRateLevel(successRate)}`"
              :style="{ width: `${successRate ?? 0}%` }"
            ></div>
          </div>
          <div class="metric-bar-meta">
            <span>{{ recentStats?.requestCount ?? 0 }} 请求</span>
            <span v-if="props.metrics?.consecutiveFailures" class="error-text">{{ props.metrics.consecutiveFailures }} 连续失败</span>
          </div>
        </div>

        <!-- 缓存命中率 -->
        <div v-if="cacheHitRate !== undefined" class="metric-bar-row">
          <div class="metric-bar-head">
            <span class="metric-bar-label">
              <v-icon size="12" color="medium-emphasis" class="mr-1">mdi-lightning-bolt</v-icon>
              缓存命中率
            </span>
            <span class="metric-bar-value" :class="`rate-${getRateLevel(cacheHitRate)}`">
              {{ cacheHitRate?.toFixed(0) }}%
            </span>
          </div>
          <div class="metric-bar-track" :class="`track-${getRateLevel(cacheHitRate)}`">
            <div
              class="metric-bar-fill"
              :class="`fill-${getRateLevel(cacheHitRate)}`"
              :style="{ width: `${cacheHitRate ?? 0}%` }"
            ></div>
          </div>
        </div>

        <!-- Token 使用 -->
        <div v-if="recentStats && (recentStats.inputTokens + recentStats.outputTokens + recentStats.cacheReadTokens) > 0" class="token-summary">
          <span><v-icon size="12" color="medium-emphasis">mdi-code-braces</v-icon> 输入 {{ formatTokenCount(recentStats.inputTokens) }}</span>
          <span><v-icon size="12" color="medium-emphasis">mdi-chat-outline</v-icon> 输出 {{ formatTokenCount(recentStats.outputTokens) }}</span>
          <span v-if="recentStats.cacheReadTokens"><v-icon size="12" color="medium-emphasis">mdi-lightning-bolt</v-icon> 缓存 {{ formatTokenCount(recentStats.cacheReadTokens) }}</span>
        </div>
      </div>

      <!-- API密钥管理 -->
      <v-expansion-panels variant="accordion" rounded="lg" class="mb-4">
        <v-expansion-panel>
          <v-expansion-panel-title>
            <div class="d-flex align-center justify-space-between w-100">
              <div class="d-flex align-center ga-2">
                <v-icon size="small">mdi-key-chain</v-icon>
                <span class="text-body-2 font-weight-medium">API密钥管理</span>
              </div>
              <v-chip
                :color="channel.apiKeys.length ? 'secondary' : 'warning'"
                size="large"
                variant="tonal"
                density="comfortable"
                rounded="lg"
                class="mr-2 key-count-chip"
                :style="keyChipStyle"
              >
                <v-icon start size="18">mdi-key</v-icon>
                {{ channel.apiKeys.length }}
              </v-chip>
            </div>
          </v-expansion-panel-title>
          <v-expansion-panel-text>
            <div class="d-flex align-center justify-space-between mb-3">
              <span class="text-body-2 font-weight-medium">已配置的密钥</span>
              <v-btn
                size="small"
                color="primary"
                icon
                variant="elevated"
                rounded="lg"
                @click="$emit('addKey', channel.index)"
              >
                <v-icon>mdi-plus</v-icon>
              </v-btn>
            </div>
            
            <div v-if="channel.apiKeys.length" class="d-flex flex-column ga-2" style="max-height: 150px; overflow-y: auto;">
              <div
                v-for="(key, index) in channel.apiKeys"
                :key="index"
                class="d-flex align-center justify-space-between pa-2 bg-surface rounded"
              >
                <code class="text-caption flex-1-1 text-truncate mr-2">{{ maskApiKey(key) }}</code>
                <div class="d-flex align-center ga-1">
                  <!-- 置顶按钮：仅最后一个 key 显示 -->
                  <v-tooltip v-if="index === channel.apiKeys.length - 1 && channel.apiKeys.length > 1" text="置顶" location="top" :open-delay="150">
                    <template #activator="{ props: tooltipProps }">
                      <v-btn v-bind="tooltipProps" size="x-small" color="warning" icon variant="text" rounded="md" @click="$emit('moveKeyToTop', channel.index, key)">
                        <v-icon size="small">mdi-arrow-up-bold</v-icon>
                      </v-btn>
                    </template>
                  </v-tooltip>
                  <!-- 置底按钮：仅第一个 key 显示 -->
                  <v-tooltip v-if="index === 0 && channel.apiKeys.length > 1" text="置底" location="top" :open-delay="150">
                    <template #activator="{ props: tooltipProps }">
                      <v-btn v-bind="tooltipProps" size="x-small" color="warning" icon variant="text" rounded="md" @click="$emit('moveKeyToBottom', channel.index, key)">
                        <v-icon size="small">mdi-arrow-down-bold</v-icon>
                      </v-btn>
                    </template>
                  </v-tooltip>
                  <v-tooltip :text="copiedKeyIndex === index ? '已复制!' : '复制密钥'" location="top" :open-delay="150">
                    <template #activator="{ props: tooltipProps }">
                      <v-btn
                        v-bind="tooltipProps"
                        size="x-small"
                        :color="copiedKeyIndex === index ? 'success' : 'primary'"
                        icon
                        variant="text"
                        rounded="md"
                        @click="copyApiKey(key, index)"
                      >
                        <v-icon size="small">{{ copiedKeyIndex === index ? 'mdi-check' : 'mdi-content-copy' }}</v-icon>
                      </v-btn>
                    </template>
                  </v-tooltip>
                  <v-btn
                    size="x-small"
                    color="error"
                    icon
                    variant="text"
                    rounded="md"
                    @click="$emit('removeKey', channel.index, getOriginalKey(key))"
                  >
                    <v-icon size="small">mdi-close</v-icon>
                  </v-btn>
                </div>
              </div>
            </div>
            
            <div v-else class="text-center py-4">
              <span class="text-body-2 text-medium-emphasis">暂无API密钥</span>
            </div>
          </v-expansion-panel-text>
        </v-expansion-panel>
      </v-expansion-panels>

      <!-- 操作按钮 -->
      <div class="action-buttons d-flex flex-wrap ga-2 justify-end w-100">
        <v-btn
          size="small"
          color="primary"
          variant="outlined"
          rounded="lg"
          class="action-btn"
          :prepend-icon="copiedConfig ? 'mdi-check' : 'mdi-content-copy'"
          @click="copyChannelConfig"
        >
          {{ copiedConfig ? '已复制' : '复制配置' }}
        </v-btn>

        <v-btn
          size="small"
          color="success"
          variant="outlined"
          rounded="lg"
          class="action-btn"
          prepend-icon="mdi-test-tube"
          @click="handleQuickTest"
        >
          快捷测试
        </v-btn>
        
        <v-btn
          size="small"
          color="primary"
          variant="outlined"
          rounded="lg"
          class="action-btn"
          prepend-icon="mdi-speedometer"
          @click="$emit('ping', channel.index)"
        >
          测试延迟
        </v-btn>
        
        <v-btn
          size="small"
          color="info"
          variant="outlined"
          rounded="lg"
          class="action-btn"
          prepend-icon="mdi-pencil"
          @click="$emit('edit', channel)"
        >
          编辑
        </v-btn>
        
        <v-btn
          size="small"
          color="error"
          variant="text"
          rounded="lg"
          class="action-btn danger-action"
          prepend-icon="mdi-delete"
          @click="$emit('delete', channel.index)"
        >
          删除
        </v-btn>
      </div>
    </v-card-text>
  </v-card>

  <!-- 快捷测试弹窗 -->
  <QuickTestModal
    v-model="showQuickTestModal"
    :channel="channel"
    :api-type="apiType"
  />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Channel, ChannelMetrics, TimeWindowStats } from '../services/api'
import QuickTestModal from './QuickTestModal.vue'

interface Props {
  channel: Channel
  apiType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
  metrics?: ChannelMetrics | null
}

const props = defineProps<Props>()

// 复制功能相关状态
const copiedKeyIndex = ref<number | null>(null)

// 快捷测试弹窗状态
const showQuickTestModal = ref(false)
const copiedConfig = ref(false)

defineEmits<{
  edit: [channel: Channel]
  delete: [channelId: number]
  addKey: [channelId: number]
  removeKey: [channelId: number, apiKey: string]
  moveKeyToTop: [channelId: number, apiKey: string]
  moveKeyToBottom: [channelId: number, apiKey: string]
  ping: [channelId: number]
  togglePin: [channelId: number]
  quickTest: [channelId: number]
}>()

const handleQuickTest = () => {
  showQuickTestModal.value = true
}

// 复制渠道配置到剪贴板
const copyChannelConfig = async () => {
  try {
    // 获取第一个密钥
    const firstKey = props.channel.apiKeys.length > 0 ? props.channel.apiKeys[0] : ''
    
    // 构建配置文本
    const configText = `名称: ${props.channel.name}
URL: ${props.channel.baseUrl}
密钥: ${firstKey}`
    
    // 复制到剪贴板
    await navigator.clipboard.writeText(configText)
    
    // 显示已复制状态
    copiedConfig.value = true
    setTimeout(() => {
      copiedConfig.value = false
    }, 2000)
  } catch (error) {
    console.error('复制配置失败:', error)
  }
}

// 获取服务类型对应的芯片颜色
const getServiceChipColor = () => {
  const colorMap: Record<string, string> = {
    openai: 'info',
    claude: 'success',
    gemini: 'accent',
    chat: 'primary'
  }
  return colorMap[props.channel.serviceType] || 'primary'
}

// 获取状态对应的颜色
const getStatusColor = () => {
  const colorMap: Record<string, string> = {
    'active': 'success',
    'suspended': 'warning',
    'disabled': 'grey',
    'deprecated': 'grey',
    'healthy': 'success',
    'error': 'error',
    'unknown': 'warning'
  }
  return colorMap[props.channel.status || 'unknown']
}

// 获取状态图标
const getStatusIcon = () => {
  const iconMap: Record<string, string> = {
    'active': 'mdi-check-circle',
    'suspended': 'mdi-pause-circle',
    'disabled': 'mdi-stop-circle',
    'deprecated': 'mdi-archive-clock-outline',
    'healthy': 'mdi-check-circle',
    'error': 'mdi-alert-circle',
    'unknown': 'mdi-help-circle'
  }
  return iconMap[props.channel.status || 'unknown']
}

// 获取状态文本
const getStatusText = () => {
  const textMap: Record<string, string> = {
    'active': '活跃',
    'suspended': '熔断',
    'disabled': '备用',
    'deprecated': '弃用',
    'healthy': '健康',
    'error': '错误',
    'unknown': '未检测'
  }
  return textMap[props.channel.status || 'unknown']
}

// 状态解释文案（悬浮提示）
const getStatusTooltip = () => {
  const status = props.channel.status || 'unknown'
  if (status === 'healthy') return '连接正常：最近一次检测通过'
  if (status === 'error') return '连接异常：请检查基础 URL、网络或 API 密钥'
  if (status === 'active') return '活跃渠道：会参与故障转移调度'
  if (status === 'suspended') return '熔断渠道：暂时后置，可手动恢复'
  if (status === 'disabled') return '备用渠道：不参与故障转移调度'
  if (status === 'deprecated') return '弃用渠道：不参与调度，超过 3 天会自动清理'
  return '尚未检测：请点击“测试延迟”进行检测'
}

// 掩码API密钥用于显示
const maskApiKey = (key: string): string => {
  if (key.length <= 10) return key.slice(0, 3) + '***' + key.slice(-2)
  return key.slice(0, 8) + '***' + key.slice(-5)
}

// 获取原始密钥（用于删除操作），现在直接传递原始密钥
const getOriginalKey = (originalKey: string) => {
  return originalKey
}

// 复制API密钥到剪贴板
const copyApiKey = async (key: string, index: number) => {
  try {
    await navigator.clipboard.writeText(key)
    copiedKeyIndex.value = index

    // 2秒后重置复制状态
    setTimeout(() => {
      copiedKeyIndex.value = null
    }, 2000)
  } catch (err) {
    console.error('复制密钥失败:', err)
    // 降级方案：使用传统的复制方法
    const textArea = document.createElement('textarea')
    textArea.value = key
    textArea.style.position = 'fixed'
    textArea.style.left = '-999999px'
    textArea.style.top = '-999999px'
    document.body.appendChild(textArea)
    textArea.focus()
    textArea.select()

    try {
      document.execCommand('copy')
      copiedKeyIndex.value = index

      setTimeout(() => {
        copiedKeyIndex.value = null
      }, 2000)
    } catch (err) {
      console.error('降级复制方案也失败:', err)
    } finally {
      textArea.remove()
    }
  }
}

// 获取服务类型图标
const getServiceIcon = () => {
  const iconMap: Record<string, string> = {
    'openai': 'mdi-robot',
    'claude': 'mdi-message-processing',
    'gemini': 'mdi-diamond-stone',
    'chat': 'mdi-chat-processing',
    'images': 'mdi-image'
  }
  return iconMap[props.channel.serviceType] || 'mdi-api'
}

// 获取服务类型图标颜色
const getServiceIconColor = () => {
  const colorMap: Record<string, string> = {
    'openai': 'primary',
    'claude': 'orange',
    'gemini': 'purple',
    'chat': 'primary',
    'images': 'warning'
  }
  return colorMap[props.channel.serviceType] || 'grey'
}

// 获取服务类型显示名称
const getServiceDisplayName = () => {
  const nameMap: Record<string, string> = {
    'openai': 'OpenAI API',
    'claude': 'Claude API',
    'gemini': 'Gemini API',
    'chat': 'OpenAI Chat',
    'images': 'OpenAI Images'
  }
  return nameMap[props.channel.serviceType] || 'Custom API'
}

// 获取延迟等级
const getLatencyLevel = () => {
  if (!props.channel.latency) return 'unknown'
  
  if (props.channel.latency < 200) return 'excellent'
  if (props.channel.latency < 500) return 'good'
  if (props.channel.latency < 1000) return 'fair'
  return 'poor'
}

// Chip 动态文本颜色，避免在浅色背景上出现近黑文本
const keyChipStyle = computed(() => {
  const hasKeys = props.channel.apiKeys.length > 0
  return {
    color: hasKeys ? 'rgb(var(--v-theme-on-secondary))' : 'rgb(var(--v-theme-on-warning))',
    fontSize: '0.95rem'
  }
})

// ===== 指标条相关 =====
const hasMetricsData = computed(() => {
  return props.metrics !== null && props.metrics !== undefined
})

const recentStats = computed(() => {
  if (!props.metrics || !props.metrics.timeWindows) return null
  return props.metrics.timeWindows['15m'] ?? null
})

const successRate = computed(() => {
  return recentStats.value?.successRate ?? undefined
})

const cacheHitRate = computed(() => {
  const stats = recentStats.value
  if (!stats) return undefined
  const inputTokens = stats.inputTokens ?? 0
  const cacheReadTokens = stats.cacheReadTokens ?? 0
  const denom = inputTokens + cacheReadTokens
  if (denom <= 0) return undefined
  return stats.cacheHitRate ?? (cacheReadTokens / denom * 100)
})

const getRateLevel = (rate?: number): string => {
  if (rate === undefined || rate === null) return 'unknown'
  if (rate >= 90) return 'high'
  if (rate >= 70) return 'medium'
  return 'low'
}

const formatTokenCount = (tokens?: number): string => {
  if (!tokens || tokens <= 0) return '0'
  if (tokens >= 1000000) return `${(tokens / 1000000).toFixed(1)}M`
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}K`
  return tokens.toString()
}

// 根据服务类型设置卡片强调色（明暗模式自动随主题变量变更）
const serviceStyle = computed(() => {
  const map: Record<string, string> = {
    openai: 'var(--v-theme-info)',
    claude: 'var(--v-theme-success)',
    gemini: 'var(--v-theme-accent)',
    chat: 'var(--v-theme-primary)'
  }
  const value = map[props.channel.serviceType] || 'var(--v-theme-primary)'
  return {
    '--card-accent-rgb': value
  } as Record<string, string>
})
</script>

<style scoped>
/* --- BASE STYLES (LIGHT MODE) --- */
.channel-card {
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  position: relative;
  overflow: hidden;
  background: rgb(var(--v-theme-surface));
  border: 1px solid rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.3);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.03);
  border-radius: 12px;
}

/* 左侧彩色强调条，突出渠道类型颜色 */
.channel-card::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  background: linear-gradient(
    to bottom,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.9),
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.5)
  );
}

.channel-card:not(:hover) {
  /* default state */
}

.channel-card:hover {
  transform: translateY(-3px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.07);
  border-color: rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.5);
}

.card-header-gradient {
  background: linear-gradient(135deg,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.20) 0%,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.10) 50%,
    rgba(var(--v-theme-accent), 0.12) 100%);
  position: relative;
  border-top-left-radius: inherit;
  border-top-right-radius: inherit;
}

.service-icon-wrapper {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.18) 0%,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.10) 100%);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
  border: 1px solid rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.25);
  transition: all 0.3s ease;
}

.channel-card:hover .service-icon-wrapper {
  transform: scale(1.1);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
}

.service-chip {
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06);
  border: none;
}

/* --- INDICATORS (LIGHT) --- */
.status-badge, .latency-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-radius: 8px;
  font-size: 0.75rem;
  font-weight: 500;
}

.status-badge {
  background-color: rgba(0, 0, 0, 0.05);
}
.status-badge.status-healthy { color: rgb(var(--v-theme-success)); background-color: rgba(var(--v-theme-success), 0.12); }
.status-badge.status-error { color: rgb(var(--v-theme-error)); background-color: rgba(var(--v-theme-error), 0.12); }
.status-badge.status-unknown { color: rgb(var(--v-theme-secondary)); background-color: rgba(var(--v-theme-secondary), 0.12); }

.latency-badge {
  font-weight: 600;
}
.latency-badge.latency-excellent { color: #2e7d32; background: rgba(76, 175, 80, 0.1); }
.latency-badge.latency-good { color: #f57c00; background: rgba(255, 193, 7, 0.1); }
.latency-badge.latency-fair { color: #e65100; background: rgba(255, 152, 0, 0.1); }
.latency-badge.latency-poor { color: #c62828; background: rgba(244, 67, 54, 0.1); }

/* --- PIN BUTTON (LIGHT) --- */
.pin-btn {
  min-width: 32px !important; width: 32px; height: 32px;
  border-radius: 12px !important;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}

.pin-btn:hover {
  transform: scale(1.1);
}

.key-count-chip {
  font-weight: 700;
}

/* --- KEYFRAMES --- */
@keyframes shimmer {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(100%); }
}

@keyframes slideInUp {
  from { opacity: 0; transform: translateY(30px); }
  to { opacity: 1; transform: translateY(0); }
}

.channel-card {
  animation: slideInUp 0.6s ease-out;
}

/* ===== 实时指标条样式 ===== */
.metrics-bars {
  background: rgba(var(--v-theme-primary), 0.03);
  border: 1px solid rgba(var(--v-theme-outline), 0.2);
  border-radius: 10px;
  padding: 12px 14px;
}

.v-theme--dark .metrics-bars {
  background: rgba(99, 102, 241, 0.04);
  border-color: rgba(99, 102, 241, 0.12);
}

.metric-bar-row {
  margin-bottom: 8px;
}
.metric-bar-row:last-child {
  margin-bottom: 0;
}

.metric-bar-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 5px;
}

.metric-bar-label {
  font-size: 0.72rem;
  font-weight: 600;
  color: rgb(var(--v-theme-on-surface-variant));
  display: flex;
  align-items: center;
}

.metric-bar-value {
  font-size: 0.85rem;
  font-weight: 700;
  font-family: 'Fira Code', 'JetBrains Mono', monospace;
  line-height: 1;
}

.metric-bar-value.rate-high { color: #22C55E; }
.metric-bar-value.rate-medium { color: #D97706; }
.metric-bar-value.rate-low { color: #DC2626; }
.metric-bar-value.rate-unknown { color: rgb(var(--v-theme-on-surface-variant)); }

.v-theme--dark .metric-bar-value.rate-high { color: #4ADE80; }
.v-theme--dark .metric-bar-value.rate-medium { color: #FBBF24; }
.v-theme--dark .metric-bar-value.rate-low { color: #F87171; }

.metric-bar-track {
  height: 8px;
  background: rgba(var(--v-theme-outline), 0.25);
  border-radius: 4px;
  overflow: hidden;
  position: relative;
}

.metric-bar-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.8s cubic-bezier(0.4, 0, 0.2, 1);
  position: relative;
  min-width: 2px;
}

.metric-bar-fill.fill-high {
  background: linear-gradient(90deg, #22C55E, #4ADE80);
  box-shadow: 0 0 8px rgba(34, 197, 94, 0.35);
}
.metric-bar-fill.fill-medium {
  background: linear-gradient(90deg, #D97706, #F59E0B);
  box-shadow: 0 0 8px rgba(217, 119, 6, 0.4);
}
.metric-bar-fill.fill-low {
  background: linear-gradient(90deg, #DC2626, #EF4444);
  box-shadow: 0 0 8px rgba(220, 38, 38, 0.4);
}

.v-theme--dark .metric-bar-fill.fill-high {
  background: linear-gradient(90deg, #22C55E, #4ADE80);
  box-shadow: 0 0 10px rgba(34, 197, 94, 0.3);
}
.v-theme--dark .metric-bar-fill.fill-medium {
  background: linear-gradient(90deg, #D97706, #FBBF24);
  box-shadow: 0 0 10px rgba(251, 191, 36, 0.3);
}
.v-theme--dark .metric-bar-fill.fill-low {
  background: linear-gradient(90deg, #DC2626, #F87171);
  box-shadow: 0 0 10px rgba(248, 113, 113, 0.3);
}

.metric-bar-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 4px;
  font-size: 0.65rem;
  color: rgb(var(--v-theme-on-surface-variant));
  opacity: 0.7;
}

.metric-bar-meta .error-text {
  color: #DC2626;
  font-weight: 600;
}
.v-theme--dark .metric-bar-meta .error-text {
  color: #F87171;
}

.token-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid rgba(var(--v-theme-outline), 0.15);
  font-size: 0.68rem;
  color: rgb(var(--v-theme-on-surface-variant));
  opacity: 0.75;
}

.token-summary span {
  display: flex;
  align-items: center;
  gap: 3px;
}

/* @keyframes shimmer */
.v-theme--dark .channel-card {
  /* 暗色下加深类型底色透明度，保证可见 */
  background: linear-gradient(
    0deg,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.10),
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.10)
  ), linear-gradient(135deg, rgba(19, 22, 36, 0.95), rgba(28, 31, 51, 0.9));
  border: 1px solid rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.35);
  box-shadow:
    0 4px 24px rgba(0, 0, 0, 0.3),
    0 1px 8px rgba(0, 0, 0, 0.2),
    inset 0 1px 0 rgba(255, 255, 255, 0.03);
}

.v-theme--dark .channel-card:not(.current-channel):hover {
  border-color: rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.6);
  box-shadow:
    0 20px 40px rgba(0, 0, 0, 0.36),
    0 8px 24px rgba(0, 0, 0, 0.24),
    0 0 30px rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.08);
}

.v-theme--dark .card-header-gradient {
  background: linear-gradient(135deg,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.22) 0%,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.12) 50%,
    rgba(124, 58, 237, 0.10) 100%);
}

.v-theme--dark .service-icon-wrapper {
  background: linear-gradient(135deg,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.22) 0%,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.12) 100%);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.24);
  border: 1px solid rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.3);
}

.v-theme--dark .channel-card::before {
  background: linear-gradient(
    to bottom,
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.95),
    rgba(var(--card-accent-rgb, var(--v-theme-primary)), 0.6)
  );
}

.v-theme--dark .channel-card:hover .service-icon-wrapper {
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.32);
  border-color: rgba(255, 255, 255, 0.2);
}

.v-theme--dark .service-chip {
  border: none;
}

/* --- INDICATORS (DARK) --- */
.v-theme--dark .status-badge {
  background-color: rgba(255, 255, 255, 0.08);
}
.v-theme--dark .status-badge.status-healthy { color: #86EFAC; background-color: rgba(34, 197, 94, 0.18); }
.v-theme--dark .status-badge.status-error { color: #FCA5A5; background-color: rgba(248, 113, 113, 0.2); }
.v-theme--dark .status-badge.status-unknown { color: #CBD5E1; background-color: rgba(148, 163, 184, 0.18); }

.v-theme--dark .latency-badge.latency-excellent { color: #86EFAC; background: rgba(34, 197, 94, 0.22); }
.v-theme--dark .latency-badge.latency-good { color: #FDE68A; background: rgba(251, 191, 36, 0.2); }
.v-theme--dark .latency-badge.latency-fair { color: #FCD49B; background: rgba(251, 146, 60, 0.22); }
.v-theme--dark .latency-badge.latency-poor { color: #FCA5A5; background: rgba(248, 113, 113, 0.25); }
</style>
