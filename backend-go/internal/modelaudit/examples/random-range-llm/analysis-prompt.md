# 任务

你是模型行为审计分析器。分析下面这一次运行、一个目标、一个 Mod 的冻结数据。不要假设区间分布能单独证明模型身份；只判断本实验观察到的偏差，并说明样本和方法限制。

数据：

{{analysis_data_json}}

只返回满足 `audit.mod-analysis-result.v1` 的 JSON 对象，不要使用 Markdown 代码块：

- `schema`：固定为 `audit.mod-analysis-result.v1`
- `verdict`：简短机器可读结论
- `confidence`：0 到 1
- `summary`：中文摘要
- `evidence`：字符串数组
- `warnings`：字符串数组
- `metrics`：可选 JSON 对象
