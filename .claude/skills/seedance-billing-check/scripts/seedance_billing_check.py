#!/usr/bin/env python3
"""Seedance 计费实测：对部署实例发起真实付费调用，用官方口径复算应扣额度并对账。

只依赖标准库。用法见同目录 ../SKILL.md。
"""

import argparse
import json
import math
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# ---------------------------------------------------------------------------
# 官方价格与参数，来源见 references/official-prices.md，官方调价后同步更新
# ---------------------------------------------------------------------------

# 元/百万 token（在线推理）。键为 (分辨率档, 是否含视频输入)，分辨率档 "base" 表示 480p/720p。
OFFICIAL_PRICES = {
    "seedance-2.0": {
        ("base", False): 46.0,
        ("base", True): 28.0,
        ("1080p", False): 51.0,
        ("1080p", True): 31.0,
        ("4k", False): 26.0,
        ("4k", True): 16.0,
    },
    "seedance-2.0-fast": {
        ("base", False): 37.0,
        ("base", True): 22.0,
    },
    "seedance-2.0-mini": {
        ("base", False): 23.0,
        ("base", True): 14.0,
    },
    "seedance-2.5": {
        ("base", False): 70.0,
        ("base", True): 42.0,
    },
}

# 16:9 输出的像素宽高，用于按官方公式估算 token 用量
RESOLUTION_PIXELS = {
    "480p": (864, 480),
    "720p": (1280, 720),
    "1080p": (1920, 1080),
    "4k": (3840, 2160),
}
DEFAULT_FPS = 24

# 480p 的官方实际像素宽高与推测值有约 3% 偏差，单独放宽容差
FORMULA_TOLERANCE = {"480p": 0.20, "720p": 0.15, "1080p": 0.15, "4k": 0.15}

PROMPT = "a calm ocean wave rolling onto an empty beach at sunrise, slow camera pan"


def price_family(model: str) -> str:
    """把带版本日期的模型 ID 归到官方价格表的模型族。"""
    normalized = model.replace("_", "-").lower()
    for family in ("seedance-2.0-fast", "seedance-2.0-mini", "seedance-2.5", "seedance-2.0"):
        # 实例上的模型 ID 用连字符写版本号（seedance-2-0-260128），官方文档用点号
        if family.replace(".", "-") in normalized or family in normalized:
            return family
    return ""


def resolution_tier(resolution: str) -> str:
    res = (resolution or "").strip().lower()
    if res == "1080p":
        return "1080p"
    if res == "4k":
        return "4k"
    return "base"


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------


class ApiError(RuntimeError):
    pass


def request_json(method, url, token, body=None, extra_headers=None, timeout=120):
    data = None
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if extra_headers:
        headers.update(extra_headers)
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:800]
        raise ApiError(f"{method} {url} -> HTTP {exc.code}: {detail}") from exc
    except urllib.error.URLError as exc:
        raise ApiError(f"{method} {url} -> 连接失败: {exc.reason}") from exc
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ApiError(f"{method} {url} -> 返回非 JSON: {raw[:400]}") from exc


class Client:
    def __init__(self, base_url, relay_key, admin_token, user_id=None):
        self.base = base_url.rstrip("/")
        self.relay_key = relay_key
        self.admin_token = admin_token
        self.admin_headers = {"New-API-User": str(user_id)} if user_id else None

    def admin_get(self, path, params=None):
        url = self.base + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        payload = request_json("GET", url, self.admin_token, extra_headers=self.admin_headers)
        if not payload.get("success", True):
            raise ApiError(f"GET {path} 返回 success=false: {payload.get('message')}")
        return payload.get("data")

    def relay_post(self, path, body):
        return request_json("POST", self.base + path, self.relay_key, body=body, timeout=180)

    def relay_get(self, path):
        return request_json("GET", self.base + path, self.relay_key)


# ---------------------------------------------------------------------------
# 预检
# ---------------------------------------------------------------------------


