import requests
import time
import os
import zipfile
import json

# Configuration
API_URL = "http://127.0.0.1:18080"
USERNAME = "admin"
PASSWORD = "password"

# Colors
GREEN = "\033[92m"
RED = "\033[91m"
RESET = "\033[0m"

def register():
    print(f"Registering user {USERNAME}...")
    resp = requests.post(f"{API_URL}/api/v1/auth/register", json={
        "username": USERNAME, 
        "password": PASSWORD,
        "email": "admin@example.com"
    })
    # 201 Created or 409 Conflict (if already exists) are both fine
    if resp.status_code == 201:
        print(f"{GREEN}User registered.{RESET}")
    elif resp.status_code == 409:
        print(f"User already exists, proceeding to login.")
    else:
        print(f"{RED}Registration failed: {resp.text}{RESET}")
        exit(1)

def login():
    register()
    print(f"Logging in as {USERNAME}...")
    resp = requests.post(f"{API_URL}/api/v1/auth/login", json={"username": USERNAME, "password": PASSWORD})
    if resp.status_code != 200:
        print(f"{RED}Login failed: {resp.text}{RESET}")
        exit(1)
    return resp.json()["token"]

def create_problem_zip():
    print("Creating sample problem zip...")
    os.makedirs("temp_problem", exist_ok=True)
    with open("temp_problem/1.in", "w") as f:
        f.write("1 2")
    with open("temp_problem/1.out", "w") as f:
        f.write("3")
    
    with zipfile.ZipFile("problem.zip", "w") as zf:
        zf.write("temp_problem/1.in", "1.in")
        zf.write("temp_problem/1.out", "1.out")
    
    # Cleanup
    os.remove("temp_problem/1.in")
    os.remove("temp_problem/1.out")
    os.rmdir("temp_problem")

def upload_problem(token):
    print("Uploading problem...")
    create_problem_zip()
    
    headers = {"Authorization": f"Bearer {token}"}
    files = {"file": open("problem.zip", "rb")}
    data = {"title": "A+B Problem", "time_limit": 1000, "memory_limit": 128}
    
    resp = requests.post(f"{API_URL}/api/v1/problems", headers=headers, files=files, data=data)
    if resp.status_code != 200:
        print(f"{RED}Upload failed: {resp.text}{RESET}")
        exit(1)
    
    problem_id = resp.json().get("id") or resp.json().get("problem_id")
    # Actually checking the response format of HandleCreateProblem...
    # It might return {"message": "ok", "id": 1} or simliar.
    # Let's assume standard response or check it if fails.
    print(f"{GREEN}Problem uploaded, ID: {problem_id}{RESET}")
    return problem_id

def submit_solution(token, problem_id):
    print("Submitting solution...")
    code = """
#include <iostream>
int main() {
    int a, b;
    std::cin >> a >> b;
    std::cout << (a + b);
    return 0;
}
"""
    headers = {"Authorization": f"Bearer {token}"}
    data = {
        "problem_id": int(problem_id),
        "language": 1, # CPP
        "code": code,
        "time_limit": 1000,
        "memory_limit": 128
    }
    
    resp = requests.post(f"{API_URL}/api/v1/submit", headers=headers, json=data)
    if resp.status_code != 200:
        print(f"{RED}Submit failed: {resp.text}{RESET}")
        exit(1)
        
    job_id = resp.json()["job_id"]
    print(f"{GREEN}Submitted, Job ID: {job_id}{RESET}")
    return job_id

def poll_status(token, job_id):
    print(f"Polling status for {job_id}...")
    headers = {"Authorization": f"Bearer {token}"}
    
    for _ in range(20): # 20 seconds timeout
        resp = requests.get(f"{API_URL}/api/v1/status/{job_id}", headers=headers)
        if resp.status_code != 200:
            print(f"{RED}Status check failed: {resp.text}{RESET}")
            exit(1)
            
        data = resp.json()
        status = data["status"]
        print(f"Current status: {status}")
        
        if status == "Finished" or status == "Accepted" or status == "Wrong Answer":
            # Check detailed result
            res_data = data.get("data", {})
            final_status = res_data.get("status") if isinstance(res_data, dict) else status
            if final_status == "Accepted":
                 print(f"{GREEN}Test Passed! Result: Accepted{RESET}")
            else:
                 print(f"{RED}Test Failed! Result: {final_status}{RESET}")
                 print(f"Details: {json.dumps(res_data, indent=2)}")
            return
            
        time.sleep(1)
        
    print(f"{RED}Timeout waiting for result{RESET}")

def main():
    try:
        token = login()
        pid = upload_problem(token)
        job_id = submit_solution(token, pid)
        poll_status(token, job_id)
    except Exception as e:
        print(f"{RED}Error: {e}{RESET}")
    finally:
        if os.path.exists("problem.zip"):
            os.remove("problem.zip")

if __name__ == "__main__":
    main()
