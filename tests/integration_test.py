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
        print(f"❌ Register failed: {e}")
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
        print(f"❌ Login failed: {e}")
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
            print(f"✅ Submitted task, Job ID: {result.get('job_id')}")
            return result.get('job_id')
    except urllib.error.URLError as e:
        print(f"❌ Submit failed: {e}")
        return None

def poll_status(job_id):
    url = f"{API_URL}/status/{job_id}"
    
    for _ in range(20): # Poll for 10 seconds (0.5s interval)
        try:
            with urllib.request.urlopen(url) as response:
                result = json.loads(response.read().decode('utf-8'))
                status = result.get('status')
                print(f"   Status for {job_id}: {status}")
                
                if status == "Finished":
                    return result.get('data') # Return the detailed result
                
        except urllib.error.URLError as e:
            print(f"⚠️ Poll error: {e}")
            
        time.sleep(0.5)
    
    print(f"❌ Timed out waiting for job {job_id}")
    return None

def test_aplusb(token):
    print("\n=== Testing A+B Problem (Expect Accepted) ===")
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
        print("✅ A+B Test Passed!")
        return True
    else:
        print(f"❌ A+B Test Failed: {result}")
        return False

def test_tle(token):
    print("\n=== Testing Infinite Loop (Expect Time Limit Exceeded) ===")
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
        print("✅ TLE Test Passed!")
        return True
    else:
        print(f"❌ TLE Test Failed: {result}")
        return False

def test_forkbomb(token):
    print("\n=== Testing Fork Bomb (Expect System Survival) ===")
    # Fork bomb code
    code = """
    #include <unistd.h>
    int main() {
        while(1) fork();
        return 0;
    }
    """
    job_id = submit_task(token, code)
    if not job_id: return False
    
    # We expect either Runtime Error or Time Limit Exceeded (if sandbox works)
    # Or just system survival.
    result = poll_status(job_id)
    
    # Verify system is still responsive by submitting A+B again
    print("   Verifying system health after fork bomb...")
    if test_aplusb(token):
        print("✅ System Survived Fork Bomb!")
        return True
    else:
        print("❌ System Died after Fork Bomb!")
        return False

def setup_test_cases():
    import os
    DATA_DIR = "data"
    os.makedirs(DATA_DIR, exist_ok=True)
    
    # 1.in / 1.out for A+B (Problem 1001)
    with open(f"{DATA_DIR}/1.in", "w") as f:
        f.write("1 2")
    with open(f"{DATA_DIR}/1.out", "w") as f:
        f.write("3")
        
    print(f"✅ Test cases generated in {DATA_DIR}/")

if __name__ == "__main__":
    print(f"🚀 Starting Integration Test against {API_URL}")
    
    setup_test_cases()
    
    # 1. Register
    print("\nRegistering User...")
    if not register_user("testuser", "password123"):
        sys.exit(1)
        
    # 2. Login
    print("Logging in...")
    token = login_user("testuser", "password123")
    if not token:
        sys.exit(1)
    print(f"✅ Got Token: {token[:10]}...")

    passed = True
    passed &= test_aplusb(token)
    
    # Enable all
    if passed:
        passed &= test_tle(token)
        passed &= test_forkbomb(token)
    
    if passed:
        print("\n🎉 All Integration Tests Passed!")
        sys.exit(0)
    else:
        print("\n❌ Some tests failed.")
        sys.exit(1)