def build_cases(names, video_url_from_env):
    """把档位名解析成用例，含视频输入的排到后面（需要前一档的产出当输入）。"""
    cases = []
    for name in names:
        has_video = name.startswith("video-")
        resolution = name[len("video-"):] if has_video else name
        if resolution not in RESOLUTION_PIXELS:
            raise SystemExit(f"未知档位 {name}，可选：480p/720p/1080p/4k，含视频输入加 video- 前缀")
        cases.append({"name": name, "resolution": resolution, "has_video": has_video})
    cases.sort(key=lambda c: c["has_video"])
    if any(c["has_video"] for c in cases) and not video_url_from_env:
        if not any(not c["has_video"] for c in cases):
            raise SystemExit(
                "含视频输入档位需要素材：要么设置 SEEDANCE_TEST_VIDEO_URL，"
                "要么在 --cases 里同时保留一个无视频档位供脚本先产出视频"
            )
    return cases


def estimate_tokens(resolution, output_seconds, input_seconds=0):
    width, height = RESOLUTION_PIXELS[resolution]
    return int((input_seconds + output_seconds) * width * height * DEFAULT_FPS / 1024)


def preflight(client, model, cases, seconds, usd_rate_override):
    family = price_family(model)
    if not family:
        raise SystemExit(
            f"模型 {model} 不在官方价格表的已知模型族里（2.0 / 2.0-fast / 2.0-mini / 2.5）。"
            "如果是新模型，先更新 OFFICIAL_PRICES 和 references/official-prices.md。"
        )
    prices = OFFICIAL_PRICES[family]

    status = client.admin_get("/api/status") or {}
    quota_per_unit = float(status.get("quota_per_unit") or 500000.0)
    usd_rate = float(usd_rate_override or status.get("usd_exchange_rate") or 7.3)

    user = client.admin_get("/api/user/self") or {}
    group = user.get("group") or ""
    quota_before = int(user.get("quota") or 0)

    # /api/pricing 的模型列表在 data 里，分组倍率在同级的 group_ratio 里，所以取整个 payload
    raw = request_json(
        "GET", client.base + "/api/pricing", client.admin_token, extra_headers=client.admin_headers
    )
    pricing = raw.get("data") or []
    group_ratios = raw.get("group_ratio") or {}
    group_ratio = float(group_ratios.get(group, 1.0))

    entry = next((p for p in pricing if p.get("model_name") == model), None)
    problems = []
    if entry is None:
        problems.append(f"模型 {model} 不在实例的可用模型列表里（渠道未配置该模型，或当前分组不可用）")
        quota_type, model_ratio = None, None
    else:
        quota_type = entry.get("quota_type")
        model_ratio = float(entry.get("model_ratio") or 0)
        if quota_type == 1:
            problems.append(
                f"模型 {model} 配了固定价格（model_price={entry.get('model_price')}），"
                "会走按次计费并整体跳过 token 差额结算，测不出真实计费。请先删掉固定价格配置，只保留模型倍率。"
            )
        elif not model_ratio:
            problems.append(f"模型 {model} 的模型倍率为 0 或未配置")

    base_price_cny = prices[("base", False)]
    expected_ratio = base_price_cny / usd_rate / 2.0
    ratio_note = None
    if model_ratio:
        drift = (model_ratio - expected_ratio) / expected_ratio
        ratio_note = {
            "configured": model_ratio,
            "expected_at_official_cost": round(expected_ratio, 4),
            "drift_pct": round(drift * 100, 1),
        }
        if abs(drift) > 0.5:
            problems.append(
                f"模型倍率 {model_ratio} 与官方基准价换算值 {expected_ratio:.4f} 相差 {drift * 100:.0f}%，"
                "确认倍率是否配在了「480p/720p + 无视频输入」基准档上"
            )

    plan = []
    total_quota = 0
    for case in cases:
        tier = resolution_tier(case["resolution"])
        if (tier, case["has_video"]) not in prices:
            plan.append({**case, "supported": False, "reason": f"{family} 不支持 {case['resolution']} 档"})
            continue
        input_seconds = seconds if case["has_video"] else 0
        tokens = estimate_tokens(case["resolution"], seconds, input_seconds)
        other_ratio = prices[(tier, case["has_video"])] / base_price_cny
        quota = math.floor(tokens * (model_ratio or 0) * group_ratio * other_ratio)
        total_quota += quota
        plan.append(
            {
                **case,
                "supported": True,
                "tier": tier,
                "expected_other_ratio": round(other_ratio, 6),
                "official_unit_price_cny": prices[(tier, case["has_video"])],
                "estimated_tokens": tokens,
                "estimated_quota": quota,
                "estimated_cny": round(tokens * prices[(tier, case["has_video"])] / 1e6, 2),
            }
        )

    if total_quota > quota_before:
        problems.append(
            f"余额不足：预计消耗 {total_quota} 额度，当前余额 {quota_before}"
        )

    return {
        "model": model,
        "family": family,
        "group": group,
        "group_ratio": group_ratio,
        "model_ratio": model_ratio,
        "quota_type": quota_type,
        "quota_per_unit": quota_per_unit,
        "usd_exchange_rate": usd_rate,
        "quota_before": quota_before,
        "base_price_cny": base_price_cny,
        "model_ratio_check": ratio_note,
        "plan": plan,
        "estimated_total_quota": total_quota,
        "estimated_total_cny": round(sum(p.get("estimated_cny", 0) for p in plan), 2),
        "problems": problems,
    }


