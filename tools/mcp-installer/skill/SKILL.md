---
name: metricspire
description: 使用 MetricSpire MCP 查询业务指标、进行受控探索分析，并处理连接或登录问题。不用于仓库开发。
---

# MetricSpire

直接使用已加载的 MetricSpire MCP。普通查数不需要读取 memory、项目代码或凭据文件。

- 先 `list_namespaces` 确认业务域，按 `discover_query_routes` 选路。分析指标用 `search_metrics → get_metric_details → prepare_query → run_query`；在线指标搜索和详情保持 `route=online_metrics`，再用数字 code 调用 `query_online_metrics`。仅任务未完成时轮询，不重复 explain/plan。旧服务缺少快速工具时才用旧流程。
- 比较人群、排除项、分母、计数粒度和时间口径。同族定义有实质差异而用户未选择时，列出选项并停止；“总额”不表示包含测试账号或选择最宽人群。明确选择后携带详情返回的 manifest 版本。
- 临时分组、分桶、多阶段或占比分析，用 `discover_analysis → plan_analysis → submit_analysis → get_analysis`；确认已有指标不能满足且用户同意探索后，先核实已授权的源、字段和限制。namespace 使用业务域，source 放在 query.source；无须预先登记结果指标。
- 探索只使用服务端已加载并授权的语义与受支持算子；粒度、时间、标签时点或分母有歧义时具体澄清。缺工具、源、字段或关联时说明缺口，不猜物理映射，也不以换工具绕过权限或超时。
- 同一任务只提交一次，轮询同一 job；用户明确要求重新查询时发起新的授权查询，旧结果不证明当前权限或快照未变；需要取消时用 `cancel_query` 或 `cancel_analysis`。保留参数、job_id 和最终状态；不绕过 MCP 执行 SQL 或另查数据。
- 工具未加载时，只检查一次当前连接。资源列表为空不证明服务故障；本地语义文件也不证明指标已接入。
- 本地 Codex 使用 `metricspire-cli`，由已有 Databricks CLI profile 自动获取和刷新用户令牌。连接失败先报告失败阶段；不运行 `codex mcp login`、logout、清缓存、切 profile 或扩 scopes。仅确认 CLI 用户授权失效时，请用户对原 workspace/profile 执行 `databricks auth login`。
- 安装后重连或打开新会话，并实际调用 `list_namespaces`；CLI 有授权不代表 MCP 已连接。权限拒绝后停止当前请求，报告 request_id，不再探查其他工具或路线；查询能力不足另行说明。

结果注明时间范围、时区、粒度、分母及目录标记的可信状态；未提交、未完成或未验证的项目明确说明。
