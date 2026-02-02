import requests
import time
import random
from concurrent.futures import ThreadPoolExecutor

# 配置
API_URL = "http://127.0.0.1:18080/submit"
TOTAL_REQUESTS = 100
CONCURRENCY = 20 # 对应你 API Server 的 20 线程

def generate_code_with_random_comment():
    """生成带随机注释的代码，保证不命中缓存"""
    rand_num = random.randint(100000, 999999)
    return {
        "source_code": f"// Random ID: {rand_num}\n#include <iostream>\nint main() {{ return 0; }}",
        "language": "cpp",
        "time_limit": 1000,
        "memory_limit": 128
    }

def submit_task(_):
    payload = generate_code_with_random_comment()
    try:
        # 这里的请求会经过：API -> Redis 队列 -> 返回 Response
        resp = requests.post(API_URL, json=payload, timeout=5)
        return resp.status_code == 200
    except:
        return False

if __name__ == "__main__":
    print(f"🚀 开始测试 API -> Redis 管道吞吐量...")
    start_time = time.perf_counter()

    with ThreadPoolExecutor(max_workers=CONCURRENCY) as executor:
        results = list(executor.map(submit_task, range(TOTAL_REQUESTS)))

    duration = time.perf_counter() - start_time
    success = sum(results)
    print(f"\n📊 测试报告")
    print(f"⏱️ 总耗时: {duration:.2f} s")
    print(f"📊 实际 QPS (带 Redis 写入): {success/duration:.0f} req/s")