# ---------------------------------------------------------------------------
# 单档位执行
# ---------------------------------------------------------------------------


def submit(client, model, case, seconds, video_url):
    metadata = {"resolution": case["resolution"]}
    if case["has_video"]:
        # 上游要求参考视频显式带 role=reference_video，否则 400 InvalidParameter
        metadata["content"] = [
            {"type": "video_url", "video_url": {"url": video_url}, "role": "reference_video"}
        ]
    body = {"model": model, "prompt": PROMPT, "seconds": str(seconds), "metadata": metadata}
    resp = client.relay_post("/v1/video/generations", body)
    task_id = resp.get("task_id") or resp.get("id")
    if not task_id:
        raise ApiError(f"提交未返回 task id：{json.dumps(resp, ensure_ascii=False)[:400]}")
    return task_id, resp


TERMINAL_OK = ("completed", "succeeded", "success")
TERMINAL_FAIL = ("failed", "failure", "error")


def poll_video(client, task_id, timeout, interval=10):
    deadline = time.time() + timeout
    last = {}
    while time.time() < deadline:
        payload = client.relay_get(f"/v1/video/generations/{task_id}")
        # 实例把任务体裹在 {"code":..., "data":{...}} 信封里，状态是大写的 SUCCESS/FAILURE
        last = payload.get("data") if isinstance(payload.get("data"), dict) else payload
        status = (last.get("status") or "").lower()
        if status in TERMINAL_OK or status in TERMINAL_FAIL:
            last["_failed"] = status in TERMINAL_FAIL
            return last
        time.sleep(interval)
    last["_timeout"] = True
    return last


def admin_task(client, task_id):
    page = client.admin_get("/api/task/", {"task_id": task_id, "page_size": 10})
    items = (page or {}).get("items") or []
    return next((t for t in items if t.get("task_id") == task_id), None)


def parse_other(log):
    raw = log.get("other")
    if not raw:
        return {}
    try:
        return json.loads(raw) if isinstance(raw, str) else dict(raw)
    except (json.JSONDecodeError, TypeError, ValueError):
        return {}


def fetch_settlement_logs(client, model, task_id, since_ts):
    """找出该任务的差额结算日志（消费或退款），它的 other 里带 task_id。"""
    found = []
    for log_type in (2, 6):  # 2=消费 6=退款
        page = client.admin_get(
            "/api/log/",
            {
                "type": log_type,
                "model_name": model,
                "start_timestamp": since_ts,
                "page_size": 100,
            },
        )
        for log in ((page or {}).get("items") or []):
            other = parse_other(log)
            if other.get("task_id") == task_id:
                found.append({**log, "_other": other})
    return found


def wait_settlement(client, model, task_id, since_ts, timeout, interval=10):
    deadline = time.time() + timeout
    while True:
        logs = fetch_settlement_logs(client, model, task_id, since_ts)
        if logs:
            return logs
        if time.time() >= deadline:
            return []
        time.sleep(interval)


