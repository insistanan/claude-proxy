<template>
  <div class="evidence-block">
    <div class="section-heading">
      <h2>{{ title }}</h2>
      <span>{{ page.total }} 项</span>
    </div>
    <div v-if="page.evidence.length" class="evidence-list">
      <div v-for="item in page.evidence" :key="item.id" class="evidence-row">
        <div>
          <strong>{{ item.id }}</strong>
          <small>{{ item.kind === 'sample' ? '运行样本' : '策略/判分结果' }}</small>
        </div>
        <code :title="item.sha256">{{ item.sha256 }}</code>
        <span>{{ formatTime(item.createdAt) }}</span>
        <span>{{ versionLabel(item.schema) }}</span>
      </div>
    </div>
    <div v-else class="empty-inline">当前页没有证据元数据</div>
  </div>
</template>

<script setup lang="ts">
import type { ModelAuditEvidencePage, ModelAuditVersionedRef } from '@/services/api'

defineProps<{
  title: string
  page: ModelAuditEvidencePage
}>()

const formatTime = (value: string): string => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString('zh-CN', { hour12: false })
}

const versionLabel = (ref: ModelAuditVersionedRef): string => `${ref.id}@${ref.semanticVersion} (${ref.implementationVersion})`
</script>

<style scoped>
.section-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin: 26px 0 10px; }
.section-heading h2 { margin: 0; font-size: 1rem; }
.section-heading span { color: rgba(var(--v-theme-on-surface), 0.5); font-size: 12px; }
.evidence-list { border: 1px solid rgba(var(--v-theme-outline), 0.35); }
.evidence-row { display: grid; grid-template-columns: minmax(180px, 1.2fr) minmax(180px, 1fr) 150px minmax(180px, 1fr); align-items: center; gap: 12px; padding: 11px 14px; border-bottom: 1px solid rgba(var(--v-theme-outline), 0.22); font-size: 12px; }
.evidence-row:last-child { border-bottom: 0; }
.evidence-row > div { min-width: 0; }
.evidence-row strong, .evidence-row small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.evidence-row small { color: rgba(var(--v-theme-on-surface), 0.48); }
.evidence-row code { overflow: hidden; max-width: 260px; font-family: 'Fira Code', monospace; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.empty-inline { padding: 18px 0; color: rgba(var(--v-theme-on-surface), 0.5); font-size: 13px; }
@media (max-width: 960px) { .evidence-row { grid-template-columns: minmax(150px, 1fr) minmax(150px, 1fr); } }
@media (max-width: 600px) { .evidence-row { grid-template-columns: 1fr; } }
</style>
