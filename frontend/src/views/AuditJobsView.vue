<template>
  <div class="audit-jobs-view">
    <div class="audit-page-header">
      <div>
        <h1 class="text-h5 font-weight-bold mb-1">{{ evaluationMode ? '能力评测' : '模型审计' }}</h1>
        <div class="text-body-2 text-medium-emphasis">
          {{
            activeView === 'jobs'
              ? evaluationMode
                ? `共 ${totalJobs} 条评测记录`
                : `共 ${totalJobs} 个任务`
              : evaluationMode
                ? `已加载 ${questionBanks.length} 个题库`
                : `已加载 ${mods.length} 个探针 Mod`
          }}
        </div>
      </div>
      <div class="audit-page-actions">
        <v-btn
          variant="text"
          prepend-icon="mdi-refresh"
          :loading="activeView === 'jobs' ? loading : evaluationMode ? questionBanksLoading : modsLoading"
          :disabled="saving"
          @click="activeView === 'jobs' ? loadJobs() : evaluationMode ? reloadQuestionBanks() : reloadMods()"
        >
          刷新
        </v-btn>
        <v-btn
          v-if="activeView === 'mods' && !evaluationMode"
          variant="outlined"
          prepend-icon="mdi-folder-arrow-down"
          :disabled="modsLoading"
          @click="openImportModDialog"
        >
          导入目录
        </v-btn>
        <v-btn
          v-if="activeView === 'jobs' || !evaluationMode"
          color="primary"
          prepend-icon="mdi-plus"
          :disabled="activeView === 'jobs' ? catalogLoading || !catalogReady : evaluationMode || modsLoading"
          @click="activeView === 'jobs' ? openCreateDialog() : openCreateModDialog()"
        >
          {{ activeView === 'jobs' ? (evaluationMode ? '开始评测' : '新建任务') : '新建 Mod' }}
        </v-btn>
      </div>
    </div>

    <v-tabs
      v-model="activeView"
      color="primary"
      class="audit-view-tabs"
      :aria-label="evaluationMode ? '能力评测页面' : '模型审计页面'"
    >
      <v-tab value="jobs" prepend-icon="mdi-format-list-bulleted">{{ evaluationMode ? '评测记录' : '审计任务' }}</v-tab>
      <v-tab value="mods" :prepend-icon="evaluationMode ? 'mdi-database' : 'mdi-puzzle-outline'">{{
        evaluationMode ? '知识题库' : '探针库'
      }}</v-tab>
    </v-tabs>

    <v-alert v-if="pageError" type="error" variant="tonal" closable class="mb-4" @click:close="pageError = ''">
      {{ pageError }}
    </v-alert>
    <v-alert v-else-if="catalogNotice" type="warning" variant="tonal" class="mb-4">
      {{ catalogNotice }}
    </v-alert>

    <template v-if="activeView === 'jobs'">
      <div :class="['audit-list', { 'audit-list--evaluation': evaluationMode }]" :aria-busy="loading">
        <div class="audit-list-header" aria-hidden="true">
          <span>{{ evaluationMode ? '评测' : '任务' }}</span>
          <span>{{ evaluationMode ? '能力' : '工作负载' }}</span>
          <span>目标</span>
          <span v-if="evaluationMode">评分</span>
          <span v-if="!evaluationMode">运行方式</span>
          <span v-if="!evaluationMode">预算</span>
          <span class="text-right">操作</span>
        </div>

        <v-skeleton-loader v-if="loading && jobs.length === 0" type="list-item-three-line@4" />

        <div v-else-if="jobs.length === 0" class="audit-empty-state">
          <v-icon size="42" color="medium-emphasis">mdi-test-tube</v-icon>
          <div class="text-subtitle-1 font-weight-medium mt-3">暂无{{ evaluationMode ? '评测记录' : '审计任务' }}</div>
          <v-btn
            color="primary"
            variant="tonal"
            class="mt-4"
            prepend-icon="mdi-plus"
            :disabled="catalogLoading || !catalogReady"
            @click="openCreateDialog"
          >
            {{ evaluationMode ? '开始首次评测' : '创建首个任务' }}
          </v-btn>
        </div>

        <div v-for="job in jobs" :key="job.id" class="audit-list-row">
          <div class="audit-job-main" data-label="任务">
            <div class="audit-job-title-line">
              <span class="font-weight-semibold audit-job-name">{{ job.name }}</span>
              <v-chip :color="statusMeta(job.status).color" size="small" variant="tonal">
                {{ statusMeta(job.status).label }}
              </v-chip>
            </div>
            <div class="text-caption text-medium-emphasis mt-1">{{ job.id }} · r{{ job.revision }}</div>
          </div>

          <div data-label="工作负载">
            <div class="text-body-2 font-weight-medium">{{ workloadLabel(job) }}</div>
            <div class="text-caption text-medium-emphasis mt-1">{{ workloadDetail(job) }}</div>
          </div>

          <div data-label="目标">
            <div class="text-body-2">{{ job.targets.length }} 个渠道</div>
            <div class="text-caption text-medium-emphasis mt-1 audit-target-summary">{{ targetSummary(job) }}</div>
          </div>

          <div v-if="evaluationMode" class="evaluation-score-cell" data-label="评分">
            <template v-if="evaluationScore(job) !== null">
              <div class="evaluation-score-value">{{ evaluationScore(job)?.toFixed(1) }}</div>
              <div class="text-caption text-medium-emphasis">满分 100</div>
            </template>
            <template v-else>
              <v-chip size="small" variant="tonal" :color="evaluationResultMeta(job).color">
                {{ evaluationResultMeta(job).label }}
              </v-chip>
            </template>
          </div>

          <div v-if="!evaluationMode" data-label="运行方式">
            <div class="text-body-2 font-weight-medium">
              {{
                job.schedule.oneShot
                  ? evaluationMode
                    ? '单次评测'
                    : '单次检查'
                  : formatInterval(job.schedule.intervalMs)
              }}
            </div>
            <div class="text-caption text-medium-emphasis mt-1">
              {{ job.schedule.oneShot ? '执行一次' : `${formatDateTime(job.schedule.startAt)} 起` }}
            </div>
          </div>

          <div v-if="!evaluationMode" data-label="预算">
            <div class="text-body-2">{{ job.budget.maxRequests }} 请求</div>
            <div class="text-caption text-medium-emphasis mt-1">
              {{ formatCompactNumber(job.budget.maxTotalTokens) }} tokens
            </div>
          </div>

          <div class="audit-row-actions" data-label="操作">
            <v-btn
              v-if="evaluationReportId(job)"
              prepend-icon="mdi-open-in-new"
              color="primary"
              variant="tonal"
              size="small"
              :aria-label="`查看 ${job.name} 的评测结果`"
              @click="openEvaluationResult(evaluationReportId(job))"
            >
              查看结果
            </v-btn>

            <v-tooltip v-if="!evaluationMode" text="立即运行" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-play-circle"
                  variant="text"
                  class="audit-icon-btn"
                  :loading="actionKey === `${job.id}:run`"
                  :disabled="job.status !== 'enabled' || actionBusy"
                  :aria-label="`立即运行 ${job.name}`"
                  @click="startRun(job)"
                />
              </template>
            </v-tooltip>

            <v-tooltip text="运行记录" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-format-list-bulleted"
                  variant="text"
                  class="audit-icon-btn"
                  :disabled="actionBusy"
                  :aria-label="`查看 ${job.name} 的运行记录`"
                  @click="openRunsDialog(job)"
                />
              </template>
            </v-tooltip>

            <v-tooltip v-if="!evaluationMode" text="编辑" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-pencil"
                  variant="text"
                  class="audit-icon-btn"
                  :disabled="!canEdit(job) || actionBusy"
                  :aria-label="`编辑 ${job.name}`"
                  @click="openEditDialog(job)"
                />
              </template>
            </v-tooltip>

            <v-tooltip v-if="!evaluationMode" :text="job.status === 'enabled' ? '暂停' : '启用'" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  :icon="job.status === 'enabled' ? 'mdi-pause-circle' : 'mdi-check-circle'"
                  variant="text"
                  class="audit-icon-btn"
                  :color="job.status === 'enabled' ? 'warning' : 'success'"
                  :loading="actionKey === `${job.id}:transition`"
                  :disabled="!canToggle(job) || actionBusy"
                  :aria-label="`${job.status === 'enabled' ? '暂停' : '启用'} ${job.name}`"
                  @click="toggleJob(job)"
                />
              </template>
            </v-tooltip>

            <v-tooltip text="删除" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-delete"
                  color="error"
                  variant="text"
                  class="audit-icon-btn"
                  :disabled="job.status === 'running' || actionBusy"
                  :aria-label="`删除 ${job.name}`"
                  @click="confirmDelete(job)"
                />
              </template>
            </v-tooltip>
          </div>
        </div>
      </div>

      <div v-if="pageCount > 1" class="d-flex justify-center mt-5">
        <v-pagination v-model="page" :length="pageCount" :disabled="loading" @update:model-value="loadJobs" />
      </div>
    </template>

    <template v-else-if="evaluationMode">
      <v-alert v-if="questionBankIssues.length" type="warning" variant="tonal" class="mb-4">
        <div class="font-weight-medium mb-1">有 {{ questionBankIssues.length }} 个题库目录未能加载</div>
        <div v-for="issue in questionBankIssues" :key="`${issue.directory}:${issue.message}`" class="text-body-2">
          {{ issue.directory }}：{{ issue.message }}
        </div>
      </v-alert>
      <div class="audit-mod-list" :aria-busy="questionBanksLoading">
        <div class="audit-mod-list-header" aria-hidden="true">
          <span>题库</span><span>题目数</span><span>版本</span><span>源目录</span><span></span>
        </div>
        <v-skeleton-loader v-if="questionBanksLoading && questionBanks.length === 0" type="list-item-three-line@3" />
        <div v-else-if="questionBanks.length === 0" class="audit-empty-state">
          <v-icon size="42" color="medium-emphasis">mdi-database</v-icon>
          <div class="text-subtitle-1 font-weight-medium mt-3">暂无可用题库</div>
        </div>
        <div v-for="bank in questionBanks" :key="bank.contentSha256" class="audit-mod-row">
          <div>
            <div class="font-weight-medium">{{ bank.name }}</div>
            <div class="text-caption text-medium-emphasis mt-1">{{ bank.id }}</div>
            <div class="text-body-2 text-medium-emphasis mt-1">{{ bank.description }}</div>
          </div>
          <div class="text-body-2">{{ bank.questionCount }} 道</div>
          <div class="text-body-2">v{{ bank.version }}</div>
          <div class="text-caption audit-path-text">{{ bank.sourcePath }}</div>
          <div></div>
        </div>
      </div>
    </template>

    <template v-else>
      <v-alert v-if="modIssues.length" type="warning" variant="tonal" class="mb-4">
        <div class="font-weight-medium mb-1">有 {{ modIssues.length }} 个目录未能加载</div>
        <div v-for="issue in modIssues" :key="`${issue.directory}:${issue.message}`" class="text-body-2">
          {{ issue.directory }}：{{ issue.message }}
        </div>
      </v-alert>

      <div class="audit-mod-list" :aria-busy="modsLoading">
        <div class="audit-mod-list-header" aria-hidden="true">
          <span>探针</span><span>分析方式</span><span>版本</span><span>源目录</span><span class="text-right">操作</span>
        </div>
        <v-skeleton-loader v-if="modsLoading && mods.length === 0" type="list-item-three-line@3" />
        <div v-else-if="mods.length === 0" class="audit-empty-state">
          <v-icon size="42" color="medium-emphasis">mdi-puzzle-outline</v-icon>
          <div class="text-subtitle-1 font-weight-medium mt-3">暂无探针 Mod</div>
          <div class="text-body-2 text-medium-emphasis mt-1">从案例创建，或导入本机已有目录</div>
        </div>
        <div v-for="mod in mods" :key="mod.reference.contentSha256" class="audit-mod-row">
          <div>
            <div class="font-weight-medium">{{ mod.name }}</div>
            <div class="text-caption text-medium-emphasis mt-1">{{ mod.reference.id }}</div>
            <div class="text-body-2 text-medium-emphasis mt-1">{{ mod.description }}</div>
          </div>
          <div>
            <v-chip size="small" variant="tonal" :color="analysisModeMeta(mod.analysisMode).color">{{
              analysisModeMeta(mod.analysisMode).label
            }}</v-chip>
          </div>
          <div class="text-body-2">v{{ mod.reference.version }}</div>
          <div class="text-caption audit-path-text">{{ mod.sourcePath }}</div>
          <div class="audit-row-actions">
            <v-tooltip text="复制源目录" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-content-copy"
                  variant="text"
                  class="audit-icon-btn"
                  aria-label="复制源目录"
                  @click="copyText(mod.sourcePath, '源目录已复制')"
                />
              </template>
            </v-tooltip>
            <v-tooltip text="编辑 Mod" location="bottom">
              <template #activator="{ props }">
                <v-btn
                  v-bind="props"
                  icon="mdi-pencil"
                  variant="text"
                  class="audit-icon-btn"
                  aria-label="编辑 Mod"
                  @click="openEditModDialog(mod)"
                />
              </template>
            </v-tooltip>
          </div>
        </div>
      </div>
    </template>

    <v-dialog v-model="formDialog" max-width="1040" persistent scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>{{ evaluationMode ? '开始能力评测' : editingJob ? '编辑审计任务' : '新建审计任务' }}</span>
          <v-btn
            icon="mdi-close"
            variant="text"
            class="audit-icon-btn"
            aria-label="关闭"
            :disabled="saving"
            @click="closeFormDialog"
          />
        </v-card-title>
        <v-divider />

        <v-card-text class="audit-form-body">
          <v-alert v-if="formError" type="error" variant="tonal" class="mb-5">{{ formError }}</v-alert>

          <template v-if="evaluationMode">
            <section class="evaluation-form">
              <v-select
                v-model="form.capabilityPresetKey"
                label="测试能力"
                :items="capabilityEvaluationItems"
                variant="outlined"
                density="comfortable"
                :disabled="capabilityEvaluationOptions.length === 0"
              />

              <v-select
                :model-value="form.channelKeys[0] || ''"
                label="渠道"
                :items="channelSelectItems"
                variant="outlined"
                density="comfortable"
                :loading="channelLoading"
                :disabled="channelLoading"
                @update:model-value="selectEvaluationChannel"
              />

              <v-combobox
                v-if="selectedChannelOptions[0]"
                v-model="targetSetting(selectedChannelOptions[0].key).model"
                label="模型"
                :items="targetModelDiscovery(selectedChannelOptions[0].key).models"
                item-title="title"
                item-value="value"
                :return-object="false"
                :loading="targetModelDiscovery(selectedChannelOptions[0].key).loading"
                variant="outlined"
                density="comfortable"
                clearable
              >
                <template #no-data>
                  <div class="pa-3 text-caption text-medium-emphasis">未找到模型，可直接输入模型名称</div>
                </template>
              </v-combobox>

              <div v-if="selectedCapabilityEvaluation" class="evaluation-summary" aria-live="polite">
                <div>
                  <span>题库</span>
                  <strong>{{ selectedCapabilityEvaluation.bankName }}</strong>
                </div>
                <div>
                  <span>题目</span>
                  <strong>{{ selectedCapabilityEvaluation.questionCount }} 道</strong>
                </div>
                <div>
                  <span>完成后</span>
                  <strong>记录到该渠道与模型</strong>
                </div>
              </div>
            </section>
          </template>

          <template v-else>
            <section class="audit-form-section">
              <div class="audit-section-heading">运行方式</div>
              <v-btn-toggle
                v-model="form.executionMode"
                mandatory
                color="primary"
                variant="outlined"
                class="audit-mode-toggle"
                aria-label="运行方式"
                :disabled="Boolean(editingJob)"
              >
                <v-btn value="once" prepend-icon="mdi-play-circle">{{
                  evaluationMode ? '单次评测' : '单次检查'
                }}</v-btn>
                <v-btn value="continuous" prepend-icon="mdi-clock-outline">持续任务</v-btn>
              </v-btn-toggle>
            </section>

            <v-divider class="my-5" />

            <section class="audit-form-section">
              <div class="audit-section-heading">基本信息</div>
              <div class="audit-form-grid audit-form-grid--basic">
                <v-text-field
                  v-model="form.name"
                  label="任务名称"
                  variant="outlined"
                  density="comfortable"
                  maxlength="120"
                  counter
                  required
                />
                <v-select
                  v-if="!editingJob && !isSingleRun"
                  v-model="form.initialStatus"
                  label="创建后状态"
                  :items="initialStatusOptions"
                  variant="outlined"
                  density="comfortable"
                />
                <v-text-field
                  v-else-if="editingJob"
                  :model-value="statusMeta(editingJob.status).label"
                  label="当前状态"
                  variant="outlined"
                  density="comfortable"
                  readonly
                />
              </div>
            </section>

            <v-divider class="my-5" />

            <section class="audit-form-section">
              <div class="audit-section-heading-row">
                <div class="audit-section-heading">{{ evaluationMode ? '评测内容' : '审计内容' }}</div>
                <v-chip size="small" variant="tonal" color="primary">
                  {{ form.workloadKind === 'identity' ? identityPresetSummary : capabilityPresetRunSummary }}
                </v-chip>
              </div>
              <v-btn-toggle
                v-if="!evaluationMode"
                v-model="form.workloadKind"
                mandatory
                color="primary"
                variant="outlined"
                class="audit-workload-toggle mb-4"
                aria-label="审计内容类型"
              >
                <v-btn value="identity" prepend-icon="mdi-shield-refresh">身份与混用</v-btn>
                <v-btn value="capability" prepend-icon="mdi-speedometer">通用能力</v-btn>
              </v-btn-toggle>

              <div v-if="form.workloadKind === 'identity'" class="audit-identity-section">
                <div class="audit-preset-selector mb-4">
                  <div class="text-body-2 font-weight-bold mb-2">选择检测档位</div>
                  <v-btn-toggle
                    v-model="form.identityPresetLevel"
                    mandatory
                    color="primary"
                    variant="outlined"
                    class="audit-preset-toggle"
                    aria-label="身份审计档位"
                    @update:model-value="applyIdentityPreset"
                  >
                    <v-btn value="low">
                      <div class="preset-btn-content">
                        <span class="preset-btn-title">低档</span>
                        <span class="preset-btn-subtitle"
                          >约 {{ identityPresetByLevel('low')?.estimateRequests ?? '—' }} 请求</span
                        >
                      </div>
                    </v-btn>
                    <v-btn value="medium">
                      <div class="preset-btn-content">
                        <span class="preset-btn-title">中档</span>
                        <span class="preset-btn-subtitle"
                          >约 {{ identityPresetByLevel('medium')?.estimateRequests ?? '—' }} 请求</span
                        >
                      </div>
                    </v-btn>
                    <v-btn value="high">
                      <div class="preset-btn-content">
                        <span class="preset-btn-title">高档</span>
                        <span class="preset-btn-subtitle"
                          >约 {{ identityPresetByLevel('high')?.estimateRequests ?? '—' }} 请求</span
                        >
                      </div>
                    </v-btn>
                    <v-btn value="custom">
                      <div class="preset-btn-content">
                        <span class="preset-btn-title">自定义</span>
                        <span class="preset-btn-subtitle">手动配置</span>
                      </div>
                    </v-btn>
                  </v-btn-toggle>
                  <div v-if="form.identityPresetLevel !== 'custom'" class="preset-description">
                    {{ identityPresetDescription }}
                  </div>
                  <v-alert v-else type="info" variant="tonal" density="compact" class="mt-3">
                    自定义档位的测试结果仅供参考，不生成正式身份结论
                  </v-alert>
                </div>

                <div class="audit-strategy-list">
                  <div v-for="strategy in strategies" :key="strategy.ref.id" class="audit-strategy-row">
                    <div class="audit-strategy-checkbox">
                      <v-checkbox
                        :model-value="strategyState(strategy.ref.id).enabled"
                        color="primary"
                        hide-details
                        density="compact"
                        :aria-label="`启用 ${strategy.ref.id}`"
                        @update:model-value="setStrategyEnabled(strategy.ref.id, Boolean($event))"
                      />
                    </div>
                    <div class="audit-strategy-content">
                      <div class="d-flex flex-wrap align-center ga-2 mb-1">
                        <span class="text-body-2 font-weight-bold">{{ strategyName(strategy.ref.id) }}</span>
                        <v-chip v-if="strategy.formalEligible" size="x-small" color="success" variant="tonal"
                          >正式资格</v-chip
                        >
                        <v-chip v-if="strategy.applicability?.models?.length" size="x-small" variant="outlined"
                          >Sol 专项</v-chip
                        >
                        <v-chip
                          v-if="strategyModMeta(strategy.ref.id)"
                          size="x-small"
                          :color="strategyModMeta(strategy.ref.id)?.color"
                          variant="tonal"
                        >
                          Mod · {{ strategyModMeta(strategy.ref.id)?.label }}
                        </v-chip>
                      </div>
                      <div class="text-caption text-medium-emphasis">{{ strategy.description }}</div>
                    </div>
                    <div class="audit-strategy-controls">
                      <v-text-field
                        v-if="sampleCountProperty(strategy)"
                        :model-value="strategySampleCount(strategy.ref.id)"
                        label="样本数"
                        type="number"
                        variant="outlined"
                        density="compact"
                        :error-messages="strategySampleCountError(strategy)"
                        :hint="`范围 ${sampleCountProperty(strategy)?.minimum ?? 1}–${sampleCountProperty(strategy)?.maximum ?? strategy.maximumSamples}，步长 ${sampleCountProperty(strategy)?.multipleOf || 1}`"
                        persistent-hint
                        :min="sampleCountProperty(strategy)?.minimum"
                        :max="sampleCountProperty(strategy)?.maximum"
                        :step="sampleCountProperty(strategy)?.multipleOf || 1"
                        :disabled="!strategyState(strategy.ref.id).enabled"
                        @update:model-value="setStrategySampleCount(strategy.ref.id, $event)"
                      />
                      <v-text-field
                        v-if="sampleIntervalProperty(strategy)"
                        :model-value="strategySampleIntervalSeconds(strategy.ref.id)"
                        label="请求间隔（秒）"
                        type="number"
                        variant="outlined"
                        density="compact"
                        hide-details
                        min="0"
                        :max="Number(sampleIntervalProperty(strategy)?.maximum || 3600000) / 1000"
                        :disabled="!strategyState(strategy.ref.id).enabled"
                        @update:model-value="setStrategySampleIntervalSeconds(strategy.ref.id, $event)"
                      />
                    </div>
                  </div>
                </div>

                <div v-if="needsAnalyzer" class="audit-analyzer-section">
                  <div>
                    <div class="text-body-2 font-weight-medium">AI 分析模型</div>
                    <div class="text-caption text-medium-emphasis mt-1">
                      只用于解释当前运行的 Mod 数据，不会写入 Mod，也不会向脚本传递密钥。
                    </div>
                  </div>
                  <div class="audit-form-grid audit-form-grid--three mt-4">
                    <v-select
                      v-model="form.analyzerChannelKey"
                      label="分析渠道"
                      :items="analyzerChannelItems"
                      variant="outlined"
                      density="comfortable"
                    />
                    <v-combobox
                      v-model="form.analyzerModel"
                      label="分析模型"
                      :items="analyzerModelItems"
                      item-title="title"
                      item-value="value"
                      :return-object="false"
                      variant="outlined"
                      density="comfortable"
                      :loading="analyzerDiscovery?.loading"
                    />
                    <v-select
                      v-model="form.analyzerThinking"
                      label="分析思考档位"
                      :items="analyzerThinkingOptions"
                      variant="outlined"
                      density="comfortable"
                    />
                  </div>
                </div>
              </div>

              <div v-else>
                <v-select
                  v-model="form.capabilityPresetKey"
                  label="能力预设"
                  :items="capabilityPresetOptions"
                  variant="outlined"
                  density="comfortable"
                  :disabled="capabilityPresets.length === 0"
                />
                <div v-if="selectedCapabilityPreset" class="audit-preset-summary">
                  <span>{{ selectedCapabilityPreset.tasks.length }} 个任务配置</span>
                  <span>最多 {{ selectedCapabilityPreset.limits.requests }} 个请求</span>
                  <span>{{ selectedCapabilityPreset.formalEligible ? '可生成正式结果' : '仅预览结果' }}</span>
                </div>
              </div>
            </section>

            <v-divider class="my-5" />

            <section class="audit-form-section">
              <div class="audit-section-heading">目标渠道</div>
              <v-autocomplete
                v-model="form.channelKeys"
                label="选择一个或多个渠道"
                :items="channelSelectItems"
                multiple
                chips
                closable-chips
                clearable
                variant="outlined"
                density="comfortable"
                :loading="channelLoading"
                :disabled="channelLoading"
              />

              <div v-if="selectedChannelOptions.length" class="audit-target-editor">
                <div v-for="option in selectedChannelOptions" :key="option.key" class="audit-target-row">
                  <div class="audit-target-identity">
                    <v-chip size="x-small" variant="outlined">{{ protocolLabel(option.kind) }}</v-chip>
                    <span class="text-body-2 font-weight-medium">{{ option.channel.name }}</span>
                    <span class="text-caption text-medium-emphasis">{{ option.channel.id }}</span>
                  </div>
                  <div class="audit-target-model">
                    <v-combobox
                      v-model="targetSetting(option.key).model"
                      label="模型（留空使用渠道默认）"
                      :items="targetModelDiscovery(option.key).models"
                      item-title="title"
                      item-value="value"
                      :return-object="false"
                      :loading="targetModelDiscovery(option.key).loading"
                      variant="outlined"
                      density="compact"
                      hide-details
                      clearable
                    >
                      <template #no-data>
                        <div class="pa-3 text-caption text-medium-emphasis">
                          <template v-if="targetModelDiscovery(option.key).loading">正在获取模型列表...</template>
                          <template v-else-if="targetModelDiscovery(option.key).error"
                            >暂时无法显示模型列表，仍可直接输入模型名称</template
                          >
                          <template v-else>未找到匹配模型，可直接输入模型名称</template>
                        </div>
                      </template>
                    </v-combobox>
                    <div v-if="targetModelDiscovery(option.key).error" class="audit-model-feedback" role="alert">
                      <span>{{ targetModelDiscovery(option.key).error }}</span>
                      <v-btn
                        icon="mdi-refresh"
                        variant="text"
                        color="warning"
                        class="audit-icon-btn"
                        :loading="targetModelDiscovery(option.key).loading"
                        :disabled="saving"
                        :aria-label="`重新获取 ${option.channel.name} 的模型列表`"
                        @click="loadTargetModels(option, true)"
                      />
                    </div>
                  </div>
                  <v-select
                    v-model="targetSetting(option.key).thinking"
                    label="思考档位"
                    :items="thinkingOptions(option.kind)"
                    variant="outlined"
                    density="compact"
                    hide-details
                  />
                </div>
              </div>
            </section>

            <template v-if="!isSingleRun">
              <v-divider class="my-5" />

              <section class="audit-form-section">
                <div class="audit-section-heading-row">
                  <div class="audit-section-heading">运行计划</div>
                  <span class="text-caption text-medium-emphasis">{{ scheduleSummary }}</span>
                </div>
                <div class="audit-form-grid audit-form-grid--three">
                  <v-text-field
                    v-model="form.startAtLocal"
                    label="开始时间"
                    type="datetime-local"
                    variant="outlined"
                    density="comfortable"
                  />
                  <v-text-field
                    v-model.number="form.intervalHours"
                    label="执行间隔（小时）"
                    type="number"
                    min="6"
                    max="720"
                    step="1"
                    variant="outlined"
                    density="comfortable"
                  />
                  <v-select
                    v-model="form.endMode"
                    label="结束方式"
                    :items="endModeOptions"
                    variant="outlined"
                    density="comfortable"
                  />
                </div>

                <div v-if="form.endMode !== 'none'" class="audit-form-grid audit-form-grid--two">
                  <v-text-field
                    v-if="form.endMode === 'endAt'"
                    v-model="form.endAtLocal"
                    label="结束时间"
                    type="datetime-local"
                    variant="outlined"
                    density="comfortable"
                  />
                  <v-text-field
                    v-else
                    v-model.number="form.durationHours"
                    label="持续时长（小时）"
                    type="number"
                    min="1"
                    step="1"
                    variant="outlined"
                    density="comfortable"
                  />
                </div>
              </section>
            </template>

            <v-divider class="my-5" />

            <section class="audit-form-section">
              <button
                type="button"
                class="audit-advanced-toggle"
                :aria-expanded="advancedOpen"
                @click="advancedOpen = !advancedOpen"
              >
                <span class="audit-advanced-toggle__label">
                  <v-icon icon="mdi-tune" size="20" />
                  <span>高级设置</span>
                </span>
                <span class="audit-advanced-summary">{{ budgetSummary }}</span>
                <v-icon :icon="advancedOpen ? 'mdi-chevron-up' : 'mdi-chevron-down'" size="20" />
              </button>

              <v-expand-transition>
                <div v-show="advancedOpen" class="audit-advanced-content">
                  <div v-if="!isSingleRun" class="audit-form-grid audit-form-grid--two mb-5">
                    <v-text-field
                      v-model.number="form.jitterMinutes"
                      label="随机抖动（分钟）"
                      type="number"
                      min="0"
                      step="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                    <v-text-field
                      v-model="form.timeZone"
                      label="IANA 时区"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                  </div>

                  <div class="audit-subsection-heading">单次运行预算</div>
                  <div class="audit-form-grid audit-form-grid--five">
                    <v-text-field
                      v-model.number="form.maxRequests"
                      label="请求数"
                      type="number"
                      min="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                    <v-text-field
                      v-model.number="form.maxInputTokens"
                      label="输入 tokens"
                      type="number"
                      min="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                    <v-text-field
                      v-model.number="form.maxOutputTokens"
                      label="输出 tokens"
                      type="number"
                      min="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                    <v-text-field
                      v-model.number="form.maxTotalTokens"
                      label="总 tokens"
                      type="number"
                      min="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                    <v-text-field
                      v-model.number="form.maxConcurrentRequests"
                      label="并发"
                      type="number"
                      min="1"
                      variant="outlined"
                      density="comfortable"
                      hide-details
                    />
                  </div>
                </div>
              </v-expand-transition>
            </section>
          </template>
        </v-card-text>

        <v-divider />
        <v-card-actions class="audit-dialog-actions">
          <v-btn variant="text" :disabled="saving" @click="closeFormDialog">取消</v-btn>
          <v-btn
            color="primary"
            :prepend-icon="editingJob ? 'mdi-content-save' : isSingleRun ? 'mdi-play-circle' : 'mdi-plus'"
            :loading="saving"
            @click="saveJob"
          >
            {{ editingJob ? '保存修改' : evaluationMode ? '开始评测' : isSingleRun ? '开始检查' : '创建持续任务' }}
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="deleteDialog" max-width="460" persistent>
      <v-card>
        <v-card-title>删除{{ evaluationMode ? '评测' : '审计' }}任务</v-card-title>
        <v-card-text> 确定删除“{{ deletingJob?.name }}”吗？历史运行和报告仍会保留。 </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" :disabled="actionBusy" @click="deleteDialog = false">取消</v-btn>
          <v-btn color="error" :loading="actionKey === `${deletingJob?.id}:delete`" @click="deleteJob">删除</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="runsDialog" max-width="820" scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>{{ runsJob?.name }} · 运行记录</span>
          <v-btn icon="mdi-close" variant="text" class="audit-icon-btn" aria-label="关闭" @click="runsDialog = false" />
        </v-card-title>
        <v-divider />
        <v-card-text class="pa-0">
          <v-progress-linear v-if="runsLoading" indeterminate />
          <v-alert v-if="runsError" type="error" variant="tonal" class="ma-4">{{ runsError }}</v-alert>
          <div v-else-if="!runsLoading && runs.length === 0" class="pa-8 text-center text-medium-emphasis">
            暂无运行记录
          </div>
          <div v-else class="audit-runs-list">
            <div
              v-for="run in runs"
              :key="run.id"
              :class="['audit-run-row', { 'audit-run-row--evaluation': evaluationMode }]"
            >
              <div>
                <div class="d-flex flex-wrap align-center ga-2">
                  <span class="font-weight-medium">{{ formatDateTime(run.createdAt) }}</span>
                  <v-chip size="x-small" :color="runStatusColor(run.status)" variant="tonal">{{
                    runStatusLabel(run.status)
                  }}</v-chip>
                  <v-chip size="x-small" variant="outlined">{{ run.trigger === 'manual' ? '手动' : '定时' }}</v-chip>
                </div>
                <div class="text-caption text-medium-emphasis mt-1">{{ run.id }}</div>
              </div>
              <div class="text-body-2">{{ run.usage.requests }}/{{ run.budget.maxRequests }} 请求</div>
              <div class="text-body-2">{{ formatCompactNumber(run.usage.totalTokens) }} tokens</div>
              <div v-if="evaluationMode" class="audit-run-result">
                <template v-if="runScore(run) !== null">
                  <strong>{{ runScore(run)?.toFixed(1) }}</strong><span>/100</span>
                </template>
                <span v-else class="text-medium-emphasis">{{ runResultLabel(run) }}</span>
              </div>
              <div class="audit-run-actions">
                <v-btn
                  v-if="evaluationMode && run.result?.reportId"
                  prepend-icon="mdi-open-in-new"
                  color="primary"
                  variant="tonal"
                  @click="openEvaluationResult(run.result.reportId)"
                >
                  查看结果
                </v-btn>
                <v-btn
                  v-if="!evaluationMode"
                  prepend-icon="mdi-chart-line"
                  variant="text"
                  :disabled="actionBusy"
                  @click="openAnalysesDialog(run)"
                >
                  分析
                </v-btn>
                <v-btn
                  v-if="run.status === 'pending' || run.status === 'running'"
                  color="error"
                  variant="text"
                  :loading="actionKey === `${run.id}:cancel`"
                  :disabled="actionBusy"
                  @click="cancelRun(run)"
                >
                  取消
                </v-btn>
              </div>
            </div>
          </div>
          <div v-if="runPageCount > 1" class="audit-runs-pagination">
            <v-pagination
              v-model="runPage"
              :length="runPageCount"
              :disabled="runsLoading || actionBusy"
              @update:model-value="loadCurrentRunPage"
            />
          </div>
        </v-card-text>
      </v-card>
    </v-dialog>

    <v-dialog v-model="analysesDialog" max-width="980" scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>Mod 分析 · {{ analysesRun?.id }}</span>
          <v-btn
            icon="mdi-close"
            variant="text"
            class="audit-icon-btn"
            aria-label="关闭"
            @click="analysesDialog = false"
          />
        </v-card-title>
        <v-divider />
        <v-card-text class="audit-analysis-body">
          <v-progress-linear v-if="analysesLoading" indeterminate class="mb-4" />
          <v-alert v-if="analysesError" type="error" variant="tonal" class="mb-4">{{ analysesError }}</v-alert>
          <div v-else-if="!analysesLoading && analyses.length === 0" class="audit-empty-state">
            本次运行没有 Mod 分析记录
          </div>
          <v-expansion-panels v-else variant="accordion">
            <v-expansion-panel v-for="analysis in analyses" :key="analysis.id">
              <v-expansion-panel-title>
                <div class="audit-analysis-title">
                  <div>
                    <div class="font-weight-medium">{{ modName(analysis.mod.id) }}</div>
                    <div class="text-caption text-medium-emphasis">
                      目标 {{ analysis.targetId }} · {{ analysis.mod.version }}
                    </div>
                  </div>
                  <v-chip size="small" variant="tonal" :color="analysisStatusMeta(analysis.status).color">
                    {{ analysisStatusMeta(analysis.status).label }}
                  </v-chip>
                </div>
              </v-expansion-panel-title>
              <v-expansion-panel-text>
                <div class="audit-analysis-meta">
                  <div>
                    <span>分析方式</span><strong>{{ analysisModeMeta(analysis.mode).label }}</strong>
                  </div>
                  <div>
                    <span>输入 SHA</span><code>{{ analysis.inputSha256 }}</code>
                  </div>
                  <div v-if="analysis.interpreter">
                    <span>解释器</span><strong>{{ analysis.interpreterVersion || analysis.interpreter }}</strong>
                  </div>
                  <div v-if="analysis.exitCode !== undefined">
                    <span>退出码</span><strong>{{ analysis.exitCode }}</strong>
                  </div>
                  <div v-if="analysis.usage.requests">
                    <span>分析用量</span><strong>{{ analysis.usage.totalTokens }} tokens</strong>
                  </div>
                </div>

                <div
                  v-if="analysis.status === 'completed' || analysis.status === 'failed'"
                  class="d-flex justify-end mt-3"
                >
                  <v-btn
                    variant="outlined"
                    prepend-icon="mdi-refresh"
                    :loading="actionKey === `${analysis.id}:rerun`"
                    @click="rerunAnalysis(analysis)"
                  >
                    重新分析
                  </v-btn>
                </div>

                <v-alert v-if="analysis.failure" type="error" variant="tonal" class="mt-4">{{
                  analysis.failure
                }}</v-alert>

                <template v-if="analysis.mode === 'manual'">
                  <div class="audit-path-list mt-4">
                    <div v-for="item in manualAnalysisPaths(analysis)" :key="item.label" class="audit-path-row">
                      <span>{{ item.label }}</span
                      ><code>{{ item.value }}</code>
                      <v-btn
                        icon="mdi-content-copy"
                        variant="text"
                        class="audit-icon-btn"
                        :aria-label="`复制${item.label}`"
                        @click="copyText(item.value, `${item.label}已复制`)"
                      />
                    </div>
                  </div>
                  <div class="audit-method-block mt-4">
                    <div class="audit-subsection-heading">分析方法</div>
                    <pre>{{ analysis.method }}</pre>
                  </div>
                  <v-textarea
                    v-if="analysis.status === 'awaiting_manual'"
                    v-model="manualResultTexts[analysis.id]"
                    label="analysis-output.json"
                    variant="outlined"
                    rows="9"
                    class="mt-4 audit-code-input"
                    :error-messages="manualResultErrors[analysis.id] ? [manualResultErrors[analysis.id]] : []"
                  />
                  <div v-if="analysis.status === 'awaiting_manual'" class="d-flex justify-end">
                    <v-btn
                      color="primary"
                      prepend-icon="mdi-database-import"
                      :loading="actionKey === `${analysis.id}:manual`"
                      @click="submitManualAnalysis(analysis)"
                    >
                      导入分析结果
                    </v-btn>
                  </div>
                </template>

                <div v-if="analysis.result" class="audit-method-block mt-4">
                  <div class="audit-subsection-heading">分析结果</div>
                  <pre>{{ formatJSON(analysis.result) }}</pre>
                </div>
                <details class="audit-raw-details mt-4">
                  <summary>查看冻结输入</summary>
                  <pre>{{ formatJSON(analysis.input) }}</pre>
                </details>
              </v-expansion-panel-text>
            </v-expansion-panel>
          </v-expansion-panels>
        </v-card-text>
      </v-card>
    </v-dialog>

    <v-dialog v-model="modDialog" max-width="1120" persistent scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>{{ editingMod ? `编辑 ${editingMod.descriptor.name}` : '新建探针 Mod' }}</span>
          <v-btn
            icon="mdi-close"
            variant="text"
            class="audit-icon-btn"
            aria-label="关闭"
            :disabled="modSaving"
            @click="closeModDialog"
          />
        </v-card-title>
        <v-divider />
        <v-card-text class="audit-mod-editor-body">
          <v-alert v-if="modError" type="error" variant="tonal" class="mb-4">{{ modError }}</v-alert>
          <div class="audit-form-grid audit-form-grid--two">
            <v-select
              v-if="!editingMod"
              v-model="selectedExampleId"
              label="从案例开始"
              :items="modExampleItems"
              variant="outlined"
              density="comfortable"
              @update:model-value="applySelectedExample"
            />
            <v-text-field
              v-model="modDirectoryName"
              label="Mod 目录名"
              variant="outlined"
              density="comfortable"
              :readonly="Boolean(editingMod)"
            />
          </div>
          <div class="audit-mod-editor mt-2">
            <nav class="audit-mod-files" aria-label="Mod 文件">
              <button
                v-for="fileName in modFileNames"
                :key="fileName"
                type="button"
                :class="['audit-mod-file', { 'audit-mod-file--active': selectedModFile === fileName }]"
                @click="selectedModFile = fileName"
              >
                <v-icon :icon="fileIcon(fileName)" size="18" />
                <span>{{ fileName }}</span>
              </button>
              <div class="audit-mod-file-add">
                <v-text-field
                  v-model="newModFileName"
                  label="新增文件"
                  variant="outlined"
                  density="compact"
                  hide-details
                  @keyup.enter="addModFile"
                />
                <v-btn
                  icon="mdi-plus"
                  variant="text"
                  class="audit-icon-btn"
                  aria-label="添加文件"
                  @click="addModFile"
                />
              </div>
              <div v-if="editingMod?.binaryFiles?.length" class="audit-binary-note">
                {{ editingMod.binaryFiles.length }} 个二进制文件会原样保留
              </div>
            </nav>
            <div class="audit-mod-file-editor">
              <div class="audit-mod-file-toolbar">
                <strong>{{ selectedModFile || '请选择文件' }}</strong>
                <div class="d-flex align-center ga-2">
                  <v-chip v-if="selectedModFile" size="x-small" variant="outlined">{{
                    fileLanguage(selectedModFile)
                  }}</v-chip>
                  <v-btn
                    v-if="selectedModFile && selectedModFile !== 'manifest.json'"
                    icon="mdi-delete"
                    color="error"
                    variant="text"
                    class="audit-icon-btn"
                    aria-label="删除当前文件"
                    @click="deleteSelectedModFile"
                  />
                </div>
              </div>
              <v-textarea
                v-if="selectedModFile"
                v-model="modFiles[selectedModFile]"
                variant="outlined"
                rows="24"
                hide-details
                class="audit-code-input"
                spellcheck="false"
              />
            </div>
          </div>
        </v-card-text>
        <v-divider />
        <v-card-actions class="audit-dialog-actions">
          <v-btn variant="text" :disabled="modSaving" @click="closeModDialog">取消</v-btn>
          <v-btn color="primary" prepend-icon="mdi-content-save" :loading="modSaving" @click="saveMod"
            >保存并加载</v-btn
          >
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="importModDialog" max-width="620" persistent>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>导入本机 Mod 目录</span>
          <v-btn
            icon="mdi-close"
            variant="text"
            class="audit-icon-btn"
            aria-label="关闭"
            :disabled="modSaving"
            @click="importModDialog = false"
          />
        </v-card-title>
        <v-divider />
        <v-card-text class="pa-6">
          <v-alert v-if="modError" type="error" variant="tonal" class="mb-4">{{ modError }}</v-alert>
          <v-text-field
            v-model="importSourcePath"
            label="源目录绝对路径"
            variant="outlined"
            density="comfortable"
            autofocus
          />
          <v-text-field
            v-model="importDirectoryName"
            label="目标目录名（可选）"
            variant="outlined"
            density="comfortable"
            hint="留空时使用源目录名"
            persistent-hint
          />
        </v-card-text>
        <v-divider />
        <v-card-actions class="audit-dialog-actions">
          <v-btn variant="text" :disabled="modSaving" @click="importModDialog = false">取消</v-btn>
          <v-btn color="primary" prepend-icon="mdi-folder-arrow-down" :loading="modSaving" @click="importMod"
            >导入并加载</v-btn
          >
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-snackbar v-model="snackbar" :color="snackbarColor" location="top right" :timeout="3500">
      {{ snackbarText }}
    </v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ApiError,
  api,
  type ApiTab,
  type Channel,
  type ModelAuditCapabilityEvaluationOption,
  type ModelAuditCapabilityPreset,
  type ModelAuditIdentityPreset,
  type ModelEvaluationQuestionBankDescriptor,
  type ModelAuditJob,
  type ModelAuditJobDefinition,
  type ModelAuditJobStatus,
  type ModelAuditModAnalysisRecord,
  type ModelAuditModAnalysisMode,
  type ModelAuditModCatalogResponse,
  type ModelAuditModDescriptor,
  type ModelAuditModEditable,
  type ModelAuditModExample,
  type ModelAuditProtocolDescriptor,
  type ModelAuditRunPresentation,
  type ModelAuditStrategyDescriptor,
  type ModelAuditStrategySelection
} from '@/services/api'

