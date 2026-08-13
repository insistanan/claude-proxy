<template>
  <div class="audit-report-view">
    <header class="report-header">
      <v-btn icon="mdi-chevron-left" variant="text" aria-label="返回" title="返回" @click="goBack" />
      <div class="report-heading">
        <div class="report-eyebrow">{{ workloadKind === 'capability' ? '能力评测报告' : '模型审计报告' }}</div>
        <h1>{{ channelName }}</h1>
        <div v-if="report" class="report-id">{{ report.id }}</div>
      </div>
      <div v-if="detail" class="report-actions">
        <v-btn
          v-if="summary?.activeRunId"
          color="error"
          variant="tonal"
          prepend-icon="mdi-stop-circle"
          :loading="actionLoading === 'cancel'"
          :disabled="Boolean(actionLoading)"
          @click="cancelActiveRun"
        >取消当前运行</v-btn>
        <v-btn
          v-else
          color="primary"
          variant="tonal"
          prepend-icon="mdi-play-circle"
          :loading="actionLoading === 'run'"
          :disabled="Boolean(actionLoading)"
          @click="startRunAgain"
        >重新检查</v-btn>
        <v-btn
          color="secondary"
          variant="outlined"
          prepend-icon="mdi-content-save"
          :loading="actionLoading === 'export'"
          :disabled="Boolean(actionLoading)"
          @click="exportHTML"
        >导出 HTML</v-btn>
      </div>
    </header>

    <v-progress-linear v-if="loading" indeterminate color="primary" class="report-progress" />

    <v-alert v-if="error" type="error" variant="tonal" class="report-error">
      {{ error }}
      <template #append>
        <v-btn variant="text" @click="loadReport">重试</v-btn>
      </template>
    </v-alert>

    <template v-if="detail">
      <v-alert v-if="detail.run.status === 'failed'" type="error" variant="tonal" role="alert" aria-live="assertive" class="report-error">
        {{ detail.run.failureMessage || '本次检查未完成，后台没有返回具体原因。' }}
      </v-alert>
      <section class="report-overview" aria-label="报告摘要">
        <div class="overview-status">
          <v-chip :color="summaryStatus.color" variant="tonal">{{ summaryStatus.label }}</v-chip>
          <span :class="['freshness-label', { 'freshness-label--stale': summary?.freshness?.state === 'stale' }]">
            {{ freshnessLabel }}
          </span>
        </div>
        <dl class="overview-grid">
          <div><dt>渠道</dt><dd>{{ summary?.channelId }}</dd></div>
          <div><dt>协议</dt><dd>{{ targetProtocol }}</dd></div>
          <div><dt>解析模型</dt><dd>{{ resolvedModel }}</dd></div>
          <div><dt>思考档位</dt><dd>{{ report?.target.requested.thinking || '未设置' }}</dd></div>
          <div><dt>报告时间</dt><dd>{{ formatTime(report?.createdAt) }}</dd></div>
          <div><dt>样本</dt><dd>{{ report?.sampleCount ?? 0 }}</dd></div>
          <div><dt>身份置信度</dt><dd>{{ identity ? formatPercent(identity.confidence) : '--' }}</dd></div>
          <div><dt>能力覆盖率</dt><dd>{{ capability ? formatPercent(capability.coverage) : '--' }}</dd></div>
          <div><dt>请求用量</dt><dd>{{ formatNumber(report?.usage.requests) }}</dd></div>
          <div><dt>Token 用量</dt><dd>{{ formatNumber(report?.usage.totalTokens) }}</dd></div>
        </dl>
      </section>

      <v-tabs v-model="activeTab" color="primary" class="report-tabs" show-arrows>
        <v-tab v-if="identity || workloadKind === 'identity'" value="identity" prepend-icon="mdi-signature">身份与混用</v-tab>
        <v-tab v-if="capability || workloadKind === 'capability'" value="capability" prepend-icon="mdi-chart-areaspline">能力结果</v-tab>
        <v-tab value="evidence" prepend-icon="mdi-database">样本与证据</v-tab>
        <v-tab value="versions" prepend-icon="mdi-code-braces">配置与版本</v-tab>
      </v-tabs>

      <v-window v-model="activeTab" class="report-window">
        <v-window-item value="identity">
          <section class="report-section">
            <div v-if="identity" class="metric-strip">
              <div><span>结论</span><strong>{{ identityConclusionLabel(identity.conclusion) }}</strong></div>
              <div><span>正式结论</span><strong>{{ identity.formal ? '是' : '否' }}</strong></div>
              <div><span>一致性</span><strong>{{ formatPercent(identity.consistencyScore) }}</strong></div>
              <div><span>适用信号</span><strong>{{ identity.applicableSignals }}</strong></div>
              <div><span>相关组</span><strong>{{ identity.correlationGroups }}</strong></div>
              <div><span>候选</span><strong>{{ identity.candidateFit.bestCandidate || '未确定' }}</strong></div>
            </div>
            <div v-else class="empty-section">本报告没有身份审计结果</div>

            <template v-if="identity">
              <div class="section-heading">
                <h2>混用估计</h2>
                <span>{{ identity.mixture?.converged ? '已收敛' : '未形成估计' }}</span>
              </div>
              <div v-if="mixtureComponents.length" class="mixture-list">
                <div v-for="component in mixtureComponents" :key="component.candidate" class="mixture-row">
                  <div class="mixture-label">
                    <strong>{{ component.candidate }}</strong>
                    <span>{{ formatPercent(component.proportion) }}（{{ formatPercent(component.lower) }}-{{ formatPercent(component.upper) }}）</span>
                  </div>
                  <div class="score-track" aria-hidden="true">
                    <span :style="{ width: barWidth(component.proportion * 100) }" />
                  </div>
                </div>
              </div>
              <div v-else class="empty-inline">未生成混用比例</div>

              <div class="section-heading">
                <h2>身份信号</h2>
                <span>{{ identity.signals.length }} 项</span>
              </div>
              <div class="table-scroll">
                <v-table density="compact" class="audit-table">
                  <thead><tr><th>信号</th><th>状态</th><th>分数</th><th>可靠度</th><th>样本</th><th>基线</th><th>说明</th></tr></thead>
                  <tbody>
                    <tr v-for="signal in identity.signals" :key="signal.id">
                      <td><strong>{{ signalKindLabel(signal.kind) }}</strong><small>{{ signal.correlationGroup }}</small></td>
                      <td><v-chip size="x-small" :color="signalStatusColor(signal.status)" variant="tonal">{{ signalStatusLabel(signal.status) }}</v-chip></td>
                      <td>{{ signal.score === undefined ? '--' : formatPercent(signal.score) }}</td>
                      <td>{{ formatPercent(signal.reliability) }}</td>
                      <td>{{ signal.sampleCount }}</td>
                      <td class="mono-cell" :title="signal.baseline?.sha256">{{ signal.baseline ? `${signal.baseline.id}@${signal.baseline.version}` : '--' }}</td>
                      <td class="reason-cell">{{ signal.reason || '--' }}</td>
                    </tr>
                  </tbody>
                </v-table>
              </div>

              <div v-if="identity.qualification.reasons?.length" class="reason-band">
                <strong>正式资格限制</strong>
                <span v-for="reason in identity.qualification.reasons" :key="reason">{{ reason }}</span>
              </div>
            </template>
          </section>
        </v-window-item>

        <v-window-item value="capability">
          <section class="report-section">
            <div v-if="capability" class="metric-strip">
              <div><span>能力指数</span><strong>{{ capability.index?.toFixed(1) ?? '--' }}</strong></div>
              <div><span>报告状态</span><strong>{{ detail.run.status === 'failed' ? '评测未完成' : capabilityStatusLabel(capability.status) }}</strong></div>
              <div><span>正式维度</span><strong>{{ capability.formalDimensions }}/7</strong></div>
              <div><span>已判分</span><strong>{{ capability.counts.scored }}/{{ capability.counts.planned }}</strong></div>
              <div><span>覆盖率</span><strong>{{ formatPercent(capability.coverage) }}</strong></div>
            </div>
            <div v-else class="empty-section">本报告没有能力审计结果</div>

            <div v-if="capability" class="dimension-list">
              <div v-for="dimension in capability.dimensions" :key="dimension.dimension" class="dimension-row">
                <div class="dimension-heading">
                  <div>
                    <strong>{{ dimensionLabel(dimension.dimension) }}</strong>
                    <span>{{ dimension.formal ? '正式' : capabilityStatusLabel(dimension.score === undefined ? 'insufficient_evidence' : 'provisional') }}</span>
                  </div>
                  <div class="dimension-value">
                    <strong>{{ dimension.score?.toFixed(1) ?? '--' }}</strong>
                    <span v-if="dimension.interval">{{ dimension.interval.lower.toFixed(1) }}-{{ dimension.interval.upper.toFixed(1) }}</span>
                  </div>
                </div>
                <div class="score-track"><span :style="{ width: barWidth(dimension.score) }" /></div>
                <div class="dimension-meta">
                  <span>覆盖 {{ formatPercent(dimension.coverage) }}</span>
                  <span>判分 {{ dimension.counts.scored }}/{{ dimension.counts.planned }}</span>
                  <span v-if="dimension.counts.unsupported">不支持 {{ dimension.counts.unsupported }}</span>
                  <span v-if="dimension.counts.executionFailed">执行失败 {{ dimension.counts.executionFailed }}</span>
                  <span v-if="dimension.counts.scoringFailed">判分失败 {{ dimension.counts.scoringFailed }}</span>
                  <span v-if="dimension.counts.missing">未执行 {{ dimension.counts.missing }}</span>
                </div>
                <div v-if="dimension.reasons?.length" class="dimension-reasons" role="alert" aria-live="polite">
                  <span v-for="reason in dimension.reasons" :key="reason">{{ reason }}</span>
                </div>
              </div>
            </div>

            <div v-if="capability?.reasons?.length" class="reason-band">
              <strong>报告限制</strong>
              <span v-for="reason in capability.reasons" :key="reason">{{ reason }}</span>
            </div>
          </section>
        </v-window-item>

        <v-window-item value="evidence">
          <section class="report-section">
            <AuditEvidenceTable title="运行样本" :page="samples" />
            <v-pagination v-if="samplePageCount > 1" v-model="samplePage" :length="samplePageCount" density="compact" class="evidence-pagination" />

            <AuditEvidenceTable title="策略与判分结果" :page="strategyResults" class="strategy-evidence" />
            <v-pagination v-if="strategyPageCount > 1" v-model="strategyPage" :length="strategyPageCount" density="compact" class="evidence-pagination" />
          </section>
        </v-window-item>

        <v-window-item value="versions">
          <section class="report-section">
            <div class="version-grid">
              <div><span>详情 Schema</span><code>{{ versionLabel(detail.schema) }}</code></div>
              <div><span>报告 Schema</span><code>{{ versionLabel(report?.schema) }}</code></div>
              <div><span>任务 ID / 修订</span><code>{{ report?.jobId }} / {{ report?.jobRevision }}</code></div>
              <div><span>任务快照 SHA-256</span><code :title="report?.jobSnapshotSha256">{{ report?.jobSnapshotSha256 }}</code></div>
              <div v-if="identity"><span>身份聚合器</span><code>{{ versionLabel(identity.aggregator) }}</code></div>
              <div v-if="capability"><span>能力聚合器</span><code>{{ versionLabel(capability.aggregator) }}</code></div>
              <div v-if="capability"><span>任务包</span><code>{{ versionLabel(capability.package) }}</code></div>
              <div v-if="capability"><span>任务包 SHA-256</span><code :title="capability.packageSha256">{{ capability.packageSha256 }}</code></div>
              <div><span>请求轮廓</span><code>{{ report?.target.requested.requestProfile }}</code></div>
              <div><span>运行 ID</span><code>{{ detail.run.id }}</code></div>
              <div><span>触发方式 / 状态</span><code>{{ triggerLabel(detail.run.trigger) }} / {{ runStatusLabel(detail.run.status) }}</code></div>
              <div><span>新鲜度策略</span><code>{{ versionLabel(report?.freshnessPolicy.ref) }} / {{ formatDuration(report?.freshnessPolicy.maxAgeMs) }}</code></div>
            </div>

            <div v-if="baselineRows.length" class="section-heading"><h2>基线引用</h2><span>{{ baselineRows.length }} 项</span></div>
            <div v-if="baselineRows.length" class="table-scroll">
              <v-table density="compact" class="audit-table">
                <thead><tr><th>信号</th><th>基线</th><th>版本</th><th>SHA-256</th><th>兼容</th></tr></thead>
                <tbody>
                  <tr v-for="row in baselineRows" :key="row.signalId">
                    <td>{{ row.signalId }}</td><td>{{ row.baseline?.id || '--' }}</td><td>{{ row.baseline?.version || '--' }}</td>
                    <td class="mono-cell" :title="row.baseline?.sha256">{{ row.baseline?.sha256 || '--' }}</td>
                    <td>{{ row.compatible ? '是' : '否' }}</td>
                  </tr>
                </tbody>
              </v-table>
            </div>
          </section>
        </v-window-item>
      </v-window>
    </template>

    <v-snackbar v-model="showActionMessage" :color="actionMessageColor" timeout="4000">
      {{ actionMessage }}
    </v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type ModelAuditEvidencePage, type ModelAuditReportDetailResult, type ModelAuditSummaryStatus, type ModelAuditVersionedRef } from '@/services/api'
