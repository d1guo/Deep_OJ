cmake_minimum_required(VERSION 3.20)
project(Deep_OJ VERSION 1.0 LANGUAGES CXX)

# ==========================================
# 1. 编译标准与全局配置
# ==========================================
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# 强制开启线程支持（gRPC 和 Crow 必需）
find_package(Threads REQUIRED)
add_compile_options(-pthread)

# ==========================================
# 2. 依赖查找 (使用 PkgConfig 简化路径)
# ==========================================
find_package(PkgConfig REQUIRED)

pkg_check_modules(GRPC REQUIRED grpc++ grpc)
pkg_check_modules(PROTO REQUIRED protobuf)
pkg_check_modules(SSL REQUIRED openssl)
pkg_check_modules(YAML REQUIRED yaml-cpp)

# 查找开发工具路径
find_program(PROTOC_EXEC NAMES protoc REQUIRED)
find_program(GRPC_CPP_PLUGIN NAMES grpc_cpp_plugin protoc-gen-grpc-cpp PATHS /usr/bin /usr/local/bin)

# 如果环境里路径依然找不到（针对 Docker 环境补丁）
if(NOT GRPC_CPP_PLUGIN OR NOT EXISTS "${GRPC_CPP_PLUGIN}")
    set(GRPC_CPP_PLUGIN "/usr/bin/grpc_cpp_plugin")
endif()

message(STATUS "🚀 使用 gRPC 插件路径: ${GRPC_CPP_PLUGIN}")

# ==========================================
# 3. gRPC 代码生成逻辑 (核心修复点)
# ==========================================
function(add_grpc_proto TARGET_NAME PROTO_FILE)
    get_filename_component(PROTO_ABS "${PROTO_FILE}" ABSOLUTE)
    get_filename_component(PROTO_PATH "${PROTO_ABS}" DIRECTORY)
    get_filename_component(PROTO_NAME "${PROTO_ABS}" NAME_WE)
    
    # 使用绝对路径，确保生成的代码路径清晰
    set(GENERATED_DIR "${CMAKE_BINARY_DIR}/generated/${PROTO_NAME}")
    
    # 源代码与头文件列表
    set(GEN_SRCS "${GENERATED_DIR}/${PROTO_NAME}.pb.cc" "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.cc")
    set(GEN_HDRS "${GENERATED_DIR}/${PROTO_NAME}.pb.h" "${GENERATED_DIR}/${PROTO_NAME}.grpc.pb.h")

    add_custom_command(
        OUTPUT ${GEN_SRCS} ${GEN_HDRS}
        # 1. 强制先创建目录
        COMMAND ${CMAKE_COMMAND} -E make_directory "${GENERATED_DIR}"
        # 2. 调用 protoc 生成代码
        # 注意：这里不再在 --cpp_out= 后面加引号，CMake 的 VERBATIM 会处理空格
        COMMAND ${PROTOC_EXEC}
                -I "${PROTO_PATH}"
                --cpp_out=${GENERATED_DIR}
                --grpc_out=${GENERATED_DIR}
                --plugin=protoc-gen-grpc=${GRPC_CPP_PLUGIN}
                "${PROTO_ABS}"
        DEPENDS "${PROTO_ABS}"
        COMMENT "Generating gRPC/Protobuf code for ${PROTO_NAME}"
        VERBATIM
    )
    
    # 封装为静态库
    add_library(${TARGET_NAME} STATIC ${GEN_SRCS} ${GEN_HDRS})
    target_include_directories(${TARGET_NAME} PUBLIC "${GENERATED_DIR}" "${PROTO_PATH}")
    # 链接必要的库
    target_link_libraries(${TARGET_NAME} PUBLIC ${GRPC_LIBRARIES} ${PROTO_LIBRARIES} Threads::Threads)
endfunction()

# 自动寻找并加载 judge.proto
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/proto/judge.proto")
    add_grpc_proto(judge_proto "proto/judge.proto")
elseif(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/src/proto/judge.proto")
    add_grpc_proto(judge_proto "src/proto/judge.proto")
endif()

# ==========================================
# 4. 可选依赖处理 (Redis & Crow)
# ==========================================

# Redis++ 处理
pkg_check_modules(REDISPP redis++)
find_library(HIREDIS_LIB hiredis)
if(REDISPP_FOUND AND HIREDIS_LIB)
    add_compile_definitions(HAVE_REDIS)
    set(REDIS_LIBS ${REDISPP_LIBRARIES} ${HIREDIS_LIB})
else()
    # 针对手动编译 redis++ 路径的 fallback
    include_directories(/usr/local/include)
    set(REDIS_LIBS redis++ hiredis)
endif()

# Crow 处理
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/src/api/crow_all.h")
    add_compile_definitions(HAVE_CROW)
    include_directories("${CMAKE_CURRENT_SOURCE_DIR}/src/api")
endif()

# ==========================================
# 5. 生成目标 (按模块构建)
# ==========================================

# --- A. Sandbox 核心逻辑库 ---
add_library(deep_oj_core STATIC
    src/worker/sandbox.cpp
    src/worker/sandbox_isolation.cpp
    src/worker/config.cpp
)
target_include_directories(deep_oj_core PUBLIC src/worker)
target_link_libraries(deep_oj_core PUBLIC ${YAML_LIBRARIES} ${SSL_LIBRARIES} Threads::Threads)

# --- B. Worker (评测单元) ---
add_executable(oj_worker src/worker/main.cpp)
target_link_libraries(oj_worker PRIVATE deep_oj_core judge_proto ${GRPC_LIBRARIES} Threads::Threads)

# --- C. Scheduler (调度中心) ---
add_executable(oj_scheduler src/scheduler/main.cpp)
target_link_libraries(oj_scheduler PRIVATE judge_proto ${GRPC_LIBRARIES} Threads::Threads)

# --- D. API Server (管理端) ---
add_executable(oj_server src/api/main.cpp)
target_link_libraries(oj_server PRIVATE judge_proto ${SSL_LIBRARIES} ${REDIS_LIBS} ${GRPC_LIBRARIES} Threads::Threads)

# 复制配置文件到构建目录
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/config.yaml")
    configure_file("config.yaml" "config.yaml" COPYONLY)
endif()