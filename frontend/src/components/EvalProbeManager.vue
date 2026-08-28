<template>
  <v-navigation-drawer :model-value="modelValue" location="end" temporary width="560" @update:model-value="emit('update:modelValue', $event)">
    <div class="pa-4">
      <div class="d-flex align-center ga-2 mb-4">
        <div class="text-h6">题目</div>
        <v-spacer />
        <v-btn v-if="mode === 'list'" size="small" variant="tonal" prepend-icon="mdi-plus" @click="startCreate">新增</v-btn>
        <v-btn v-else size="small" variant="text" @click="mode = 'list'">返回列表</v-btn>
        <v-btn icon="mdi-close" size="small" variant="text" @click="emit('update:modelValue', false)" />
      </div>

      <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mb-3" closable @click:close="error = ''">
        {{ error }}
      </v-alert>

      <!-- 列表 -->
      <template v-if="mode === 'list'">
        <v-list density="compact" bg-color="transparent">
          <v-list-item v-for="probe in probes" :key="probe.id" class="px-2">
            <v-list-item-title class="text-body-2">
              {{ probe.name }}
              <v-chip v-if="probe.builtin" size="x-small" variant="tonal" class="ml-1">内置</v-chip>
            </v-list-item-title>
            <v-list-item-subtitle v-if="probe.description" class="text-caption mb-1">
              {{ probe.description }}
            </v-list-item-subtitle>
            <v-list-item-subtitle class="text-caption">
              {{ probe.category === 'authenticity' ? '真伪' : '智商' }} · 抽取 {{ probe.extract.kind }} ·
              判定 {{ probe.judge.kind }} · {{ probe.sampleCount }} 次 · {{ probe.cheap ? '便宜' : '费' }}
            </v-list-item-subtitle>
            <template #append>
              <v-btn
                v-if="probe.builtin"
                icon="mdi-content-copy"
                size="x-small"
                variant="text"
                title="复制成自建题再改"
                @click="startCopy(probe)"
              />
              <template v-else>
                <v-btn icon="mdi-pencil" size="x-small" variant="text" @click="startEdit(probe)" />
                <v-btn icon="mdi-delete" size="x-small" variant="text" color="error" @click="askDelete(probe)" />
              </template>
            </template>
          </v-list-item>
        </v-list>
        <div class="text-caption text-medium-emphasis mt-4">
          内置题只能复制后改。
        </div>
      </template>

      <!-- 逐步表单 -->
      <template v-else>
        <div class="d-flex ga-2 mb-4">
          <v-chip
            v-for="(label, index) in STEP_LABELS"
            :key="label"
            size="small"
            :color="step === index ? 'primary' : undefined"
            :variant="step === index ? 'flat' : 'outlined'"
          >
            {{ index + 1 }}. {{ label }}
          </v-chip>
        </div>

        <v-window v-model="step">
          <!-- 第一步：题面 -->
          <v-window-item :value="0">
            <v-text-field v-model.trim="form.name" label="名称" density="compact" variant="outlined" class="mb-3" hide-details />
            <v-text-field
              v-model.trim="form.slug"
              label="slug（英文唯一标识）"
              placeholder="auth-xxx / iq-xxx"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <v-select
              v-model="form.category"
              :items="CATEGORY_ITEMS"
              item-title="title"
              item-value="value"
              label="分类"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <v-textarea
              v-model="form.prompt"
              label="题面 prompt"
              rows="5"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <v-textarea
              v-model="form.system"
              label="system（可空）"
              rows="2"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <div class="d-flex ga-3 mb-3">
              <v-text-field v-model.number="form.maxTokens" type="number" label="maxTokens" density="compact" variant="outlined" hide-details />
              <v-text-field v-model.number="form.temperature" type="number" step="0.1" label="温度" density="compact" variant="outlined" hide-details />
              <v-text-field v-model.number="form.sampleCount" type="number" label="采样次数" density="compact" variant="outlined" hide-details />
            </div>
            <v-select
              v-model="form.thinking"
              :items="THINKING_ITEMS"
              item-title="title"
              item-value="value"
              label="思考"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <v-select
              v-model="form.applicableServiceTypes"
              :items="SERVICE_TYPE_ITEMS"
              label="适用 serviceType"
              multiple
              chips
              density="compact"
              variant="outlined"
              hide-details
            />
          </v-window-item>

          <!-- 第二步：判定方式 -->
          <v-window-item :value="1">
            <v-select
              v-model="form.extractKind"
              :items="EXTRACT_ITEMS"
              label="抽取方式"
              density="compact"
              variant="outlined"
              class="mb-3"
              hide-details
            />
            <v-select
              v-model="form.judgeKind"
              :items="JUDGE_ITEMS"
              label="判定方式"
              density="compact"
              variant="outlined"
              class="mb-4"
              hide-details
            />

            <template v-if="form.judgeKind === 'exact'">
              <v-text-field v-model="form.expected" label="期望文本（命中即通过）" density="compact" variant="outlined" class="mb-3" hide-details />
              <v-switch v-model="form.caseInsensitive" label="忽略大小写" color="primary" density="compact" hide-details />
            </template>

            <template v-else-if="form.judgeKind === 'regex'">
              <v-text-field v-model="form.pattern" label="正则" density="compact" variant="outlined" hide-details />
            </template>

            <template v-else-if="form.judgeKind === 'numeric'">
              <v-text-field v-model.number="form.expectedNumber" type="number" label="期望数字" density="compact" variant="outlined" hide-details />
            </template>

            <template v-else-if="form.judgeKind === 'protocol'">
              <v-switch v-model="form.expectModelEcho" label="要求回显请求模型" color="primary" density="compact" hide-details />
              <v-switch v-model="form.expectUsage" label="要求带 usage" color="primary" density="compact" hide-details />
              <v-text-field
                v-model.trim="form.expectContentTypes"
                label="必须出现的内容类型（逗号分隔，如 text,thinking）"
                density="compact"
                variant="outlined"
                class="mb-3 mt-3"
                hide-details
              />
              <v-select
                v-model="form.expectThinking"
                :items="EXPECT_ITEMS"
                label="thinking 期望"
                density="compact"
                variant="outlined"
                class="mb-3"
                hide-details
              />
              <v-select
                v-model="form.expectSignature"
                :items="SIGNATURE_ITEMS"
                label="signature 期望"
                density="compact"
                variant="outlined"
                hide-details
              />
            </template>

            <template v-else-if="form.judgeKind === 'distribution'">
              <div class="text-caption text-medium-emphasis mb-3">
                指纹类必须搭配抽取 numbers 且采样 ≥ 2；采样超过 3 次就不再算便宜套件，不能挂值班。
              </div>
              <v-text-field v-model.number="form.minSamples" type="number" label="最少样本数" density="compact" variant="outlined" class="mb-3" hide-details />
              <v-text-field
                v-model.number="form.suspectUniqueRatio"
                type="number"
                step="0.05"
                label="去重比例低于此值记存疑"
                density="compact"
                variant="outlined"
                class="mb-3"
                hide-details
              />
              <v-switch v-model="form.failIfIdentical" label="全部相同直接判失败" color="primary" density="compact" hide-details />
              <v-switch v-model="form.compareHistogram" label="跨渠道比对直方图（只提示不改结论）" color="primary" density="compact" hide-details />
            </template>

            <template v-else-if="form.judgeKind === 'rubric'">
              <div class="text-caption text-medium-emphasis mb-3">
                裁判是被测渠道自己的第二次请求，因此真伪类禁止 rubric，且 rubric 不能进值班套件。
              </div>
              <v-textarea v-model="form.analysisPrompt" label="分析说明（判定标准）" rows="6" density="compact" variant="outlined" hide-details />
            </template>

            <template v-else-if="form.judgeKind === 'observe'">
              <div class="text-caption text-medium-emphasis mb-3">
                只判「有没有思考 token」。不判占比——上游的 output 是否已经把思考算进去，各家口径不一
                （Anthropic / OpenAI 含、xAI 不含、Gemini 文档自相矛盾），中转还会改写 usage，按占比设阈值等于给自己造误报。
                占比会照样算出来写进结果详情，只是不参与判定。
              </div>
              <v-switch
                v-model="form.expectThinkingUsage"
                label="要求观测到思考用量（拿不到字段记 insufficient，回报 0 才记存疑）"
                color="primary"
                density="compact"
                hide-details
              />
            </template>

            <div v-else class="text-caption text-medium-emphasis">这种判定方式没有额外参数。</div>
          </v-window-item>

          <!-- 第三步：校验入库 -->
          <v-window-item :value="2">
            <div class="text-body-2 mb-3">校验会先问后端：这条题能不能被现有引擎跑起来。</div>

            <v-alert v-if="report && report.ok" type="success" variant="tonal" density="compact" class="mb-3">
              可以入库。{{ report.cheap ? '属于便宜题，可进值班套件。' : '不便宜，只能手动跑。' }}
            </v-alert>
            <v-alert v-else-if="report && report.mode === 'needs_new_grader'" type="warning" variant="tonal" density="compact" class="mb-3">
              需要新的 grader，不要硬塞：引擎不认识
              <template v-if="report.extractKind">抽取 <code>{{ report.extractKind }}</code></template>
              <template v-if="report.judgeKind"> 判定 <code>{{ report.judgeKind }}</code></template>
              。先在 <code>internal/eval</code> 里实现对应 kind，再回来加题。
            </v-alert>
            <v-alert v-else-if="report && report.mode === 'unsupported_grader'" type="warning" variant="tonal" density="compact" class="mb-3">
              这种判定组合引擎明确不做（例如真伪题用 rubric 自评）。换一种判定方式，不要写新 grader 绕过去。
            </v-alert>
            <v-alert v-else-if="report" type="error" variant="tonal" density="compact" class="mb-3">题面还不合格。</v-alert>

            <ul v-if="report?.errors?.length" class="text-body-2 mb-3 pl-5">
              <li v-for="item in report.errors" :key="item">{{ item }}</li>
            </ul>
          </v-window-item>
        </v-window>

        <div class="d-flex ga-2 mt-4">
          <v-btn v-if="step > 0" variant="text" @click="step -= 1">上一步</v-btn>
          <v-spacer />
          <v-btn v-if="step < 2" color="primary" variant="tonal" @click="goNext">下一步</v-btn>
          <template v-else>
            <v-btn variant="tonal" :loading="validating" @click="runValidate">校验</v-btn>
            <v-btn color="primary" :disabled="!report?.ok" :loading="saving" @click="save">
              {{ editingId ? '保存' : '入库' }}
            </v-btn>
          </template>
        </div>
      </template>
    </div>

    <v-dialog v-model="deleteDialog" max-width="420">
      <v-card>
        <v-card-title class="text-body-1">删除题目</v-card-title>
        <v-card-text class="text-body-2">
          删除「{{ pendingDelete?.name }}」后，引用它的套件会在下次运行时报错，需要先把它从套件里摘掉。
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="deleteDialog = false">取消</v-btn>
          <v-btn color="error" :loading="deleting" @click="confirmDelete">删除</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </v-navigation-drawer>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { api, type EvalProbe, type EvalValidateReport } from '@/services/api'

