#!/bin/bash
# 启动 Deep-OJ 服务进行集成测试

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

cleanup() {
    echo -e "\n${RED}停止服务中...${NC}"
    pkill -f "etcd"
    pkill -f "bin/api"
    pkill -f "bin/scheduler"
    echo "qsrlys67" | sudo -S pkill -f "oj_worker"
}
trap cleanup EXIT

# 1. 检查依赖
echo -e "${GREEN}检查依赖项...${NC}"
MISSING=0
for cmd in etcd psql python3 go; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}缺失依赖: $cmd${NC}"
        MISSING=1
    fi
done

if [ $MISSING -eq 1 ]; then
    echo "请先安装缺失的依赖项."
    exit 1
fi

# 2. 启动基础设施
echo -e "${GREEN}启动基础设施...${NC}"

# 启动 Etcd
if ! pgrep etcd > /dev/null; then
    nohup etcd > /dev/null 2>&1 &
    sleep 2
fi

# 检查 Redis
if ! redis-cli ping > /dev/null 2>&1; then
    echo -e "${RED}Redis 未运行! 请启动 Redis.${NC}"
    exit 1
fi
echo "清空 Redis..."
redis-cli FLUSHALL > /dev/null

# 检查 PostgreSQL & 初始化
echo "初始化数据库..."
if ! command -v psql &> /dev/null; then
    echo -e "${RED}找不到 psql 命令${NC}"
    exit 1
fi

DB_USER="postgres"
DB_NAME="deep_oj"

# 检查数据库是否存在，不存在则创建
if sudo -u $DB_USER psql -lqt | cut -d \| -f 1 | grep -qw $DB_NAME; then
    echo "数据库 $DB_NAME 已存在，跳过创建。"
else
    echo "创建数据库 $DB_NAME..."
    sudo -u $DB_USER createdb $DB_NAME
fi

# 运行迁移 (创建表)
# 注意: 为了测试环境纯净，这里先删除旧表
sudo -u $DB_USER psql -d $DB_NAME -c "DROP TABLE IF EXISTS submissions CASCADE;"
sudo -u $DB_USER psql -d $DB_NAME -c "
CREATE TABLE IF NOT EXISTS submissions (
    id SERIAL,
    job_id TEXT PRIMARY KEY,
    problem_id INT,
    user_id INT,
    code TEXT NOT NULL,
    language INT NOT NULL,
    time_limit INT,
    memory_limit INT,
    status TEXT NOT NULL,
    result JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);"

# 创建用户表
sudo -u $DB_USER psql -d $DB_NAME -c "DROP TABLE IF EXISTS users CASCADE;"
sudo -u $DB_USER psql -d $DB_NAME -c "
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT,
    email TEXT,
    github_id TEXT UNIQUE,
    avatar_url TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 授权给 deep_oj 用户
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO deep_oj;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO deep_oj;
"

# 3. 启动应用组件
echo -e "${GREEN}启动系统组件...${NC}"

# 启动 Go API
cd go
nohup ./bin/api > ../api.log 2>&1 &
API_PID=$!
echo "API 已启动 (PID: $API_PID)"
cd ..

# 启动 Go Scheduler
cd go
nohup ./bin/scheduler > ../scheduler.log 2>&1 &
SCHEDULER_PID=$!
echo "调度器已启动 (PID: $SCHEDULER_PID)"
cd ..

# 启动 C++ Worker (需要 root 权限配置 Cgroup)
# 使用 sudo -b 后台运行，并且把日志重定向
# 必须设置 ETCD_ENDPOINTS 才能进行服务注册
echo "qsrlys67" | sudo -S -E -b ETCD_ENDPOINTS=localhost:2379 WORKER_ADDR=localhost:50051 ./build/oj_worker > worker.log 2>&1
echo "Worker 已启动 (sudo)"

# 等待服务就绪
sleep 3

# 4. 运行集成测试脚本 (Python)
echo -e "${GREEN}运行集成测试...${NC}"

echo -e "${GREEN}开始 API 集成测试: http://localhost:8080/api/v1${NC}"

# 确保 data 目录存在
mkdir -p data

# 运行 Python 测试脚本
python3 tests/integration_test.py
EXIT_CODE=$?

if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}集成测试验证成功!${NC}"
    
    # 给指标轮询器 (1s interval) 留一点反应时间，确保看到最终状态
    sleep 2
    echo -e "\n${GREEN}验证监控指标...${NC}"
    echo "--- API 指标 (:8080) ---"
    curl -s http://localhost:8080/metrics | grep "http_requests_total" | head -n 5
    
    echo -e "\n--- 调度器指标 (:9091) ---"
    curl -s http://localhost:9091/metrics | grep "scheduler_queue_depth"
    curl -s http://localhost:9091/metrics | grep "submission_result_total"
    curl -s http://localhost:9091/metrics | grep "scheduler_active_workers"
else
    echo -e "${RED}集成测试验证失败! 请检查日志: api.log, scheduler.log, worker.log${NC}"
fi

exit $EXIT_CODE