type EndMode = 'none' | 'endAt' | 'duration'
type ExecutionMode = 'once' | 'continuous'

interface ChannelOption {
  key: string
  kind: ApiTab
  channel: Channel
  disabled: boolean
  disabledReason?: string
}

interface TargetSetting {
  model: string | null
  thinking: string
}

interface TargetModelDiscovery {
  models: Array<{ title: string; value: string }>
  loading: boolean
  loaded: boolean
  error: string
  requestId: number
}

interface StrategyState {
  enabled: boolean
  config: Record<string, unknown>
}

interface AuditFormState {
  name: string
  initialStatus: 'draft' | 'enabled'
  executionMode: ExecutionMode
  workloadKind: 'identity' | 'capability'
  identityPresetLevel: 'low' | 'medium' | 'high' | 'custom'
  capabilityPresetKey: string
  strategyStates: Record<string, StrategyState>
  channelKeys: string[]
  targetSettings: Record<string, TargetSetting>
  startAtLocal: string
  endMode: EndMode
  endAtLocal: string
  durationHours: number
  intervalHours: number
  jitterMinutes: number
  timeZone: string
  maxRequests: number
  maxInputTokens: number
  maxOutputTokens: number
  maxTotalTokens: number
  maxConcurrentRequests: number
  analyzerChannelKey: string
  analyzerModel: string
  analyzerThinking: string
}

