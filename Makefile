.PHONY: all build clean test run bench docker-build docker-up docker-down \
	bench-outbox resume-metrics coverage

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
	go run scripts/benchmark_submit.go -c 50 -n 5000

# Outbox 故障矩阵
bench-outbox:
	@echo "正在执行 Outbox 故障矩阵..."
	bash scripts/bench_outbox_fault_matrix.sh

# 简历指标汇总
resume-metrics:
	@echo "正在汇总简历指标..."
	bash scripts/collect_resume_metrics.sh

# 覆盖率采集
coverage:
	@echo "正在采集覆盖率..."
	bash scripts/collect_coverage.sh

# 清理
clean: docker-down
	@echo "正在清理..."
	rm -rf src/go/bin
	rm -rf build
	rm -rf data
	rm -f *.log
	@echo "清理完成。"
