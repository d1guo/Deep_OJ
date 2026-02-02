#include <iostream>
#include <cassert>
#include <nlohmann/json.hpp>

#include "../src/common/submit_handler.h"

using json = nlohmann::json;

// Simple mock runner: returns Accepted if code contains "return 0;", else TLE
json MockRunner(const std::string& code, const std::string& problem_id) {
    json j;
    if (code.find("return 0;") != std::string::npos) {
        j["status"] = "Accepted";
        j["time"] = 10;
    } else {
        j["status"] = "TLE";
        j["time"] = 5000;
    }
    j["problem_id"] = problem_id;
    return j;
}

int main() {
    // 1. Test Accepted path
    std::string code_ok = "#include <bits/stdc++.h>\nint main(){ return 0; }";
    json res1 = SubmitHandler(code_ok, "prob1", nullptr, MockRunner);
    assert(res1["status"] == "Accepted");

    // 2. Test TLE path
    std::string code_bad = "while(1){}";
    json res2 = SubmitHandler(code_bad, "prob1", nullptr, MockRunner);
    assert(res2["status"] == "TLE");

    std::cout << "submit_test passed" << std::endl;
    return 0;
}
