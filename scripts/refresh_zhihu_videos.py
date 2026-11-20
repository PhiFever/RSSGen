# /// script
# requires-python = ">=3.10"
# dependencies = ["PyYAML>=6,<7"]
# ///
"""查找缺少视频超链接的知乎视频想法，并用 RSSGen CLI 逐条重刷。"""

import argparse
import json
import re
import subprocess
import sys
import time
from datetime import datetime
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen

import yaml


class VideoLinks(HTMLParser):
    def __init__(self):
        super().__init__()
        self.has_link = False
        self.anchor_text = None

    def handle_starttag(self, tag, attrs):
        if tag != "a":
            return
        url = urlsplit(dict(attrs).get("href") or "")
        self.anchor_text = None
        if url.scheme in ("http", "https") and url.hostname:
            self.anchor_text = []
            video_page = url.hostname in ("www.zhihu.com", "zhihu.com") and re.fullmatch(
                r"/video/\d+/?", url.path
            )
            if video_page or url.path.lower().endswith(".mp4"):
                self.has_link = True

    def handle_data(self, data):
        if self.anchor_text is not None:
            self.anchor_text.append(data)

    def handle_endtag(self, tag):
        if tag == "a" and self.anchor_text is not None:
            if "".join(self.anchor_text).strip() == "查看视频":
                self.has_link = True
            self.anchor_text = None


def has_video_link(content):
    parser = VideoLinks()
    parser.feed(content)
    parser.close()
    return parser.has_link


def candidates(get, action):
    # 按 ID 稳定翻页，收集完候选再写入。
    offset = 0
    while True:
        query = urlencode(
            {"search": action, "order": "id", "direction": "asc",
             "limit": 100, "offset": offset, "status": ["read", "unread"]},
            doseq=True,
        )
        page = get("entries?" + query)
        for entry in page["entries"]:
            if (
                re.fullmatch(r"/feed/zhihu/[^/]+/?", urlsplit(entry["feed"]["feed_url"]).path)
                and re.fullmatch(r"https://www\.zhihu\.com/pin/\d+/?", entry["url"])
                and entry["title"].startswith(f"[{action}]")
                and not has_video_link(entry["content"])
            ):
                yield entry
        offset += len(page["entries"])
        if not page["entries"] or offset >= page["total"]:
            return


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default="config.yml", type=Path, help="RSSGen 配置文件")
    parser.add_argument("--cli", default="rssgen", help="含 --entry-id 的 RSSGen 可执行文件")
    parser.add_argument("--apply", action="store_true", help="执行重刷，默认仅列出候选")
    args = parser.parse_args()
    config = yaml.safe_load(args.config.read_text())["backfill"]

    def get(path):
        request = Request(
            config["miniflux_url"].rstrip("/") + "/v1/" + path,
            headers={"X-Auth-Token": config["miniflux_api_token"]},
        )
        with urlopen(request, timeout=60) as response:
            return json.load(response)

    actions = {"发布了视频": "pin", "赞同了视频": "voteup_pin", "收藏了视频": "collected_pin"}
    entries = [(entry, category) for action, category in actions.items()
               for entry in candidates(get, action)]
    print(f"候选 {len(entries)} 篇", flush=True)
    failed = []
    for entry, category in reversed(entries):
        entry_id = entry["id"]
        print(f"feed={entry['feed_id']} entry={entry_id} {entry['url']}", flush=True)
        if not args.apply:
            continue

        # Miniflux 保存到秒，知乎活动游标精确到毫秒。
        published = datetime.fromisoformat(entry["published_at"].replace("Z", "+00:00"))
        stamp = int(published.timestamp()) * 1000
        result = subprocess.run([
            args.cli, "backfill", "--config", str(args.config), "--source", "zhihu", "refresh",
            "--include", category, "--include-self-interaction", "--feed-id", str(entry["feed_id"]), "--entry-id", str(entry_id),
            "--start-before", str(stamp + 2000), "--stop-after", str(stamp - 1000),
        ], capture_output=True, text=True)
        if result.returncode or not has_video_link(get(f"entries/{entry_id}")["content"]):
            failed.append(entry_id)
            print(result.stdout, result.stderr, file=sys.stderr, flush=True)
        # CLI 各自启动限速器，调用之间也留出请求间隔。
        time.sleep(1)
    if args.apply:
        print(f"读回验证成功 {len(entries) - len(failed)} 篇，未修复 ID {failed}", flush=True)
    return bool(failed)


if __name__ == "__main__":
    sys.exit(main())
