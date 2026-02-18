# Multi-stage Dockerfile for Deep-OJ

# Stage 1: C++ Builder
FROM ubuntu:22.04 AS cpp_builder
ENV DEBIAN_FRONTEND=noninteractive

# Install dependencies
RUN apt-get update -o Acquire::Retries=5 -o Acquire::http::Timeout=30 && \
    apt-get install -y --no-install-recommends --fix-missing \
    build-essential cmake g++ make pkg-config git wget ca-certificates \
    libgrpc++-dev libprotobuf-dev protobuf-compiler \
    libabsl-dev libc-ares-dev \
    libhiredis-dev libssl-dev libyaml-cpp-dev \
    libseccomp-dev \
    libboost-all-dev \
    libgtest-dev \
    && rm -rf /var/lib/apt/lists/*

# Install JSON library (separate run to use cache for above)
RUN apt-get update -o Acquire::Retries=5 -o Acquire::http::Timeout=30 && \
    apt-get install -y --no-install-recommends --fix-missing nlohmann-json3-dev && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Build redis-plus-plus (needed for legacy or shared libs, though judge_engine doesn't use it, core might if I didn't clean correctly)
# But I removed redis from core. So skipping redis-plus-plus might be safe for C++ part?
# Wait, let's keep it just in case some legacy file includes it.
RUN git clone https://github.com/sewenew/redis-plus-plus.git && \
    cd redis-plus-plus && \
    mkdir build && cd build && \
    cmake -DREDIS_PLUS_PLUS_BUILD_TEST=OFF -DCMAKE_BUILD_TYPE=Release -DCMAKE_CXX_STANDARD=20 .. && \
    make -j"$(nproc)" && \
    make install && \
    cd ../.. && rm -rf redis-plus-plus

# Build C++ Project (Judge Engine)
COPY . /workspace
RUN rm -rf build && mkdir -p build && \
    cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build --target judge_engine -j"$(nproc)" && \
    cmake --build build --target security_test_runner -j"$(nproc)"

# Stage 2: Go Builder
FROM golang:1.24 AS go_builder
ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /workspace
COPY src/go/go.mod src/go/go.sum ./
RUN go mod download

COPY src/go/ ./
RUN CGO_ENABLED=0 go build -o /bin/oj_api ./cmd/api
RUN CGO_ENABLED=0 go build -o /bin/oj_scheduler ./cmd/scheduler
RUN CGO_ENABLED=0 go build -o /bin/oj_worker ./cmd/worker

# Stage 3: Runtime
FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update -o Acquire::Retries=5 -o Acquire::http::Timeout=30 && \
    apt-get install -y --no-install-recommends --fix-missing \
    ca-certificates g++ tzdata \
    libgrpc++-dev libprotobuf-dev \
    libabsl-dev \
    libhiredis-dev libssl-dev libyaml-cpp-dev \
    libseccomp-dev \
    && ln -snf /usr/share/zoneinfo/Etc/UTC /etc/localtime \
    && echo "Etc/UTC" > /etc/timezone \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy C++ Binaries
COPY --from=cpp_builder /workspace/build/judge_engine /app/judge_engine
COPY --from=cpp_builder /workspace/build/security_test_runner /app/security_test_runner
# Copy Redis++ libs if needed (probably not for judge_engine, but good practice)
COPY --from=cpp_builder /usr/local/lib/libredis++* /usr/local/lib/
RUN ldconfig

# Copy Go Binaries
COPY --from=go_builder /bin/oj_api /app/oj_api
COPY --from=go_builder /bin/oj_scheduler /app/oj_scheduler
COPY --from=go_builder /bin/oj_worker /app/oj_worker

# Create User
RUN groupadd -r deep_oj && useradd -r -g deep_oj deep_oj

# Copy Config and Migrations
COPY config.yaml /app/config.yaml
COPY sql/migrations /app/sql/migrations

# Create necessary directories and set permissions
RUN mkdir -p /data/testcases && \
    mkdir -p /sys/fs/cgroup && \
    chown -R deep_oj:deep_oj /app && \
    chown -R deep_oj:deep_oj /data && \
    chown root:root /app/config.yaml && \
    chmod 644 /app/config.yaml

EXPOSE 8080 18080 50051 50052

CMD ["/bin/bash"]
