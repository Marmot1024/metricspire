---
name: metricspire
description: 使用 MetricSpire MCP 查询业务指标，发现可用指标和口径，并处理连接或登录问题。适用于 MetricSpire 查数，不用于仓库开发。
---

# MetricSpire

直接使用已加载的 MetricSpire MCP。普通查数不需要读取 memory、项目代码或凭据文件。

- 先 `list_namespaces`、`search_metrics` 核实指标、维度和默认口径；缺失或冲突的定义向用户提出具体问题，不猜。
- 按 `explain_query → plan_query → submit_query → get_query` 完成查询，保留参数、job_id 和最终状态。只提交用户请求范围内的查询；不绕过 MCP 执行 SQL 或另查数据。
- 工具未加载时，只检查一次当前连接。资源列表为空不证明服务故障；本地语义文件也不证明指标已接入。
- 仅客户端明确提示需要登录时，引导用户对现有 `metricspire` 连接执行 `codex mcp login metricspire`，一次失败即停止并报告实际错误。已有授权先复用，不因工具缺失、超时或单次 401 反复登录；不 logout、清缓存、改 profile 或扩 OAuth scopes。
- 登录完成后重新加载连接或打开新会话，实际调用 `list_namespaces` 验证；浏览器成功回调不代表查询成功。权限拒绝和查询能力不足分别报告，不把它们解释为缺少登录权限。

结果注明时间范围、时区、粒度、分母及目录标记的可信状态；未提交、未完成或未验证的项目明确说明。
