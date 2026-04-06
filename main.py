"""RSSGen 开发入口 — 本地运行用"""

import uvicorn

if __name__ == "__main__":
    uvicorn.run("RSSGen.app:app", host="127.0.0.1", port=8000, reload=True)
