# 知乎 x-zse-96 签名生成 Demo

知乎 API 签名生成工具，无需浏览器环境模拟。

## 快速开始

### 1. 安装 Node.js

确保系统已安装 Node.js（v18+）。

### 2. 配置 Cookie

编辑 `demo.py` 文件，找到 `COOKIE` 变量，填入你的知乎 Cookie：

```python
COOKIE = """
_zap=xxx; d_c0=AXCWcRzPxxx; z_c0=xxx; ...
"""
```

**Cookie 获取方法：**
1. 打开浏览器访问 https://www.zhihu.com 并登录
2. 按 F12 打开开发者工具 →「网络」标签
3. 找任意一个 API 请求，复制请求头中的 `Cookie` 字段

**关键字段：** Cookie 必须包含 `d_c0`

### 3. 运行

```bash
uv run demo.py --url "<知乎API URL>"
```

示例：
```bash
uv run demo.py --url "https://www.zhihu.com/api/v4/questions/659012275/answers?limit=5"
```

## 文件说明

| 文件 | 说明 |
|------|------|
| `zhihu_lite.js` | 签名核心（轻量版，无浏览器模拟） |
| `sign_minimal.js` | Node.js 签名入口 |
| `other.js` | SM4-like 加密算法（知乎前端提取） |
| `demo.py` | Python 测试脚本 |

## 单独使用 Node.js

```bash
node sign_minimal.js "<url>" "<d_c0值>"
```

输出示例：
```json
{
  "source": "101_3_3.0+/api/v4/xxx+AXCWcRzP...",
  "x_zse_93": "101_3_3.0",
  "x_zse_96": "2.0_xxxxxxxxxxxxxxx"
}
```

## 注意事项

- 仅用于学习研究，请遵守知乎平台规则
- Cookie 有效期有限，过期后需要重新获取
- 大量请求可能触发风控，请控制频率