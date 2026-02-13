.PHONY: all build clean test run docker-build docker-up docker-down

# 默认目标
all: docker-build

# Docker 构建
docker-build:
	@echo "正在构建 Docker 镜像..."
	docker-compose build

# Docker 启动
docker-up:
	@echo "正在启动服务..."
	docker-compose up -d

# Docker 停止
docker-down:
	@echo "正在停止服务..."
	docker-compose down

# 运行集成测试 (E2E)
test:
	@echo "正在运行集成测试..."
	python3 tests/integration/test_e2e.py

# 启动压力测试 (Benchmark)
bench:
	@echo "正在运行压力测试..."
	go run benchmark/submit_bench.go -c 50 -d 10s

# 清理
clean: docker-down
	@echo "正在清理..."
	rm -rf src/go/bin
	rm -rf build
	rm -rf data
	rm -f *.log
	@echo "清理完成。"
