import urllib.request
import urllib.parse
import json
import time
import sys

API_URL = "http://localhost:8080/api/v1"

def register_user(username, password):
    url = f"{API_URL}/auth/register"
    data = {"username": username, "password": password}
    req = urllib.request.Request(url, data=json.dumps(data).encode('utf-8'), headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req) as response:
            return True
    except urllib.error.URLError as e:
        print(f"注册失败：{e}")
        return False

def login_user(username, password):
    url = f"{API_URL}/auth/login"
    data = {"username": username, "password": password}
    req = urllib.request.Request(url, data=json.dumps(data).encode('utf-8'), headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req) as response:
            result = json.loads(response.read().decode('utf-8'))
            return result.get('token')
    except urllib.error.URLError as e:
        print(f"登录失败：{e}")
        return None

def submit_task(token, code, language=1):
    url = f"{API_URL}/submit"
    data = {
        "code": code,
        "language": language,
        "time_limit": 1000,
        "memory_limit": 65536,
        "problem_id": 1001
    }
    
    headers = {
        'Content-Type': 'application/json',
        'Authorization': f'Bearer {token}'
    }
    
    req = urllib.request.Request(url, 
        data=json.dumps(data).encode('utf-8'),
        headers=headers
    )
    
    try:
        with urllib.request.urlopen(req) as response:
            result = json.loads(response.read().decode('utf-8'))
            print(f"提交成功，Job ID：{result.get('job_id')}")
            return result.get('job_id')
    except urllib.error.URLError as e:
        print(f"提交失败：{e}")
        return None

def poll_status(job_id):
    url = f"{API_URL}/status/{job_id}"
    
    for _ in range(20): # 轮询 10 秒（间隔 0.5 秒）
        try:
            with urllib.request.urlopen(url) as response:
                result = json.loads(response.read().decode('utf-8'))
                status = result.get('status')
                print(f"任务 {job_id} 状态：{status}")
                
                if status == "Finished":
                    return result.get('data') # 返回详细结果
                
        except urllib.error.URLError as e:
            print(f"轮询异常：{e}")
            
        time.sleep(0.5)
    
    print(f"等待任务 {job_id} 超时")
    return None

def test_aplusb(token):
    print("\n=== 测试 A+B 题目（期望 Accepted）===")
    timestamp = time.time()
    code = f"""
    #include <iostream>
    using namespace std;
    int main() {{
        // Timestamp: {timestamp}
        int a, b;
        cin >> a >> b;
        cout << a + b << endl;
        return 0;
    }}
    """
    job_id = submit_task(token, code)
    if not job_id: return False
    
    result = poll_status(job_id)
    if result and result.get('status') == "Accepted":
        print("A+B 测试通过！")
        return True
    else:
        print(f"A+B 测试失败：{result}")
        return False

def test_tle(token):
    print("\n=== 测试死循环（期望 Time Limit Exceeded）===")
    code = """
    #include <iostream>
    int main() {
        while(true);
        return 0;
    }
    """
    job_id = submit_task(token, code)
    if not job_id: return False
    
    result = poll_status(job_id)
    if result and result.get('status') == "Time Limit Exceeded":
        print("TLE 测试通过！")
        return True
    else:
        print(f"TLE 测试失败：{result}")
        return False

def test_forkbomb(token):
    print("\n=== 测试 Fork Bomb（期望系统可存活）===")
    # Fork bomb 测试代码
    code = """
    #include <unistd.h>
    int main() {
        while(1) fork();
        return 0;
    }
    """
    job_id = submit_task(token, code)
    if not job_id: return False
    
    # 若沙箱生效，通常会得到 Runtime Error 或 Time Limit Exceeded。
    result = poll_status(job_id)
    
    # 再次提交 A+B，验证系统是否仍可用。
    print("正在验证 fork bomb 后的系统健康状态...")
    if test_aplusb(token):
        print("系统在 Fork Bomb 后仍然可用！")
        return True
    else:
        print("系统在 Fork Bomb 后不可用！")
        return False

def setup_test_cases():
    import os
    DATA_DIR = "data"
    os.makedirs(DATA_DIR, exist_ok=True)
    
    # 为 A+B（Problem 1001）准备 1.in / 1.out
    with open(f"{DATA_DIR}/1.in", "w") as f:
        f.write("1 2")
    with open(f"{DATA_DIR}/1.out", "w") as f:
        f.write("3")
        
    print(f"测试数据已生成：{DATA_DIR}/")

if __name__ == "__main__":
    print(f"开始集成测试，目标地址：{API_URL}")
    
    setup_test_cases()
    
    # 1. 注册
    print("\n正在注册用户...")
    if not register_user("testuser", "password123"):
        sys.exit(1)
        
    # 2. 登录
    print("正在登录...")
    token = login_user("testuser", "password123")
    if not token:
        sys.exit(1)
    print(f"获取到 Token：{token[:10]}...")

    passed = True
    passed &= test_aplusb(token)
    
    # 若前序通过，继续后续用例
    if passed:
        passed &= test_tle(token)
        passed &= test_forkbomb(token)
    
    if passed:
        print("\n所有集成测试通过！")
        sys.exit(0)
    else:
        print("\n存在测试失败。")
        sys.exit(1)
