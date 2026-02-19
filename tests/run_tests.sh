#!/bin/bash
#
# Deep-OJ 综合测试脚本
# 
# 功能:
# 1. 编译并运行所有测试用例
# 2. 验证安全机制 (Cgroups, Seccomp, Chroot)
# 3. 验证判题结果 (AC, TLE, MLE, RE, CE, WA)
#
# 使用方法:
#   ./run_tests.sh [--security-only | --functional-only]
#
# 注意:
# - 安全测试在沙箱外运行，部分测试（如文件系统访问）预期行为不同
# - 完整安全验证需要在 oj_worker 沙箱内运行
#
# 作者: Deep-OJ Team
# 日期: 2026-02-10

# 不使用 set -e: 安全测试会产生非零退出码，由 check_result 处理

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # 无颜色

# 目录配置
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
SECURITY_TESTS="$SCRIPT_DIR/security/test_codes"
FUNCTIONAL_TESTS="$SCRIPT_DIR/functional/test_codes"
BUILD_DIR="$PROJECT_ROOT/build"
WORKSPACE="/tmp/oj_test_$$"

# 测试计数
TOTAL=0
PASSED=0
FAILED=0
WARNED=0

# 初始化
init() {
    echo -e "${BLUE}=====================================${NC}"
    echo -e "${BLUE}   Deep-OJ 综合测试套件 v2.0${NC}"
    echo -e "${BLUE}=====================================${NC}"
    echo ""
    
    mkdir -p "$WORKSPACE"
    
    # 检查编译器
    if command -v g++ &> /dev/null; then
        CXX="g++"
    elif command -v clang++ &> /dev/null; then
        CXX="clang++"
    else
        echo -e "${RED} C++ 编译器未找到${NC}"
        exit 1
    fi
    
    echo -e "${GREEN} 测试环境初始化完成${NC}"
    echo -e "   编译器: $CXX"
    echo -e "   工作目录: $WORKSPACE"
    echo ""
}

# 清理
cleanup() {
    rm -rf "$WORKSPACE"
}

trap cleanup EXIT

# 编译测试用例
compile_test() {
    local src=$1
    local out=$2
    
    $CXX -std=c++17 -O2 -o "$out" "$src" -lpthread 2>&1
    return $?
}

# 运行测试 (带超时)
run_with_timeout() {
    local exe=$1
    local timeout=$2
    local input=$3
    local output=$4
    
    if [ -n "$input" ]; then
        timeout --signal=KILL "$timeout" "$exe" < "$input" > "$output" 2>&1
    else
        timeout --signal=KILL "$timeout" "$exe" > "$output" 2>&1
    fi
    return $?
}

# 检查测试结果
check_result() {
    local name=$1
    local expected=$2
    local actual=$3
    local details=$4
    
    TOTAL=$((TOTAL + 1))
    
    if [ "$expected" = "$actual" ]; then
        PASSED=$((PASSED + 1))
        echo -e "${GREEN} 通过${NC}: $name"
        echo -e "   预期: $expected, 实际: $actual"
    else
        FAILED=$((FAILED + 1))
        echo -e "${RED} 失败${NC}: $name"
        echo -e "   预期: $expected, 实际: $actual"
        if [ -n "$details" ]; then
            echo -e "   详情: $details"
        fi
    fi
}

# 标记为警告 (非严格失败)
check_warn() {
    local name=$1
    local msg=$2
    local details=$3
    
    TOTAL=$((TOTAL + 1))
    WARNED=$((WARNED + 1))
    echo -e "${YELLOW}警告${NC}: $name"
    echo -e "   $msg"
    if [ -n "$details" ]; then
        echo -e "   详情: $details"
    fi
}

# 测试: 编译错误 (CE)
test_compile_error() {
    echo -e "\n${YELLOW}=== 编译错误测试 (CE) ===${NC}"
    
    for src in "$FUNCTIONAL_TESTS"/ce_*.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "CE" "编译成功"
        else
            check_result "$name" "CE" "CE"
        fi
    done
}

# 测试: 正确答案 (AC)
test_accepted() {
    echo -e "\n${YELLOW}=== 正确答案测试 (AC) ===${NC}"
    
    for src in "$FUNCTIONAL_TESTS"/ac_*.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "AC" "CE"
            continue
        fi
        
        # 准备输入
        if [ "$name" = "ac_aplusb" ]; then
            echo "1 2" > "$WORKSPACE/input.txt"
            expected_output="3"
        else
            touch "$WORKSPACE/input.txt"
            expected_output="Hello, World!"
        fi
        
        if run_with_timeout "$WORKSPACE/$name" 2 "$WORKSPACE/input.txt" "$WORKSPACE/output.txt"; then
            actual_output=$(cat "$WORKSPACE/output.txt" | tr -d '\n' | head -c 100)
            if [[ "$actual_output" == *"$expected_output"* ]]; then
                check_result "$name" "AC" "AC"
            else
                check_result "$name" "AC" "WA" "输出: $actual_output"
            fi
        else
            check_result "$name" "AC" "RE/TLE"
        fi
    done
}