def run_case(client, model, case, seconds, ctx, video_url, poll_timeout, settle_timeout):
    result = {"case": case["name"], "resolution": case["resolution"], "has_video": case["has_video"]}
    since_ts = int(time.time()) - 60
    print(f"\n=== {case['name']} 提交任务 ...", flush=True)
    task_id, submit_resp = submit(client, model, case, seconds, video_url)
    result["task_id"] = task_id
    print(f"    task_id={task_id}，等待生成（最长 {poll_timeout}s）", flush=True)

    video = poll_video(client, task_id, poll_timeout)
    result["video_status"] = video.get("status")
    result["video_url"] = (
        video.get("result_url")
        or ((video.get("data") or {}).get("content") or {}).get("video_url")
        or (video.get("metadata") or {}).get("url")
    )
    if video.get("_timeout"):
        result["outcome"] = "timeout"
        result["note"] = f"轮询 {poll_timeout}s 未终态，最后状态 {video.get('status')}"
        return result
    if video.get("_failed"):
        result["outcome"] = "failed"
        result["note"] = json.dumps(video.get("error") or {}, ensure_ascii=False)
        return result

    print("    生成完成，等待差额结算落库 ...", flush=True)
    logs = wait_settlement(client, model, task_id, since_ts, settle_timeout)
    task = admin_task(client, task_id)
    result["task_quota"] = (task or {}).get("quota")

    upstream = {}
    if task and task.get("data"):
        data = task["data"]
        if isinstance(data, str):
            try:
                data = json.loads(data)
            except json.JSONDecodeError:
                data = {}
        upstream = (data or {}).get("usage") or {}
        result["upstream_resolution"] = (data or {}).get("resolution")
        result["upstream_duration"] = (data or {}).get("duration")
        result["upstream_fps"] = (data or {}).get("framespersecond")
    result["upstream_usage"] = upstream

    settlement = None
    for log in logs:
        if "token" in (log.get("content") or "") or log["_other"].get("actual_quota") is not None:
            settlement = log
            break
    settlement = settlement or (logs[0] if logs else None)

    if settlement:
        other = settlement["_other"]
        result["settlement"] = {
            "log_type": settlement.get("type"),
            "content": settlement.get("content"),
            "delta_quota": settlement.get("quota"),
            "pre_consumed_quota": other.get("pre_consumed_quota"),
            "actual_quota": other.get("actual_quota"),
            "model_ratio": other.get("model_ratio"),
            "group_ratio": other.get("group_ratio"),
            "video_input": other.get("video_input"),
        }
        match = re.search(r"tokens=(\d+)", settlement.get("content") or "")
        result["billed_tokens"] = int(match.group(1)) if match else None
    else:
        result["settlement"] = None
        result["billed_tokens"] = None
        result["note"] = (
            "任务成功但等不到差额结算日志。若 upstream_usage 为空，说明上游没返回 usage，"
            "扣费停留在预扣的小额上（严重欠收）；否则检查模型是否被配成固定价格、或后台任务轮询间隔过长。"
        )

    result["outcome"] = "settled" if settlement else "no_settlement"
    result["assertions"] = verify_case(result, case, ctx, seconds)
    return result