type AuditView = 'jobs' | 'mods'

const route = useRoute()
const router = useRouter()
const evaluationMode = computed(() => route.name === 'model-evaluation')
const activeView = ref<AuditView>('jobs')

const pageSize = 20
const page = ref(1)
const totalJobs = ref(0)
const jobs = ref<ModelAuditJob[]>([])
const latestRuns = ref<Record<string, ModelAuditRunPresentation>>({})
const loading = ref(false)
const pageError = ref('')

const strategies = ref<ModelAuditStrategyDescriptor[]>([])
const capabilityPresets = ref<ModelAuditCapabilityPreset[]>([])
const capabilityEvaluationOptions = ref<ModelAuditCapabilityEvaluationOption[]>([])
const identityPresets = ref<ModelAuditIdentityPreset[]>([])
const questionBanks = ref<ModelEvaluationQuestionBankDescriptor[]>([])
const questionBankIssues = ref<Array<{ directory: string; bankId?: string; message: string }>>([])
const questionBanksLoading = ref(false)
const protocolDescriptors = ref<ModelAuditProtocolDescriptor[]>([])
const channelOptions = ref<ChannelOption[]>([])
const targetModelDiscoveries = reactive<Record<string, TargetModelDiscovery>>({})
const catalogLoading = ref(false)
const channelLoading = ref(false)
const catalogNotice = ref('')

