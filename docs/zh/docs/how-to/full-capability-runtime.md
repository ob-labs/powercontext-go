# 完整能力 Memory 验证

本指南验证现有 Go Server 的 Source 到 Memory 路径。它不要求新的 API 或
provider 专用集成。使用拥有 SQLite 数据库和 Memory 提取配置的 Server：

```sh
./bin/powercontext server run
```

全程使用同一个稳定的 Scope ID。capture、flush、entry list 和 search 请求
必须使用同一个值。

```sh
SCOPE_ID=project:quickstart
SOURCE_ID="quickstart-$(date +%s)-$$"
```

## Capture Source

用唯一的 Source ID 写入一个可长期保留的事实。只有 HTTP 状态为 202、
`status` 为 `accepted` 并且包含数字 `position` 的响应，才表示写入已接受。

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/sources/content \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\",\"source_id\":\"${SOURCE_ID}\",\"content\":\"Prefer small, verifiable PowerContext changes.\"}"
```

记录响应中的 `position`；它是下一步要确认的 Source journal 边界。

## Flush 并检查 Memory

对该 Scope 执行 flush，把 pending Source 处理为 Memory。

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/flush \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\"}"
```

响应包含 `current_cursor`，它必须到达 capture 的 `position`。只有
`current_cursor` 已到达该位置时，`idle` 响应才是有效结果；否则再次 flush。

列出当前 Memory entry：

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/entries/list \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\"}"
```

找到 `source_refs` 中包含 `${SOURCE_ID}` 的 entry，并记录其
`citation.entry_id`。这才证明刚才 capture 的 Source 已形成权威 Memory。

## 搜索已记录的 Entry

在同一个 Scope 中搜索并确认返回刚才记录的 citation：

```sh
curl -fsS -X POST http://127.0.0.1:8000/v1/memory/search \
  -H 'content-type: application/json' \
  -d "{\"scope_id\":\"${SCOPE_ID}\",\"query\":\"verifiable PowerContext changes\",\"mode\":\"auto\",\"limit\":10}"
```

完整闭环要求命中结果含有记录的 `citation.entry_id`，并且 `matched_by`
数组非空。实际搜索模式由当前 SQLite capability 决定：不配置 embedding
model 时仍可使用 FTS；vector 和 hybrid 需要配置 embedding projection。