def verify_case(result, case, ctx, seconds):
    """四条断言 + 两项参考信息。返回列表，每项含 name/passed/detail。"""
    checks = []
    prices = OFFICIAL_PRICES[ctx["family"]]
    tier = resolution_tier(case["resolution"])
    base_price = prices[("base", False)]
    expected_ratio = prices[(tier, case["has_video"])] / base_price

    upstream = result.get("upstream_usage") or {}
    completion = upstream.get("completion_tokens")
    total = upstream.get("total_tokens")
    billed = result.get("billed_tokens")
    settlement = result.get("settlement") or {}

    # 1. token 口径：官方明确以 completion_tokens 为准
    if billed is None or completion is None:
        checks.append(
            {
                "name": "token_field",
                "passed": None,
                "detail": f"数据不足（billed={billed}, completion_tokens={completion}, total_tokens={total}）",
            }
        )
    else:
        passed = billed == completion
        detail = f"计费用 {billed}，上游 completion_tokens={completion}，total_tokens={total}"
        if not passed and total is not None and billed == total:
            over = (total - completion) / completion * 100 if completion else float("inf")
            detail += f" → 实例用的是 total_tokens，相对官方口径超收 {over:.1f}%"
        checks.append({"name": "token_field", "passed": passed, "detail": detail})

    # 2. 分辨率 / 视频输入倍率
    actual_ratio = settlement.get("video_input")
    if abs(expected_ratio - 1.0) < 1e-9:
        passed = actual_ratio is None
        detail = f"基准档应无 video_input 倍率，实际 {actual_ratio}"
    elif actual_ratio is None:
        passed = False
        detail = f"缺少 video_input 倍率，期望 {expected_ratio:.6f}（{prices[(tier, case['has_video'])]}/{base_price}）"
    else:
        passed = abs(float(actual_ratio) - expected_ratio) < 1e-4
        detail = f"实际 {actual_ratio}，期望 {expected_ratio:.6f}"
    checks.append({"name": "other_ratio", "passed": passed, "detail": detail})

    # 3. 扣费额度复算
    model_ratio = settlement.get("model_ratio") or ctx["model_ratio"]
    group_ratio = settlement.get("group_ratio") or ctx["group_ratio"]
    actual_quota = settlement.get("actual_quota")
    if None in (billed, model_ratio, group_ratio, actual_quota):
        checks.append({"name": "quota_recompute", "passed": None, "detail": "缺少倍率或扣费数据，无法复算"})
    else:
        multiplier = float(actual_ratio) if actual_ratio is not None else 1.0
        expected_quota = math.floor(billed * float(model_ratio) * float(group_ratio) * multiplier)
        passed = abs(expected_quota - int(actual_quota)) <= 1  # 截断转换允许 1 点误差
        checks.append(
            {
                "name": "quota_recompute",
                "passed": passed,
                "detail": (
                    f"实际 {actual_quota}，复算 {expected_quota} "
                    f"= floor({billed} × {model_ratio} × {group_ratio} × {multiplier})"
                ),
            }
        )

    # 参考：官方人民币对照。官方按 completion_tokens 计价，所以基准取 completion，
    # 这样 token 口径用错时这里也会同步暴露出差额。
    official_tokens = completion or billed
    if official_tokens:
        official_cny = official_tokens * prices[(tier, case["has_video"])] / 1e6
        info = {"official_cny": round(official_cny, 4)}
        if actual_quota:
            charged_cny = int(actual_quota) / ctx["quota_per_unit"] * ctx["usd_exchange_rate"]
            info["charged_cny"] = round(charged_cny, 4)
            info["markup_pct"] = round((charged_cny - official_cny) / official_cny * 100, 1)
        checks.append(
            {
                "name": "cny_reference",
                "passed": None,
                "detail": "官方 ¥{official_cny}｜实例 ¥{charged_cny}｜加价 {markup_pct}%".format(
                    official_cny=info["official_cny"],
                    charged_cny=info.get("charged_cny", "?"),
                    markup_pct=info.get("markup_pct", "?"),
                ),
            }
        )

    # 参考：token 用量公式
    if completion:
        input_seconds = seconds if case["has_video"] else 0
        estimated = estimate_tokens(case["resolution"], seconds, input_seconds)
        drift = (completion - estimated) / estimated
        tolerance = FORMULA_TOLERANCE.get(case["resolution"], 0.15)
        note = "在容差内" if abs(drift) <= tolerance else "超出容差"
        if case["has_video"]:
            note += f"（估算假设输入视频时长={input_seconds}s；含视频输入另有官方最低 token 用量下限，实际高于估算属正常）"
        checks.append(
            {
                "name": "token_formula_reference",
                "passed": None,
                "detail": f"实际 {completion}，公式估算 {estimated}，偏差 {drift * 100:+.1f}%，{note}",
            }
        )

    return checks


# ---------------------------------------------------------------------------
# 输出
# ---------------------------------------------------------------------------


