"""知乎签名生成单元测试（纯 Python 实现）"""

from RSSGen.sign.zhihu.sign import X_ZSE_93_VERSION, X_ZSE_96_PREFIX, get_signature


class TestZhihuSigner:
    def test_get_signature_returns_valid_format(self):
        url = "https://www.zhihu.com/api/v4/questions/123/answers?limit=5"
        d_c0 = "test_dc0_value"

        result = get_signature(url, d_c0)

        assert "x_zse_93" in result
        assert result["x_zse_93"] == X_ZSE_93_VERSION
        assert "x_zse_96" in result
        assert result["x_zse_96"].startswith(X_ZSE_96_PREFIX)

    def test_get_signature_different_urls_produce_different_results(self):
        sig1 = get_signature(
            "https://www.zhihu.com/api/v4/questions/111/answers", "dc0"
        )
        sig2 = get_signature(
            "https://www.zhihu.com/api/v4/questions/222/answers", "dc0"
        )

        assert sig1["x_zse_96"] != sig2["x_zse_96"]
