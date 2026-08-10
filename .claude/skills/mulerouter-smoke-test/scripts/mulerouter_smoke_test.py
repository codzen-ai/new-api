#!/usr/bin/env python3
"""MuleRouter carrothub 渠道联调冒烟测试。

分三段跑，前两段不花钱：

1. preflight  读 /api/pricing 确认三个模型都配了固定价格，读余额，列出本轮预算
2. guards     发一批应当被拒的请求，确认护栏生效且没有扣钱
3. live       真实提交 -> 轮询 -> 完成，核对响应形状、产物、扣费金额

用法见 SKILL.md。
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

QUOTA_PER_UNIT = 500_000.0
TASK_ID_PREFIX = "task_"
VENDOR_PREFIX = "/vendors/carrothub/v1"

# 上游价目（美元），来自各端点文档页。基准档与 billing_vars 的倍率基准一致。
# 实例配的 model_price 可以更高（加价是运营决策），preflight 只在低于成本价时警告。
UPSTREAM_BASE_PRICE = {
    "carrothub/qwen-image-edit-spicy/generation": 0.04,
    "carrothub/z-image-spicy/generation": 0.013,
    "carrothub/wan2.2-i2v-spicy/generation": 0.10,
}

# 每个用例声明它期望的倍率，这是断言的核心：倍率不对就是计费不对。
CASES = {
    "z-image": {
        "model": "carrothub/z-image-spicy/generation",
        "endpoint": "z-image-spicy",
        "body": {"prompt": "a calico cat sitting on a sunlit windowsill", "width": 1024, "height": 1024},
        "expect_ratios": {"prompt_extend": 1.076923},
        "artifact_key": "images",
        "produces_image": True,
        "needs_image": False,
    },
    "z-image-noextend": {
        "model": "carrothub/z-image-spicy/generation",
        "endpoint": "z-image-spicy",
        "body": {"prompt": "a red bicycle leaning on a brick wall", "prompt_extend": False},
        "expect_ratios": {"prompt_extend": 1.0},
        "artifact_key": "images",
        "produces_image": True,
        "needs_image": False,
    },
    "qwen-edit": {
        "model": "carrothub/qwen-image-edit-spicy/generation",
        "endpoint": "qwen-image-edit-spicy",
        "body": {"prompt": "make the background a snowy mountain"},
        "expect_ratios": {},
        "artifact_key": "images",
        "produces_image": True,
        "needs_image": True,
    },
    "wan-480p": {
        "model": "carrothub/wan2.2-i2v-spicy/generation",
        "endpoint": "wan2.2-i2v-spicy",
        "body": {"prompt": "the camera slowly pushes in", "duration": 5, "resolution": "480p"},
        "expect_ratios": {"seconds": 1.0, "resolution": 1.0},
        "artifact_key": "videos",
        "produces_image": False,
        "needs_image": True,
    },
    "wan-720p-8s": {
        "model": "carrothub/wan2.2-i2v-spicy/generation",
        "endpoint": "wan2.2-i2v-spicy",
        "body": {"prompt": "the camera orbits around the subject", "duration": 8, "resolution": "720p"},
        "expect_ratios": {"seconds": 1.6, "resolution": 2.0},
        "artifact_key": "videos",
        "produces_image": False,
        "needs_image": True,
    },
}

DEFAULT_CASES = ["z-image", "qwen-edit", "wan-480p"]


class ApiError(RuntimeError):
    pass


def request_raw(method, url, token, body=None, extra_headers=None, timeout=120):
    """返回 (status, headers, parsed_body)，HTTP 错误码也正常返回而不抛异常 —
    护栏检查关心的正是 4xx，把它当异常处理会让调用方很别扭。"""
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
            status, raw, resp_headers = resp.status, resp.read().decode("utf-8", "replace"), dict(resp.headers)
    except urllib.error.HTTPError as exc:
        status, raw, resp_headers = exc.code, exc.read().decode("utf-8", "replace"), dict(exc.headers or {})
    except urllib.error.URLError as exc:
        raise ApiError(f"{method} {url} -> 连接失败: {exc.reason}") from exc

    try:
        parsed = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        parsed = {"_raw": raw[:600]}
    return status, resp_headers, parsed


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
        status, _, payload = request_raw("GET", url, self.admin_token, extra_headers=self.admin_headers)
        if status != 200 or not payload.get("success", True):
            # 带上完整 URL：认证失败最常见的原因是打错了实例，而不是令牌本身无效。
            raise ApiError(f"GET {url} -> HTTP {status}: {payload.get('message') or payload}")
        return payload.get("data")

    def relay(self, method, path, body=None, token=...):
        key = self.relay_key if token is ... else token
        return request_raw(method, self.base + path, key, body=body, timeout=180)


# ---------------------------------------------------------------------------
# preflight
# ---------------------------------------------------------------------------


def preflight(client, case_names):
    # /api/pricing 的分组倍率在 data 同级，所以要整个 payload，不能走 admin_get。
    status, _, payload = request_raw("GET", client.base + "/api/pricing", client.admin_token,
                                     extra_headers=client.admin_headers)
    if status != 200 or not payload.get("success", True):
        raise ApiError(f"GET /api/pricing -> HTTP {status}: {payload.get('message') or payload}")
    pricing = {item["model_name"]: item for item in (payload.get("data") or [])}
    group_ratios = payload.get("group_ratio") or {}

    user = client.admin_get("/api/user/self") or {}
    group = user.get("group") or "default"
    group_ratio = float(group_ratios.get(group, 1.0))

    models = {}
    problems = []
    for name in case_names:
        model_name = CASES[name]["model"]
        if model_name in models:
            continue
        entry = pricing.get(model_name)
        if entry is None:
            problems.append(f"{model_name}: 未出现在 /api/pricing —— 渠道没配这个模型，或渠道被禁用")
            continue
        if entry.get("quota_type") != 1:
            problems.append(f"{model_name}: quota_type={entry.get('quota_type')}，没配「模型固定价格」。"
                            f"异步任务按次计费，配成倍率会走 token 结算路径，本测试的金额断言没有意义")
            continue
        price = float(entry.get("model_price") or 0)
        cost = UPSTREAM_BASE_PRICE.get(model_name)
        if cost and price < cost:
            problems.append(f"{model_name}: 配置价 ${price} 低于上游成本价 ${cost}，每次调用都在亏钱")
        models[model_name] = price

    plan = []
    for name in case_names:
        case = CASES[name]
        price = models.get(case["model"])
        if price is None:
            continue
        multiplier = 1.0
        for ratio in case["expect_ratios"].values():
            multiplier *= ratio
        plan.append({
            "case": name,
            "model": case["model"],
            "model_price": price,
            "ratios": case["expect_ratios"],
            "expected_usd": price * multiplier * group_ratio,
            "expected_quota": expected_quota(price, case["expect_ratios"], group_ratio),
        })

    return {
        "group": group,
        "group_ratio": group_ratio,
        "quota_before": user.get("quota"),
        "models": models,
        "plan": plan,
        "problems": problems,
        "total_usd": sum(item["expected_usd"] for item in plan),
    }


def expected_quota(model_price, ratios, group_ratio):
    """复算实例应扣的额度。

    跟随 relay_task.go 的两步取整：先把按次价折成基础额度，再乘倍率。两步都是截断
    （common.QuotaFromFloat），所以不能合成一次乘法，否则会出现 1 个额度的偏差。
    """
    base = int(model_price * QUOTA_PER_UNIT * group_ratio)
    multiplier = 1.0
    for ratio in ratios.values():
        multiplier *= ratio
    return int(base * multiplier)


# ---------------------------------------------------------------------------
# 护栏检查（不花钱）
# ---------------------------------------------------------------------------


def guard_checks(client):
    """每条都应当在预扣费之前被拒。这些是计费安全不变量的直接体现：
    未声明的成本字段、越界的计费乘数、未登记的模型，都不允许进入扣费链路。

    每条同时断言状态码和拒绝理由。只看状态码是不够的 —— 一个无关的 400（比如
    请求根本没走到校验就被别的中间件拒了）会让所有护栏一起假通过，而假通过的
    安全测试比没有测试更危险。
    """
    guards = [
        {
            "name": "undeclared_cost_param",
            "why": "未在 billing_vars 声明的成本字段（n）必须被拒，否则就是一个无界乘数入口",
            "path": f"{VENDOR_PREFIX}/z-image-spicy/generation",
            "body": {"prompt": "guard check", "n": 4},
            "expect": (400,),
            "must_contain": ["billing_vars"],
        },
        {
            "name": "duration_outside_enum",
            "why": "duration 是计费乘数，枚举外的值必须 400 拒绝而不是静默 clamp",
            "path": f"{VENDOR_PREFIX}/wan2.2-i2v-spicy/generation",
            "body": {"prompt": "guard check", "image": "https://example.com/x.png", "duration": 9},
            "expect": (400,),
            "must_contain": ["duration"],
        },
        {
            "name": "duration_absurdly_large",
            "why": "包装过的负数（JSON 大整数）不能变成巨额乘数",
            "path": f"{VENDOR_PREFIX}/wan2.2-i2v-spicy/generation",
            "body": {"prompt": "guard check", "image": "https://example.com/x.png",
                     "duration": 18446744073686646784},
            "expect": (400,),
            "must_contain": ["duration"],
        },
        {
            "name": "width_out_of_range",
            "why": "声明了边界的字段即使不计费也要守住上界",
            "path": f"{VENDOR_PREFIX}/z-image-spicy/generation",
            "body": {"prompt": "guard check", "width": 4096},
            "expect": (400,),
            "must_contain": ["width"],
        },
        {
            "name": "unlisted_model",
            "why": "路由表里没有的模型既没有价格也没有乘数约束，必须拒绝",
            "path": f"{VENDOR_PREFIX}/not-a-real-model/generation",
            "body": {"prompt": "guard check"},
            # 未登记的模型通常先死在渠道选择上（503「无可用渠道」，平台既有约定），
            # 只有当渠道的 models 列了这个名字、路由表却没有时，才会走到适配器的 404。
            # 两条都是正确的拒绝路径，关键是拒绝理由指向这个模型而不是别的什么错误。
            "expect": (400, 404, 503),
            "must_contain": ["not-a-real-model", "无可用渠道", "not configured", "no available channel"],
        },
        {
            "name": "missing_token",
            "why": "未鉴权的请求不能进入中转链路",
            "path": f"{VENDOR_PREFIX}/z-image-spicy/generation",
            "body": {"prompt": "guard check"},
            "expect": (401,),
            "must_contain": ["token", "令牌", "unauthorized"],
            "token": None,
        },
    ]

    results = []
    for guard in guards:
        token = guard.get("token", ...)
        status, _, payload = client.relay("POST", guard["path"], guard["body"], token=token)
        message = extract_message(payload)
        lowered = message.lower()
        matched = next((needle for needle in guard["must_contain"] if needle.lower() in lowered), None)
        results.append({
            "name": guard["name"],
            "why": guard["why"],
            "status": status,
            "passed": status in guard["expect"] and matched is not None,
            "expect": list(guard["expect"]),
            "expect_message": guard["must_contain"],
            "matched": matched,
            "message": message,
        })
    return results


def extract_message(payload):
    if not isinstance(payload, dict):
        return str(payload)[:200]
    info = payload.get("task_info")
    if isinstance(info, dict) and isinstance(info.get("error"), dict):
        return str(info["error"].get("detail") or info["error"].get("title"))[:300]
    error = payload.get("error")
    if isinstance(error, dict):
        return str(error.get("message"))[:300]
    return str(payload.get("message") or payload)[:300]


# ---------------------------------------------------------------------------
# 实跑
# ---------------------------------------------------------------------------


def run_case(client, name, image_url, poll_timeout, poll_interval):
    case = CASES[name]
    path = f"{VENDOR_PREFIX}/{case['endpoint']}/generation"
    body = dict(case["body"])
    if case["needs_image"]:
        if not image_url:
            return {"case": name, "error": "该用例需要输入图片，但没有可用的 image_url"}
        body["image"] = image_url

    submitted_at = int(time.time()) - 5
    status, headers, payload = client.relay("POST", path, body)
    result = {
        "case": name,
        "model": case["model"],
        "request": body,
        "submit_status": status,
        "submit_body": payload,
        "submitted_at": submitted_at,
    }
    if status != 202:
        result["error"] = f"提交未返回 202：HTTP {status} {extract_message(payload)}"
        return result

    info = (payload.get("task_info") or {})
    task_id = info.get("id") or ""
    result["task_id"] = task_id
    result["submit_status_field"] = info.get("status")
    # 提交响应头带回本次实际生效的倍率，比事后从日志文案里解析可靠得多。
    try:
        result["actual_ratios"] = json.loads(headers.get("X-New-Api-Other-Ratios") or "{}")
    except json.JSONDecodeError:
        result["actual_ratios"] = {}

    if not task_id.startswith(TASK_ID_PREFIX):
        result["error"] = f"返回的 task_info.id 不是本站任务 ID：{task_id}（上游 UUID 泄漏？）"
        return result

    result.update(poll_task(client, path, task_id, poll_timeout, poll_interval))
    return result


def poll_task(client, path, task_id, timeout, interval):
    deadline = time.time() + timeout
    last = {}
    while True:
        status, _, payload = client.relay("GET", f"{path}/{task_id}")
        last = {"poll_status": status, "final_body": payload}
        info = (payload.get("task_info") or {}) if isinstance(payload, dict) else {}
        state = info.get("status")
        if state in ("completed", "failed"):
            last["final_state"] = state
            for key in ("images", "videos", "audios"):
                if payload.get(key):
                    last["artifact_key"] = key
                    last["artifacts"] = payload[key]
                    break
            if state == "failed":
                last["fail_reason"] = extract_message(payload)
            return last
        if time.time() >= deadline:
            last["final_state"] = state or "unknown"
            last["error"] = f"轮询 {timeout}s 未到终态，停在 {state}"
            return last
        time.sleep(interval)


def find_consume_log(client, model_name, since_ts, task_id, attempts=6, interval=5):
    """提交日志在扣费后立即写入，但日志走批量写盘，所以要给它几秒。
    任务提交日志的 other 里没有 task_id，只能按模型名 + 时间窗口取最新一条。"""
    for _ in range(attempts):
        page = client.admin_get("/api/log/", {
            "type": 2, "model_name": model_name, "start_timestamp": since_ts, "page_size": 50,
        }) or {}
        items = page.get("items") or []
        if items:
            newest = max(items, key=lambda item: item.get("created_at", 0))
            return newest
        time.sleep(interval)
    return None


def verify_case(result, case_name, group_ratio, model_price):
    case = CASES[case_name]
    checks = []

    def check(name, passed, detail):
        checks.append({"name": name, "passed": bool(passed), "detail": detail})

    check("submit_accepted", result.get("submit_status") == 202,
          f"HTTP {result.get('submit_status')}，task_info.status={result.get('submit_status_field')}")
    check("task_id_masked", str(result.get("task_id", "")).startswith(TASK_ID_PREFIX),
          f"task_info.id={result.get('task_id')}（必须是本站 ID，不能泄漏上游 UUID）")

    actual_ratios = result.get("actual_ratios") or {}
    expected_ratios = case["expect_ratios"]
    ratios_match = all(abs(float(actual_ratios.get(key, 0)) - value) < 1e-4
                       for key, value in expected_ratios.items())
    check("billing_ratios", ratios_match,
          f"期望 {expected_ratios}，实际 {actual_ratios}")

    state = result.get("final_state")
    check("task_completed", state == "completed",
          f"终态 {state}" + (f"：{result.get('fail_reason')}" if result.get("fail_reason") else ""))

    if state == "completed":
        check("artifact_key", result.get("artifact_key") == case["artifact_key"],
              f"期望产物字段 {case['artifact_key']}，实际 {result.get('artifact_key')}")
        artifacts = result.get("artifacts") or []
        check("artifact_url", bool(artifacts) and str(artifacts[0]).startswith("http"),
              f"产物 {artifacts[:1]}")

    log = result.get("consume_log")
    if log is not None:
        want = expected_quota(model_price, expected_ratios, group_ratio)
        got = int(log.get("quota") or 0)
        # ±1 容差吸收两步截断与浮点表示的差异，不是为了掩盖数量级错误。
        check("quota_charged", abs(got - want) <= 1,
              f"期望扣费 {want}，实际 {got}（model_price={model_price} × 倍率 × 分组倍率 {group_ratio}）")
    else:
        check("quota_charged", False, "没找到该模型的消费日志")

    result["checks"] = checks
    result["passed"] = all(item["passed"] for item in checks)
    return result


# ---------------------------------------------------------------------------
# 输出
# ---------------------------------------------------------------------------


def print_preflight(ctx, case_names, base_url):
    print("\n=== 预检 ===")
    # 打出实例地址：NEW_API_BASE_URL 常常从别的 skill 继承下来指向另一个环境，
    # 而"令牌无效"的报错不会告诉你其实是打错了实例。
    print(f"实例 {base_url}")
    print(f"用户分组 {ctx['group']}，分组倍率 {ctx['group_ratio']}，当前额度 {ctx['quota_before']}")
    for model_name, price in ctx["models"].items():
        cost = UPSTREAM_BASE_PRICE.get(model_name)
        margin = f"（上游成本 ${cost}）" if cost else ""
        print(f"  {model_name}: 固定价 ${price} {margin}")
    if ctx["problems"]:
        print("\n配置问题：")
        for problem in ctx["problems"]:
            print(f"  ! {problem}")
    print("\n本轮计划：")
    for item in ctx["plan"]:
        print(f"  {item['case']:<16} 倍率 {item['ratios'] or '无'} -> "
              f"预计扣费 {item['expected_quota']} 额度（约 ${item['expected_usd']:.4f}）")
    missing = [name for name in case_names if name not in {item["case"] for item in ctx["plan"]}]
    if missing:
        print(f"  跳过（模型未配置）：{', '.join(missing)}")
    print(f"\n合计约 ${ctx['total_usd']:.4f}")


def print_guards(guards):
    print("\n=== 护栏检查（不花钱）===")
    for guard in guards:
        mark = "PASS" if guard["passed"] else "FAIL"
        hit = f"命中「{guard['matched']}」" if guard.get("matched") else "理由不符"
        print(f"  [{mark}] {guard['name']:<26} HTTP {guard['status']} (期望 {guard['expect']})，{hit}")
        if not guard["passed"]:
            print(f"         {guard['why']}")
            print(f"         期望理由含：{guard['expect_message']}")
            print(f"         实际返回：{guard['message']}")


def print_results(results):
    print("\n=== 实跑结果 ===")
    for result in results:
        if result.get("error") and not result.get("checks"):
            print(f"  [ERROR] {result['case']}: {result['error']}")
            continue
        mark = "PASS" if result.get("passed") else "FAIL"
        print(f"  [{mark}] {result['case']} ({result.get('task_id', '-')})")
        for check in result.get("checks", []):
            if not check["passed"]:
                print(f"         x {check['name']}: {check['detail']}")


def main():
    parser = argparse.ArgumentParser(description="MuleRouter carrothub 渠道联调冒烟测试")
    parser.add_argument("--base-url", default=os.environ.get("NEW_API_BASE_URL", "http://localhost:3000"))
    parser.add_argument("--api-key", default=os.environ.get("NEW_API_KEY"))
    parser.add_argument("--admin-token", default=os.environ.get("NEW_API_ADMIN_TOKEN"))
    parser.add_argument("--user-id", default=os.environ.get("NEW_API_USER_ID"))
    parser.add_argument("--cases", default=",".join(DEFAULT_CASES),
                        help=f"逗号分隔，可选：{', '.join(CASES)}")
    parser.add_argument("--image-url", default=os.environ.get("MULEROUTER_TEST_IMAGE_URL"),
                        help="图生图/图生视频用例的输入图；不给则用 z-image 用例的产出")
    parser.add_argument("--dry-run", action="store_true", help="只跑预检和护栏检查，不花钱")
    parser.add_argument("--yes", action="store_true", help="确认花钱实跑")
    parser.add_argument("--skip-guards", action="store_true")
    parser.add_argument("--poll-timeout", type=int, default=600)
    parser.add_argument("--poll-interval", type=int, default=10)
    parser.add_argument("--out", default="/tmp/mulerouter-smoke-report.json")
    args = parser.parse_args()

    if not args.api_key or not args.admin_token:
        parser.error("需要 --api-key（中转令牌 sk-）和 --admin-token（管理员访问令牌）")

    case_names = [name.strip() for name in args.cases.split(",") if name.strip()]
    unknown = [name for name in case_names if name not in CASES]
    if unknown:
        parser.error(f"未知用例：{', '.join(unknown)}；可选 {', '.join(CASES)}")
    # 需要输入图的用例排到后面，好用前面产出的图当输入。
    case_names.sort(key=lambda name: CASES[name]["needs_image"])

    client = Client(args.base_url, args.api_key, args.admin_token, args.user_id)
    report = {"base_url": client.base, "cases": case_names}

    ctx = preflight(client, case_names)
    report["preflight"] = ctx
    print_preflight(ctx, case_names, client.base)

    if not args.skip_guards:
        guards = guard_checks(client)
        report["guards"] = guards
        print_guards(guards)
        after = (client.admin_get("/api/user/self") or {}).get("quota")
        report["guards_free"] = after == ctx["quota_before"]
        print(f"\n  护栏阶段额度变化：{ctx['quota_before']} -> {after} "
              f"({'未扣费，符合预期' if report['guards_free'] else '有扣费，护栏可能跑在预扣费之后'})")

    if args.dry_run or not args.yes:
        print("\n未加 --yes，止步于免费检查。确认预算后加 --yes 实跑。")
        write_report(report, args.out)
        return 0 if all(g["passed"] for g in report.get("guards", [])) else 1

    if ctx["problems"]:
        print("\n配置有问题，先修再跑。")
        write_report(report, args.out)
        return 1

    results = []
    image_url = args.image_url
    for name in case_names:
        if CASES[name]["model"] not in ctx["models"]:
            continue
        print(f"\n--- 跑 {name} ---")
        result = run_case(client, name, image_url, args.poll_timeout, args.poll_interval)
        if result.get("task_id"):
            result["consume_log"] = find_consume_log(
                client, CASES[name]["model"], result["submitted_at"], result["task_id"])
            verify_case(result, name, ctx["group_ratio"], ctx["models"][CASES[name]["model"]])
        results.append(result)
        if CASES[name]["produces_image"] and not image_url and result.get("artifacts"):
            image_url = result["artifacts"][0]
            print(f"    产出图片将用作后续用例的输入：{image_url}")

    report["results"] = results
    quota_after = (client.admin_get("/api/user/self") or {}).get("quota")
    report["quota_after"] = quota_after
    spent = (ctx["quota_before"] or 0) - (quota_after or 0)
    report["quota_spent"] = spent
    charged = sum(int((r.get("consume_log") or {}).get("quota") or 0) for r in results)
    report["quota_reconciled"] = abs(spent - charged) <= len(results)

    print_results(results)
    print(f"\n额度：{ctx['quota_before']} -> {quota_after}，共扣 {spent}，日志合计 {charged}"
          f"（{'对得上' if report['quota_reconciled'] else '对不上，可能有额度泄漏或重复扣费'}）")

    write_report(report, args.out)
    ok = (all(r.get("passed") for r in results)
          and all(g["passed"] for g in report.get("guards", []))
          and report["quota_reconciled"])
    return 0 if ok else 1


def write_report(report, path):
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(report, handle, ensure_ascii=False, indent=2)
    print(f"\n完整报告：{path}")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ApiError as exc:
        print(f"错误：{exc}", file=sys.stderr)
        sys.exit(2)
    except KeyboardInterrupt:
        print("\n已中断", file=sys.stderr)
        sys.exit(130)