# 测试: 超时 (TLE)
test_time_limit() {
    echo -e "\n${YELLOW}=== 超时测试 (TLE) ===${NC}"
    
    for src in "$FUNCTIONAL_TESTS"/tle_*.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "TLE" "CE"
            continue
        fi
        
        # 设置 1 秒超时
        if run_with_timeout "$WORKSPACE/$name" 1 "" "$WORKSPACE/output.txt" 2>/dev/null; then
            check_result "$name" "TLE" "AC (未超时)"
        else
            exit_code=$?
            if [ $exit_code -eq 137 ] || [ $exit_code -eq 124 ]; then
                check_result "$name" "TLE" "TLE"
            else
                check_result "$name" "TLE" "RE (exit $exit_code)"
            fi
        fi
    done
}

# 测试: 内存超限 (MLE)
test_memory_limit() {
    echo -e "\n${YELLOW}=== 内存超限测试 (MLE) ===${NC}"
    echo -e "${CYAN}   注: 内存超限会导致进程被 OOM Killer 终止 (exit ≠ 0)，这是预期行为${NC}"
    
    for src in "$FUNCTIONAL_TESTS"/mle_*.cpp "$SECURITY_TESTS"/memory_bomb.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "MLE" "CE"
            continue
        fi
        
        # 运行程序 (无 ulimit，让 OS 的 OOM killer 处理)
        if run_with_timeout "$WORKSPACE/$name" 3 "" "$WORKSPACE/output.txt" 2>/dev/null; then
            check_result "$name" "MLE" "AC (未超限)"
        else
            exit_code=$?
            # 任何非零退出都算 MLE 通过 (被 OOM 杀死/崩溃/超时)
            check_result "$name" "MLE" "MLE"
        fi
    done
}

# 测试: 运行时错误 (RE)
test_runtime_error() {
    echo -e "\n${YELLOW}=== 运行时错误测试 (RE) ===${NC}"
    
    for src in "$FUNCTIONAL_TESTS"/re_*.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "RE" "CE"
            continue
        fi
        
        if run_with_timeout "$WORKSPACE/$name" 3 "" "$WORKSPACE/output.txt" 2>/dev/null; then
            check_result "$name" "RE" "AC (未崩溃)"
        else
            exit_code=$?
            # 任何非零退出都算 RE 通过 (SIGSEGV, SIGFPE, SIGKILL 等)
            check_result "$name" "RE" "RE"
        fi
    done
}

# 测试: 安全测试
test_security() {
    echo -e "\n${YELLOW}=== 安全测试 ===${NC}"
    echo -e "${CYAN}   注: 安全测试在沙箱外运行，部分攻击会'成功'，这是预期行为${NC}"
    echo -e "${CYAN}   完整安全验证需在 oj_worker 沙箱内运行${NC}"
    echo ""
    
    # --------------------------------------------------
    # Fork 炸弹测试 (仅编译验证)
    # 注: 实际运行 fork 炸弹需要 Cgroups pids.max 限制
    #     在沙箱外运行会导致系统资源耗尽
    # --------------------------------------------------
    echo -e "${CYAN}--- Fork 炸弹测试 (编译验证) ---${NC}"
    for src in "$SECURITY_TESTS"/fork_bomb.cpp "$SECURITY_TESTS"/async_fork_bomb.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_warn "$name" "编译成功 (实际防御需在沙箱内通过 Cgroups pids.max 验证)"
        else
            check_result "$name" "编译成功" "CE"
        fi
    done
    
    # --------------------------------------------------
    # 网络测试
    # --------------------------------------------------
    echo -e "\n${CYAN}--- 网络访问测试 ---${NC}"
    for src in "$SECURITY_TESTS"/network_test.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "被拦截" "CE"
            continue
        fi
        
        run_with_timeout "$WORKSPACE/$name" 3 "" "$WORKSPACE/output.txt" 2>/dev/null
        exit_code=$?
        output=$(cat "$WORKSPACE/output.txt" 2>/dev/null | head -5)
        if [ $exit_code -ne 0 ]; then
            check_result "$name" "被拦截" "被拦截"
        elif [[ "$output" == *"成功"* ]] || [[ "$output" == *"Connected"* ]]; then
            check_warn "$name" "沙箱外网络可访问 (预期行为，沙箱内会被 Seccomp 拦截)"
        else
            check_result "$name" "被拦截" "被拦截"
        fi
    done
    
    # --------------------------------------------------
    # 文件系统安全测试
    # --------------------------------------------------
    echo -e "\n${CYAN}--- 文件系统安全测试 ---${NC}"
    for src in "$SECURITY_TESTS"/filesystem_escape.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "沙箱内拦截" "CE"
            continue
        fi
        
        run_with_timeout "$WORKSPACE/$name" 2 "" "$WORKSPACE/output.txt" 2>/dev/null
        output=$(cat "$WORKSPACE/output.txt" 2>/dev/null | head -10)
        if [[ "$output" == *"LEAK"* ]]; then
            check_warn "$name" "沙箱外文件可访问 (预期行为，沙箱内 chroot 会阻止)" "$(echo "$output" | grep LEAK | head -3)"
        else
            check_result "$name" "沙箱内拦截" "沙箱内拦截"
        fi
    done

    # --------------------------------------------------
    # 系统调用攻击测试
    # --------------------------------------------------
    echo -e "\n${CYAN}--- 系统调用攻击测试 ---${NC}"
    for src in "$SECURITY_TESTS"/syscall_attack.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "沙箱内拦截" "CE"
            continue
        fi
        
        run_with_timeout "$WORKSPACE/$name" 2 "" "$WORKSPACE/output.txt" 2>/dev/null
        exit_code=$?
        output=$(cat "$WORKSPACE/output.txt" 2>/dev/null | head -5)
        # 沙箱外 system()/execve() 可能成功，这不算 fail
        check_warn "$name" "沙箱外系统调用可执行 (预期行为，沙箱内 Seccomp 会拦截)" "exit=$exit_code"
    done

    # --------------------------------------------------
    # 其他安全测试 (逃逸/ptrace/多线程/信号)
    # --------------------------------------------------
    echo -e "\n${CYAN}--- 沙箱逃逸 & 权限攻击测试 ---${NC}"
    for src in "$SECURITY_TESTS"/escape_test.cpp "$SECURITY_TESTS"/ptrace_test.cpp \
               "$SECURITY_TESTS"/multithread_bypass.cpp "$SECURITY_TESTS"/signal_attack.cpp; do
        [ -f "$src" ] || continue
        name=$(basename "$src" .cpp)
        
        if ! compile_test "$src" "$WORKSPACE/$name" > /dev/null 2>&1; then
            check_result "$name" "被终止" "CE"
            continue
        fi
        
        run_with_timeout "$WORKSPACE/$name" 2 "" "$WORKSPACE/output.txt" 2>/dev/null
        exit_code=$?
        if [ $exit_code -ne 0 ]; then
            # 非零退出 = 进程被终止/崩溃/超时 = 安全
            check_result "$name" "被终止" "被终止"
        else
            output=$(cat "$WORKSPACE/output.txt" 2>/dev/null | head -5)
            if [[ "$output" == *"LEAK"* ]] || [[ "$output" == *"success"* ]]; then
                check_result "$name" "被终止" "可能泄露!" "$output"
            else
                check_result "$name" "被终止" "被终止"
            fi
        fi
    done
}