import AuditEvidenceTable from '@/components/AuditEvidenceTable.vue'

const route = useRoute()
const router = useRouter()
const result = shallowRef<ModelAuditReportDetailResult>()
const loading = ref(false)
const error = ref('')
const activeTab = ref('identity')
const samplePage = ref(1)
const strategyPage = ref(1)
const actionLoading = ref<'' | 'run' | 'cancel' | 'export'>('')
const actionMessage = ref('')
const actionMessageColor = ref<'success' | 'error'>('success')
const showActionMessage = ref(false)
let requestGeneration = 0

const reportId = computed(() => String(route.params.reportId || '').trim())
const detail = computed(() => result.value?.detail)
const workloadKind = computed(() => detail.value?.workloadKind || (route.name === 'evaluation-report' ? 'capability' : 'identity'))
const report = computed(() => detail.value?.report)
const summary = computed(() => detail.value?.summary)
const identity = computed(() => report.value?.identity)
const capability = computed(() => report.value?.capability)
const channelName = computed(() => report.value?.target.resolved?.channelName || summary.value?.channelId || (workloadKind.value === 'capability' ? '评测详情' : '审计详情'))
const resolvedModel = computed(() => report.value?.target.resolved?.resolvedModel || report.value?.target.requested.model || '未解析')
const targetProtocol = computed(() => {
  const requested = report.value?.target.requested.protocol
  const wire = report.value?.target.resolved?.wireProtocol
  return wire && wire !== requested ? `${requested} → ${wire}` : (requested || '--')
})
const mixtureComponents = computed(() => [...(identity.value?.mixture?.components || [])].sort((left, right) => right.proportion - left.proportion))
const baselineRows = computed(() => identity.value?.baselineChecks || [])
const emptyEvidencePage: ModelAuditEvidencePage = { evidence: [], total: 0, page: 1, pageSize: 10 }
const samples = computed(() => result.value?.samples || emptyEvidencePage)
const strategyResults = computed(() => result.value?.strategyResults || emptyEvidencePage)
const samplePageCount = computed(() => Math.max(1, Math.ceil((result.value?.samples.total || 0) / (result.value?.samples.pageSize || 10))))
const strategyPageCount = computed(() => Math.max(1, Math.ceil((result.value?.strategyResults.total || 0) / (result.value?.strategyResults.pageSize || 10))))