defineProps<{
  modelValue: boolean
  probes: EvalProbe[]
}>()

const emit = defineEmits<{
  'update:modelValue': [boolean]
  changed: []
}>()

const STEP_LABELS = ['题面', '判定方式', '校验入库']
const CATEGORY_ITEMS = [
  { title: '真伪（authenticity）', value: 'authenticity' },
  { title: '智商（iq）', value: 'iq' }
]
const THINKING_ITEMS = [
  { title: '不开思考', value: '' },
  { title: '开启思考', value: 'enabled' }
]
const SERVICE_TYPE_ITEMS = ['claude', 'openai', 'gemini', 'responses']
const EXTRACT_ITEMS = ['text', 'usage', 'protocol', 'svg', 'numbers']
const JUDGE_ITEMS = ['exact', 'regex', 'numeric', 'protocol', 'svg', 'distribution', 'rubric', 'observe']
const EXPECT_ITEMS = ['', 'required', 'forbidden', 'observe']
const SIGNATURE_ITEMS = ['', 'present', 'absent', 'observe']

const emptyForm = () => ({
  name: '',
  slug: '',
  category: 'authenticity',
  prompt: '',
  system: '',
  maxTokens: 256,
  temperature: 0,
  sampleCount: 1,
  thinking: '',
  applicableServiceTypes: [...SERVICE_TYPE_ITEMS],
  extractKind: 'text',
  judgeKind: 'exact',
  expected: '',
  caseInsensitive: false,
  pattern: '',
  expectedNumber: 0,
  expectModelEcho: false,
  expectUsage: false,
  expectContentTypes: '',
  expectThinking: '',
  expectSignature: '',
  minSamples: 2,
  suspectUniqueRatio: 0.2,
  failIfIdentical: false,
  compareHistogram: false,
  analysisPrompt: '',
  expectThinkingUsage: false
})