const formDialog = ref(false)
const editingJob = ref<ModelAuditJob | null>(null)
const saving = ref(false)
const formError = ref('')
const advancedOpen = ref(false)

const deleteDialog = ref(false)
const deletingJob = ref<ModelAuditJob | null>(null)
const actionKey = ref('')

const runsDialog = ref(false)
const runsJob = ref<ModelAuditJob | null>(null)
const runs = ref<ModelAuditRunPresentation[]>([])
const runsLoading = ref(false)
const runsError = ref('')
const runPageSize = 20
const runPage = ref(1)
const totalRuns = ref(0)

const mods = ref<ModelAuditModDescriptor[]>([])
const modIssues = ref<Array<{ directory: string; modId?: string; message: string }>>([])
const modExamples = ref<ModelAuditModExample[]>([])
const modsLoading = ref(false)
const modDialog = ref(false)
const editingMod = ref<ModelAuditModEditable | null>(null)
const modSaving = ref(false)
const modError = ref('')
const selectedExampleId = ref('')
const modDirectoryName = ref('')
const modFiles = reactive<Record<string, string>>({})
const selectedModFile = ref('')
const newModFileName = ref('')
const importModDialog = ref(false)
const importSourcePath = ref('')
const importDirectoryName = ref('')

const analysesDialog = ref(false)
const analysesRun = ref<ModelAuditRunPresentation | null>(null)
const analyses = ref<ModelAuditModAnalysisRecord[]>([])
const analysesLoading = ref(false)
const analysesError = ref('')
const manualResultTexts = reactive<Record<string, string>>({})
const manualResultErrors = reactive<Record<string, string>>({})

const snackbar = ref(false)
const snackbarText = ref('')
const snackbarColor = ref<'success' | 'error' | 'info'>('success')

const initialStatusOptions = [
  { title: '启用并按计划运行', value: 'enabled' },
  { title: '保存为草稿', value: 'draft' }
]

const endModeOptions = [
  { title: '持续运行', value: 'none' },
  { title: '指定结束时间', value: 'endAt' },
  { title: '指定持续时长', value: 'duration' }
]

const protocolNames: Record<ApiTab, string> = {
  messages: 'Messages',
  responses: 'Responses',
  gemini: 'Gemini',
  chat: 'Chat',
  images: 'Images'
}

const pageCount = computed(() => Math.max(1, Math.ceil(totalJobs.value / pageSize)))
const runPageCount = computed(() => Math.max(1, Math.ceil(totalRuns.value / runPageSize)))
const actionBusy = computed(() => Boolean(actionKey.value))
const catalogReady = computed(
  () =>
    protocolDescriptors.value.length > 0 &&
    (evaluationMode.value
      ? capabilityPresets.value.length > 0
      : strategies.value.length > 0 && identityPresets.value.length > 0)
)
const selectedChannelOptions = computed(() => {
  const keys = new Set(form.channelKeys)
  return channelOptions.value.filter(option => keys.has(option.key) && !option.disabled)
})
const channelSelectItems = computed(() =>
  channelOptions.value.map(option => ({
    title: option.disabledReason
      ? `${protocolLabel(option.kind)} · ${option.channel.name} · ${option.disabledReason}`
      : `${protocolLabel(option.kind)} · ${option.channel.name}`,
    value: option.key,
    props: { disabled: option.disabled }
  }))
)
const capabilityPresetOptions = computed(() =>
  capabilityPresets.value.map(preset => ({
    title: `${capabilityModeLabel(preset.mode)} · v${preset.ref.semanticVersion} · ${preset.tasks.length} 项配置`,
    value: versionedRefKey(preset.ref)
  }))
)
const selectedCapabilityPreset = computed(() =>
  capabilityPresets.value.find(item => versionedRefKey(item.ref) === form.capabilityPresetKey)
)
const capabilityEvaluationItems = computed(() =>
  capabilityEvaluationOptions.value.map(option => ({
    title: `${option.label} · ${option.bankName} · ${option.questionCount} 道`,
    value: versionedRefKey(option.preset)
  }))
)
const selectedCapabilityEvaluation = computed(() =>
  capabilityEvaluationOptions.value.find(option => versionedRefKey(option.preset) === form.capabilityPresetKey)
)

const form = reactive<AuditFormState>(newFormState())
const isSingleRun = computed(() => form.executionMode === 'once')
const enabledStrategyCount = computed(
  () => strategies.value.filter(strategy => strategyState(strategy.ref.id).enabled).length
)
const identityPresetSummary = computed(() => {
  if (form.identityPresetLevel === 'custom') return `${enabledStrategyCount.value} 个策略（自定义）`
  const preset = identityPresetByLevel(form.identityPresetLevel)
  return preset
    ? `${identityPresetLevelLabel(preset.level)} · 约 ${preset.estimateRequests} 请求`
    : `${enabledStrategyCount.value} 个策略`
})
const identityPresetDescription = computed(() => {
  return identityPresetByLevel(form.identityPresetLevel)?.description || ''
})

function identityPresetByLevel(level: string) {
  return identityPresets.value.find(item => item.level === level)
}
function identityPresetLevelLabel(level: string) {
  return ({ low: '低档', medium: '中档', high: '高档' } as Record<string, string>)[level] || level
}
const capabilityPresetRunSummary = computed(() =>
  selectedCapabilityPreset.value ? `${selectedCapabilityPreset.value.limits.requests} 请求` : '未选择'
)
const budgetSummary = computed(
  () =>
    `${form.maxRequests} 请求 · ${formatCompactNumber(form.maxTotalTokens)} tokens · 并发 ${form.maxConcurrentRequests}`
)
const scheduleSummary = computed(
  () =>
    `${formatInterval(form.intervalHours * 3_600_000)}${form.jitterMinutes > 0 ? ` · 抖动 ${form.jitterMinutes} 分钟` : ''}`
)
const modByID = computed(() => new Map(mods.value.map(mod => [mod.reference.id, mod])))
const needsAnalyzer = computed(() =>
  strategies.value.some(strategy => {
    return strategyState(strategy.ref.id).enabled && modByID.value.get(strategy.ref.id)?.analysisMode === 'llm'
  })
)
const analyzerChannelItems = computed(() =>
  channelOptions.value
    .filter(option => !option.disabled && option.kind !== 'images')
    .map(option => ({ title: `${protocolLabel(option.kind)} · ${option.channel.name}`, value: option.key }))
)
const analyzerChannelOption = computed(() =>
  channelOptions.value.find(option => option.key === form.analyzerChannelKey && !option.disabled)
)
const analyzerDiscovery = computed(() =>
  analyzerChannelOption.value ? targetModelDiscovery(analyzerChannelOption.value.key) : undefined
)
const analyzerModelItems = computed(() => analyzerDiscovery.value?.models || [])
const analyzerThinkingOptions = computed(() =>
  analyzerChannelOption.value ? thinkingOptions(analyzerChannelOption.value.kind) : []
)
const modFileNames = computed(() =>
  Object.keys(modFiles).sort((left, right) => {
    if (left === 'manifest.json') return -1
    if (right === 'manifest.json') return 1
    return left.localeCompare(right)
  })
)
const modExampleItems = computed(() =>
  modExamples.value.map(example => ({
    title: `${example.name} · ${analysisModeMeta(example.mode).label}`,
    value: example.id
  }))
)

function newFormState(): AuditFormState {
  const start = new Date(Date.now() + 5 * 60 * 1000)
  return {
    name: '',
    initialStatus: 'enabled',
    executionMode: 'once',
    workloadKind: evaluationMode.value ? 'capability' : 'identity',
    identityPresetLevel: 'low',
    capabilityPresetKey: '',
    strategyStates: {},
    channelKeys: [],
    targetSettings: {},
    startAtLocal: toLocalDateTime(start),
    endMode: 'none',
    endAtLocal: '',
    durationHours: 168,
    intervalHours: 24,
    jitterMinutes: 0,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    maxRequests: 100,
    maxInputTokens: 100_000,
    maxOutputTokens: 100_000,
    maxTotalTokens: 200_000,
    maxConcurrentRequests: 1,
    analyzerChannelKey: '',
    analyzerModel: '',
    analyzerThinking: ''
  }
}

function resetForm(state: AuditFormState) {
  Object.assign(form, state)
}

async function loadJobs() {
  loading.value = true
  pageError.value = ''
  try {
    const response = await api.listModelAuditJobs(
      page.value,
      pageSize,
      evaluationMode.value ? 'capability' : 'identity'
    )
    jobs.value = response.page.jobs
    latestRuns.value = response.latestRuns || {}
    totalJobs.value = response.page.total
    if (page.value > pageCount.value) {
      page.value = pageCount.value
      await loadJobs()
    }
  } catch (error) {
    pageError.value = errorMessage(error, evaluationMode.value ? '加载评测任务失败' : '加载审计任务失败')
  } finally {
    loading.value = false
  }
}

async function loadCatalogs() {
  catalogLoading.value = true
  catalogNotice.value = ''
  try {
    const capabilityDescriptors = await api.getModelAuditCapabilities()
    protocolDescriptors.value = capabilityDescriptors.protocols
    if (evaluationMode.value) {
      const capabilityResponse = await api.getModelAuditCapabilityPresets()
      if (!capabilityResponse.catalog.available)
        throw new Error(capabilityResponse.catalog.reason || '能力题库目录不可用')
      capabilityPresets.value = capabilityResponse.catalog.presets
      capabilityEvaluationOptions.value = capabilityResponse.catalog.evaluationOptions || []
      questionBanks.value = capabilityResponse.catalog.questionBanks?.banks || []
      questionBankIssues.value = capabilityResponse.catalog.questionBanks?.issues || []
    } else {
      const [strategyResponse, identityPresetResponse] = await Promise.all([
        api.getModelAuditStrategies(),
        api.getModelAuditIdentityPresets()
      ])
      if (!strategyResponse.available) throw new Error(strategyResponse.reason || '身份策略目录不可用')
      strategies.value = strategyResponse.strategies
      identityPresets.value = identityPresetResponse.presets
    }
  } catch (error) {
    catalogNotice.value = errorMessage(error, evaluationMode.value ? '加载能力评测目录失败' : '加载模型审计目录失败')
  } finally {
    catalogLoading.value = false
  }
}

async function loadMods() {
  modsLoading.value = true
  try {
    const response = await api.getModelAuditMods()
    applyModCatalog(response)
  } catch (error) {
    pageError.value = errorMessage(error, '加载探针 Mod 失败')
  } finally {
    modsLoading.value = false
  }
}

async function reloadMods() {
  modsLoading.value = true
  pageError.value = ''
  try {
    const response = await api.reloadModelAuditMods()
    applyModCatalog(response)
    const strategyResponse = await api.getModelAuditStrategies()
    if (strategyResponse.available) strategies.value = strategyResponse.strategies
    showSnackbar(`已加载 ${mods.value.length} 个 Mod`, modIssues.value.length ? 'info' : 'success')
  } catch (error) {
    pageError.value = errorMessage(error, '重新加载探针 Mod 失败')
  } finally {
    modsLoading.value = false
  }
}

