cmake_minimum_required(VERSION 3.20)
project(Deep-OJ VERSION 0.1 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# ========================================================
# 1. 寻找依赖库
# ========================================================

# [新增] 寻找 yaml-cpp 用于读取配置文件
find_package(yaml-cpp REQUIRED)

find_package(Protobuf REQUIRED)
find_package(absl REQUIRED)
find_package(Threads REQUIRED)

# 手动寻找 gRPC (保持你原有的逻辑)
find_program(GRPC_CPP_PLUGIN NAMES grpc_cpp_plugin REQUIRED)
find_library(LIB_GRPCXX NAMES grpc++ REQUIRED)
find_library(LIB_GRPC NAMES grpc REQUIRED)
find_library(LIB_GPR NAMES gpr REQUIRED)
find_library(LIB_REFLECTION NAMES grpc++_reflection REQUIRED)

set(GRPC_LIBRARIES ${LIB_GRPCXX} ${LIB_GRPC} ${LIB_GPR})

# ========================================================
# Helper Function for gRPC
# ========================================================
function(add_grpc_proto TARGET_NAME PROTO_FILE)
    if(NOT IS_ABSOLUTE "${PROTO_FILE}")
        set(PROTO_FILE "${CMAKE_CURRENT_SOURCE_DIR}/${PROTO_FILE}")
    endif()

    get_filename_component(PROTO_PATH "${PROTO_FILE}" DIRECTORY)
    get_filename_component(PROTO_NAME "${PROTO_FILE}" NAME_WE)

    set(GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated/${PROTO_NAME}")
    set(GENERATED_SRCS
        "${GENERATED_DIR}/${PROTO_NAME}.pb.cc"
        "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.cc")
    set(GENERATED_HDRS
        "${GENERATED_DIR}/${PROTO_NAME}.pb.h"
        "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.h")

    add_custom_command(
        OUTPUT ${GENERATED_SRCS} ${GENERATED_HDRS}
        COMMAND ${CMAKE_COMMAND} -E make_directory "${GENERATED_DIR}"
        COMMAND ${Protobuf_PROTOC_EXECUTABLE}
        ARGS --grpc_out "${GENERATED_DIR}"
             --cpp_out "${GENERATED_DIR}"
             -I "${PROTO_PATH}"
             --plugin=protoc-gen-grpc=${GRPC_CPP_PLUGIN}
             "${PROTO_FILE}"
        DEPENDS "${PROTO_FILE}"
        COMMENT "Generating gRPC sources from ${PROTO_FILE}"
        VERBATIM
    )

    add_library(${TARGET_NAME} STATIC ${GENERATED_SRCS} ${GENERATED_HDRS})
    target_include_directories(${TARGET_NAME} PUBLIC "${GENERATED_DIR}" "${PROTO_PATH}")
    target_link_libraries(${TARGET_NAME} PUBLIC ${GRPC_LIBRARIES} ${Protobuf_LIBRARIES})
    target_compile_features(${TARGET_NAME} PUBLIC cxx_std_20)
endfunction()

# 自动生成 gRPC 代码
add_grpc_proto(judge_proto "proto/judge.proto")

# ========================================================
# [Core Library] The Sandbox
# ========================================================
add_library(deep_oj_core STATIC
    src/worker/sandbox.cpp
    src/worker/sandbox_isolation.cpp
    src/worker/config.cpp   # <--- 注意这里必须是 src/worker/
)

target_include_directories(deep_oj_core PUBLIC src/worker)

# [新增] 链接 yaml-cpp
# 使用 PRIVATE 链接，因为 yaml-cpp 的头文件只在 config.cpp 内部使用，
# 没有暴露给 deep_oj_core 的头文件（sandbox_internal.h 是纯C结构体）。
target_link_libraries(deep_oj_core 
    PUBLIC Threads::Threads 
    PRIVATE yaml-cpp
)

# ========================================================
# [自动化] 配置文件同步
# 只要 src/config.yaml 有变动，CMake 就会自动把它复制到 build 目录
# ========================================================
configure_file(
    "${CMAKE_CURRENT_SOURCE_DIR}/config.yaml"  # 源文件位置
    "${CMAKE_CURRENT_BINARY_DIR}/config.yaml"      # 目标位置 (即 build 目录)
    COPYONLY                                        # 只复制，不进行变量替换
)

target_compile_features(deep_oj_core PUBLIC cxx_std_20)

# ========================================================
# Executables
# ========================================================

# ... 前面的代码 ...

# 1. Scheduler
add_executable(scheduler src/scheduler/main.cpp)
target_link_libraries(scheduler PRIVATE 
    judge_proto 
    ${GRPC_LIBRARIES} 
    ${LIB_REFLECTION} 
    ${Protobuf_LIBRARIES} 
    Threads::Threads
    # [新增] 加上 absl 依赖
    absl::synchronization 
    absl::strings 
    absl::flat_hash_map
)

# 2. Worker
add_executable(worker src/worker/main.cpp)
target_link_libraries(worker PRIVATE 
    deep_oj_core 
    judge_proto 
    ${GRPC_LIBRARIES} 
    ${LIB_REFLECTION} 
    ${Protobuf_LIBRARIES} 
    Threads::Threads
    # [新增] 加上 absl 依赖
    absl::synchronization 
    absl::strings 
    absl::flat_hash_map
)

# 3. Security Test Suite
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/tests/security_test.cpp")
    add_executable(security_test tests/security_test.cpp)
    target_link_libraries(security_test PRIVATE deep_oj_core Threads::Threads)
endif()