<template>
  <div class="audit-summary" :class="{ 'audit-summary--compact': compact }" aria-live="polite">
    <div class="audit-heading">
      <v-icon size="14" color="primary">mdi-shield-refresh</v-icon>
      <span>模型审计</span>
      <v-progress-circular v-if="loading && !summary" indeterminate size="12" width="2" color="primary" />
    </div>

    <div v-if="!hasStableId" class="audit-unavailable">缺少稳定 ID</div>
    <div v-else-if="!summary && loading" class="audit-unavailable">读取中</div>
    <div v-else-if="!summary" class="audit-unavailable audit-unavailable--error">
      <v-icon size="13">mdi-alert-circle-outline</v-icon>
      {{ error || '摘要不可用' }}
    </div>

    <template v-else>
      <v-chip size="x-small" :color="statusMeta.color" variant="tonal" class="audit-status">
        {{ statusMeta.label }}
      </v-chip>

      <div class="audit-field" :title="identityTitle">
        <span class="audit-label">身份</span>
        <strong>{{ identityLabel }}</strong>
        <span v-if="summary.identity" class="audit-detail">置信 {{ formatPercent(summary.identity.confidence) }}</span>
      </div>

      <div class="audit-field" :title="mixtureLabel">
        <span class="audit-label">混用</span>
        <strong class="audit-ellipsis">{{ mixtureLabel }}</strong>
      </div>

      <div class="audit-field" :title="capabilityTitle">
        <span class="audit-label">{{ capabilityDimensionLabel }}</span>
        <strong>{{ capabilityLabel }}</strong>
        <span v-if="summary.capability" class="audit-detail">
          覆盖 {{ formatPercent(summary.capability.coverage)
          }}<template v-if="!summary.capability.dimension"> · {{ summary.capability.scoredDimensions }}/7 维</template>
        </span>
      </div>

      <div class="audit-field audit-field--samples">
        <span class="audit-label">样本</span>
        <strong>{{ summary.sampleCount }}</strong>
      </div>

      <div class="audit-field audit-field--time" :title="reportedAtTitle">
        <v-icon size="13">mdi-clock-outline</v-icon>
        <span>{{ reportedAtLabel }}</span>
        <span v-if="summary.freshness?.state === 'stale'" class="audit-stale">已过期</span>
      </div>

      <v-tooltip v-if="error" :text="error" location="top">
        <template #activator="{ props: tooltipProps }">
          <v-icon v-bind="tooltipProps" size="14" color="warning">mdi-alert-circle-outline</v-icon>
        </template>
      </v-tooltip>

      <v-tooltip v-if="summary.reportId" :text="reportDetailLabel" location="top">
        <template #activator="{ props: tooltipProps }">
          <v-btn
            v-bind="tooltipProps"
            icon="mdi-chevron-right"
            size="x-small"
            variant="text"
            :aria-label="reportDetailLabel"
            class="audit-detail-button"
            @click="$emit('open', summary.reportId)"
          />
        </template>
      </v-tooltip>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ModelAuditChannelSummary } from '@/services/api'

defineEmits<{
  (_e: 'open', _reportId: string): void
}>()

const props = withDefaults(
  defineProps<{
    summary?: ModelAuditChannelSummary
    loading?: boolean
    error?: string
    hasStableId?: boolean
    compact?: boolean
  }>(),
  {
    summary: undefined,
    loading: false,
    error: '',
    hasStableId: true,
    compact: false
  }
)

const reportDetailLabel = computed(() => '查看模型审计详情')

const statusMetadata: Record<ModelAuditChannelSummary['primaryStatus'], { label: string; color: string }> = {
  not_detected: { label: '未检测', color: 'secondary' },
  running: { label: '检测中', color: 'info' },
  complete: { label: '结果完整', color: 'primary' },
  partial: { label: '部分结果', color: 'warning' },
  failed: { label: '运行失败', color: 'error' },
  unsupported: { label: '协议不支持', color: 'secondary' },
  insufficient_evidence: { label: '证据不足', color: 'warning' },
  stale: { label: '结果过期', color: 'warning' }
}

const identityLabels: Record<string, string> = {
  matched: '匹配',
  suspected_substitution: '疑似替换',
  suspected_mixture: '疑似混用',
  unknown: '未知',
  insufficient_evidence: '证据不足',
  unsupported: '不支持'
}

const capabilityStatusLabels: Record<string, string> = {
  complete: '正式',
  provisional: '临时结果',
  partial_budget: '预算中止',
  insufficient_evidence: '证据不足'
}

const statusMeta = computed(() => statusMetadata[props.summary?.primaryStatus ?? 'not_detected'])

