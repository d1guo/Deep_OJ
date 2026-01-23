# ===================================================
# Stage 1: 构建环境 (Builder)
# ===================================================
FROM ubuntu:22.04 AS builder

# 1. 防止 apt 交互界面卡住
ENV DEBIAN_FRONTEND=noninteractive

# 2. 安装编译工具和依赖库 (一次性装全)
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    git \
    libgrpc++-dev \
    libprotobuf-dev \
    protobuf-compiler-grpc \
    libyaml-cpp-dev \
    && rm -rf /var/lib/apt/lists/*

# 3. 准备工作目录
WORKDIR /app

# 4. 复制源码
COPY . .

# 5. 编译项目
# -B build: 指定构建目录
# -D CMAKE_BUILD_TYPE=Release: 开启优化
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build --parallel $(nproc)

# ===================================================
# Stage 2: 生产环境 (Runtime)
# ===================================================
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# 1. 安装运行时必须的库 (Runtime Deps)
# 注意：必须安装 g++，因为沙箱里需要调用它来编译用户代码！
RUN apt-get update && apt-get install -y \
    libgrpc++1 \
    libprotobuf23 \
    libyaml-cpp0.7 \
    g++ \
    && rm -rf /var/lib/apt/lists/*

# 2. 创建非特权用户 (nobody 通常 ID 是 65534，Ubuntu 自带，不用创建)
# 只要确保 config.yaml 里写的是 65534 即可

# 3. 准备工作目录
WORKDIR /app

# 4. 从构建层复制二进制文件
COPY --from=builder /app/build/worker /app/worker
# 同时也把测试程序拷进来，方便在容器里自测
COPY --from=builder /app/build/security_test /app/security_test

# 5. 复制配置文件 (关键！)
# CMake 里的 configure_file 只在 build 时生效，
# 这里我们直接把源码里的 config.yaml 拷进去，或者拷 build 生成的也行
COPY --from=builder /app/build/config.yaml /app/config.yaml

# 6. 暴露 gRPC 端口 (虽然你是 Unix Socket，但保留端口以防未来改 TCP)
EXPOSE 50051

# 7. 启动命令
CMD ["./worker"]