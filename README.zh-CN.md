<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>指标库与数据服务，通过 MCP 连接 AI。</strong></p>

<p align="center"><a href="README.md">English</a> · <strong>简体中文</strong> · <a href="README.ja.md">日本語</a></p>

MetricSpire 是一个开源的指标库与数据服务。它集中管理指标定义和版本，通过 HTTP API 提供数据查询，并通过 MCP 接入 AI 助手。

你也可以按需接入外部语义知识库，以审核过的数据源、字段和关联定义支撑探索性分析，无需先将每个临时分析结果登记为固定指标。

> **开发预览版。** 查询自己的数据前，需要部署并配置服务，以及相应的数据权限。目前尚无生产就绪的正式版本。[查看当前状态](docs/development-status.md)（英文）。

## 一个例子：从问题到结果

配置好公开的订单样例数据（TPCH）后，AI 助手可以把下面的问题转换成指标查询：

> 查询样例订单 1–5，按客户类型和订单状态分组，返回收入、订单数和平均订单金额。

MetricSpire 根据已发布的定义计算结果。下面是仓库[预期样例结果](testdata/acceptance/databricks-tpch/expected.json)的前两行：

| 客户类型 | 订单状态 | 收入 | 订单数 | 平均订单金额 |
| --- | --- | ---: | ---: | ---: |
| BUILDING | F | 228570.52 | 1 | 228570.52 |
| BUILDING | O | 203010.51 | 1 | 203010.51 |

这个样例将收入定义为所选订单总金额之和。你可以查看对应的[指标定义](testdata/acceptance/databricks-tpch/model.yaml)与[结构化请求](testdata/acceptance/databricks-tpch/query.json)。表格引用已保存的测试结果，并非实时演示。AI 客户端负责理解问题，MetricSpire 负责校验并执行结构化请求。

## 从哪里开始？

| 你的目标 | 入口 | 需要准备 |
| --- | --- | --- |
| 在本地 Codex CLI 中使用团队服务 | [获取团队安装器](docs/mcp-installer.md#get-the-installer) | 已部署的实例、团队提供的安装器，以及你自己的 Databricks 访问权限。 |
| 连接其他 MCP 客户端 | [MCP 连接指南](docs/mcp.md#connect) | 实例地址，以及该部署支持的身份认证配置。 |
| 不连接数据，先理解指标定义 | [运行离线示例](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database) | Go；无需数据库或云账号。示例生成查询计划，不返回数据结果。 |
| 为团队部署服务 | [服务配置指南](docs/getting-started.md#run-the-http-service) | PostgreSQL、身份认证配置和分析引擎。 |

已有网页访问权限？打开团队实例，从**指标目录**开始。仓库不附带公开服务实例，也不包含公司的指标定义。上述详细指南目前为英文。

## 它在数据系统里负责哪一部分？

团队把审核过的指标定义连接到现有数仓表。MetricSpire 检查用户权限，选择当前生效的定义，执行查询并返回带类型的结果表。网页、HTTP API 和 MCP 工具使用同一套服务。维护者可以审核和发布定义变更，保留历史，需要时回滚到之前的版本。

**Databricks SQL 是首个分析引擎。** PostgreSQL 保存指标定义、版本和审计记录。数据采集与加工仍需由上游完成；MetricSpire 不替代这条链路，也不提供图表看板。它不托管 AI 模型，不接受调用方提交任意 SQL。

探索性分析使用经过审核的结构化语义目录，接入方式见[外部语义与配置](docs/exploration.md)。另外可以单独启用[在线 HTTP 查询](docs/online-query.md)，读取 PostgreSQL 中的预计算汇总数据。这两条路径需要分别配置和验收。

## 文档与项目状态

- [快速上手](docs/getting-started.md) — 离线示例、本地指标目录与服务配置。
- [MCP 接入](docs/mcp.md) · [HTTP API](docs/http-api.md) — 连接客户端或应用。
- [团队配置](config/README.md) — 私有运行配置、业务别名和已审核的语义导出文件。
- [架构](docs/architecture.md) — 指标定义、权限与执行边界。
- [开发状态](docs/development-status.md) — 已实现能力、验证证据和待完成项。
- [贡献指南](CONTRIBUTING.md) · [全部文档](docs/README.md)。

以上文档目前为英文。当前尚未确立生产就绪、通用多引擎分析或生产延迟与可用性保证。试用指标始终保留未验证标识。语义契约 [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json) 与计划中的产品 `v0.1.0` 分别管理版本。

采用 [Apache 2.0](LICENSE) 许可。公开示例使用中立数据；公司定义、凭据和生产数据应保留在仓库之外。
