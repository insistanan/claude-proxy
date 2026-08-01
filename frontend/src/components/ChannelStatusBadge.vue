<template>
  <div class="status-badge" :class="[statusClass, { 'has-metrics': showMetrics }]">
    <v-tooltip location="top" content-class="status-tooltip">
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

const statusConfig = computed(() => STATUS_CONFIG[props.status] || STATUS_CONFIG.unknown)
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
  border-radius: 8px;
  cursor: help;
  transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
  font-size: 12px;
  font-weight: 500;
  border: 1px solid transparent;
}

.badge-content:hover {
  filter: brightness(1.05);
  transform: scale(1.03);
}

.status-label {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.3px;
}

/* 状态样式 — 更清晰的视觉区分 */
.status-active .badge-content {
  background: rgba(16, 185, 129, 0.12);
  color: rgb(var(--v-theme-success));
  border-color: rgba(16, 185, 129, 0.15);
}

.status-suspended .badge-content {
  background: rgba(245, 158, 11, 0.12);
  color: rgb(var(--v-theme-warning));
  border-color: rgba(245, 158, 11, 0.15);
}

.status-disabled .badge-content {
  background: rgba(148, 163, 184, 0.1);
  color: #64748B;
  border-color: rgba(148, 163, 184, 0.12);
}

.status-deprecated .badge-content {
  background: rgba(148, 163, 184, 0.06);
  color: #94A3B8;
  border-color: rgba(148, 163, 184, 0.08);
}

.status-error .badge-content {
  background: rgba(239, 68, 68, 0.12);
  color: rgb(var(--v-theme-error));
  border-color: rgba(239, 68, 68, 0.15);
}

.status-unknown .badge-content {
  background: rgba(148, 163, 184, 0.06);
  color: #94A3B8;
  border-color: rgba(148, 163, 184, 0.08);
}

/* 暗色模式 */
.v-theme--dark .status-active .badge-content {
  background: rgba(16, 185, 129, 0.15);
}
.v-theme--dark .status-suspended .badge-content {
  background: rgba(245, 158, 11, 0.15);
}
.v-theme--dark .status-disabled .badge-content {
  background: rgba(148, 163, 184, 0.1);
}
.v-theme--dark .status-deprecated .badge-content {
  background: rgba(148, 163, 184, 0.08);
}
.v-theme--dark .status-error .badge-content {
  background: rgba(239, 68, 68, 0.15);
}

/* 手机端简化 */
@media (max-width: 600px) {
  .status-label { display: none; }
  .badge-content { padding: 2px; background: transparent !important; }
  .status-icon { font-size: 16px !important; }
}

.tooltip-content { max-width: 200px; }
</style>

<style>
.status-tooltip {
  background: #FFFFFF !important;
  color: #1E293B !important;
  border: 1px solid rgba(0, 0, 0, 0.06) !important;
  border-radius: 10px !important;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.1) !important;
  padding: 8px 14px !important;
  font-size: 0.8rem !important;
}

.v-theme--dark .status-tooltip {
  background: #1E293B !important;
  color: #F1F5F9 !important;
  border: 1px solid rgba(255, 255, 255, 0.08) !important;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3) !important;
}
</style>