const mode = ref<'list' | 'form'>('list')
const step = ref(0)
const form = ref(emptyForm())
const editingId = ref('')
const report = ref<EvalValidateReport | null>(null)
const validating = ref(false)
const saving = ref(false)
const deleting = ref(false)
const error = ref('')
const deleteDialog = ref(false)
const pendingDelete = ref<EvalProbe | null>(null)

const startCreate = () => {
  form.value = emptyForm()
  editingId.value = ''
  report.value = null
  step.value = 0
  mode.value = 'form'
}

const formFromProbe = (probe: EvalProbe) => ({
  ...emptyForm(),
  name: probe.name,
  slug: probe.slug,
  category: probe.category,
  prompt: probe.stimulus.prompt,
  system: probe.stimulus.system || '',
  maxTokens: probe.stimulus.maxTokens || 256,
  temperature: probe.stimulus.temperature ?? 0,
  sampleCount: probe.sampleCount,
  thinking: probe.stimulus.thinking || '',
  applicableServiceTypes: [...(probe.applicableServiceTypes || [])],
  extractKind: probe.extract.kind,
  judgeKind: probe.judge.kind,
  expected: probe.judge.expected || '',
  caseInsensitive: !!probe.judge.caseInsensitive,
  pattern: probe.judge.pattern || '',
  expectedNumber: probe.judge.expectedNumber ?? 0,
  expectModelEcho: !!probe.judge.expectModelEcho,
  expectUsage: !!probe.judge.expectUsage,
  expectContentTypes: (probe.judge.expectContentTypes || []).join(','),
  expectThinking: probe.judge.expectThinking || '',
  expectSignature: probe.judge.expectSignature || '',
  minSamples: probe.judge.minSamples ?? 2,
  suspectUniqueRatio: probe.judge.suspectUniqueRatio ?? 0.2,
  failIfIdentical: !!probe.judge.failIfIdentical,
  compareHistogram: !!probe.judge.compareHistogram,
  analysisPrompt: probe.judge.analysisPrompt || '',
  expectThinkingUsage: !!probe.judge.expectThinkingUsage
})

