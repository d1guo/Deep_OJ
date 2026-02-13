cmake_minimum_required(VERSION 3.20)
project(Deep_OJ VERSION 1.0 LANGUAGES CXX)

# 1. Global Config
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

include_directories(${CMAKE_SOURCE_DIR}/include)
include_directories(${CMAKE_SOURCE_DIR}/src/core/worker)

find_package(Threads REQUIRED)
add_compile_options(-pthread)

# 2. Dependencies
find_package(PkgConfig REQUIRED)

# YAML-CPP
pkg_check_modules(YAML yaml-cpp)
if(NOT YAML_FOUND)
    find_package(yaml-cpp REQUIRED)
    if(TARGET yaml-cpp)
        set(YAML_LIBRARIES yaml-cpp)
    else()
        message(FATAL_ERROR "yaml-cpp not found!")
    endif()
endif()
message(STATUS "YAML Config: FOUND=${YAML_FOUND}, LIBS=${YAML_LIBRARIES}")

# LibSeccomp
pkg_check_modules(SECCOMP REQUIRED libseccomp)
include_directories(${SECCOMP_INCLUDE_DIRS})

# Nlohmann JSON (Header Only)
find_path(JSON_INCLUDE_DIR nlohmann/json.hpp)
if (NOT JSON_INCLUDE_DIR)
    message(STATUS "nlohmann/json.hpp not found via find_path, trying standard /usr/include")
    if(EXISTS "/usr/include/nlohmann/json.hpp")
        set(JSON_INCLUDE_DIR "/usr/include")
    else()
        message(FATAL_ERROR "nlohmann/json.hpp not found! Please install nlohmann-json3-dev")
    endif()
endif()
include_directories(${JSON_INCLUDE_DIR})
message(STATUS "JSON Include Dir: ${JSON_INCLUDE_DIR}")

# 3. Targets

# --- A. Sandbox Core ---
add_library(deep_oj_core STATIC
    src/core/worker/sandbox.cpp
    src/core/worker/sandbox_isolation.cpp
    src/core/worker/config.cpp
    src/core/worker/seccomp_rules.cpp
    src/core/worker/cgroup_manager.cpp
)
# Removed etcd_registry.cpp as it requires curl and is not needed for core execution

target_link_libraries(deep_oj_core PUBLIC 
    Threads::Threads 
    ${YAML_LIBRARIES} 
    ${SECCOMP_LIBRARIES}
    stdc++fs
)

# --- B. Judge Engine (CLI) ---
add_executable(judge_engine src/core/worker/main.cpp)
target_link_libraries(judge_engine deep_oj_core)

# --- C. Security Test Runner ---
find_package(GTest REQUIRED)
add_executable(security_test_runner tests/security_integration_test.cpp)
target_link_libraries(security_test_runner deep_oj_core GTest::GTest GTest::Main)

# Copy Config
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/config.yaml")
    configure_file("config.yaml" "config.yaml" COPYONLY)
endif()