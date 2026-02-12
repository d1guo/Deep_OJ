cmake_minimum_required(VERSION 3.20)
project(Deep_OJ VERSION 1.0 LANGUAGES CXX)

# ==========================================
# 1. 全局配置
# ==========================================
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# [关键修复 1] 全局包含根目录下的 include 文件夹
# 这样 #include "picosha2.h" 和 "crow_all.h" 才能找到文件
include_directories(${CMAKE_SOURCE_DIR}/include)

find_package(Threads REQUIRED)
add_compile_options(-pthread)

# ==========================================
# 2. 依赖查找
# ==========================================
find_package(PkgConfig REQUIRED)

pkg_check_modules(GRPC REQUIRED grpc++ grpc)
pkg_check_modules(PROTO REQUIRED protobuf)
pkg_check_modules(SSL REQUIRED openssl)
pkg_check_modules(YAML REQUIRED yaml-cpp)
pkg_check_modules(SECCOMP REQUIRED libseccomp)

# CURL (用于 Etcd HTTP API，可选)
pkg_check_modules(CURL libcurl)
if(NOT CURL_FOUND)
    find_package(CURL)
endif()

# Redis 依赖 (增强查找)
pkg_check_modules(REDISPP redis++)
find_library(HIREDIS_LIB hiredis)

if(REDISPP_FOUND AND HIREDIS_LIB)
    set(REDIS_LIBS ${REDISPP_LIBRARIES} ${HIREDIS_LIB})
else()
    # Fallback: 手动指定库名
    set(REDIS_LIBS redis++ hiredis)
endif()

# gRPC 插件查找
find_program(PROTOC_EXEC NAMES protoc REQUIRED)
find_program(GRPC_CPP_PLUGIN NAMES grpc_cpp_plugin protoc-gen-grpc-cpp PATHS /usr/bin /usr/local/bin)
if(NOT GRPC_CPP_PLUGIN OR NOT EXISTS "${GRPC_CPP_PLUGIN}")
    set(GRPC_CPP_PLUGIN "/usr/bin/grpc_cpp_plugin")
endif()

# ==========================================
# 3. gRPC 代码生成
# ==========================================
function(add_grpc_proto TARGET_NAME PROTO_FILE)
    get_filename_component(PROTO_ABS "${PROTO_FILE}" ABSOLUTE)
    get_filename_component(PROTO_PATH "${PROTO_ABS}" DIRECTORY)
    get_filename_component(PROTO_NAME "${PROTO_ABS}" NAME_WE)
    
    set(GENERATED_DIR "${CMAKE_BINARY_DIR}/generated/${PROTO_NAME}")
    set(GEN_SRCS "${GENERATED_DIR}/${PROTO_NAME}.pb.cc" "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.cc")
    set(GEN_HDRS "${GENERATED_DIR}/${PROTO_NAME}.pb.h" "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.h")

    add_custom_command(
        OUTPUT ${GEN_SRCS} ${GEN_HDRS}
        COMMAND ${CMAKE_COMMAND} -E make_directory "${GENERATED_DIR}"
        COMMAND ${PROTOC_EXEC}
                -I "${PROTO_PATH}"
                --cpp_out=${GENERATED_DIR}
                --grpc_out=${GENERATED_DIR}
                --plugin=protoc-gen-grpc=${GRPC_CPP_PLUGIN}
                "${PROTO_ABS}"
        DEPENDS "${PROTO_ABS}"
        VERBATIM
    )
    
    add_library(${TARGET_NAME} STATIC ${GEN_SRCS} ${GEN_HDRS})
    target_include_directories(${TARGET_NAME} PUBLIC "${GENERATED_DIR}" "${PROTO_PATH}")
    target_link_libraries(${TARGET_NAME} PUBLIC ${GRPC_LIBRARIES} ${PROTO_LIBRARIES} Threads::Threads)
endfunction()

# 加载 Proto
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/proto/judge.proto")
    add_grpc_proto(judge_proto "proto/judge.proto")
endif()

# ==========================================
# 4. 生成目标
# ==========================================

# --- A. Sandbox Core ---
add_library(deep_oj_core STATIC
    src/worker/sandbox.cpp
    src/worker/sandbox_isolation.cpp
    src/worker/config.cpp
    src/worker/seccomp_rules.cpp
    src/worker/cgroup_manager.cpp
    src/worker/etcd_registry.cpp
)
target_include_directories(deep_oj_core PUBLIC src/worker ${SECCOMP_INCLUDE_DIRS})
target_link_libraries(deep_oj_core PUBLIC ${YAML_LIBRARIES} ${SSL_LIBRARIES} Threads::Threads ${SECCOMP_LIBRARIES})
if(CURL_FOUND)
    target_link_libraries(deep_oj_core PUBLIC ${CURL_LIBRARIES})
    target_include_directories(deep_oj_core PUBLIC ${CURL_INCLUDE_DIRS})
endif()

# --- B. Worker (评测机) ---
add_executable(oj_worker src/worker/main.cpp)
target_link_libraries(oj_worker PRIVATE 
    deep_oj_core 
    judge_proto 
    ${GRPC_LIBRARIES} 
    ${REDIS_LIBS}   # Worker 也需要 Redis 来上报结果
    Threads::Threads
)

# oj_scheduler 已移除 (被 Go Scheduler 替代)

# oj_server 已移除 (被 Go API 替代)

# --- E. Integration Tests ---
add_executable(security_test_runner tests/security_integration_test.cpp)
target_link_libraries(security_test_runner PRIVATE 
    deep_oj_core 
    ${YAML_LIBRARIES}
    Threads::Threads
)

# 复制配置文件
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/config.yaml")
    configure_file("config.yaml" "config.yaml" COPYONLY)
endif()