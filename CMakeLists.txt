cmake_minimum_required(VERSION 3.20)

project(Deep-OJ VERSION 0.1 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

find_package(Protobuf REQUIRED)
find_package(gRPC REQUIRED)
find_package(Threads REQUIRED)

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
        COMMAND protobuf::protoc
        ARGS --grpc_out "${GENERATED_DIR}"
             --cpp_out "${GENERATED_DIR}"
             -I "${PROTO_PATH}"
             --plugin=protoc-gen-grpc=$<TARGET_FILE:gRPC::grpc_cpp_plugin>
             "${PROTO_FILE}"
        DEPENDS "${PROTO_FILE}" protobuf::protoc gRPC::grpc_cpp_plugin
        COMMENT "Generating gRPC sources from ${PROTO_FILE}"
        VERBATIM
    )

    add_library(${TARGET_NAME} STATIC ${GENERATED_SRCS} ${GENERATED_HDRS})
    target_include_directories(${TARGET_NAME} PUBLIC "${GENERATED_DIR}" "${PROTO_PATH}")
    target_link_libraries(${TARGET_NAME} PUBLIC gRPC::grpc++ protobuf::libprotobuf)
    target_compile_features(${TARGET_NAME} PUBLIC cxx_std_20)
endfunction()

# 生成 gRPC 代码库 target: judge_proto
add_grpc_proto(judge_proto "proto/judge.proto")

# ========================================================
# [Core Library] The Sandbox
# ========================================================
add_library(deep_oj_core STATIC 
    src/worker/sandbox.cpp 
    src/worker/sandbox_isolation.cpp
)

target_include_directories(deep_oj_core PUBLIC src/worker)
target_link_libraries(deep_oj_core PUBLIC Threads::Threads)
target_compile_features(deep_oj_core PUBLIC cxx_std_20)


# ========================================================
# Executables
# ========================================================

# 1. Scheduler (The Client)
add_executable(scheduler src/scheduler/main.cpp)
target_link_libraries(scheduler PRIVATE judge_proto gRPC::grpc++ gRPC::grpc++_reflection protobuf::libprotobuf Threads::Threads)
target_compile_features(scheduler PRIVATE cxx_std_20)

# 2. Worker (The Server)
add_executable(worker src/worker/main.cpp)
target_link_libraries(worker PRIVATE deep_oj_core judge_proto gRPC::grpc++ gRPC::grpc++_reflection protobuf::libprotobuf Threads::Threads)
target_compile_features(worker PRIVATE cxx_std_20)

# 3. Security Test Suite (Standalone)
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/tests/security_test.cpp")
    add_executable(security_test tests/security_test.cpp)
    target_link_libraries(security_test PRIVATE deep_oj_core Threads::Threads)
    target_compile_features(security_test PRIVATE cxx_std_20)
endif()
