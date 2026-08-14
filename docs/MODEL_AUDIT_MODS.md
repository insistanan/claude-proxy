# 模型审计 Mod 使用指南

模型审计 Mod 是放在 `.config/model-audit/mods/<目录名>/` 下的一组文件。新增或修改 Mod 后，在“模型审计 > 探针库”点击刷新即可加载，不需要重新编译项目。

## 一个 Mod 包含什么

```text
my-mod/
├── manifest.json
├── probe-prompt.md
├── rules.json
├── method.md
├── result-schema.json
├── analysis-prompt.md   # LLM 模式使用
└── analysis.py          # Python 模式使用
```

- `manifest.json`：声明采样、解析、聚合和分析方式。
- `probe-prompt.md`：发给被测渠道的题目，支持 Go template 变量。
- `rules.json`：对统计摘要执行的确定性规则。
- `method.md`：记录实验和分析方法，页面会展示给用户。
- `result-schema.json`：描述统一分析结果格式。
- `analysis-prompt.md`：仅 LLM 分析模式需要，必须包含 `{{analysis_data_json}}`。
- `analysis.py`：仅 Python 自动分析模式需要。

程序会为每次加载的内容计算 SHA256，并保存到 `.config/model-audit/cache/<sha>/`。任务保存时会冻结具体实现版本，因此编辑目录不会改变已经创建任务所引用的旧版本。

## 采样方式

`sampling.mode` 支持两种值：

- `independent`：按任务配置的样本数发起多次请求。`sampleIntervalMs` 是相邻请求之间的等待时间。
- `batch`：只发起一次请求，要求回答包含一组观测值。适合“单次生成 300 个随机数”这类探针。

任务页面可以覆盖 `sampleCount` 和 `sampleIntervalMs`。持续任务的执行间隔是整次运行之间的间隔，与 Mod 内相邻样本请求间隔不是一回事。

## 解析与统计

解析器支持 `text`、`number`、`json`、`regex` 和 `enum`。聚合器支持原始值、频数、直方图和数值统计。

数值统计会生成数量、最小值、最大值、均值、标准差、P50 和 P90。规则文件按顺序匹配，支持：

```text
exists  not_exists  eq  neq  gt  gte  lt  lte  contains
```

规则只负责可重复的初步判断。复杂解释交给后续 LLM、Python 或手动分析。

## 三种分析方式

### LLM 分析

`analysis.mode` 设置为 `llm`。任务创建页会要求选择分析渠道和模型，Mod 本身不绑定模型。

`analysis-prompt.md` 中使用 `{{analysis_data_json}}` 注入当前 `run + target + mod` 的冻结数据。分析请求计入该次运行的请求和 token 预算。

### Python 自动分析

`analysis.mode` 设置为 `python`。后端按以下顺序查找解释器：manifest 指定值、项目配置、`py -3`、`python`、`python3`。

默认执行方式：

```text
analysis.py --input analysis-input.json --output analysis-output.json
```

脚本只收到文件路径，不会收到渠道密钥。stdout、stderr、退出码和解释器版本会记录到 `.config/model-audit.db`。

### 手动外部分析

`analysis.mode` 设置为 `manual`，用于 Java、EXE 或其他程序。项目不会自动执行命令。

运行记录的“分析”页面会显示：

- Mod 源目录
- 冻结快照目录
- 本次运行目录
- 已替换 `{input}`、`{output}` 的命令
- `method.md` 内容

外部程序生成结果后，在页面粘贴 JSON 并点击“导入分析结果”。

已完成或失败的分析可以点击“重新分析”。项目会复用原记录的冻结输入和原 Mod SHA，新建一条分析记录，不会重新发送探针题目，也不会覆盖旧结果。

## 统一分析输入与输出

输入文件使用 `audit.mod-analysis-input.v1`，包含当前运行和目标信息、原始回答、解析值、用量、统计摘要及规则结果。分析器不能读取其他运行的数据。

输出文件使用以下合同：

```json
{
  "schema": "audit.mod-analysis-result.v1",
  "verdict": "machine_readable_verdict",
  "confidence": 0.75,
  "summary": "中文结论摘要",
  "metrics": {},
  "evidence": ["支持该判断的观察"],
  "warnings": ["方法限制或替代解释"]
}
```

`schema`、`verdict`、`confidence`、`summary` 必填；`confidence` 必须在 0 到 1 之间。

## 从案例创建

项目内置三个案例，页面“探针库 > 新建 Mod”可以直接选择：

- `mod.random-range-llm`：单次生成 300 个随机数，直方图规则加 LLM 分析。
- `mod.number-stability-python`：多次获取数字，由 Python 自动生成分析结果。
- `mod.enum-manual`：统计 A/B/C 选择，演示外部 EXE 或 Java 程序手动分析。

案例源文件位于 `backend-go/internal/modelaudit/examples/`。

新增探针时，先选最接近的案例，修改 `manifest.json` 的 `id`、`version`、名称和描述，再修改题目、规则及分析文件。保存后页面会立即校验所有引用文件和合同；失败目录不会影响其他 Mod 加载。

## 目录导入

在“探针库 > 导入目录”输入本机绝对路径。后端先把整个源目录读入并校验，在 Mod 根目录创建临时副本，成功后再提交并重新加载。更新已有 Mod 时使用内容 SHA 做并发检查，避免覆盖刚被其他操作修改的目录。

所有运行数据、Mod 版本和分析记录都存放在独立数据库 `.config/model-audit.db`，不写入 `conversations.db` 或 `metrics.db`。