def print_preflight(ctx):
    print("=== 预检 ===")
    print(f"模型      : {ctx['model']}（价格族 {ctx['family']}）")
    print(f"分组      : {ctx['group']}（分组倍率 {ctx['group_ratio']}）")
    print(f"计费方式  : {'倍率(按量)' if ctx['quota_type'] == 0 else ctx['quota_type']}")
    print(f"模型倍率  : {ctx['model_ratio']}")
    if ctx["model_ratio_check"]:
        chk = ctx["model_ratio_check"]
        print(
            f"倍率核对  : 官方基准价 ¥{ctx['base_price_cny']}/百万token @ 汇率 {ctx['usd_exchange_rate']} "
            f"→ 应配 {chk['expected_at_official_cost']}，实配 {chk['configured']}（{chk['drift_pct']:+}%）"
        )
    print(f"当前余额  : {ctx['quota_before']}")
    print("\n待测档位：")
    for p in ctx["plan"]:
        if not p.get("supported"):
            print(f"  - {p['name']:<14} 跳过：{p['reason']}")
            continue
        print(
            f"  - {p['name']:<14} 官方单价 ¥{p['official_unit_price_cny']}/百万token"
            f"｜预期倍率 {p['expected_other_ratio']:.4f}"
            f"｜预估 {p['estimated_tokens']} tokens"
            f"｜预估扣 {p['estimated_quota']} 额度（官方约 ¥{p['estimated_cny']}）"
        )
    print(f"\n合计预估：{ctx['estimated_total_quota']} 额度，官方口径约 ¥{ctx['estimated_total_cny']}")
    if ctx["problems"]:
        print("\n!! 预检问题：")
        for problem in ctx["problems"]:
            print(f"   - {problem}")


