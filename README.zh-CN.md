# cpa-key-quota

CLIProxyAPI / Plus 上的「按客户端 Key 费用限额」插件。

绑定已在 Plus `api-keys` 里的 Key（提交明文一次，只存 sha256 哈希 + preview）。后台可改限额，也可再贴一次新明文来换绑；明文不落盘。Plus 继续鉴权、统计、路由。插件只给策略表内的 Key 记账，超限返回 429。未绑定或已停用的策略零动作。

停用 = 暂时摘掉限额闸，Key 在 Plus 侧照常可用。解绑才从策略表删除。删 `api-keys` 才会 401。

单价只读 Plus 价目表 `GET /v0/management/billing/model-prices`。插件不做价目编辑页。
