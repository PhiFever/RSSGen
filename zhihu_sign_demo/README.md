# 知乎 x-zse-96 签名生成 Demo

最小化演示项目，展示知乎 API 签名生成流程。

## 快速开始

### 1. 安装依赖

```bash
cd zhihu_sign_demo
npm install
pip install requests
```

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
python demo.py --url "<your url>"
```

## 单独使用 Node.js

```bash
node sign_gen.js "https://www.zhihu.com/api/v4/questions/659012275/answers" "你的d_c0值"
```

输出示例：

```json
{
  "url": "https://www.zhihu.com/api/v4/questions/659012275/answers",
  "d_c0": "AXCWcRzPxxx...",
  "source": "101_3_3.0+/api/v4/questions/659012275/answers+AXCWcRzPxxx...",
  "x_zse_93": "101_3_3.0",
  "x_zse_96": "2.0_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
}
```

## 签名算法原理

```
签名源字符串 = zse93 + url_path + d_c0 + body(可选) + xZst81(可选)
              ↓
           MD5 哈希
              ↓
         SM4-like 加密
              ↓
         "2.0_" + 结果
```

## 注意事项

- 仅用于学习研究，请遵守知乎平台规则
- Cookie 有效期有限，过期后需要重新获取
- 大量请求可能触发风控，请控制频率