const startEdit = (probe: EvalProbe) => {
  form.value = formFromProbe(probe)
  editingId.value = probe.id
  report.value = null
  step.value = 0
  mode.value = 'form'
}

/** 内置题改不了，复制一份成自建题：换 slug、清掉 id，其余原样带过来。 */
const startCopy = (probe: EvalProbe) => {
  form.value = { ...formFromProbe(probe), slug: `${probe.slug}-copy`, name: `${probe.name}（副本）` }
  editingId.value = ''
  report.value = null
  step.value = 0
  mode.value = 'form'
}

const buildProbe = (): Partial<EvalProbe> => {
  const value = form.value
  return {
    slug: value.slug,
    name: value.name,
    category: value.category,
    stimulus: {
      prompt: value.prompt,
      system: value.system || undefined,
      maxTokens: value.maxTokens,
      temperature: value.temperature,
      thinking: value.thinking || undefined
    },
    extract: { kind: value.extractKind },
    judge: {
      kind: value.judgeKind,
      // numeric 也写一份字符串 expected：期望值正好是 0 时，只填 expectedNumber 会被后端当成"没填"。
      expected: value.judgeKind === 'numeric' ? String(value.expectedNumber) : value.expected || undefined,
      caseInsensitive: value.caseInsensitive || undefined,
      pattern: value.pattern || undefined,
      expectedNumber: value.judgeKind === 'numeric' ? value.expectedNumber : undefined,
      expectModelEcho: value.expectModelEcho || undefined,
      expectUsage: value.expectUsage || undefined,
      expectContentTypes: value.expectContentTypes
        ? value.expectContentTypes.split(',').map(item => item.trim()).filter(Boolean)
        : undefined,
      expectThinking: value.expectThinking || undefined,
      expectSignature: value.expectSignature || undefined,
      minSamples: value.minSamples || undefined,
      suspectUniqueRatio: value.suspectUniqueRatio || undefined,
      failIfIdentical: value.failIfIdentical || undefined,
      compareHistogram: value.compareHistogram || undefined,
      analysisPrompt: value.analysisPrompt || undefined,
      expectThinkingUsage: value.expectThinkingUsage || undefined
    },
    sampleCount: value.sampleCount,
    applicableServiceTypes: value.applicableServiceTypes
  }
}

const goNext = async () => {
  if (step.value === 1) {
    step.value = 2
    await runValidate()
    return
  }
  step.value += 1
}

const runValidate = async () => {
  validating.value = true
  error.value = ''
  try {
    report.value = await api.validateEvalProbe(buildProbe())
  } catch (err) {
    report.value = null
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    validating.value = false
  }
}

const save = async () => {
  saving.value = true
  error.value = ''
  try {
    if (editingId.value) {
      await api.updateEvalProbe(editingId.value, buildProbe())
    } else {
      await api.createEvalProbe(buildProbe())
    }
    emit('changed')
    mode.value = 'list'
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    saving.value = false
  }
}

const askDelete = (probe: EvalProbe) => {
  pendingDelete.value = probe
  deleteDialog.value = true
}

const confirmDelete = async () => {
  if (!pendingDelete.value) return
  deleting.value = true
  error.value = ''
  try {
    await api.deleteEvalProbe(pendingDelete.value.id)
    emit('changed')
    deleteDialog.value = false
    pendingDelete.value = null
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    deleting.value = false
  }
}
</script>
