<template>
  <div class="status-badge" :class="[statusClass, { 'has-metrics': showMetrics }]">
    <v-tooltip location="top" content-class="status-tooltip" :open-on-focus="false">
      <template #activator="{ props: tooltipProps }">
        <div class="badge-content" v-bind="tooltipProps">
          <v-icon :size="iconSize" class="status-icon">{{ statusIcon }}</v-icon>
          <span v-if="showLabel" class="status-label">{{ statusLabel }}</span>
        </div>
      </template>
      <div class="tooltip-content">
        <div class="font-weight-bold mb-1">{{ statusLabel }}</div>
        <template v-if="metrics">
          <div class="text-caption" style="line-height: 1.6;">
            <div>请求数: {{ metrics.requestCount }}</div>
            <div>成功率: {{ metrics.successRate?.toFixed(1) || 0 }}%</div>
            <div>连续失败: {{ metrics.consecutiveFailures }}</div>
            <div v-if="metrics.circuitState && metrics.circuitState !== 'closed'">
              熔断状态: {{ metrics.circuitState === 'open' ? '熔断中' : '半开探测' }}
              <template v-if="metrics.circuitConsecutiveFailures">（连续失败 {{ metrics.circuitConsecutiveFailures }}）</template>
            </div>
            <div v-if="metrics.lastSuccessAt">最后成功: {{ formatTime(metrics.lastSuccessAt) }}</div>
            <div v-if="metrics.lastFailureAt">最后失败: {{ formatTime(metrics.lastFailureAt) }}</div>
          </div>
        </template>
        <div v-else class="text-caption text-medium-emphasis">暂无指标数据</div>
      </div>
    </v-tooltip>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ChannelStatus, ChannelMetrics } from '../services/api'

const props = withDefaults(defineProps<{
  status: ChannelStatus | 'healthy' | 'error' | 'unknown'
  metrics?: ChannelMetrics
  showLabel?: boolean
  size?: 'small' | 'default' | 'large'
}>(), { showLabel: true, size: 'default' })

const STATUS_CONFIG: Record<string, { icon: string; color: string; label: string; class: string }> = {
  active:     { icon: 'mdi-check-circle', color: 'success', label: '活跃', class: 'status-active' },
  healthy:    { icon: 'mdi-check-circle', color: 'success', label: '健康', class: 'status-active' },
  suspended:  { icon: 'mdi-pause-circle', color: 'warning', label: '熔断', class: 'status-suspended' },
  disabled:   { icon: 'mdi-close-circle', color: 'error',   label: '禁用', class: 'status-disabled' },
  deprecated: { icon: 'mdi-archive-clock-outline', color: 'grey', label: '弃用', class: 'status-deprecated' },
  error:      { icon: 'mdi-alert-circle', color: 'error',   label: '错误', class: 'status-error' },
  unknown:    { icon: 'mdi-help-circle',  color: 'grey',    label: '未知', class: 'status-unknown' }
}

// 渠道级熔断器运行时态（dashboard API 的 circuitState）：覆盖 active 渠道的展示。
// open=熔断（红）、half_open=探测中（黄）；closed 不覆盖原状态。
const CIRCUIT_CONFIG: Record<string, { icon: string; color: string; label: string; class: string }> = {
  open:      { icon: 'mdi-flash-alert', color: 'error',   label: '熔断', class: 'status-circuit-open' },
  half_open: { icon: 'mdi-check-underline-circle', color: 'warning', label: '探测中', class: 'status-circuit-half-open' }
}

const circuitConfig = computed(() => {
  if (props.status !== 'active' && props.status !== 'healthy') return null
  const state = props.metrics?.circuitState
  if (!state || state === 'closed') return null
  return CIRCUIT_CONFIG[state] ?? null
})

const statusConfig = computed(() => circuitConfig.value ?? STATUS_CONFIG[props.status] ?? STATUS_CONFIG.unknown)
const statusIcon = computed(() => statusConfig.value.icon)
const statusLabel = computed(() => statusConfig.value.label)
const statusClass = computed(() => statusConfig.value.class)
const showMetrics = computed(() => !!props.metrics)

const iconSize = computed(() => {
  switch (props.size) {
    case 'small': return 14
    case 'large': return 22
    default: return 18
  }
})

const formatTime = (dateStr: string): string => {
  const date = new Date(dateStr)
  const now = new Date()
  const diff = now.getTime() - date.getTime()
  if (diff < 60000) return '刚刚'
  if (diff < 3600000) return `${Math.floor(diff / 60000)} 分钟前`
  if (diff < 86400000) return `${Math.floor(diff / 3600000)} 小时前`
  return date.toLocaleDateString()
}
</script>

<style scoped>
.status-badge {
  display: inline-flex;
  align-items: center;
}

.badge-content {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  /* 不对称切角 — 与全站形状语言一致 */
  border-radius: 6px 2px 6px 2px;
  cursor: help;
  transition: transform 0.18s var(--ease-spring), box-shadow 0.18s ease, background 0.18s ease;
  font-size: 12px;
  font-weight: 600;
  border: 1px solid transparent;
}

.badge-content:hover {
  transform: translateY(-1px) scale(1.04);
}

.status-label {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.03em;
}

/* ===== 状态样式 — 全部走主题变量，明暗主题自动跟随；
        边框比底色更实，靠轮廓而非高饱和填充制造对比 ===== */
.status-active .badge-content {
  background: rgba(var(--v-theme-success), 0.14);
  color: rgb(var(--v-theme-success));
  border-color: rgba(var(--v-theme-success), 0.45);
  position: relative;
  padding-left: 24px;
}

