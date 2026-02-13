
import requests
import zipfile
import os
import io

API_URL = "http://127.0.0.1:18080/api/v1"

def login():
    # Register first just in case
    requests.post(f"{API_URL}/auth/register", json={
        "username": "tester",
        "password": "password",
        "email": "tester@example.com"
    })
    resp = requests.post(f"{API_URL}/auth/login", json={
        "username": "tester",
        "password": "password"
    })
    if resp.status_code == 200:
        return resp.json().get("token")
    print(f"Login failed: {resp.text}")
    return None

def create_zip(files):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, 'w') as z:
        for name, content in files.items():
            z.writestr(name, content)
    buffer.seek(0)
    return buffer

def test_upload(token, name, files, expected_status):
    print(f"Testing {name}...", end=" ", flush=True)
    zip_buffer = create_zip(files)
    
    # Multipart form data
    files_payload = {'file': ('test.zip', zip_buffer, 'application/zip')}
    data_payload = {
        'title': f'{name} Problem',
        'time_limit': '1000',
        'memory_limit': '128',
        'difficulty': 'Easy'
    }
    headers = {"Authorization": f"Bearer {token}"}
    
    try:
        resp = requests.post(f"{API_URL}/problems", files=files_payload, data=data_payload, headers=headers)
        if resp.status_code == expected_status:
            print(f"PASS (Status: {resp.status_code})")
            if resp.status_code == 200:
                # Cleanup if successful
                prob_id = resp.json().get('id')
                # requests.delete(f"{API_URL}/problems/{prob_id}", headers=headers) # Optional cleanup
        else:
            print(f"FAIL (Expected {expected_status}, Got {resp.status_code})")
            print(resp.text)
    except Exception as e:
        print(f"ERROR: {e}")

def main():
    token = login()
    if not token:
        return

    # 1. Valid Case
    test_upload(token, "Valid", {
        "1.in": "input data",
        "1.out": "output data"
    }, 200)

    # 2. Invalid Case: Missing .in
    test_upload(token, "Missing Input", {
        "1.txt": "text",
        "1.out": "output"
    }, 400)

    # 3. Invalid Case: Missing .out
    test_upload(token, "Missing Output", {
        "1.in": "input",
        "1.txt": "text"
    }, 400)
    
    # 4. Empty Zip
    test_upload(token, "Empty Zip", {}, 400)

    # 5. Mismatched (Logic currently allows this, but let's see)
    test_upload(token, "Mismatched Names", {
        "1.in": "input",
        "2.out": "output"
    }, 400) # Strict logic should reject this

if __name__ == "__main__":
    main()
