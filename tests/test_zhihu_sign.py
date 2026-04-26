"""知乎签名生成单元测试"""

import pytest
from pathlib import Path

from RSSGen.routes.zhihu import ZhihuSigner


@pytest.fixture(autouse=True)
def reset_signer_ctx():
    """每个测试前后重置 MiniRacer 实例，避免 event loop 冲突"""
    ZhihuSigner._ctx = None
    yield
    ZhihuSigner._ctx = None


class TestZhihuSigner:
    def test_init_loads_js_file(self):
        """初始化时加载 JS 文件"""
        signer = ZhihuSigner()
        assert signer._ctx is not None

    def test_get_signature_returns_valid_format(self):
        """签名返回正确格式"""
        signer = ZhihuSigner()
        url = "https://www.zhihu.com/api/v4/questions/123/answers?limit=5"
        d_c0 = "test_dc0_value"

        result = signer.get_signature(url, d_c0)

        assert "x_zse_93" in result
        assert result["x_zse_93"] == "101_3_3.0"
        assert "x_zse_96" in result
        assert result["x_zse_96"].startswith("2.0_")

    def test_get_signature_different_urls_produce_different_results(self):
        """不同 URL 产生不同签名"""
        signer = ZhihuSigner()

        sig1 = signer.get_signature(
            "https://www.zhihu.com/api/v4/questions/111/answers", "dc0"
        )
        sig2 = signer.get_signature(
            "https://www.zhihu.com/api/v4/questions/222/answers", "dc0"
        )

        assert sig1["x_zse_96"] != sig2["x_zse_96"]