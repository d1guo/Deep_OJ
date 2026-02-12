# Multi-stage Dockerfile for Deep-OJ

# ============================
# Stage 1: Builder (编译环境)
# ============================
FROM ubuntu:22.04 AS builder
ENV DEBIAN_FRONTEND=noninteractive

# 1. 安装编译依赖

RUN sed -i 's/[a-z]\+.ubuntu.com/mirror.nju.edu.cn/g' /etc/apt/sources.list && \
    export http_proxy="http://192.168.0.103:7890" && \
    export https_proxy="http://192.168.0.103:7890" && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
    build-essential cmake g++ make pkg-config git wget ca-certificates \
    libgrpc++-dev libprotobuf-dev protobuf-compiler \
    protobuf-compiler-grpc \
    libabsl-dev libc-ares-dev \
    libhiredis-dev libssl-dev libyaml-cpp-dev \
    libboost-all-dev \
    nlohmann-json3-dev \
    && rm -rf /var/lib/apt/lists/*

# 🔥 核心补丁：确保 gRPC 插件绝对可用且可执行
RUN ln -s /usr/bin/protoc-gen-grpc-cpp /usr/bin/grpc_cpp_plugin || true && \
    chmod +x /usr/bin/grpc_cpp_plugin

WORKDIR /workspace

# 2. 编译 redis-plus-plus (保持 C++20 一致性)
RUN git clone https://github.com/sewenew/redis-plus-plus.git && \
    cd redis-plus-plus && \
    mkdir build && cd build && \
    cmake -DREDIS_PLUS_PLUS_BUILD_TEST=OFF -DCMAKE_BUILD_TYPE=Release -DCMAKE_CXX_STANDARD=20 .. && \
    make -j"$(nproc)" && \
    make install && \
    cd ../.. && rm -rf redis-plus-plus

# 3. 编译项目代码
COPY . /workspace
# 保持 rm -rf build 以清理潜在的本地污染
RUN rm -rf build && mkdir -p build && \
    cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build -j"$(nproc)"

# ============================
# Stage 2: Runtime (运行环境)
# ============================
FROM ubuntu:22.04 AS runtime
ENV DEBIAN_FRONTEND=noninteractive

# 4. 安装运行时必要的动态库 (注意：去掉了 builder 专用的开发工具)
# - g++: Worker 运行用户代码必选
# - libgrpc++ / libyaml-cpp: 确保程序能加载 .so 文件
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates g++ \
    libgrpc++-dev libprotobuf-dev \
    libabsl-dev \
    libhiredis-dev libssl-dev libyaml-cpp-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# 5. 从 Builder 搬运手工编译的库 (Redis++)
COPY --from=builder /usr/local/lib/libredis++* /usr/local/lib/
COPY --from=builder /usr/local/include/sw /usr/local/include/sw
# 刷新动态库缓存
RUN ldconfig

# 6. 搬运编译好的二进制文件和配置
COPY --from=builder /workspace/build /workspace/build
COPY --from=builder /workspace/config.yaml /workspace/config.yaml

# 暴露常用的 API 和 gRPC 端口
EXPOSE 8080 18080 50051 50052

# 保持默认进入 bash，由 docker-compose 指定具体运行哪个程序
CMD ["/bin/bash"]