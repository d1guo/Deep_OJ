#!/bin/bash
set -e

# Build Image
echo "Building deep-oj:v3..."
docker build -t deep-oj:v3 .

# Network
docker network create deep-oj-net || true

# 1. MinIO (Already running? check and reuse or run)
if [ ! "$(docker ps -q -f name=oj-minio)" ]; then
    echo "Starting MinIO..."
    docker run -d --name oj-minio \
        --network deep-oj-net \
        -p 9000:9000 -p 9001:9001 \
        -e MINIO_ROOT_USER=minioadmin \
        -e MINIO_ROOT_PASSWORD=minioadmin \
        minio/minio:RELEASE.2024-01-18T22-51-28Z server /data --console-address ":9001"
fi

# 2. Etcd
echo "Starting Etcd..."
docker rm -f oj-etcd || true
docker run -d --name oj-etcd \
    --network deep-oj-net \
    -p 2381:2379 \
    registry.cn-hangzhou.aliyuncs.com/google_containers/etcd:3.5.0-0 \
    /usr/local/bin/etcd --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://oj-etcd:2379

# 3. Redis
echo "Starting Redis..."
docker rm -f oj-redis || true
docker run -d --name oj-redis \
    --network deep-oj-net \
    -p 6380:6379 \
    redis:alpine

# 4. Postgres
echo "Starting Postgres..."
docker rm -f oj-postgres || true
docker run -d --name oj-postgres \
    --network deep-oj-net \
    -p 5433:5432 \
    -e POSTGRES_USER=deep_oj \
    -e POSTGRES_PASSWORD=secret \
    -e POSTGRES_DB=deep_oj \
    -v $(pwd)/sql/migrations:/docker-entrypoint-initdb.d \
    postgres:15-alpine

# Wait for DB
echo "Waiting for Postgres..."
sleep 5

# 5. Worker
echo "Starting Worker..."
docker rm -f oj-worker || true
docker run -d --name oj-worker \
    --network deep-oj-net \
    --privileged \
    -p 50051:50051 \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v $(pwd)/data/workspace:/data/workspace:rw \
    -e REDIS_URL=oj-redis:6379 \
    -e MINIO_ENDPOINT=oj-minio:9000 \
    -e MINIO_ACCESS_KEY=minioadmin \
    -e MINIO_SECRET_KEY=minioadmin \
    -e MINIO_BUCKET=deep-oj-problems \
    -e ETCD_ENDPOINTS=oj-etcd:2379 \
    -e WORKER_ADDR=oj-worker:50051 \
    -e JUDGER_BIN=/app/judge_engine \
    -e WORKSPACE=/data/workspace \
    deep-oj:v3 /app/oj_worker

# 6. Scheduler
echo "Starting Scheduler..."
docker rm -f oj-scheduler || true
docker run -d --name oj-scheduler \
    --network deep-oj-net \
    -p 50052:50052 \
    -e REDIS_URL=oj-redis:6379 \
    -e WORKER_ADDR=oj-worker:50051 \
    -e ETCD_ENDPOINTS=oj-etcd:2379 \
    -e PGPASSWORD=secret \
    -e DATABASE_URL=postgres://deep_oj:secret@oj-postgres:5432/deep_oj?sslmode=disable \
    deep-oj:v3 /app/oj_scheduler

# 7. API
echo "Starting API..."
docker rm -f oj-api || true
docker run -d --name oj-api \
    --network deep-oj-net \
    -p 18080:18080 \
    -e PORT=18080 \
    -e REDIS_URL=oj-redis:6379 \
    -e SCHEDULER_ADDR=oj-scheduler:50052 \
    -e PGPASSWORD=secret \
    -e DATABASE_URL=postgres://deep_oj:secret@oj-postgres:5432/deep_oj?sslmode=disable \
    -e MINIO_ENDPOINT=oj-minio:9000 \
    -e MINIO_ACCESS_KEY=minioadmin \
    -e MINIO_SECRET_KEY=minioadmin \
    -e MINIO_BUCKET=deep-oj-problems \
    deep-oj:v3 /app/oj_api

echo "All services started."
docker ps
