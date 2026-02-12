.PHONY: all build clean test run

# 默认目标
all: build

# 编译所有组件
build: build-go build-cpp

build-go:
	@echo "正在编译 Go 服务 (API & Scheduler)..."
	cd go && go mod tidy
	cd go && go build -o bin/api cmd/api/main.go
	cd go && go build -o bin/scheduler cmd/scheduler/main.go
	@echo "Go 服务编译完成。"

build-cpp:
	@echo "正在编译 C++ Worker..."
	mkdir -p build
	cd build && cmake .. && make -j4
	@echo "C++ Worker 编译完成。"

# 运行集成测试 (全链路验证)
test: build
	@echo "正在运行集成测试..."
	./scripts/start_integration.sh

# 启动系统 (标准启动)
run: build
	@echo "正在启动 Deep-OJ V3.0..."
	./scripts/start_integration.sh

# 清理构建产物
clean:
	@echo "正在清理..."
	rm -rf go/bin
	rm -rf build
	rm -rf data
	rm -f *.log
	@echo "清理完成。"