# 测试: Cgroups v2 验证
test_cgroups() {
    echo -e "\n${YELLOW}=== Cgroups v2 验证 ===${NC}"
    
    # 检查 cgroups v2 是否挂载
    if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
        echo -e "${GREEN} Cgroups v2 已挂载${NC}"
        echo -e "   可用控制器: $(cat /sys/fs/cgroup/cgroup.controllers)"
        
        TOTAL=$((TOTAL + 1))
        PASSED=$((PASSED + 1))
    else
        echo -e "${YELLOW}Cgroups v2 未挂载或使用 v1${NC}"
        TOTAL=$((TOTAL + 1))
        WARNED=$((WARNED + 1))
    fi
    
    # 检查关键控制器
    for controller in pids memory; do
        TOTAL=$((TOTAL + 1))
        if [ -f /sys/fs/cgroup/cgroup.controllers ] && grep -q "$controller" /sys/fs/cgroup/cgroup.controllers; then
            PASSED=$((PASSED + 1))
            echo -e "${GREEN} $controller 控制器可用${NC}"
        else
            WARNED=$((WARNED + 1))
            echo -e "${YELLOW}$controller 控制器未找到 (可能需要 Docker 容器内运行)${NC}"
        fi
    done
}

# 打印总结
print_summary() {
    echo -e "\n${BLUE}=====================================${NC}"
    echo -e "${BLUE}   测试结果总结${NC}"
    echo -e "${BLUE}=====================================${NC}"
    echo ""
    echo -e "总计: $TOTAL"
    echo -e "${GREEN}通过: $PASSED${NC}"
    if [ $WARNED -gt 0 ]; then
        echo -e "${YELLOW}警告: $WARNED${NC} (沙箱外预期行为)"
    fi
    echo -e "${RED}失败: $FAILED${NC}"
    echo ""
    
    if [ $FAILED -eq 0 ]; then
        echo -e "${GREEN} 所有测试通过!${NC}"
        if [ $WARNED -gt 0 ]; then
            echo -e "${YELLOW}   (部分安全测试需在沙箱内验证完整效果)${NC}"
        fi
    else
        echo -e "${RED}有 $FAILED 个测试失败，请检查日志${NC}"
    fi
}

# 主函数
main() {
    init
    
    case "$1" in
        --security-only)
            test_security
            test_cgroups
            ;;
        --functional-only)
            test_compile_error
            test_accepted
            test_time_limit
            test_memory_limit
            test_runtime_error
            ;;
        *)
            test_compile_error
            test_accepted
            test_time_limit
            test_memory_limit
            test_runtime_error
            test_security
            test_cgroups
            ;;
    esac
    
    print_summary
    
    exit $FAILED
}

main "$@"