function applyModCatalog(response: ModelAuditModCatalogResponse) {
  if (!response?.catalog) throw new Error('探针 Mod 接口未返回 catalog')

  // 兼容修复前的后端：Go 的 nil slice 会编码为 null，而空目录在前端应始终表现为空数组。
  const rawMods: unknown = response.catalog.mods
  const rawIssues: unknown = response.catalog.issues
  if (rawMods !== null && !Array.isArray(rawMods)) throw new Error('探针 Mod 接口返回的 mods 格式无效')
  if (rawIssues !== null && !Array.isArray(rawIssues)) throw new Error('探针 Mod 接口返回的 issues 格式无效')
  mods.value = rawMods === null ? [] : rawMods
  modIssues.value = rawIssues === null ? [] : rawIssues
}

async function loadModExamples() {
  try {
    const response = await api.getModelAuditModExamples()
    modExamples.value = response.examples
  } catch (error) {
    pageError.value = errorMessage(error, '加载 Mod 案例失败')
  }
}

async function loadChannels() {
  channelLoading.value = true
  const kinds: ApiTab[] = ['messages', 'responses', 'gemini', 'chat', 'images']
  const settled = await Promise.allSettled(kinds.map(kind => api.getChannelDashboard(kind)))
  const loaded: ChannelOption[] = []
  const failed: string[] = []
  settled.forEach((result, index) => {
    const kind = kinds[index]
    if (result.status === 'rejected') {
      failed.push(protocolLabel(kind))
      return
    }
    result.value.channels.forEach(channel => {
      const hasStableId = Boolean(channel.id?.trim())
      const unsupported = kind === 'images'
      loaded.push({
        key: `${kind}:${channel.id || channel.index}`,
        kind,
        channel,
        disabled: !hasStableId || unsupported,
        disabledReason: unsupported ? '审计资产不支持图片协议' : !hasStableId ? '缺少稳定 ID' : undefined
      })
    })
  })
  channelOptions.value = loaded
  channelLoading.value = false
  if (failed.length > 0) {
    catalogNotice.value = `以下渠道目录加载失败：${failed.join('、')}`
  }
}

function strategySampleCount(id: string): number {
  return Number(strategyState(id).config.sampleCount || 0)
}

function openCreateDialog() {
  const state = newFormState()
  const defaultEvaluation =
    capabilityEvaluationOptions.value.find(item => !item.dimension) || capabilityEvaluationOptions.value[0]
  const defaultPreset = capabilityPresets.value.find(item => item.mode === 'quick') || capabilityPresets.value[0]
  state.capabilityPresetKey = defaultEvaluation
    ? versionedRefKey(defaultEvaluation.preset)
    : defaultPreset
      ? versionedRefKey(defaultPreset.ref)
      : ''
  state.workloadKind = evaluationMode.value ? 'capability' : 'identity'

  if (!evaluationMode.value) {
    state.identityPresetLevel = 'low'
    for (const strategy of strategies.value) {
      state.strategyStates[strategy.ref.id] = { enabled: false, config: defaultStrategyConfig(strategy) }
    }
    applyIdentityPresetToState(state, 'low')
  }

  const analyzer = channelOptions.value.find(option => !option.disabled && option.kind !== 'images')
  if (analyzer) {
    state.analyzerChannelKey = analyzer.key
    state.analyzerModel = analyzer.channel.defaultModel || ''
    state.analyzerThinking = thinkingOptions(analyzer.kind).some(item => item.value === 'medium') ? 'medium' : ''
  }
  editingJob.value = null
  resetForm(state)
  advancedOpen.value = false
  formError.value = ''
  formDialog.value = true
}

function selectEvaluationChannel(value: unknown) {
  const key = typeof value === 'string' ? value : ''
  form.channelKeys = key ? [key] : []
  if (!key) return
  const option = channelOptions.value.find(item => item.key === key && !item.disabled)
  if (!option) return
  const setting = targetSetting(key)
  if (!setting.model?.trim()) setting.model = option.channel.defaultModel || ''
  void loadTargetModels(option)
}

function applyIdentityPresetToState(state: AuditFormState, level: 'low' | 'medium' | 'high') {
  const preset = identityPresets.value.find(item => item.level === level)
  if (!preset) throw new Error(`后端未提供 ${level} 身份审计预设`)
  const selections = new Map(preset.strategies.map(item => [item.strategyId, item]))
  for (const strategy of strategies.value) {
    const selection = selections.get(strategy.ref.id)
    if (selection && state.strategyStates[strategy.ref.id]) {
      state.strategyStates[strategy.ref.id].enabled = selection.enabled
      state.strategyStates[strategy.ref.id].config = { ...selection.config }
    }
  }
}

function openEditDialog(job: ModelAuditJob) {
  const state = newFormState()
  state.name = job.name
  state.executionMode = job.schedule.oneShot ? 'once' : 'continuous'
  state.workloadKind = job.workload.kind
  const existingStrategyIds = new Set((job.workload.strategies || []).map(item => item.strategyId))
  const availableStrategyIds = new Set(strategies.value.map(strategy => strategy.ref.id))
  const unavailableStrategies = [...existingStrategyIds].filter(id => !availableStrategyIds.has(id))
  if (job.workload.kind === 'identity' && unavailableStrategies.length > 0) {
    showSnackbar(`以下历史策略当前不可用，已阻止编辑：${unavailableStrategies.join('、')}`, 'error')
    return
  }
  if (job.workload.kind === 'capability' && job.workload.capabilityPreset) {
    state.capabilityPresetKey = versionedRefKey(job.workload.capabilityPreset)
    if (!capabilityPresets.value.some(item => versionedRefKey(item.ref) === state.capabilityPresetKey)) {
      showSnackbar(`历史能力预设当前不可用，已阻止编辑：${job.workload.capabilityPreset.id}`, 'error')
      return
    }
  } else {
    const defaultPreset = capabilityPresets.value[0]
    state.capabilityPresetKey = defaultPreset ? versionedRefKey(defaultPreset.ref) : ''
  }
  state.startAtLocal = toLocalDateTime(new Date(job.schedule.startAt))
  state.intervalHours = job.schedule.intervalMs / 3_600_000
  state.jitterMinutes = job.schedule.jitterMs / 60_000
  state.timeZone = job.schedule.timeZone
  state.maxRequests = job.budget.maxRequests
  state.maxInputTokens = job.budget.maxInputTokens
  state.maxOutputTokens = job.budget.maxOutputTokens
  state.maxTotalTokens = job.budget.maxTotalTokens
  state.maxConcurrentRequests = job.budget.maxConcurrentRequests
  if (job.analyzer) {
    const analyzerOption = channelOptions.value.find(
      option => option.kind === job.analyzer?.channelKind && option.channel.id === job.analyzer?.channelId
    )
    if (analyzerOption) state.analyzerChannelKey = analyzerOption.key
    state.analyzerModel = job.analyzer.model
    state.analyzerThinking = job.analyzer.thinking || ''
  }
  if (job.schedule.endAt) {
    state.endMode = 'endAt'
    state.endAtLocal = toLocalDateTime(new Date(job.schedule.endAt))
  } else if (job.schedule.durationMs) {
    state.endMode = 'duration'
    state.durationHours = job.schedule.durationMs / 3_600_000
  }
  const existingStrategies = new Map((job.workload.strategies || []).map(item => [item.strategyId, item]))
  for (const strategy of strategies.value) {
    const existing = existingStrategies.get(strategy.ref.id)
    state.strategyStates[strategy.ref.id] = {
      enabled: existing?.enabled || false,
      config: existing ? { ...existing.config } : defaultStrategyConfig(strategy)
    }
  }
  const optionsByIdentity = new Map(channelOptions.value.map(option => [`${option.kind}:${option.channel.id}`, option]))
  const unresolvedTargets: string[] = []
  for (const target of job.targets) {
    const option = optionsByIdentity.get(`${target.channelKind}:${target.channelId}`)
    if (!option || option.disabled) {
      unresolvedTargets.push(`${protocolLabel(target.channelKind)}:${target.channelId}`)
      continue
    }
    state.channelKeys.push(option.key)
    state.targetSettings[option.key] = { model: target.model || '', thinking: target.thinking || '' }
  }
  if (unresolvedTargets.length > 0) {
    showSnackbar(`以下历史目标当前无法解析，已阻止编辑：${unresolvedTargets.join('、')}`, 'error')
    return
  }
  editingJob.value = job
  resetForm(state)
  advancedOpen.value = false
  formError.value = ''
  formDialog.value = true
}

function closeFormDialog() {
  if (saving.value) return
  formDialog.value = false
  editingJob.value = null
  formError.value = ''
}

async function saveJob() {
  formError.value = ''
  const validationError = validateForm()
  if (validationError) {
    formError.value = validationError
    return
  }
  saving.value = true
  try {
    const definition = buildDefinition()
    if (editingJob.value) {
      await api.updateModelAuditJob(editingJob.value.id, editingJob.value.revision, definition)
      showSnackbar('任务已更新', 'success')
    } else {
      const created = await api.createModelAuditJob(
        definition,
        evaluationMode.value || isSingleRun.value ? 'enabled' : form.initialStatus
      )
      if (isSingleRun.value) {
        try {
          const response = await api.startModelAuditJobRun(created.job.id, created.job.revision)
          showSnackbar(`${evaluationMode.value ? '单次评测' : '单次检查'}已启动：${response.run.id}`, 'success')
        } catch (error) {
          formDialog.value = false
          editingJob.value = null
          await loadJobs()
          showSnackbar(`单次任务已创建，但启动失败：${errorMessage(error, '未知错误')}`, 'error')
          return
        }
      } else {
        showSnackbar(form.initialStatus === 'enabled' ? '持续任务已创建并启用' : '持续任务草稿已保存', 'success')
      }
    }
    formDialog.value = false
    editingJob.value = null
    await loadJobs()
  } catch (error) {
    formError.value = errorMessage(error, evaluationMode.value ? '保存评测任务失败' : '保存审计任务失败')
    if (isConflict(error)) {
      await loadJobs()
      formDialog.value = false
      editingJob.value = null
      showSnackbar('任务已被其他操作修改，列表已刷新，请重新打开编辑', 'error')
    }
  } finally {
    saving.value = false
  }
}

function validateForm(): string {
  if (!evaluationMode.value && !form.name.trim()) return '请输入任务名称'
  if (selectedChannelOptions.value.length === 0)
    return evaluationMode.value ? '请选择渠道' : '请至少选择一个具有稳定 ID 的文本渠道'
  if (evaluationMode.value && selectedChannelOptions.value.length !== 1) return '能力评测只能选择一个渠道'
  if (form.workloadKind === 'identity') {
    const enabled = strategies.value.filter(strategy => strategyState(strategy.ref.id).enabled)
    if (enabled.length === 0) return '身份审计至少启用一个策略'
    for (const strategy of enabled) {
      const property = sampleCountProperty(strategy)
      if (!property) continue
      const value = strategySampleCount(strategy.ref.id)
      const step = property.multipleOf || 1
      if (
        !Number.isInteger(value) ||
        value < (property.minimum || 1) ||
        value > (property.maximum || strategy.maximumSamples) ||
        value % step !== 0
      ) {
        return `${strategyName(strategy.ref.id)} 的样本数无效`
      }
      const intervalProperty = sampleIntervalProperty(strategy)
      if (intervalProperty) {
        const interval = Number(strategyState(strategy.ref.id).config.sampleIntervalMs || 0)
        if (
          !Number.isInteger(interval) ||
          interval < (intervalProperty.minimum || 0) ||
          interval > (intervalProperty.maximum || 3_600_000)
        ) {
          return `${strategyName(strategy.ref.id)} 的请求间隔无效`
        }
      }
    }
    if (needsAnalyzer.value) {
      const analyzer = analyzerChannelOption.value
      if (!analyzer) return '请选择可用的 AI 分析渠道'
      if (!form.analyzerModel.trim()) return '请输入 AI 分析模型'
      if (!thinkingOptions(analyzer.kind).some(item => item.value === form.analyzerThinking))
        return 'AI 分析思考档位无效'
      if (!requestProfile(analyzer.kind)) return `${protocolLabel(analyzer.kind)} 缺少可用分析请求轮廓`
    }
  } else if (evaluationMode.value ? !selectedCapabilityEvaluation.value : !selectedCapabilityPreset.value) {
    return evaluationMode.value ? '请选择测试能力' : '请选择能力预设'
  }
  for (const option of selectedChannelOptions.value) {
    const setting = targetSetting(option.key)
    if (evaluationMode.value && !setting.model?.trim()) return '请选择或输入模型'
    if (!thinkingOptions(option.kind).some(item => item.value === setting.thinking)) {
      return `${protocolLabel(option.kind)} 不支持思考档位 ${setting.thinking || '默认'}`
    }
    if (!requestProfile(option.kind)) return `${protocolLabel(option.kind)} 缺少可用请求轮廓`
  }
  if (!isSingleRun.value) {
    const start = new Date(form.startAtLocal)
    if (Number.isNaN(start.getTime())) return '开始时间无效'
    if (!Number.isFinite(form.intervalHours) || form.intervalHours < 6 || form.intervalHours > 720)
      return '执行间隔必须在 6 至 720 小时之间'
    if (!Number.isFinite(form.jitterMinutes) || form.jitterMinutes < 0 || form.jitterMinutes >= form.intervalHours * 60)
      return '随机抖动必须非负且小于执行间隔'
    if (!form.timeZone.trim()) return '请输入 IANA 时区'
    if (form.endMode === 'endAt') {
      const end = new Date(form.endAtLocal)
      if (Number.isNaN(end.getTime()) || end <= start) return '结束时间必须晚于开始时间'
    }
    if (form.endMode === 'duration' && (!Number.isFinite(form.durationHours) || form.durationHours <= 0))
      return '持续时长必须大于 0'
  }
  const budgets = [
    form.maxRequests,
    form.maxInputTokens,
    form.maxOutputTokens,
    form.maxTotalTokens,
    form.maxConcurrentRequests
  ]
  if (budgets.some(value => !Number.isInteger(value) || value <= 0)) return '预算值必须是正整数'
  if (form.maxConcurrentRequests > form.maxRequests) return '并发上限不能超过请求数'
  return ''
}

