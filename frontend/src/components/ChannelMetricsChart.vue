<template>
  <div class="channel-chart-container">
    <v-snackbar v-model="showError" color="error" :timeout="3000" location="top">
      {{ errorMessage }}
      <template #actions>
        <v-btn variant="text" @click="showError = false">关闭</v-btn>
      </template>
    </v-snackbar>

    <div class="chart-header d-flex align-center justify-space-between mb-3">
      <div class="d-flex align-center ga-2">
        <v-btn-toggle v-model="selectedDuration" mandatory density="compact" variant="outlined" divided :disabled="isLoading" color="primary">
          <v-btn value="1h" size="small">1h</v-btn>
          <v-btn value="6h" size="small">6h</v-btn>
          <v-btn value="24h" size="small">24h</v-btn>
        </v-btn-toggle>
        <v-btn icon size="small" variant="text" :loading="isLoading" :disabled="isLoading" @click="refreshData">
          <v-icon>mdi-refresh</v-icon>
        </v-btn>
      </div>
      <v-btn icon size="small" variant="text" title="收起" @click="$emit('close')">
        <v-icon>mdi-chevron-up</v-icon>
      </v-btn>
    </div>

    <!-- Loading -->
    <div v-if="isLoading" class="d-flex justify-center align-center" style="height: 200px">
      <v-progress-circular indeterminate size="24" color="primary" width="3" />
    </div>

    <!-- Empty -->
    <div v-else-if="!hasData" class="d-flex flex-column justify-center align-center text-medium-emphasis" style="height: 200px">
      <v-icon size="36" class="mb-2" :color="isDark ? 'grey-darken-1' : 'grey-lighten-1'">mdi-chart-line-variant</v-icon>
      <div class="text-caption">选定时间范围内没有请求记录</div>
    </div>

    <!-- Charts grid -->
    <div v-else class="charts-wrapper">
      <div class="chart-grid">
        <div class="chart-item">
          <div class="chart-item-title">
            <span class="title-dot" style="background: #3B82F6;"></span>
            请求数量
          </div>
          <apexchart type="area" height="120" :options="requestCountOptions" :series="requestCountSeries" />
        </div>
        <div class="chart-item">
          <div class="chart-item-title">
            <span class="title-dot" style="background: #10B981;"></span>
            成功率
          </div>
          <apexchart type="line" height="120" :options="successRateOptions" :series="successRateSeries" />
        </div>
        <div class="chart-item">
          <div class="chart-item-title">
            <span class="title-dot" style="background: #8B5CF6;"></span>
            Token 使用量
          </div>
          <apexchart type="area" height="120" :options="tokenUsageOptions" :series="tokenUsageSeries" />
        </div>
        <div class="chart-item">
          <div class="chart-item-title">
            <span class="title-dot" style="background: #10B981;"></span>
            缓存统计
          </div>
          <apexchart type="area" height="120" :options="cacheStatsOptions" :series="cacheStatsSeries" />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useTheme } from 'vuetify'
import VueApexCharts from 'vue3-apexcharts'
import type { ApexOptions } from 'apexcharts'
import { api, channelApiByType, type ChannelKeyMetricsHistoryResponse } from '../services/api'

const apexchart = VueApexCharts

const props = defineProps<{
  channelType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
  channelIndex: number
  channelName: string
}>()

const _emit = defineEmits<{ (_e: 'close'): void }>()

const theme = useTheme()
const isDark = computed(() => theme.global.current.value.dark)
const selectedDuration = ref<'1h' | '6h' | '24h'>('6h')
const isLoading = ref(false)
const keyHistoryData = ref<ChannelKeyMetricsHistoryResponse | null>(null)
const showError = ref(false)
const errorMessage = ref('')

const hasData = computed(() => {
  if (!keyHistoryData.value || !keyHistoryData.value.keys.length) return false
  return keyHistoryData.value.keys.some(key =>
    key.dataPoints.length > 0 && key.dataPoints.some(dp => dp.requestCount > 0)
  )
})

const tc = computed(() => ({
  grid: isDark.value ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)',
  text: isDark.value ? '#94A3B8' : '#64748B',
}))

const baseChartOptions = computed<ApexOptions>(() => ({
  chart: {
    toolbar: { show: false }, zoom: { enabled: false },
    background: 'transparent', fontFamily: 'inherit', sparkline: { enabled: false }
  },
  theme: { mode: isDark.value ? 'dark' : 'light' },
  grid: {
    borderColor: tc.value.grid, strokeDashArray: 4,
    padding: { left: 6, right: 6 },
    xaxis: { lines: { show: true } }, yaxis: { lines: { show: true } }
  },
  xaxis: {
    type: 'datetime',
    labels: {
      datetimeUTC: false, format: 'HH:mm',
      style: { fontSize: '10px', colors: tc.value.text }
    },
    axisBorder: { show: false }, axisTicks: { show: false }
  },
  yaxis: { labels: { style: { fontSize: '10px', colors: tc.value.text } } },
  tooltip: {
    theme: isDark.value ? 'dark' : 'light',
    x: { format: 'MM-dd HH:mm' },
    style: { fontSize: '11px', fontFamily: 'inherit' }
  },
  legend: { show: false },
  dataLabels: { enabled: false },
  stroke: { curve: 'smooth' as const, width: 2, lineCap: 'round' },
  fill: {
    type: 'gradient' as const,
    gradient: { shadeIntensity: 1, opacityFrom: 0.35, opacityTo: 0.05, stops: [0, 85, 100] }
  }
}))

const requestCountOptions = computed<ApexOptions>(() => ({
  ...baseChartOptions.value,
  colors: ['#3B82F6'],
  yaxis: {
    min: 0,
    labels: { formatter: (val: number) => Math.round(val).toString(), style: { fontSize: '10px', colors: tc.value.text } }
  }
}))

