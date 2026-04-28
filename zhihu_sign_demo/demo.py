#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "requests>=2.33.1",
#     "mini-racer==0.14.1",
# ]
# ///
"""
知乎 x-zse-96 签名生成 Demo

使用方法:
uv run demo.py --url "<知乎API URL>"
"""

import argparse
import json
import re
import sys
import urllib.parse
from pathlib import Path

import requests
from py_mini_racer import MiniRacer

# ============================================
# 在这里填写你的知乎 Cookie (登录后从浏览器复制)
# ============================================
COOKIE = """
"""
# ============================================


# 签名 JS 文件路径
SIGN_JS_PATH = Path(__file__).parent / "zhihu_sign.js"

# 初始化 V8 引擎并加载签名 JS（惰性初始化）
_v8_ctx = None


def get_signature(url: str, d_c0: str) -> dict:
    """通过 PyMiniRacer (V8) 执行签名生成"""
    global _v8_ctx
    if _v8_ctx is None:
        js_code = SIGN_JS_PATH.read_text()
        _v8_ctx = MiniRacer()
        _v8_ctx.eval(js_code)

    result = _v8_ctx.call(
        "tv",
        url,
        "",
        {"zse93": "101_3_3.0", "dc0": d_c0, "xZst81": None},
        ""
    )

    return {
        "source": result["source"],
        "x_zse_93": "101_3_3.0",
        "x_zse_96": "2.0_" + result["signature"]
    }


def parse_cookies(cookie_str: str) -> dict:
    """解析 cookie 字符串为字典"""
    cookies = {}
    for item in cookie_str.split('; '):
        if '=' in item:
            key, value = item.split('=', 1)
            cookies[key] = value
    return cookies


def get_d_c0(cookie_str: str) -> str:
    """从 cookie 中提取 d_c0"""
    match = re.search(r'd_c0=([^;]+)', cookie_str)
    if match:
        return match.group(1)
    raise ValueError("Cookie 中缺少 d_c0 字段，请确保已登录知乎")


def get_referer(url: str) -> str:
    """根据 API URL 推断 referer"""
    # 从 API URL 推断页面 referer
    # /api/v3/moments/{user}/activities -> /people/{user}
    # /api/v4/questions/{id}/answers -> /question/{id}
    if '/moments/' in url:
        match = re.search(r'/moments/([^/]+)', url)
        if match:
            return f"https://www.zhihu.com/people/{match.group(1)}"
    elif '/questions/' in url:
        match = re.search(r'/questions/([^/]+)', url)
        if match:
            return f"https://www.zhihu.com/question/{match.group(1)}"
    return "https://www.zhihu.com/"


def test_api(url: str, signature: dict, cookies: dict):
    """测试知乎 API 请求"""
    referer = get_referer(url)

    headers = {
        "accept": "*/*",
        "accept-language": "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
        "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
        "x-requested-with": "fetch",
        "x-api-version": "3.0.40",
        "x-kl-ajax-request": "Ajax_Request",
        "x-zse-93": signature['x_zse_93'],
        "x-zse-96": signature['x_zse_96'],
        "origin": "https://www.zhihu.com",
        "referer": referer,
        "sec-ch-ua": '"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"',
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"Windows"',
        "sec-fetch-dest": "empty",
        "sec-fetch-mode": "cors",
        "sec-fetch-site": "same-origin",
    }

    print(f"\n{'='*60}")
    print("签名信息:")
    print(f"  x-zse-93: {signature['x_zse_93']}")
    print(f"  x-zse-96: {signature['x_zse_96']}")
    print(f"  referer: {referer}")
    print(f"{'='*60}")

    print(f"\n请求 URL: {url}")

    try:
        resp = requests.get(url, headers=headers, cookies=cookies, timeout=10)
        print(f"状态码: {resp.status_code}")

        if resp.status_code == 200:
            data = resp.json()
            if 'data' in data:
                count = len(data['data'])
                print(f"成功获取 {count} 条数据")
                if count > 0:
                    first = data['data'][0]
                    print(data['data'][0])
                    # 根据数据类型显示摘要
                    if 'action' in first:
                        # moments activities 格式
                        print(f"\n第一条动态类型: {first.get('action', 'unknown')}")
                        if 'target' in first:
                            target = first['target']
                            if 'excerpt' in target:
                                print(f"摘要: {target['excerpt'][:100]}...")
                            elif 'title' in target:
                                print(f"标题: {target['title']}")
                    elif 'excerpt' in first:
                        print(f"\n第一条摘要: {first['excerpt'][:100]}...")
                    elif 'content' in first:
                        print(f"\n第一条内容: {str(first['content'])[:100]}...")
                    elif 'title' in first:
                        print(f"\n第一条标题: {first['title']}")
                    else:
                        print(f"\n第一条数据键: {list(first.keys())}")
            elif 'error' in data:
                print(f"API 错误: {data['error']}")
            else:
                print(f"响应: {json.dumps(data, indent=2, ensure_ascii=False)[:500]}")
        else:
            print(f"请求失败，响应: {resp.text[:300]}")
    except requests.exceptions.Timeout:
        print("请求超时")
    except Exception as e:
        print(f"请求异常: {e}")


def main():
    parser = argparse.ArgumentParser(description='知乎签名生成 Demo')
    parser.add_argument('--url', default=None, help='知乎 API URL (默认测试问题接口)')
    args = parser.parse_args()

    # 清理 cookie 字符串
    cookie_str = COOKIE.strip().replace('\n', '').replace('\r', '')

    if not cookie_str or '把你的知乎' in cookie_str:
        print("请先编辑 demo.py 文件，在 COOKIE 变量中填写你的知乎 Cookie")
        sys.exit(1)

    # 处理 URL (支持 URL 编码输入)
    if args.url:
        test_url = urllib.parse.unquote(args.url)
    else:
        test_url = 'https://www.zhihu.com/api/v4/questions/659012275/answers?limit=5'

    # 提取 d_c0
    d_c0 = get_d_c0(cookie_str)
    print(f"提取 d_c0: {d_c0[:20]}...")

    # 生成签名
    signature = get_signature(test_url, d_c0)
    print(f"\n签名生成成功!")

    # 测试请求
    cookies = parse_cookies(cookie_str)
    test_api(test_url, signature, cookies)


if __name__ == '__main__':
    main()