# RSSGen Notifier 设计文档

## 概述

为 RSSGen 添加通知功能，当获取上游 API 数据失败时（如 403 等业务错误），使用 Apprise 发送通知消息，并向下游的 Miniflux 报错。这样使用者就不必去看日志才能发现问题。

## 设计决策

### 1. 通知触发条件
- **区分错误类型**：按 HTTP 状态码区分业务错误和临时错误
- **仅在重试全部失败后**：当后台刷新器的 3 次重试都失败后，才发送通知
- **暂时禁用出错的 feed**：业务错误后仅禁用出错的那个 feed（feed 级，按 `cache_key`=`route/feed_id` 区分），同路由下其他 feed 不受影响，防止持续报错，重启可恢复

### 2. 错误类型定义
- **业务错误**：HTTP 4xx 状态码（400, 401, 403, 404, 410, 422, 451）
- **临时错误**：HTTP 5xx 状态码和网络错误

### 3. 通知内容
- **基础信息**：订阅源标识（`cache_key`，形如 `route/feed_id`）、HTTP 状态码、错误消息、失败时间
- **格式**：纯文本

### 4. 向 Miniflux 报错
- **HTTP 502 错误**：直接返回 502，Miniflux 会记录错误并可能停止轮询

### 5. Feed 禁用机制
- **feed 级粒度**：以 `cache_key`（`route/feed_id`）为单位禁用，单个 feed 出错不波及同路由其他 feed
- **内存中禁用**：记录在 `disabled_feeds` 集合中
- **仅重启恢复**：只能通过重启 RSSGen 来恢复被禁用的 feed

### 6. Apprise 配置
- **全局配置**：在 `config.yml` 中配置一个全局的 Apprise URL，所有路由共用
- **直接使用 apprise Python 包**：不需要额外部署 Apprise API 服务器

### 7. 错误处理统一
- **统一所有路由**：所有路由脚本统一使用 `resp.raise_for_status()` 抛出 `HTTPError`
- **从异常获取状态码**：notifier 从 `HTTPError` 异常中获取 HTTP 状态码

## 架构设计

### 模块职责

```
RSSGen/
├── core/
│   ├── notifier.py      # 新增：通知模块
│   ├── refresher.py     # 修改：集成通知逻辑
│   ├── route.py         # 可选：统一错误处理
│   └── ...
├── routes/
│   ├── afdian.py        # 检查：确保使用 raise_for_status()
│   ├── zhihu.py         # 修改：统一使用 raise_for_status()
│   └── ...
└── app.py               # 修改：检查路由禁用状态
```

### 数据流

```
Miniflux 拉取 → GET /feed/{route_name}/{path}
    ↓
检查该 feed（cache_key）是否被禁用
    ↓
如果被禁用，返回 HTTP 502
    ↓
如果未被禁用，调用路由 fetch()
    ↓
如果 fetch() 成功，返回 RSS
    ↓
如果 fetch() 失败，检查错误类型
    ↓
如果是业务错误（4xx），重试 3 次
    ↓
重试全部失败后：
    - 发送 Apprise 通知
    - 禁用该 feed
    - 返回 HTTP 502
```

## 配置结构

在 `config.yml` 中添加 `notifier` 配置：

```yaml
notifier:
  enabled: true
  service_urls:  # 通知服务 URL 列表（Apprise 格式）
    - "tgram://bot_token/chat_id"
    - "mailto://user:pass@gmail.com"
  business_error_codes:  # 可选，默认值 [400, 401, 403, 404, 410, 422, 451]
    - 403
    - 401
```

**默认值**：
- `business_error_codes`：`[400, 401, 403, 404, 410, 422, 451]`

## 核心接口

### Notifier 类

```python
class Notifier:
    def __init__(self, config: dict):
        """初始化 notifier，加载配置"""
        self.enabled = config.get("enabled", False)
        self.service_urls = config.get("service_urls", [])
        self.business_error_codes = set(config.get("business_error_codes", [400, 401, 403, 404, 410, 422, 451]))
        self.disabled_feeds: set[str] = set()  # 被禁用的 feed 集合（cache_key）
        self._apprise = None  # apprise 实例（懒加载）
    
    def is_business_error(self, status_code: int) -> bool:
        """判断是否为业务错误"""
        return status_code in self.business_error_codes
    
    def is_feed_disabled(self, feed_key: str) -> bool:
        """检查 feed 是否被禁用（feed_key 即 cache_key：route/feed_id）"""
        return feed_key in self.disabled_feeds
    
    def disable_feed(self, feed_key: str):
        """禁用单个 feed（feed_key 即 cache_key：route/feed_id）"""
        self.disabled_feeds.add(feed_key)
    
    async def notify(self, feed_key: str, status_code: int, error_message: str):
        """发送通知"""
        if not self.enabled or not self.service_urls:
            return
        
        # 构建通知内容
        message = f"[RSSGen] 订阅源 {feed_key} 获取失败\n"
        message += f"状态码: {status_code}\n"
        message += f"错误: {error_message}\n"
        message += f"时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}"
        
        # 发送通知
        await self._send_notification(message)
    
    async def _send_notification(self, message: str):
        """实际发送通知的内部方法"""
        # 使用 apprise 发送通知
        pass
```

