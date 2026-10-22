"""冒烟测试脚本里不发请求的判定逻辑的单测。

python3 -m unittest discover -s .claude/skills/mulerouter-smoke-test/scripts
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import mulerouter_smoke_test as smoke  # noqa: E402


class FindVendorLeaksTest(unittest.TestCase):
    def test_clean_responses_have_no_leaks(self):
        views = {
            "GET /v1/videos/{id}": {"id": "task_abc", "model": "wan2.2-i2v-spicy", "status": "completed"},
            "GET /v1/tasks/{id}": {"task_id": "task_abc", "status": "SUCCESS"},
        }
        self.assertEqual(smoke.find_vendor_leaks(views), [])

    def test_names_the_response_and_field_of_a_leak(self):
        views = {
            "GET /v1/videos/{id}": {
                "id": "task_abc",
                "metadata": {"url": "https://mule-router-assets.muleusercontent.com/x/result_00.mp4"},
            },
        }
        self.assertEqual(smoke.find_vendor_leaks(views), [
            {"response": "GET /v1/videos/{id}", "field": "metadata.url", "needle": "muleusercontent"},
        ])

    def test_reads_into_fields_that_hold_encoded_json(self):
        # 使用日志的 other 是一个 JSON 字符串，命中要报到它里面的字段上。
        views = {
            "使用日志": {"other": '{"model_price":0.02,"upstream_model_name":"carrothub/wan2.2-i2v-spicy/generation"}'},
        }
        self.assertEqual(smoke.find_vendor_leaks(views), [
            {"response": "使用日志", "field": "other.upstream_model_name", "needle": "carrothub"},
        ])

    def test_leaves_the_plugin_key_to_issue_40(self):
        views = {"控制台任务列表": {"items": [{"platform": "mulerouter",
                                               "properties": {"upstream_model_name": "z-image-spicy"}}]}}
        self.assertEqual(smoke.find_vendor_leaks(views), [])


class VendorHiddenCheckTest(unittest.TestCase):
    CTX = {"group": "default", "group_ratio": 1.0, "models": {"wan2.2-i2v-spicy": 0.1}}

    def vendor_hidden(self, caller_views):
        result = smoke.verify_case({"task_id": "task_abc", "caller_views": caller_views}, "wan-480p", self.CTX)
        return next(check for check in result["checks"] if check["name"] == "vendor_hidden")

    def test_passes_when_no_response_mentions_the_vendor(self):
        check = self.vendor_hidden({"GET /v1/tasks/{id}": {"task_id": "task_abc", "platform": "mulerouter"}})
        self.assertTrue(check["passed"])

    def test_fails_naming_each_response_and_field(self):
        check = self.vendor_hidden({
            "GET /v1/tasks/{id}": {"task_id": "task_abc"},
            "控制台任务列表": {"items": [{"data": {"videos": ["https://x.muleusercontent.com/carrothub/r.mp4"]}}]},
        })
        self.assertFalse(check["passed"])
        self.assertIn("控制台任务列表 items[0].data.videos[0]", check["detail"])


if __name__ == "__main__":
    unittest.main()