function buildDefinition(): ModelAuditJobDefinition {
  const existingTargets = new Map(
    (editingJob.value?.targets || []).map(target => [`${target.channelKind}:${target.channelId}`, target])
  )
  const targets = selectedChannelOptions.value.map(option => {
    const setting = targetSetting(option.key)
    const model = setting.model?.trim() || ''
    const existing = existingTargets.get(`${option.kind}:${option.channel.id}`)
    return {
      id: existing?.id || `target:${option.kind}:${option.channel.id}`,
      channelId: option.channel.id!,
      channelKind: option.kind,
      protocol: option.kind,
      ...(model ? { model } : {}),
      ...(setting.thinking ? { thinking: setting.thinking } : {}),
      requestProfile: requestProfile(option.kind)
    }
  })
  let workload: ModelAuditJobDefinition['workload']
  if (form.workloadKind === 'identity') {
    const selections: ModelAuditStrategySelection[] = strategies.value.map(strategy => ({
      strategyId: strategy.ref.id,
      enabled: strategyState(strategy.ref.id).enabled,
      config: { ...strategyState(strategy.ref.id).config }
    }))
    const strategySetVersion =
      editingJob.value?.workload.kind === 'identity' ? editingJob.value.workload.strategySetVersion || '1.0.0' : '1.0.0'
    workload = { kind: 'identity', strategySetVersion, strategies: selections }
  } else {
    const preset = evaluationMode.value
      ? selectedCapabilityEvaluation.value!.preset
      : selectedCapabilityPreset.value!.ref
    workload = { kind: 'capability', capabilityPreset: { ...preset } }
  }
  const startAt = isSingleRun.value ? new Date() : new Date(form.startAtLocal)
  const schedule: ModelAuditJobDefinition['schedule'] = {
    startAt: startAt.toISOString(),
    intervalMs: Math.round((isSingleRun.value ? 6 : form.intervalHours) * 3_600_000),
    timeZone: form.timeZone.trim(),
    jitterMs: Math.round((isSingleRun.value ? 0 : form.jitterMinutes) * 60_000),
    ...(isSingleRun.value ? { oneShot: true } : {})
  }
  if (!isSingleRun.value && form.endMode === 'endAt') schedule.endAt = new Date(form.endAtLocal).toISOString()
  if (!isSingleRun.value && form.endMode === 'duration')
    schedule.durationMs = Math.round(form.durationHours * 3_600_000)
  const analyzer =
    needsAnalyzer.value && analyzerChannelOption.value
      ? {
          channelId: analyzerChannelOption.value.channel.id!,
          channelKind: analyzerChannelOption.value.kind,
          protocol: analyzerChannelOption.value.kind,
          model: form.analyzerModel.trim(),
          thinking: form.analyzerThinking,
          requestProfile: requestProfile(analyzerChannelOption.value.kind)
        }
      : undefined
  const evaluationName = evaluationMode.value
    ? `${selectedChannelOptions.value[0].channel.name} · ${targets[0].model || selectedChannelOptions.value[0].channel.defaultModel || '默认模型'} · ${selectedCapabilityEvaluation.value!.label}`
    : ''
  const evaluationLimits = selectedCapabilityEvaluation.value?.limits
  return {
    name: evaluationName || form.name.trim(),
    workload,
    targets,
    schedule,
    budget: {
      maxRequests: evaluationLimits?.requests ?? form.maxRequests,
      maxInputTokens: evaluationLimits?.inputTokens ?? form.maxInputTokens,
      maxOutputTokens: evaluationLimits?.outputTokens ?? form.maxOutputTokens,
      maxTotalTokens: evaluationLimits?.totalTokens ?? form.maxTotalTokens,
      maxConcurrentRequests: evaluationMode.value ? 1 : form.maxConcurrentRequests
    },
    ...(analyzer ? { analyzer } : {})
  }
}

async function startRun(job: ModelAuditJob) {
  actionKey.value = `${job.id}:run`
  try {
    const response = await api.startModelAuditJobRun(job.id, job.revision)
    showSnackbar(`运行已启动：${response.run.id}`, 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, evaluationMode.value ? '启动评测运行失败' : '启动审计运行失败'), 'error')
    if (isConflict(error)) await loadJobs()
  } finally {
    actionKey.value = ''
  }
}

async function toggleJob(job: ModelAuditJob) {
  const next: ModelAuditJobStatus = job.status === 'enabled' ? 'paused' : 'enabled'
  actionKey.value = `${job.id}:transition`
  try {
    await api.transitionModelAuditJob(job.id, job.revision, next)
    showSnackbar(next === 'enabled' ? '任务已启用' : '任务已暂停', 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '更新任务状态失败'), 'error')
    if (isConflict(error)) await loadJobs()
  } finally {
    actionKey.value = ''
  }
}

function confirmDelete(job: ModelAuditJob) {
  deletingJob.value = job
  deleteDialog.value = true
}

async function deleteJob() {
  const job = deletingJob.value
  if (!job) return
  actionKey.value = `${job.id}:delete`
  try {
    await api.deleteModelAuditJob(job.id, job.revision)
    deleteDialog.value = false
    deletingJob.value = null
    showSnackbar('任务已删除，历史记录保留', 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, evaluationMode.value ? '删除评测任务失败' : '删除审计任务失败'), 'error')
    if (isConflict(error)) {
      await loadJobs()
      deleteDialog.value = false
      deletingJob.value = null
      showSnackbar('任务已被其他操作修改，列表已刷新，请重新确认删除', 'error')
    }
  } finally {
    actionKey.value = ''
  }
}

async function openRunsDialog(job: ModelAuditJob) {
  runsJob.value = job
  runPage.value = 1
  totalRuns.value = 0
  runsDialog.value = true
  await loadRuns(job)
}

function latestRun(job: ModelAuditJob) {
  return latestRuns.value[job.id]
}

function runScore(run?: ModelAuditRunPresentation): number | null {
  const capability = run?.result?.capability
  const score = capability?.score ?? capability?.index
  return typeof score === 'number' && Number.isFinite(score) ? score : null
}

function evaluationScore(job: ModelAuditJob): number | null {
  return runScore(latestRun(job))
}

function evaluationReportId(job: ModelAuditJob): string {
  return latestRun(job)?.result?.reportId || ''
}

function runResultLabel(run?: ModelAuditRunPresentation): string {
  if (!run) return '等待运行'
  if (run.status === 'pending') return '等待评测'
  if (run.status === 'running') return '评测中'
  if (!run.result) return run.status === 'failed' ? '评测失败' : '结果生成中'
  if (run.result.resultStatus === 'insufficient_evidence') return '证据不足'
  if (run.result.resultStatus === 'unsupported') return '不支持评测'
  if (run.result.resultStatus === 'failed') return '评测失败'
  if (run.result.resultStatus === 'partial') return '部分结果'
  return '暂无评分'
}

function evaluationResultMeta(job: ModelAuditJob) {
  const run = latestRun(job)
  const label = runResultLabel(run)
  if (run?.status === 'running' || run?.status === 'pending') return { label, color: 'info' }
  if (run?.result?.resultStatus === 'partial' || run?.result?.resultStatus === 'insufficient_evidence')
    return { label, color: 'warning' }
  if (run?.status === 'failed' || run?.result?.resultStatus === 'failed') return { label, color: 'error' }
  return { label, color: 'default' }
}

function openEvaluationResult(reportId: string) {
  runsDialog.value = false
  void router.push({ name: 'evaluation-report', params: { reportId } })
}

async function loadRuns(job: ModelAuditJob) {
  runsLoading.value = true
  runsError.value = ''
  try {
    const response = await api.listModelAuditRuns(job.id, runPage.value, runPageSize)
    runs.value = response.page.runs
    totalRuns.value = response.page.total
    if (runPage.value > runPageCount.value) {
      runPage.value = runPageCount.value
      await loadRuns(job)
    }
  } catch (error) {
    runsError.value = errorMessage(error, '加载运行记录失败')
  } finally {
    runsLoading.value = false
  }
}

async function loadCurrentRunPage() {
  if (runsJob.value) await loadRuns(runsJob.value)
}

async function cancelRun(run: ModelAuditRunPresentation) {
  actionKey.value = `${run.id}:cancel`
  try {
    await api.cancelModelAuditRun(run.id)
    showSnackbar('运行已取消', 'success')
    if (runsJob.value) await loadRuns(runsJob.value)
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '取消运行失败'), 'error')
    if (isConflict(error) && runsJob.value) await loadRuns(runsJob.value)
  } finally {
    actionKey.value = ''
  }
}

async function openAnalysesDialog(run: ModelAuditRunPresentation) {
  analysesRun.value = run
  analyses.value = []
  analysesDialog.value = true
  await loadAnalyses(run)
}

async function loadAnalyses(run: ModelAuditRunPresentation) {
  analysesLoading.value = true
  analysesError.value = ''
  try {
    const response = await api.listModelAuditModAnalyses(run.id)
    analyses.value = response.page.analyses
    for (const analysis of analyses.value) {
      if (analysis.status === 'awaiting_manual' && !manualResultTexts[analysis.id]) {
        manualResultTexts[analysis.id] = JSON.stringify(
          {
            schema: 'audit.mod-analysis-result.v1',
            verdict: '',
            confidence: 0.5,
            summary: '',
            evidence: [],
            warnings: []
          },
          null,
          2
        )
      }
    }
  } catch (error) {
    analysesError.value = errorMessage(error, '加载 Mod 分析记录失败')
  } finally {
    analysesLoading.value = false
  }
}

async function submitManualAnalysis(analysis: ModelAuditModAnalysisRecord) {
  manualResultErrors[analysis.id] = ''
  let result: Record<string, unknown>
  try {
    const parsed: unknown = JSON.parse(manualResultTexts[analysis.id] || '')
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('结果必须是 JSON 对象')
    result = parsed as Record<string, unknown>
  } catch (error) {
    manualResultErrors[analysis.id] = errorMessage(error, '结果 JSON 无效')
    return
  }
  actionKey.value = `${analysis.id}:manual`
  try {
    await api.importManualModelAuditAnalysis(analysis.id, result)
    showSnackbar('手动分析结果已导入', 'success')
    if (analysesRun.value) await loadAnalyses(analysesRun.value)
  } catch (error) {
    manualResultErrors[analysis.id] = errorMessage(error, '导入手动分析结果失败')
  } finally {
    actionKey.value = ''
  }
}

async function rerunAnalysis(analysis: ModelAuditModAnalysisRecord) {
  actionKey.value = `${analysis.id}:rerun`
  try {
    await api.rerunModelAuditAnalysis(analysis.id)
    showSnackbar('已生成新的分析记录', 'success')
    if (analysesRun.value) await loadAnalyses(analysesRun.value)
  } catch (error) {
    showSnackbar(errorMessage(error, '重新分析失败'), 'error')
  } finally {
    actionKey.value = ''
  }
}

function clearModFiles() {
  for (const key of Object.keys(modFiles)) delete modFiles[key]
}

function addModFile() {
  const fileName = newModFileName.value.trim().replace(/\\/g, '/')
  if (
    !fileName ||
    fileName.startsWith('/') ||
    fileName.includes('..') ||
    Object.prototype.hasOwnProperty.call(modFiles, fileName)
  ) {
    modError.value = '新增文件名不能为空、重复或包含越界路径'
    return
  }
  modFiles[fileName] = ''
  selectedModFile.value = fileName
  newModFileName.value = ''
  modError.value = ''
}

function deleteSelectedModFile() {
  const fileName = selectedModFile.value
  if (!fileName || fileName === 'manifest.json') return
  delete modFiles[fileName]
  selectedModFile.value = modFileNames.value[0] || ''
}

async function openCreateModDialog() {
  if (modExamples.value.length === 0) await loadModExamples()
  editingMod.value = null
  modError.value = ''
  clearModFiles()
  selectedExampleId.value = modExamples.value[0]?.id || ''
  applySelectedExample()
  modDialog.value = true
}

async function openEditModDialog(descriptor: ModelAuditModDescriptor) {
  modError.value = ''
  modSaving.value = true
  try {
    const response = await api.getModelAuditMod(descriptor.reference.id)
    editingMod.value = response.mod
    clearModFiles()
    Object.assign(modFiles, response.mod.files)
    modDirectoryName.value =
      response.mod.descriptor.sourcePath.split(/[\\/]/).filter(Boolean).pop() || descriptor.reference.id
    selectedModFile.value = modFileNames.value[0] || ''
    selectedExampleId.value = ''
    modDialog.value = true
  } catch (error) {
    showSnackbar(errorMessage(error, '读取 Mod 文件失败'), 'error')
  } finally {
    modSaving.value = false
  }
}

function applySelectedExample() {
  const example = modExamples.value.find(item => item.id === selectedExampleId.value)
  if (!example) return
  clearModFiles()
  Object.assign(modFiles, example.files)
  modDirectoryName.value = example.id.replace(/^mod\./, '').replace(/\./g, '-')
  selectedModFile.value = modFileNames.value[0] || ''
}

function closeModDialog() {
  if (modSaving.value) return
  modDialog.value = false
  editingMod.value = null
  modError.value = ''
}

async function saveMod() {
  modError.value = ''
  if (!/^[a-z0-9][a-z0-9._-]*$/.test(modDirectoryName.value.trim())) {
    modError.value = '目录名只能包含小写字母、数字、点、下划线和连字符'
    return
  }
  try {
    const manifest = JSON.parse(modFiles['manifest.json'] || '') as { id?: string }
    if (!manifest.id?.trim()) throw new Error('manifest.json 缺少 id')
    if (editingMod.value && manifest.id !== editingMod.value.descriptor.reference.id)
      throw new Error('编辑时不能修改 Mod ID')
  } catch (error) {
    modError.value = errorMessage(error, 'manifest.json 无效')
    return
  }
  modSaving.value = true
  const wasEditing = Boolean(editingMod.value)
  try {
    if (editingMod.value) {
      await api.updateModelAuditMod(
        editingMod.value.descriptor.reference.id,
        modDirectoryName.value.trim(),
        editingMod.value.descriptor.reference.contentSha256,
        { ...modFiles }
      )
    } else {
      await api.createModelAuditMod(modDirectoryName.value.trim(), { ...modFiles })
    }
    modDialog.value = false
    editingMod.value = null
    await reloadMods()
    showSnackbar(wasEditing ? 'Mod 已更新并加载' : 'Mod 已创建并加载', 'success')
  } catch (error) {
    modError.value = errorMessage(error, '保存 Mod 失败')
  } finally {
    modSaving.value = false
  }
}

function openImportModDialog() {
  importSourcePath.value = ''
  importDirectoryName.value = ''
  modError.value = ''
  importModDialog.value = true
}