## 错误处理统一

### 需要修改的文件

1. **RSSGen/routes/zhihu.py**：
   - 将 `resp.status_code != 200` 检查改为 `resp.raise_for_status()`
   - 移除手动抛出的 `RuntimeError`

2. **其他路由脚本**：
   - 检查是否所有路由都使用了 `resp.raise_for_status()`
   - 如有不一致，统一修改

### 错误处理流程

```
路由脚本调用 scraper.get(url)
    ↓
检查响应状态码
    ↓
resp.raise_for_status()  # 统一使用这个方法
    ↓
如果状态码不是 2xx，抛出 HTTPError
    ↓
notifier 捕获 HTTPError，获取状态码
    ↓
判断是否是业务错误（4xx）
    ↓
如果是业务错误，发送通知 + 禁用该 feed
```

## 通知发送与 feed 禁用

### 通知发送流程

```
refresher._refresh_one()
    ↓ 重试全部失败
    ↓ 捕获到 HTTPError
    ↓ 获取状态码 status_code = e.response.status_code
    ↓ 检查 notifier.is_business_error(status_code)
    ↓ 如果是业务错误
    ↓ cache_key = build_cache_key(route_name, path_params)
    ↓ notifier.notify(cache_key, status_code, str(e))
    ↓ notifier.disable_feed(cache_key)
    ↓ 记录日志
```

### feed 禁用检查

```
app.py GET /feed/{route_name}/{path}
    ↓ cache_key = build_cache_key(route_name, path_parts)
    ↓ 检查 notifier.is_feed_disabled(cache_key)
    ↓ 如果被禁用
    ↓ raise HTTPException(status_code=502, detail=f"订阅源 {cache_key} 已禁用")
```

### 通知内容格式

```python
message = f"[RSSGen] 订阅源 {cache_key} 获取失败\n"
message += f"状态码: {status_code}\n"
message += f"错误: {error_message}\n"
message += f"时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}"
```

示例输出：
```
[RSSGen] 订阅源 afdian/Alice 获取失败
状态码: 403
错误: HTTP Error 403: Forbidden
时间: 2026-06-07 20:30:00
```

### 日志记录

当通知发送成功时，记录 INFO 日志：
```python
logger.info("已发送通知")
```

当通知发送失败时，记录 ERROR 日志（但不影响主流程）：
```python
logger.error(f"发送通知失败: {e}")
```

当 feed 被禁用时，记录 WARNING 日志：
```python
logger.warning(f"feed {cache_key} 已禁用（业务错误 {status_code}），重启后恢复")
```

## 测试策略

### 单元测试

1. **notifier 模块测试**：
   - 测试 `is_business_error()` 方法
   - 测试 `is_feed_disabled()` 方法
   - 测试 `disable_feed()` 方法（含 feed 级隔离）
   - 测试 `notify()` 方法（mock apprise）

2. **错误处理统一测试**：
   - 测试 `HTTPError` 异常的状态码获取
   - 测试业务错误判断逻辑

### 集成测试

1. **通知发送测试**（`tests/test_refresher.py::TestNotifierIntegration`）：
   - 模拟 HTTP 403 错误
   - 验证通知是否发送
   - 验证出错的 feed 是否被禁用，且同路由其他 feed 不受影响

2. **feed 禁用测试**（`tests/test_app_feed.py`）：
   - 验证禁用的 feed 返回 502
   - 验证同路由未禁用的 feed 不受影响

### 测试覆盖

- **正常流程**：通知未启用时不发送通知
- **业务错误**：发送通知 + 禁用该 feed
- **临时错误**：不发送通知，不禁用 feed
- **通知失败**：记录错误日志，不影响主流程
- **feed 禁用**：返回 HTTP 502

## 依赖

- **apprise**：Python 包，用于发送通知

需要添加到 `pyproject.toml`：
```toml
dependencies = [
    # ... 现有依赖
    "apprise>=1.0.0",
]
```

## 实现计划

### 阶段 1：添加依赖和配置
1. 在 `pyproject.toml` 中添加 `apprise` 依赖
2. 更新 `config.example.yml` 添加 `notifier` 配置示例

### 阶段 2：创建 notifier 模块
1. 创建 `RSSGen/core/notifier.py`
2. 实现 `Notifier` 类

### 阶段 3：统一错误处理
1. 修改 `RSSGen/routes/zhihu.py`，统一使用 `raise_for_status()`
2. 检查其他路由脚本，确保一致性

### 阶段 4：集成通知逻辑
1. 修改 `RSSGen/core/refresher.py`，集成 notifier
2. 修改 `RSSGen/app.py`，检查 feed 禁用状态

### 阶段 5：测试
1. 编写单元测试
2. 编写集成测试
3. 测试完整流程

### 阶段 6：文档更新
1. 更新 README 或 CLAUDE.md
2. 添加配置说明

## 风险与注意事项

1. **aprise 依赖**：添加新的 Python 依赖，需要确保兼容性
2. **路由脚本修改**：统一错误处理可能影响现有路由的行为
3. **内存中的禁用状态**：重启后丢失，需要用户了解这个行为
4. **通知发送失败**：需要确保通知发送失败不影响主流程