const successRateOptions = computed<ApexOptions>(() => ({
  ...baseChartOptions.value,
  colors: ['#10B981'],
  yaxis: {
    min: 0, max: 100,
    labels: { formatter: (val: number) => `${val.toFixed(0)}%`, style: { fontSize: '10px', colors: tc.value.text } }
  },
  markers: { size: 2, hover: { size: 4 } }
}))

const tokenUsageOptions = computed<ApexOptions>(() => ({
  ...baseChartOptions.value,
  colors: ['#8B5CF6', '#F97316'],
  stroke: { ...baseChartOptions.value.stroke, dashArray: [0, 5] as any },
  yaxis: {
    min: 0,
    labels: { formatter: (val: number) => {
      if (val >= 1000000) return `${(val / 1000000).toFixed(1)}M`
      if (val >= 1000) return `${(val / 1000).toFixed(1)}K`
      return Math.round(val).toString()
    }, style: { fontSize: '10px', colors: tc.value.text } }
  },
  tooltip: {
    ...baseChartOptions.value.tooltip,
    y: { formatter: (val: number) => val.toLocaleString() }
  }
}))

const cacheStatsOptions = computed<ApexOptions>(() => ({
  ...baseChartOptions.value,
  colors: ['#10B981', '#8B5CF6'],
  stroke: { ...baseChartOptions.value.stroke, dashArray: [0, 5] as any },
  yaxis: {
    min: 0,
    labels: { formatter: (val: number) => {
      if (val >= 1000000) return `${(val / 1000000).toFixed(1)}M`
      if (val >= 1000) return `${(val / 1000).toFixed(1)}K`
      return Math.round(val).toString()
    }, style: { fontSize: '10px', colors: tc.value.text } }
  },
  tooltip: {
    ...baseChartOptions.value.tooltip,
    y: { formatter: (val: number) => val.toLocaleString() }
  }
}))

const aggregatedData = computed(() => {
  if (!keyHistoryData.value || !keyHistoryData.value.keys.length) return []
  const timeMap = new Map<number, {
    timestamp: number; requestCount: number; successCount: number; failureCount: number
    inputTokens: number; outputTokens: number; cacheCreationTokens: number; cacheReadTokens: number
  }>()
  keyHistoryData.value.keys.forEach(key => {
    key.dataPoints.forEach(dp => {
      const time = new Date(dp.timestamp).getTime()
      const existing = timeMap.get(time) || {
        timestamp: time, requestCount: 0, successCount: 0, failureCount: 0,
        inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0
      }
      existing.requestCount += dp.requestCount; existing.successCount += dp.successCount
      existing.failureCount += dp.failureCount; existing.inputTokens += dp.inputTokens
      existing.outputTokens += dp.outputTokens; existing.cacheCreationTokens += dp.cacheCreationTokens
      existing.cacheReadTokens += dp.cacheReadTokens
      timeMap.set(time, existing)
    })
  })
  return Array.from(timeMap.values()).sort((a, b) => a.timestamp - b.timestamp)
})

const requestCountSeries = computed(() => {
  if (!aggregatedData.value.length) return []
  return [{ name: '请求数', data: aggregatedData.value.map(dp => ({ x: dp.timestamp, y: dp.requestCount })) }]
})

const successRateSeries = computed(() => {
  if (!aggregatedData.value.length) return []
  return [{
    name: '成功率',
    data: aggregatedData.value.filter(dp => dp.requestCount > 0).map(dp => ({
      x: dp.timestamp, y: (dp.successCount / dp.requestCount) * 100
    }))
  }]
})

const tokenUsageSeries = computed(() => {
  if (!aggregatedData.value.length) return []
  return [
    { name: '输入 Token', data: aggregatedData.value.map(dp => ({ x: dp.timestamp, y: dp.inputTokens })) },
    { name: '输出 Token', data: aggregatedData.value.map(dp => ({ x: dp.timestamp, y: dp.outputTokens })) }
  ]
})

const cacheStatsSeries = computed(() => {
  if (!aggregatedData.value.length) return []
  return [
    { name: '缓存读取', data: aggregatedData.value.map(dp => ({ x: dp.timestamp, y: dp.cacheReadTokens })) },
    { name: '缓存创建', data: aggregatedData.value.map(dp => ({ x: dp.timestamp, y: dp.cacheCreationTokens })) }
  ]
})

const refreshData = async () => {
  isLoading.value = true
  errorMessage.value = ''
  try {
    keyHistoryData.value = await channelApiByType(props.channelType).getChannelKeyMetricsHistory(props.channelIndex, selectedDuration.value)
  } catch (error) {
    console.error('Failed to fetch key metrics history:', error)
    errorMessage.value = error instanceof Error ? error.message : '获取历史数据失败'
    showError.value = true
    keyHistoryData.value = null
  } finally { isLoading.value = false }
}

watch(selectedDuration, () => refreshData())
watch(() => props.channelIndex, () => refreshData())
watch(() => props.channelType, () => refreshData())

onMounted(() => refreshData())
defineExpose({ refreshData })
</script>

<style scoped>
.channel-chart-container {
  padding: 16px 20px;
  background: rgb(var(--v-theme-surface));
  border-top: 1px solid rgba(var(--v-theme-outline), 0.15);
}

.chart-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.chart-item {
  min-width: 0;
}

.chart-item-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 600;
  color: rgba(var(--v-theme-on-surface), 0.6);
  margin-bottom: 4px;
}

.title-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

@media (max-width: 800px) {
  .chart-grid {
    grid-template-columns: 1fr;
    gap: 14px;
  }
}
</style>