/* 在线状态灯 */
.status-active .badge-content::before {
  content: '';
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: rgb(var(--v-theme-success));
  box-shadow: 0 0 8px 1px rgba(var(--v-theme-success), 0.85);
  animation: none;
}

/* 活跃状态环形脉冲 — 仅 hover 时扩散，避免整屏徽章同时脉动 */
.status-active .badge-content::after {
  content: '';
  position: absolute;
  inset: -2px;
  border-radius: inherit;
  border: 1px solid rgba(var(--v-theme-success), 0.55);
  opacity: 0;
  animation: pulse-ring 2s ease-out infinite;
  animation-play-state: paused;
  pointer-events: none;
}
.status-active .badge-content:hover::after { opacity: 1; animation-play-state: running; }

.status-suspended .badge-content {
  background: rgba(var(--v-theme-warning), 0.14);
  color: rgb(var(--v-theme-warning));
  border-color: rgba(var(--v-theme-warning), 0.45);
  position: relative;
  padding-left: 24px;
}

/* 熔断状态灯 */
.status-suspended .badge-content::before {
  content: '';
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: rgb(var(--v-theme-warning));
  box-shadow: 0 0 7px 1px rgba(var(--v-theme-warning), 0.85);
  animation: none;
}

.status-disabled .badge-content {
  background: rgba(var(--v-theme-on-surface), 0.07);
  color: rgb(var(--v-theme-on-surface-variant));
  border-color: rgba(var(--v-theme-outline), 0.5);
}

.status-deprecated .badge-content {
  background: rgba(var(--v-theme-on-surface), 0.04);
  color: rgba(var(--v-theme-on-surface-variant), 0.75);
  border-color: rgba(var(--v-theme-outline), 0.35);
}

.status-error .badge-content {
  background: rgba(var(--v-theme-error), 0.14);
  color: rgb(var(--v-theme-error));
  border-color: rgba(var(--v-theme-error), 0.5);
  position: relative;
  /* 故障徽章本体持续警报呼吸，不必等 hover 就能被余光捕捉 */
  animation: alarm-breathe 1.6s ease-in-out infinite;
}

/* 渠道级熔断（运行时 circuitState=open）：错误色 + 警报呼吸，与 status-error 同强度 */
.status-circuit-open .badge-content {
  background: rgba(var(--v-theme-error), 0.14);
  color: rgb(var(--v-theme-error));
  border-color: rgba(var(--v-theme-error), 0.5);
  position: relative;
  animation: alarm-breathe 1.6s ease-in-out infinite;
}

/* 半开探测中（circuitState=half_open）：警示黄 + 状态灯 */
.status-circuit-half-open .badge-content {
  background: rgba(var(--v-theme-warning), 0.14);
  color: rgb(var(--v-theme-warning));
  border-color: rgba(var(--v-theme-warning), 0.45);
  position: relative;
  padding-left: 24px;
}

.status-circuit-half-open .badge-content::before {
  content: '';
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: rgb(var(--v-theme-warning));
  box-shadow: 0 0 7px 1px rgba(var(--v-theme-warning), 0.85);
  animation: none;
}

/* 错误状态 — 短促冲击波警示扩散 */
.status-error .badge-content::after {
  content: '';
  position: absolute;
  inset: -2px;
  border-radius: inherit;
  border: 1px solid rgba(var(--v-theme-error), 0.7);
  animation: ring-ping 1.8s ease-out infinite;
  pointer-events: none;
}

.status-unknown .badge-content {
  background: rgba(var(--v-theme-on-surface), 0.04);
  color: rgba(var(--v-theme-on-surface-variant), 0.75);
  border-color: rgba(var(--v-theme-outline), 0.35);
}

/* 暗色模式 — 底色略提，边框保持高对比 */
.v-theme--dark .status-active .badge-content { background: rgba(var(--v-theme-success), 0.16); }
.v-theme--dark .status-suspended .badge-content { background: rgba(var(--v-theme-warning), 0.16); }
.v-theme--dark .status-disabled .badge-content { background: rgba(255, 255, 255, 0.07); }
.v-theme--dark .status-deprecated .badge-content { background: rgba(255, 255, 255, 0.045); }
.v-theme--dark .status-error .badge-content { background: rgba(var(--v-theme-error), 0.17); }

/* 手机端简化 */
@media (max-width: 600px) {
  .status-label { display: none; }
  .badge-content { padding: 2px; background: transparent !important; border-color: transparent !important; }
  .status-icon { font-size: 16px !important; }
  .status-active .badge-content::before,
  .status-suspended .badge-content::before { display: none; }
  .status-active .badge-content,
  .status-suspended .badge-content { padding-left: 10px; }
}

@media (prefers-reduced-motion: reduce) {
  .badge-content,
  .badge-content::before,
  .badge-content::after { animation: none !important; transition: none !important; }
}

.tooltip-content { max-width: 200px; }
</style>

<style>
/* Tooltip 走主题变量，明暗切换自动跟随，不再硬编码两套色值 */
.status-tooltip {
  background: rgb(var(--v-theme-surface)) !important;
  color: rgb(var(--v-theme-on-surface)) !important;
  border: 1px solid rgba(var(--v-theme-outline), 0.6) !important;
  border-radius: 8px 2px 8px 2px !important;
  box-shadow: var(--shadow-2) !important;
  padding: 8px 14px !important;
  font-size: 0.8rem !important;
}
</style>