def print_results(report):
    print("\n\n=== 结果 ===")
    symbols = {True: "PASS", False: "FAIL", None: "INFO"}
    for case in report["cases"]:
        header = f"[{case['case']}] {case.get('outcome')}"
        if case.get("billed_tokens") is not None:
            header += f"｜tokens={case['billed_tokens']}"
        if (case.get("settlement") or {}).get("actual_quota") is not None:
            header += f"｜扣费={case['settlement']['actual_quota']}"
        print("\n" + header)
        if case.get("note"):
            print(f"  说明: {case['note']}")
        for check in case.get("assertions") or []:
            print(f"  {symbols[check['passed']]:<4} {check['name']:<26} {check['detail']}")

    recon = report["reconciliation"]
    print("\n--- 额度对账 ---")
    print(
        f"  {symbols[recon['passed']]:<4} quota_reconciled           "
        f"余额减少 {recon['quota_spent']}，各档位扣费之和 {recon['sum_actual_quota']}"
    )
    failed = [
        (c["case"], chk["name"])
        for c in report["cases"]
        for chk in (c.get("assertions") or [])
        if chk["passed"] is False
    ]
    if recon["passed"] is False:
        failed.append(("全局", "quota_reconciled"))
    print("\n结论：", "全部通过" if not failed else f"{len(failed)} 项失败 → " + ", ".join(f"{c}/{n}" for c, n in failed))


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main():
    parser = argparse.ArgumentParser(description="Seedance 计费实测")
    parser.add_argument("--base-url", default=None, help="实例地址，覆盖 NEW_API_BASE_URL")
    parser.add_argument("--api-key", default=None, help="中转 API key，覆盖 NEW_API_KEY")
    parser.add_argument("--admin-token", default=None, help="管理员访问令牌，覆盖 NEW_API_ADMIN_TOKEN")
    parser.add_argument("--user-id", default=None, help="仅当实例要求 New-API-User 头时需要，覆盖 NEW_API_USER_ID")
    parser.add_argument("--video-url", default=None, help="含视频输入档位的素材 URL，覆盖 SEEDANCE_TEST_VIDEO_URL")
    parser.add_argument("--cases", default="480p,720p,1080p,video-480p", help="逗号分隔的档位")
    parser.add_argument("--model", default=os.environ.get("SEEDANCE_MODEL", "doubao-seedance-2-0-260128"))
    parser.add_argument("--seconds", type=int, default=5, help="输出视频时长")
    parser.add_argument("--dry-run", action="store_true", help="只做预检，不花钱")
    parser.add_argument("--yes", action="store_true", help="确认花费并实际执行")
    parser.add_argument("--poll-timeout", type=int, default=900, help="单任务生成轮询超时（秒）")
    parser.add_argument("--settle-timeout", type=int, default=180, help="等差额结算落库超时（秒）")
    parser.add_argument("--usd-rate", type=float, default=None, help="覆盖实例的 usd_exchange_rate")
    parser.add_argument("--out", default="/tmp/seedance-billing-report.json")
    args = parser.parse_args()

    # 凭据来源：命令行参数优先，其次环境变量。命令行传密钥会出现在进程列表和 shell 历史里，
    # 日常用环境变量更安全，参数主要方便一次性切换实例。
    base_url = args.base_url or os.environ.get("NEW_API_BASE_URL")
    relay_key = args.api_key or os.environ.get("NEW_API_KEY")
    admin_token = args.admin_token or os.environ.get("NEW_API_ADMIN_TOKEN")
    missing = [
        label
        for label, value in (
            ("--base-url / NEW_API_BASE_URL", base_url),
            ("--api-key / NEW_API_KEY", relay_key),
            ("--admin-token / NEW_API_ADMIN_TOKEN", admin_token),
        )
        if not value
    ]
    if missing:
        raise SystemExit("缺少凭据：" + "，".join(missing))

    client = Client(
        base_url,
        relay_key,
        admin_token,
        args.user_id or os.environ.get("NEW_API_USER_ID"),
    )
    env_video_url = args.video_url or os.environ.get("SEEDANCE_TEST_VIDEO_URL")
    cases = build_cases([c.strip() for c in args.cases.split(",") if c.strip()], env_video_url)

    ctx = preflight(client, args.model, cases, args.seconds, args.usd_rate)
    print_preflight(ctx)

    if args.dry_run:
        return 0
    if ctx["problems"]:
        raise SystemExit("\n预检未通过，先修配置再跑。要强行忽略请自行判断风险后重跑（本脚本不提供绕过开关）。")
    if not args.yes:
        raise SystemExit("\n这会产生真实费用。确认预检输出后加 --yes 重新执行。")

    report = {"context": ctx, "cases": []}
    video_url = env_video_url
    runnable = [p for p in ctx["plan"] if p.get("supported")]
    for plan_item in runnable:
        case = {"name": plan_item["name"], "resolution": plan_item["resolution"], "has_video": plan_item["has_video"]}
        if case["has_video"] and not video_url:
            print(f"\n跳过 {case['name']}：没有可用的输入视频（前序档位未产出可用 URL）")
            report["cases"].append({"case": case["name"], "outcome": "skipped", "note": "无输入视频素材"})
            continue
        try:
            result = run_case(
                client, args.model, case, args.seconds, ctx, video_url, args.poll_timeout, args.settle_timeout
            )
        except ApiError as exc:
            result = {"case": case["name"], "outcome": "error", "note": str(exc)}
        report["cases"].append(result)
        if not case["has_video"] and not video_url and result.get("video_url"):
            video_url = result["video_url"]
            print(f"    用该档位产出的视频作为含视频输入档位的素材: {video_url[:80]}...")

    user_after = client.admin_get("/api/user/self") or {}
    quota_after = int(user_after.get("quota") or 0)
    spent = ctx["quota_before"] - quota_after
    sum_actual = sum(
        int((c.get("settlement") or {}).get("actual_quota") or 0) for c in report["cases"]
    )
    report["reconciliation"] = {
        "quota_before": ctx["quota_before"],
        "quota_after": quota_after,
        "quota_spent": spent,
        "sum_actual_quota": sum_actual,
        "passed": abs(spent - sum_actual) <= len(report["cases"]),
    }
    report["spent_cny_official_rate"] = round(
        spent / ctx["quota_per_unit"] * ctx["usd_exchange_rate"], 2
    )

    print_results(report)
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(report, handle, ensure_ascii=False, indent=2)
    print(f"\n完整报告：{args.out}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ApiError as exc:
        print(f"\nAPI 调用失败：{exc}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("\n已中断。注意：已提交的任务仍会在上游继续执行并计费。", file=sys.stderr)
        sys.exit(130)