const identityLabel = computed(() => {
  const identity = props.summary?.identity
  return identity ? identityLabels[identity.conclusion] || identity.conclusion : '未检测'
})

const identityTitle = computed(() => {
  const identity = props.summary?.identity
  if (!identity) return '尚无身份审计结果'
  return `${identityLabel.value}；一致性 ${formatPercent(identity.consistencyScore)}；样本 ${identity.sampleCount}`
})

const mixtureLabel = computed(() => {
  const components = props.summary?.identity?.mixture?.components
  if (!components?.length) return '未估计'
  return [...components]
    .sort((left, right) => right.proportion - left.proportion)
    .slice(0, 2)
    .map(component => `${component.candidate} ${formatPercent(component.proportion)}`)
    .join(' / ')
})

const capabilityLabel = computed(() => {
  const capability = props.summary?.capability
  if (!capability) return '未检测'
  if (capability.score !== undefined) return capability.score.toFixed(1)
  if (capability.index !== undefined) return capability.index.toFixed(1)
  return capabilityStatusLabels[capability.status] || capability.status
})

const capabilityDimensionLabels: Record<string, string> = {
  math_logic: '数学与逻辑',
  code: '代码',
  instruction_following: '指令遵循',
  tool_use: '工具使用',
  long_context_multiturn: '长上下文与多轮',
  knowledge_factuality: '知识与事实性',
  repeatability: '稳定性'
}

const capabilityDimensionLabel = computed(() => {
  const dimension = props.summary?.capability?.dimension
  return dimension ? capabilityDimensionLabels[dimension] || dimension : '综合能力'
})

const capabilityTitle = computed(() => {
  const capability = props.summary?.capability
  if (!capability) return '尚无能力审计结果'
  const interval = capability.indexInterval
    ? `；区间 ${capability.indexInterval.lower.toFixed(1)}-${capability.indexInterval.upper.toFixed(1)}`
    : ''
  return `${capabilityDimensionLabel.value} ${capabilityLabel.value}；${capabilityStatusLabels[capability.status] || capability.status}${interval}；覆盖 ${formatPercent(capability.coverage)}`
})

const reportedAtLabel = computed(() => formatAuditTime(props.summary?.reportedAt))
const reportedAtTitle = computed(() => {
  const summary = props.summary
  if (!summary?.reportedAt) return '尚无审计报告时间'
  const staleAt = summary.freshness?.staleAt ? `；过期边界 ${formatAuditTime(summary.freshness.staleAt)}` : ''
  return `报告时间 ${formatAuditTime(summary.reportedAt)}${staleAt}`
})

const formatPercent = (value: number): string => `${Math.round(value * 100)}%`

const formatAuditTime = (value?: string): string => {
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间无效'
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  })
}
</script>

<style scoped>
.audit-summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 12px;
  min-height: 28px;
  margin-top: 9px;
  padding-top: 8px;
  border-top: 1px solid rgba(var(--v-theme-outline), 0.24);
  color: rgba(var(--v-theme-on-surface), 0.72);
  font-size: 12px;
  line-height: 1.35;
}

.audit-summary--compact {
  gap: 4px 10px;
  min-height: 24px;
  margin-top: 5px;
  padding-top: 5px;
}

.audit-heading,
.audit-field,
.audit-unavailable {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  gap: 4px;
}

.audit-heading {
  color: rgb(var(--v-theme-primary));
  font-weight: 700;
  white-space: nowrap;
}

.audit-status {
  flex: 0 0 auto;
}

.audit-detail-button {
  flex: 0 0 auto;
  min-width: 44px;
  min-height: 44px;
}

.audit-field {
  white-space: nowrap;
}

.audit-label {
  color: rgba(var(--v-theme-on-surface), 0.48);
}

.audit-field strong {
  color: rgba(var(--v-theme-on-surface), 0.86);
  font-weight: 700;
}

.audit-detail {
  color: rgba(var(--v-theme-on-surface), 0.5);
}

.audit-ellipsis {
  display: inline-block;
  overflow: hidden;
  max-width: 190px;
  text-overflow: ellipsis;
  vertical-align: bottom;
}

.audit-field--samples strong,
.audit-field--time {
  font-variant-numeric: tabular-nums;
}

.audit-field--time {
  color: rgba(var(--v-theme-on-surface), 0.52);
}

.audit-stale,
.audit-unavailable--error {
  color: rgb(var(--v-theme-warning));
  font-weight: 700;
}

.audit-unavailable {
  color: rgba(var(--v-theme-on-surface), 0.48);
}

@media (max-width: 600px) {
  .audit-summary {
    gap: 5px 10px;
  }

  .audit-heading,
  .audit-status {
    flex: 0 0 auto;
  }

  .audit-field--time {
    flex-basis: 100%;
  }
}
</style>