async function importMod() {
  modError.value = ''
  if (!importSourcePath.value.trim()) {
    modError.value = '请输入源目录绝对路径'
    return
  }
  modSaving.value = true
  try {
    await api.importModelAuditMod(importSourcePath.value.trim(), importDirectoryName.value.trim())
    importModDialog.value = false
    await reloadMods()
    showSnackbar('Mod 目录已导入并加载', 'success')
  } catch (error) {
    modError.value = errorMessage(error, '导入 Mod 目录失败')
  } finally {
    modSaving.value = false
  }
}

function strategyState(id: string): StrategyState {
  if (!form.strategyStates[id]) form.strategyStates[id] = { enabled: false, config: {} }
  return form.strategyStates[id]
}

function applyIdentityPreset() {
  if (form.identityPresetLevel === 'custom') return
  applyIdentityPresetToState(form, form.identityPresetLevel)
}

function setStrategyEnabled(id: string, enabled: boolean) {
  strategyState(id).enabled = enabled
  // 手动修改策略时切换到自定义档位
  if (form.identityPresetLevel !== 'custom') {
    form.identityPresetLevel = 'custom'
  }
}

function setStrategySampleCount(id: string, value: unknown) {
  strategyState(id).config.sampleCount = Number(value)
  // 手动修改样本数时切换到自定义档位
  if (form.identityPresetLevel !== 'custom') {
    form.identityPresetLevel = 'custom'
  }
}

function strategySampleCountError(strategy: ModelAuditStrategyDescriptor): string {
  if (!strategyState(strategy.ref.id).enabled) return ''
  const property = sampleCountProperty(strategy)
  if (!property) return ''
  const value = strategySampleCount(strategy.ref.id)
  const minimum = property.minimum ?? 1
  const maximum = property.maximum ?? strategy.maximumSamples
  const step = property.multipleOf || 1
  if (!Number.isInteger(value)) return '必须是整数'
  if (value < minimum || value > maximum) return `请输入 ${minimum} 至 ${maximum}`
  if (value % step !== 0) return `必须是 ${step} 的倍数`
  return ''
}

async function reloadQuestionBanks() {
  questionBanksLoading.value = true
  pageError.value = ''
  try {
    const response = await api.reloadModelEvaluationQuestionBanks()
    capabilityPresets.value = response.catalog.presets
    capabilityEvaluationOptions.value = response.catalog.evaluationOptions || []
    questionBanks.value = response.catalog.questionBanks?.banks || []
    questionBankIssues.value = response.catalog.questionBanks?.issues || []
    showSnackbar(`已加载 ${questionBanks.value.length} 个题库`, questionBankIssues.value.length ? 'info' : 'success')
  } catch (error) {
    pageError.value = errorMessage(error, '重新加载能力题库失败')
  } finally {
    questionBanksLoading.value = false
  }
}

function sampleIntervalProperty(strategy: ModelAuditStrategyDescriptor) {
  return strategy.configSchema.properties?.sampleIntervalMs
}

function strategySampleIntervalSeconds(id: string): number {
  return Number(strategyState(id).config.sampleIntervalMs || 0) / 1000
}

function setStrategySampleIntervalSeconds(id: string, value: unknown) {
  strategyState(id).config.sampleIntervalMs = Math.round(Number(value) * 1000)
}

function sampleCountProperty(strategy: ModelAuditStrategyDescriptor) {
  return strategy.configSchema.properties?.sampleCount
}

function defaultStrategyConfig(strategy: ModelAuditStrategyDescriptor): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  for (const [name, property] of Object.entries(strategy.configSchema.properties || {})) {
    if (property.default !== undefined) config[name] = property.default
    else if (property.type === 'integer' || property.type === 'number') config[name] = property.minimum || 1
  }
  return config
}

function versionedRefKey(ref: { id: string; semanticVersion: string; implementationVersion: string }) {
  return JSON.stringify([ref.id, ref.semanticVersion, ref.implementationVersion])
}

function targetSetting(key: string): TargetSetting {
  if (!form.targetSettings[key]) {
    const option = channelOptions.value.find(item => item.key === key)
    const defaultThinking = option && thinkingOptions(option.kind).some(item => item.value === 'medium') ? 'medium' : ''
    form.targetSettings[key] = { model: '', thinking: defaultThinking }
  }
  return form.targetSettings[key]
}

function targetModelDiscovery(key: string): TargetModelDiscovery {
  if (!targetModelDiscoveries[key]) {
    const defaultModel = channelOptions.value.find(item => item.key === key)?.channel.defaultModel?.trim() || ''
    targetModelDiscoveries[key] = {
      models: defaultModel ? [{ title: defaultModel, value: defaultModel }] : [],
      loading: false,
      loaded: false,
      error: '',
      requestId: 0
    }
  }
  return targetModelDiscoveries[key]
}

async function loadTargetModels(option: ChannelOption, force = false) {
  const state = targetModelDiscovery(option.key)
  if (state.loading || (state.loaded && !force)) return

  const channel = option.channel
  const baseUrl = channel.baseUrl?.trim() || channel.baseUrls?.find(url => url.trim())?.trim() || ''
  if (!baseUrl) {
    state.error = '渠道未配置有效的上游地址，无法获取模型列表。'
    return
  }

  const requestId = state.requestId + 1
  state.requestId = requestId
  state.loading = true
  state.error = ''

  try {
    const response = await api.discoverUpstreamModels({
      baseUrl,
      baseUrls: channel.baseUrls,
      apiKey: channel.apiKeys?.[0] || '',
      serviceType: channel.serviceType,
      insecureSkipVerify: channel.insecureSkipVerify,
      proxyMode: channel.proxyMode,
      proxyUrl: channel.proxyUrl
    })
    if (state.requestId !== requestId) return
    if (!Array.isArray(response.data)) throw new Error('上游模型接口返回格式无效')

    const defaultModel = channel.defaultModel?.trim() || ''
    const discoveredModels = response.data
      .map(item => item.id?.trim())
      .filter((model): model is string => Boolean(model))
    const models = Array.from(new Set([defaultModel, ...discoveredModels].filter(Boolean)))
    state.models = models.map(model => ({ title: model, value: model }))
    state.loaded = true
    if (discoveredModels.length === 0) {
      state.error = '上游未返回可用模型，可直接输入模型名称或留空使用渠道默认值。'
    }
  } catch (error) {
    if (state.requestId !== requestId) return
    state.error = `获取模型列表失败：${errorMessage(error, '未知错误')}。仍可直接输入模型名称。`
  } finally {
    if (state.requestId === requestId) state.loading = false
  }
}

function thinkingOptions(kind: ApiTab) {
  const descriptor = protocolDescriptors.value.find(item => item.protocol === kind)
  return [
    { title: '协议默认值', value: '' },
    ...(descriptor?.thinking.mappings || []).map(mapping => ({
      title: thinkingLabel(mapping.level),
      value: mapping.level
    }))
  ]
}

function requestProfile(kind: ApiTab): string {
  const profiles = protocolDescriptors.value.find(item => item.protocol === kind)?.profiles || []
  return profiles.find(item => item.id.includes('standard'))?.id || profiles[0]?.id || ''
}

function canEdit(job: ModelAuditJob) {
  return (
    catalogReady.value &&
    !channelLoading.value &&
    (job.status === 'draft' || job.status === 'enabled' || job.status === 'paused')
  )
}

function canToggle(job: ModelAuditJob) {
  return job.status === 'draft' || job.status === 'enabled' || job.status === 'paused'
}

function statusMeta(status: ModelAuditJobStatus) {
  const values: Record<ModelAuditJobStatus, { label: string; color: string }> = {
    draft: { label: '草稿', color: 'default' },
    enabled: { label: '已启用', color: 'success' },
    running: { label: '运行中', color: 'info' },
    paused: { label: '已暂停', color: 'warning' },
    expired: { label: '已结束', color: 'default' },
    deleted: { label: '已删除', color: 'error' }
  }
  return values[status]
}

function analysisModeMeta(mode: ModelAuditModAnalysisMode) {
  const values: Record<ModelAuditModAnalysisMode, { label: string; color: string }> = {
    llm: { label: 'AI 分析', color: 'primary' },
    python: { label: 'Python 自动分析', color: 'success' },
    manual: { label: '手动外部分析', color: 'warning' }
  }
  return values[mode]
}

function analysisStatusMeta(status: ModelAuditModAnalysisRecord['status']) {
  const values: Record<ModelAuditModAnalysisRecord['status'], { label: string; color: string }> = {
    pending: { label: '等待分析', color: 'default' },
    running: { label: '分析中', color: 'info' },
    awaiting_manual: { label: '等待手动结果', color: 'warning' },
    completed: { label: '分析完成', color: 'success' },
    failed: { label: '分析失败', color: 'error' }
  }
  return values[status]
}

function modName(id: string) {
  return modByID.value.get(id)?.name || id
}

function strategyModMeta(id: string) {
  const mode = modByID.value.get(id)?.analysisMode
  return mode ? analysisModeMeta(mode) : null
}

function manualAnalysisPaths(analysis: ModelAuditModAnalysisRecord) {
  return [
    { label: 'Mod 源目录', value: analysis.sourceDirectory || '' },
    { label: '冻结快照', value: analysis.snapshotDirectory || '' },
    { label: '本次运行目录', value: analysis.workingDirectory || '' },
    { label: '执行命令', value: analysis.command?.join(' ') || '' }
  ].filter(item => item.value)
}

async function copyText(value: string, successMessage: string) {
  try {
    await navigator.clipboard.writeText(value)
    showSnackbar(successMessage, 'success')
  } catch (error) {
    showSnackbar(errorMessage(error, '复制失败'), 'error')
  }
}

function formatJSON(value: unknown) {
  return JSON.stringify(value, null, 2)
}

function fileIcon(fileName: string) {
  if (fileName.endsWith('.json')) return 'mdi-code-braces'
  if (fileName.endsWith('.md')) return 'mdi-text'
  if (fileName.endsWith('.py')) return 'mdi-robot-outline'
  return 'mdi-paperclip'
}

function fileLanguage(fileName: string) {
  const extension = fileName.split('.').pop()?.toUpperCase()
  return extension || 'TEXT'
}

function workloadLabel(job: ModelAuditJob) {
  if (evaluationMode.value && job.workload.kind === 'capability') {
    const option = capabilityEvaluationOptions.value.find(
      item => versionedRefKey(item.preset) === versionedRefKey(job.workload.capabilityPreset!)
    )
    return option?.label || '能力评测'
  }
  return job.workload.kind === 'identity' ? '身份与混用' : '通用能力'
}

function workloadDetail(job: ModelAuditJob) {
  if (job.workload.kind === 'identity')
    return `${job.workload.strategies?.filter(item => item.enabled).length || 0} 个策略`
  if (evaluationMode.value) {
    const option = capabilityEvaluationOptions.value.find(
      item => versionedRefKey(item.preset) === versionedRefKey(job.workload.capabilityPreset!)
    )
    if (option) return `${option.bankName} · ${option.questionCount} 道题`
  }
  const preset = capabilityPresets.value.find(item => item.ref.id === job.workload.capabilityPreset?.id)
  return preset ? capabilityModeLabel(preset.mode) : job.workload.capabilityPreset?.id || '未解析预设'
}

function targetSummary(job: ModelAuditJob) {
  return [...new Set(job.targets.map(target => protocolLabel(target.protocol)))].join('、')
}

function protocolLabel(kind: ApiTab) {
  return protocolNames[kind] || kind
}

function strategyName(id: string) {
  const names: Record<string, string> = {
    'identity.metadata-consistency': '元数据一致性',
    'identity.sol.reasoning-profile': 'Reasoning 轮廓',
    'identity.sol.rewrite-check': '32/48 改写检查',
    'identity.sol.synthetic-coverage': '合成覆盖检查',
    'identity.sol.probability-profile': '概率行为分布',
    'identity.sol.mode-difference': '模式差分',
    'identity.sol.variation-stability': '变形稳定性'
  }
  return names[id] || modName(id)
}

function capabilityModeLabel(mode: ModelAuditCapabilityPreset['mode']) {
  return mode === 'quick' ? '快速' : mode === 'standard' ? '标准' : '深入'
}

function thinkingLabel(level: string) {
  const labels: Record<string, string> = {
    off: '关闭',
    minimal: '最小',
    low: '低',
    medium: '中',
    high: '高',
    xhigh: '超高',
    adaptive: '自适应'
  }
  return labels[level] || level
}

function formatInterval(milliseconds: number) {
  const hours = milliseconds / 3_600_000
  return hours % 24 === 0 ? `每 ${hours / 24} 天` : `每 ${hours} 小时`
}

function formatDateTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString('zh-CN', { hour12: false })
}

