---
name: metricspire
description: 使用 MetricSpire MCP 查询业务指标、进行受控探索分析，并处理连接或登录问题。不用于仓库开发。
---

# MetricSpire

直接使用已加载的 MetricSpire MCP。普通查数不需要读取 memory、项目代码或凭据文件。

- 先 `list_namespaces` 确认范围。已有指标能回答时，用 `search_metrics → explain_query → plan_query → submit_query → get_query`。
- 临时分组、分桶、多阶段或占比分析，用 `discover_analysis → plan_analysis → submit_analysis → get_analysis`；先核实已授权的源、字段和限制，无须预先登记结果指标。
- 探索只使用服务端已加载并授权的语义与受支持算子；粒度、时间、标签时点或分母有歧义时具体澄清。缺工具、源、字段或关联时说明缺口，不猜物理映射，也不以换工具绕过权限或超时。
- 同一查询只提交一次，轮询同一 job；需要取消时用 `cancel_query` 或 `cancel_analysis`。保留参数、job_id 和最终状态；不绕过 MCP 执行 SQL 或另查数据。
- 工具未加载时，只检查一次当前连接。资源列表为空不证明服务故障；本地语义文件也不证明指标已接入。
- 本地 Codex 使用 `metricspire-cli`，由已有 Databricks CLI profile 自动获取和刷新用户令牌。连接失败先报告失败阶段；不运行 `codex mcp login`、logout、清缓存、切 profile 或扩 scopes。仅确认 CLI 用户授权失效时，请用户对原 workspace/profile 执行 `databricks auth login`。
- 安装后重连或打开新会话，并实际调用 `list_namespaces`；CLI 有授权不代表 MCP 已连接。权限拒绝和查询能力不足分别报告。

结果注明时间范围、时区、粒度、分母及目录标记的可信状态；未提交、未完成或未验证的项目明确说明。