const summaryStatuses: Record<ModelAuditSummaryStatus, { label: string; color: string }> = {
  not_detected: { label: '未检测', color: 'secondary' }, running: { label: '检测中', color: 'info' },
  failed: { label: '运行失败', color: 'error' }, partial: { label: '部分结果', color: 'warning' },
  unsupported: { label: '协议不支持', color: 'secondary' }, insufficient_evidence: { label: '证据不足', color: 'warning' },
  stale: { label: '结果过期', color: 'warning' }, complete: { label: '结果完整', color: 'primary' },
}
const summaryStatus = computed(() => summaryStatuses[summary.value?.primaryStatus || 'not_detected'])
const freshnessLabel = computed(() => summary.value?.freshness?.state === 'stale' ? '已超过新鲜度阈值' : summary.value?.freshness ? '结果在有效期内' : '无报告时间')

const loadReport = async (): Promise<void> => {
  const generation = ++requestGeneration
  if (!reportId.value) {
    error.value = '报告 ID 不能为空'
    result.value = undefined
    return
  }
  loading.value = true
  error.value = ''
  try {
    const response = await api.getModelAuditReportDetail(reportId.value, samplePage.value, 10, strategyPage.value, 10)
    if (generation === requestGeneration) result.value = response.result
  } catch (caught) {
    if (generation === requestGeneration) {
      error.value = caught instanceof Error ? caught.message : '审计报告读取失败'
      result.value = undefined
    }
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

watch(() => [reportId.value, samplePage.value, strategyPage.value], () => { void loadReport() }, { immediate: true })
watch(workloadKind, (kind) => {
  if (kind === 'capability' && !identity.value) activeTab.value = 'capability'
  if (kind === 'identity' && !capability.value) activeTab.value = 'identity'
  const expectedRoute = kind === 'capability' ? 'evaluation-report' : 'audit-report'
  if (detail.value && route.name !== expectedRoute) {
    void router.replace({ name: expectedRoute, params: { reportId: reportId.value } })
  }
}, { immediate: true })

const goBack = (): void => {
  if (window.history.length > 1) { router.back(); return }
  void router.push({ name: workloadKind.value === 'capability' ? 'model-evaluation' : 'channels', params: workloadKind.value === 'capability' ? undefined : { type: summary.value?.channelKind || 'messages' } })
}

const showFeedback = (message: string, color: 'success' | 'error'): void => {
  actionMessage.value = message
  actionMessageColor.value = color
  showActionMessage.value = true
}

const startRunAgain = async (): Promise<void> => {
  if (!report.value) return
  actionLoading.value = 'run'
  try {
    const current = await api.getModelAuditJob(report.value.jobId)
    const response = current.job.schedule.oneShot || current.job.status === 'expired'
      ? await api.retryModelAuditJob(current.job.id, current.job.revision)
      : await api.startModelAuditJobRun(current.job.id, current.job.revision)
    showFeedback(`运行 ${response.run.id} 已提交`, 'success')
    await loadReport()
  } catch (caught) {
    showFeedback(caught instanceof Error ? caught.message : '运行提交失败', 'error')
  } finally {
    actionLoading.value = ''
  }
}

const cancelActiveRun = async (): Promise<void> => {
  const runId = summary.value?.activeRunId
  if (!runId) return
  actionLoading.value = 'cancel'
  try {
    await api.cancelModelAuditRun(runId)
    showFeedback('当前运行已取消', 'success')
    await loadReport()
  } catch (caught) {
    showFeedback(caught instanceof Error ? caught.message : '取消运行失败', 'error')
  } finally {
    actionLoading.value = ''
  }
}

const exportHTML = async (): Promise<void> => {
  if (!report.value) return
  actionLoading.value = 'export'
  try {
    const content = await api.exportModelAuditReportHTML(report.value.id)
    const url = URL.createObjectURL(new Blob([content], { type: 'text/html;charset=utf-8' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${report.value.id}.html`
    anchor.click()
    URL.revokeObjectURL(url)
    showFeedback('HTML 报告已导出', 'success')
  } catch (caught) {
    showFeedback(caught instanceof Error ? caught.message : 'HTML 报告导出失败', 'error')
  } finally {
    actionLoading.value = ''
  }
}

const formatPercent = (value: number): string => `${Math.round(value * 100)}%`
const formatNumber = (value?: number): string => new Intl.NumberFormat('zh-CN').format(value || 0)
const formatTime = (value?: string): string => value && !Number.isNaN(new Date(value).getTime()) ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '--'
const formatDuration = (milliseconds?: number): string => milliseconds === undefined ? '--' : milliseconds >= 86400000 ? `${Math.round(milliseconds / 86400000)} 天` : `${Math.round(milliseconds / 3600000)} 小时`
const barWidth = (value?: number): string => `${Math.max(0, Math.min(100, value || 0))}%`
const versionLabel = (ref?: ModelAuditVersionedRef): string => ref ? `${ref.id}@${ref.semanticVersion} (${ref.implementationVersion})` : '--'
const lookupLabel = (labels: Record<string, string>, value: string): string => labels[value] || value
const triggerLabel = (value: string): string => value === 'scheduled' ? '定时' : '手动'
const jobStatusLabel = (value: string): string => lookupLabel({ draft: '草稿', enabled: '已启用', running: '运行中', paused: '已暂停', expired: '已结束', deleted: '已删除' }, value)
const runStatusLabel = (value: string): string => lookupLabel({ pending: '等待中', running: '运行中', completed: '完成', partial: '部分完成', failed: '失败', cancelled: '已取消' }, value)
const capabilityStatusLabel = (value: string): string => lookupLabel({ complete: '正式', provisional: '临时结果', partial_budget: '预算中止', insufficient_evidence: '证据不足' }, value)
const identityConclusionLabel = (value: string): string => lookupLabel({ matched: '匹配', suspected_substitution: '疑似替换', suspected_mixture: '疑似混用', unknown: '未知', insufficient_evidence: '证据不足', unsupported: '不支持' }, value)
const signalStatusLabel = (value: string): string => lookupLabel({ observed: '已观察', skipped: '跳过', insufficient_evidence: '证据不足', unsupported: '不支持', failed: '失败' }, value)
const signalStatusColor = (value: string): string => ({ observed: 'primary', failed: 'error', unsupported: 'secondary', skipped: 'secondary', insufficient_evidence: 'warning' } as Record<string, string>)[value] || 'secondary'
const signalKindLabel = (value: string): string => lookupLabel({ juice_behavior: 'Juice 行为', rewrite_behavior: '改写行为', synthetic_coverage: '合成覆盖', probability_behavior: '概率行为', mode_difference: '模式差分', metadata_consistency: '元数据一致性', variation_stability: '变形稳定性' }, value)
const dimensionLabel = (value: string): string => lookupLabel({ math_logic: '数学与逻辑', code: '代码', instruction_following: '指令遵循', tool_use: '工具使用', long_context_multiturn: '长上下文与多轮', knowledge_factuality: '知识与事实性', repeatability: '重复稳定性' }, value)
</script>

<style scoped>
.audit-report-view { width: 100%; max-width: 1500px; margin: 0 auto; padding-bottom: 48px; }
.report-header { display: flex; align-items: center; gap: 12px; min-height: 76px; border-bottom: 2px solid rgba(var(--v-theme-outline), 0.55); }
.report-header > .v-btn { min-width: 44px; min-height: 44px; }
.report-heading { flex: 1; min-width: 0; }
.report-heading h1 { overflow: hidden; margin: 1px 0; font-size: 1.35rem; line-height: 1.25; text-overflow: ellipsis; white-space: nowrap; }
.report-eyebrow { color: rgb(var(--v-theme-primary)); font-size: 12px; font-weight: 700; }
.report-id { overflow: hidden; color: rgba(var(--v-theme-on-surface), 0.48); font-family: 'Fira Code', monospace; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.report-actions { display: flex; align-items: center; gap: 8px; }
.report-progress { margin-top: -2px; }
.report-error { margin: 20px 0; }
.report-overview { padding: 22px 0 18px; border-bottom: 1px solid rgba(var(--v-theme-outline), 0.42); }
.overview-status { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.freshness-label { color: rgba(var(--v-theme-on-surface), 0.55); font-size: 12px; }
.freshness-label--stale { color: rgb(var(--v-theme-warning)); font-weight: 700; }
.overview-grid { display: grid; grid-template-columns: repeat(5, minmax(120px, 1fr)); margin: 0; border: 1px solid rgba(var(--v-theme-outline), 0.35); }
.overview-grid > div { min-width: 0; padding: 12px 14px; border-right: 1px solid rgba(var(--v-theme-outline), 0.24); border-bottom: 1px solid rgba(var(--v-theme-outline), 0.24); }
.overview-grid dt, .version-grid span { color: rgba(var(--v-theme-on-surface), 0.5); font-size: 12px; }
.overview-grid dd { overflow: hidden; margin: 3px 0 0; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
.report-tabs { border-bottom: 1px solid rgba(var(--v-theme-outline), 0.35); }
.report-section { min-height: 320px; padding: 24px 0; }
.metric-strip { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); border: 1px solid rgba(var(--v-theme-outline), 0.35); }
.metric-strip > div { display: flex; flex-direction: column; gap: 4px; padding: 14px; border-right: 1px solid rgba(var(--v-theme-outline), 0.24); }
.metric-strip span { color: rgba(var(--v-theme-on-surface), 0.5); font-size: 12px; }
.metric-strip strong { font-size: 1rem; }
.section-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin: 26px 0 10px; }
.section-heading h2 { margin: 0; font-size: 1rem; }
.section-heading span { color: rgba(var(--v-theme-on-surface), 0.5); font-size: 12px; }
.mixture-list, .dimension-list { display: flex; flex-direction: column; gap: 12px; }
.mixture-row, .dimension-row { padding: 12px 0; border-bottom: 1px solid rgba(var(--v-theme-outline), 0.24); }
.mixture-label, .dimension-heading, .dimension-meta { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.mixture-label span, .dimension-heading span, .dimension-meta { color: rgba(var(--v-theme-on-surface), 0.52); font-size: 12px; }
.score-track { overflow: hidden; height: 7px; margin-top: 8px; background: rgba(var(--v-theme-outline), 0.2); border-radius: 2px; }
.score-track span { display: block; height: 100%; background: rgb(var(--v-theme-primary)); }
.dimension-heading > div { display: flex; align-items: baseline; gap: 8px; }
.dimension-value strong { font-size: 1.05rem; font-variant-numeric: tabular-nums; }
.dimension-meta { justify-content: flex-start; flex-wrap: wrap; margin-top: 7px; }
.dimension-reasons { display: flex; flex-direction: column; gap: 3px; margin-top: 8px; padding-left: 10px; border-left: 2px solid rgb(var(--v-theme-error)); color: rgb(var(--v-theme-error)); font-size: 12px; line-height: 1.5; }
.table-scroll { overflow-x: auto; border: 1px solid rgba(var(--v-theme-outline), 0.35); }
.audit-table { min-width: 860px; }
.audit-table th { color: rgba(var(--v-theme-on-surface), 0.55); font-size: 12px; }
.audit-table td { font-size: 13px; }
.audit-table td small { display: block; color: rgba(var(--v-theme-on-surface), 0.45); }
.mono-cell, .version-grid code, .evidence-row code { overflow: hidden; max-width: 260px; font-family: 'Fira Code', monospace; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.reason-cell { max-width: 300px; white-space: normal; }
.reason-band { display: flex; flex-direction: column; gap: 5px; margin-top: 22px; padding: 12px 14px; border-left: 3px solid rgb(var(--v-theme-warning)); background: rgba(var(--v-theme-warning), 0.07); font-size: 13px; }
.empty-section { display: grid; min-height: 260px; place-items: center; color: rgba(var(--v-theme-on-surface), 0.5); }
.empty-inline { padding: 18px 0; color: rgba(var(--v-theme-on-surface), 0.5); font-size: 13px; }
.strategy-evidence { margin-top: 30px; }
.evidence-pagination { margin-top: 12px; }
.version-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); border: 1px solid rgba(var(--v-theme-outline), 0.35); }
.version-grid > div { display: flex; flex-direction: column; min-width: 0; gap: 5px; padding: 13px 14px; border-right: 1px solid rgba(var(--v-theme-outline), 0.22); border-bottom: 1px solid rgba(var(--v-theme-outline), 0.22); }
.version-grid code { max-width: 100%; color: rgba(var(--v-theme-on-surface), 0.85); }
@media (max-width: 960px) { .overview-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .report-header { align-items: flex-start; flex-wrap: wrap; padding: 12px 0; } .report-actions { width: 100%; padding-left: 52px; } }
@media (max-width: 600px) { .report-actions { padding-left: 0; } .report-actions .v-btn { flex: 1; } .overview-grid, .version-grid { grid-template-columns: 1fr; } .overview-grid > div { padding: 10px 12px; } .dimension-heading { align-items: flex-start; } }
</style>
