<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>
<h1 align="center">MetricSpire</h1>
<p align="center"><strong>统一维护指标，让应用和 AI 助手按同一套定义查询。</strong></p>
<p align="center"><a href="README.md">English</a> · <strong>简体中文</strong> · <a href="README.ja.md">日本語</a></p>

MetricSpire 是开源的指标目录与数据服务，提供 HTTP 和 MCP 接口。团队审核、发布指标定义，应用和 AI 助手先了解口径，再通过同一服务查询数据。

- **维护指标**：草稿、口径评审、版本发布和网页工作台。
- **提供查询**：按指标、时间、分组发起结构化请求，检查权限并返回带类型的结果表。
- **按需探索**：加载审核后的外部语义定义，回答现有指标尚未覆盖的临时问题。

例如，团队发布“支付成功订单数”的定义，用户再问“上周各地区有多少支付成功订单？”服务按已发布口径和已配置的数据路径执行查询；应用也能通过 HTTP 请求同一指标。

> **开发预览。** 尚无正式生产版本或公开演示服务。查询自己的数据需要部署服务、配置身份和审核口径。详见[当前状态与验收边界](docs/development-status.md)。

## 只用 Go 试跑，无需数据库

安装 Go 1.26 或 1.27、Git 后运行：

```sh
git clone https://github.com/marmot1024/metricspire.git
cd metricspire
sh examples/quickstart.sh
```

脚本编译中性的订单模型与访问策略，打印**按日期、客户分群查询客单价和退款率**的计划。在新的 `dist/quickstart.*` 目录生成四份 JSON 文件，最后输出：

```text
Offline example complete. No database connection or data query was made.
```

这一步实际运行编译器与规划器，不会连接数据库，也没有查询结果。首次运行会下载 Go 依赖。[查看分步说明](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database)。

## 按你的目标继续

| 目标 | 从哪里开始 | 需要准备 |
| --- | --- | --- |
| 使用团队已有服务 | [Databricks 安装器](docs/mcp-installer.md#get-the-installer)或 [OIDC 安装器](docs/oidc-mcp.md#user-installation)；[其他 MCP 客户端](docs/mcp.md#connect) | 团队实例、连接包和自己的权限；无需下载源码。 |
| 新增或维护指标 | [首次定义 → 草稿 → 发布 → 查询](docs/metric-maintenance.md) · [可选维护者 skill](skills/metricspire-maintainer/SKILL.md) | 已确认的口径、模型维护权限、已有数据路径。 |
| 不用 Databricks 接入 | [PostgreSQL + OIDC 上手路径](docs/self-hosted.md) | 元数据与结果库、身份提供方、已准备的聚合数据；查询用户无需 Databricks 账号。 |
| 挂载已有语义层 | [导出 → 配置 → 发现 → 对账](docs/semantic-layer.md) | 审核后的 AnalysisCatalog 和受支持的探索引擎；不自动导入 Git/dbt/Cube。 |

已有网页访问权限？打开团队实例，从**指标目录**开始。详细指南目前以英文为主，三个语言的项目首页保持相同的能力范围。

## 在数据架构中的位置

现有流水线准备数据，查询引擎存储并计算数据，MetricSpire 维护定义、校验访问权限并提供结构化查询服务。

![MetricSpire 架构：网页、HTTP 与 MCP 客户端调用指标目录和数据服务；适配器分别连接查询引擎、指标元数据和可选语义导出。](docs/assets/architecture.zh-CN.svg)

| 职责 | 已实现的选项 |
| --- | --- |
| 定义、发布记录、审计 | PostgreSQL 或 MySQL 8.4 |
| 已发布指标的分析查询 | Databricks SQL |
| 预计算聚合结果服务 | PostgreSQL、ClickHouse 或 Doris，通过 HTTP/MCP 查询 |
| 基于审核语义的临时分析 | Databricks；Trino/Presto 支持受限的单表 DATE 探索 |

独立 OIDC 部署可关闭 Databricks 分析路径。各数据库的支持范围对应表中的具体职责，元数据适配器不代表通用查询适配器。MetricSpire 不替代上游 ETL，也不会创建业务表。详见[适配器范围](docs/architecture.md#adapter-support)。

## 文档与贡献

- [全部文档](docs/README.md) · [开始使用](docs/getting-started.md)
- [MCP 查询流程](docs/mcp.md) · [HTTP API](docs/http-api.md)
- [私有团队配置](config/README.md) · [运行引擎配置](docs/runtime-engines.md)
- [数据刷新与对账](docs/online-refresh.md) · [隔离技术验收](deploy/testing/README.md)
- [架构](docs/architecture.md) · [开发状态](docs/development-status.md) · [贡献指南](CONTRIBUTING.md)

采用 [Apache 2.0](LICENSE) 许可。公开示例使用中性数据；公司口径、真实字段映射、凭据与生产数据应保留在私有存储。语义契约 `metricspire.io/v1alpha1` 与计划中的产品 `v0.1.0` 分别版本化。