function formatCompactNumber(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

function toLocalDateTime(date: Date) {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function runStatusLabel(status: ModelAuditRunPresentation['status']) {
  const labels: Record<ModelAuditRunPresentation['status'], string> = {
    pending: '等待中',
    running: '运行中',
    completed: '已完成',
    partial: '部分完成',
    failed: '失败',
    cancelled: '已取消'
  }
  return labels[status]
}

function runStatusColor(status: ModelAuditRunPresentation['status']) {
  if (status === 'completed') return 'success'
  if (status === 'running' || status === 'pending') return 'info'
  if (status === 'partial') return 'warning'
  return 'error'
}

function showSnackbar(message: string, color: 'success' | 'error' | 'info') {
  snackbarText.value = message
  snackbarColor.value = color
  snackbar.value = true
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback
}

function isConflict(error: unknown) {
  return error instanceof ApiError && error.status === 409
}

watch(
  () => [...form.channelKeys],
  keys => {
    const selectedKeys = new Set(keys)
    for (const option of channelOptions.value) {
      if (selectedKeys.has(option.key) && !option.disabled) void loadTargetModels(option)
    }
  }
)

watch(
  () => form.analyzerChannelKey,
  key => {
    const option = channelOptions.value.find(item => item.key === key && !item.disabled)
    if (!option) return
    void loadTargetModels(option)
    if (!form.analyzerModel.trim()) form.analyzerModel = option.channel.defaultModel || ''
    if (!thinkingOptions(option.kind).some(item => item.value === form.analyzerThinking)) {
      form.analyzerThinking = thinkingOptions(option.kind).some(item => item.value === 'medium') ? 'medium' : ''
    }
  }
)

watch(evaluationMode, async () => {
  activeView.value = 'jobs'
  page.value = 1
  jobs.value = []
  totalJobs.value = 0
  await Promise.all([loadCatalogs(), loadJobs(), ...(evaluationMode.value ? [] : [loadMods(), loadModExamples()])])
})

onMounted(async () => {
  await Promise.all([
    loadCatalogs(),
    loadChannels(),
    loadJobs(),
    ...(evaluationMode.value ? [] : [loadMods(), loadModExamples()])
  ])
})
</script>

<style scoped>
.audit-jobs-view {
  width: 100%;
  max-width: 1500px;
  margin: 0 auto;
}

.audit-page-header,
.audit-page-actions,
.audit-dialog-title,
.audit-dialog-actions {
  display: flex;
  align-items: center;
}

.audit-page-header {
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.audit-page-actions {
  gap: 8px;
}

.audit-view-tabs {
  margin-bottom: 20px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-list {
  width: 100%;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-list-header,
.audit-list-row {
  display: grid;
  grid-template-columns:
    minmax(260px, 1.8fr) minmax(160px, 1fr) minmax(140px, 0.9fr) minmax(165px, 1fr) minmax(125px, 0.8fr)
    minmax(300px, auto);
  gap: 18px;
  align-items: center;
}

.audit-list--evaluation .audit-list-header,
.audit-list--evaluation .audit-list-row {
  grid-template-columns:
    minmax(260px, 1.55fr) minmax(170px, 0.9fr) minmax(180px, 1fr) minmax(110px, 0.55fr)
    minmax(230px, auto);
}

.evaluation-score-cell {
  min-height: 52px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: flex-start;
}

.evaluation-score-value {
  color: rgb(var(--v-theme-primary));
  font-size: 1.5rem;
  font-weight: 800;
  line-height: 1.15;
}

.audit-list-header {
  min-height: 44px;
  padding: 0 16px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
  font-weight: 700;
  letter-spacing: 0.02em;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-list-row {
  min-height: 98px;
  padding: 16px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  transition: background-color 160ms ease;
}

.audit-list-row:hover {
  background: rgba(var(--v-theme-on-surface), 0.04);
}

.audit-mod-list {
  width: 100%;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-mod-list-header,
.audit-mod-row {
  display: grid;
  grid-template-columns: minmax(280px, 1.6fr) minmax(160px, 0.8fr) 100px minmax(260px, 1fr) 120px;
  gap: 18px;
  align-items: center;
}

.audit-mod-list-header {
  min-height: 44px;
  padding: 0 16px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
  font-weight: 700;
  letter-spacing: 0.02em;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-mod-row {
  min-height: 100px;
  padding: 16px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  transition: background-color 160ms ease;
}

.audit-mod-row:hover {
  background: rgba(var(--v-theme-on-surface), 0.04);
}

.audit-path-text {
  overflow-wrap: anywhere;
}

.audit-job-title-line,
.audit-row-actions,
.audit-target-identity,
.audit-preset-summary {
  display: flex;
  align-items: center;
}

.audit-job-title-line {
  flex-wrap: wrap;
  gap: 8px;
}

.audit-job-name,
.audit-target-summary {
  overflow-wrap: anywhere;
}

.audit-row-actions {
  justify-content: flex-end;
  gap: 2px;
}

.audit-icon-btn {
  min-width: 44px;
  min-height: 44px;
}

.audit-empty-state {
  padding: 64px 16px;
  text-align: center;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-dialog-title {
  min-height: 60px;
  justify-content: space-between;
  gap: 16px;
  font-size: 1.1rem;
}

.audit-form-body {
  padding: 24px;
}

.audit-section-heading {
  margin-bottom: 14px;
  font-size: 0.875rem;
  font-weight: 700;
}

.audit-section-heading-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 32px;
  margin-bottom: 14px;
}

.audit-section-heading-row .audit-section-heading {
  margin-bottom: 0;
}

.audit-mode-toggle,
.audit-workload-toggle {
  display: inline-grid;
  grid-template-columns: repeat(2, minmax(160px, 1fr));
  height: auto;
}

.audit-mode-toggle .v-btn,
.audit-workload-toggle .v-btn {
  min-height: 48px;
}

.audit-identity-section {
  margin-top: 6px;
}

.audit-preset-selector {
  padding: 16px 20px;
  background: rgba(var(--v-theme-surface-variant), 0.3);
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 8px;
}

.audit-preset-toggle {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  width: 100%;
  height: auto;
}

.audit-preset-toggle .v-btn {
  min-height: 64px;
  padding: 10px 8px;
}

.preset-btn-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.preset-btn-title {
  font-size: 0.9375rem;
  font-weight: 700;
  line-height: 1.2;
}

.preset-btn-subtitle {
  font-size: 0.6875rem;
  font-weight: 400;
  opacity: 0.75;
  line-height: 1.3;
}

.preset-description {
  margin-top: 12px;
  padding: 10px 0;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
  line-height: 1.5;
}

.audit-form-grid {
  display: grid;
  gap: 16px;
}

.audit-form-grid--two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.audit-form-grid--basic {
  grid-template-columns: minmax(0, 2fr) minmax(240px, 1fr);
}

.evaluation-form {
  display: grid;
  gap: 18px;
  padding: 4px 0;
}

.evaluation-summary {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
  padding: 16px;
  background: rgba(var(--v-theme-surface-variant), 0.3);
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 8px;
}

.evaluation-summary > div {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.evaluation-summary span {
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.75rem;
}

.evaluation-summary strong {
  overflow-wrap: anywhere;
  font-size: 0.875rem;
}

.audit-form-grid--three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.audit-form-grid--five {
  grid-template-columns: repeat(5, minmax(130px, 1fr));
}

.audit-strategy-list,
.audit-target-editor {
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-strategy-row {
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr) minmax(180px, 280px);
  gap: 16px;
  align-items: start;
  min-height: 84px;
  padding: 14px 0;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-strategy-checkbox {
  display: flex;
  align-items: center;
  padding-top: 4px;
}

.audit-strategy-content {
  min-width: 0;
}

.audit-strategy-controls {
  display: grid;
  gap: 10px;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
}

.audit-analyzer-section {
  padding: 20px 0 8px 0;
  margin-top: 4px;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-analyzer-section > div:first-child {
  margin-bottom: 6px;
}

.audit-analyzer-section .text-body-2 {
  font-weight: 600;
}

.audit-analyzer-section .text-caption {
  display: block;
  margin-top: 4px;
  line-height: 1.5;
}

.audit-preset-summary {
  flex-wrap: wrap;
  gap: 8px 20px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
}

.audit-target-row {
  display: grid;
  grid-template-columns: minmax(240px, 1.3fr) minmax(240px, 1fr) minmax(160px, 0.8fr);
  gap: 16px;
  align-items: center;
  min-height: 80px;
  padding: 14px 0;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-target-identity {
  min-width: 0;
  flex-wrap: wrap;
  gap: 10px;
}

.audit-target-identity .text-caption {
  max-width: 100%;
  overflow-wrap: anywhere;
}

.audit-target-model {
  min-width: 0;
}

.audit-model-feedback {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  color: rgb(var(--v-theme-warning));
  font-size: 0.75rem;
  line-height: 1.5;
}

.audit-model-feedback span {
  min-width: 0;
  overflow-wrap: anywhere;
}

.audit-model-feedback .v-btn {
  flex: 0 0 auto;
  margin-left: auto;
}

.audit-advanced-toggle {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
  width: 100%;
  min-height: 48px;
  padding: 0;
  color: inherit;
  font: inherit;
  text-align: left;
  background: transparent;
  border: 0;
  cursor: pointer;
}

.audit-advanced-toggle:focus-visible {
  outline: 2px solid rgb(var(--v-theme-primary));
  outline-offset: 4px;
}

.audit-advanced-toggle__label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 0.875rem;
  font-weight: 700;
}

.audit-advanced-summary {
  min-width: 0;
  overflow: hidden;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.audit-advanced-content {
  padding-top: 18px;
}

.audit-subsection-heading {
  margin-bottom: 12px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.75rem;
  font-weight: 700;
}

.audit-dialog-actions {
  min-height: 64px;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 20px;
}

.audit-runs-list {
  width: 100%;
}

.audit-runs-pagination {
  display: flex;
  justify-content: center;
  padding: 16px;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-run-row {
  display: grid;
  grid-template-columns: minmax(260px, 1.5fr) minmax(120px, 0.6fr) minmax(120px, 0.6fr) auto;
  gap: 16px;
  align-items: center;
  min-height: 76px;
  padding: 12px 20px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-run-row--evaluation {
  grid-template-columns: minmax(240px, 1.35fr) minmax(110px, 0.55fr) minmax(110px, 0.55fr) minmax(100px, 0.45fr) auto;
}

.audit-run-result {
  display: flex;
  align-items: baseline;
  gap: 3px;
  font-size: 0.875rem;
}

.audit-run-result strong {
  color: rgb(var(--v-theme-primary));
  font-size: 1.25rem;
}

.audit-run-actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}

.audit-analysis-body {
  min-height: 240px;
  padding: 20px;
}

.audit-analysis-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  width: 100%;
  padding-right: 12px;
}

.audit-analysis-meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px 24px;
}

.audit-analysis-meta > div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.audit-analysis-meta span,
.audit-path-row > span {
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.75rem;
}

.audit-analysis-meta code,
.audit-path-row code {
  overflow-wrap: anywhere;
  white-space: normal;
}

.audit-path-list {
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-path-row {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr) 44px;
  gap: 12px;
  align-items: center;
  min-height: 48px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-method-block pre,
.audit-raw-details pre {
  max-height: 360px;
  margin: 0;
  padding: 14px;
  overflow: auto;
  color: rgb(var(--v-theme-on-surface));
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 0.78rem;
  line-height: 1.6;
  white-space: pre-wrap;
  background: rgba(var(--v-theme-on-surface), 0.045);
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 4px;
}

.audit-raw-details summary {
  min-height: 44px;
  padding: 12px 0;
  cursor: pointer;
  font-weight: 600;
}

.audit-mod-editor-body {
  padding: 22px;
}

.audit-mod-editor {
  display: grid;
  grid-template-columns: minmax(190px, 240px) minmax(0, 1fr);
  min-height: 520px;
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-mod-files {
  padding: 8px;
  border-right: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-mod-file {
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
  width: 100%;
  min-height: 44px;
  padding: 8px 10px;
  color: inherit;
  font: inherit;
  text-align: left;
  background: transparent;
  border: 0;
  border-radius: 4px;
  cursor: pointer;
}

.audit-mod-file:hover,
.audit-mod-file--active {
  background: rgba(var(--v-theme-primary), 0.1);
}

.audit-mod-file:focus-visible {
  outline: 2px solid rgb(var(--v-theme-primary));
  outline-offset: 1px;
}

.audit-mod-file span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.audit-binary-note {
  margin: 10px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.75rem;
  line-height: 1.5;
}

.audit-mod-file-add {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 40px;
  gap: 4px;
  align-items: center;
  margin-top: 10px;
}

.audit-mod-file-editor {
  min-width: 0;
  padding: 12px;
}

.audit-mod-file-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 36px;
  margin-bottom: 8px;
}

.audit-code-input :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 0.8rem;
  line-height: 1.55;
}

@media (max-width: 1100px) {
  .audit-list-header,
  .audit-list-row {
    grid-template-columns: minmax(240px, 1.6fr) minmax(150px, 1fr) minmax(130px, 0.8fr) minmax(160px, 1fr) minmax(
        300px,
        auto
      );
  }

  .audit-list-header > :nth-child(5),
  .audit-list-row > :nth-child(5) {
    display: none;
  }

  .audit-form-grid--five {
    grid-template-columns: repeat(3, minmax(150px, 1fr));
  }

  .audit-mod-list-header,
  .audit-mod-row {
    grid-template-columns: minmax(260px, 1.5fr) minmax(160px, 0.9fr) 100px minmax(120px, auto);
  }

  .audit-mod-list-header > :nth-child(4),
  .audit-mod-row > :nth-child(4) {
    display: none;
  }
}

@media (max-width: 760px) {
  .audit-page-header {
    align-items: flex-start;
  }

  .audit-page-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .audit-list-header {
    display: none;
  }

  .audit-mod-list-header {
    display: none;
  }

  .audit-mod-row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
  }

  .audit-list-row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
    min-height: 0;
    padding: 18px 8px;
  }

  .audit-list-row > [data-label] {
    display: grid;
    grid-template-columns: 100px minmax(0, 1fr);
    gap: 12px;
    align-items: start;
  }

  .audit-list-row > [data-label]::before {
    content: attr(data-label);
    color: rgb(var(--v-theme-on-surface-variant));
    font-size: 0.8125rem;
    font-weight: 700;
  }

  .audit-list-row > .audit-row-actions {
    position: relative;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding-left: 112px;
  }

  .audit-list-row > .audit-row-actions::before {
    position: absolute;
    top: 14px;
    left: 0;
  }

  .audit-job-main > * {
    grid-column: 2;
  }

  .audit-row-actions {
    justify-content: flex-start;
  }

  .audit-form-body {
    padding: 18px 16px;
  }

  .audit-form-grid--two,
  .audit-form-grid--basic,
  .audit-form-grid--three,
  .audit-form-grid--five,
  .audit-target-row,
  .audit-run-row {
    grid-template-columns: 1fr;
  }

  .evaluation-summary {
    grid-template-columns: 1fr;
  }

  .audit-mode-toggle,
  .audit-workload-toggle {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    width: 100%;
  }

  .audit-mode-toggle .v-btn,
  .audit-workload-toggle .v-btn {
    min-width: 0;
    padding-inline: 12px;
  }

  .audit-preset-toggle {
    grid-template-columns: repeat(2, 1fr);
  }

  .audit-preset-toggle .v-btn {
    min-height: 56px;
  }

  .preset-btn-title {
    font-size: 0.875rem;
  }

  .preset-btn-subtitle {
    font-size: 0.625rem;
  }

  .audit-section-heading-row {
    align-items: flex-start;
  }

  .audit-advanced-summary {
    display: none;
  }

  .audit-strategy-row {
    grid-template-columns: 56px minmax(0, 1fr);
  }

  .audit-strategy-controls {
    grid-column: 2;
    width: 100%;
  }

  .audit-analyzer-section {
    padding-left: 0;
  }

  .audit-analysis-meta,
  .audit-mod-editor {
    grid-template-columns: 1fr;
  }

  .audit-mod-files {
    display: flex;
    overflow-x: auto;
    border-right: 0;
    border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  }

  .audit-mod-file {
    flex: 0 0 auto;
    width: auto;
    max-width: 220px;
  }

  .audit-path-row {
    grid-template-columns: 1fr 44px;
  }

  .audit-path-row > span {
    grid-column: 1 / -1;
    padding-top: 8px;
  }

  .audit-run-row {
    gap: 10px;
  }

  .audit-run-actions {
    justify-content: flex-start;
  }
}

@media (prefers-reduced-motion: reduce) {
  .audit-list-row {
    transition: none;
  }
}
</style>
