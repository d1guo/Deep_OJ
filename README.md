# Deep-OJ V3.0

> **高性能分布式在线评测系统 (Online Judge)**
>
> *混合架构 | 安全沙箱 | 全链路监控*

## 项目简介
Deep-OJ V3.0 是一个面向生产环境的高并发在线评测系统。项目采用 **Go (API & 调度)** 与 **C++ (沙箱执行)** 的混合架构，在保证高开发效率的同时，实现了极致的判题性能与系统安全性。

**核心设计理念：** 快速分发，安全执行，全面可观测。

## 项目核心特性

### 1. 混合微服务架构
- **API 网关 (Go/Gin)**: 负责 RESTful 请求处理、JWT 鉴权、OAuth 2.0 登录以及漏桶/令牌桶限流。
- **调度器 (Go)**: 基于 **Redis RPOPLPUSH** 实现的可靠任务队列，结合 **Etcd** 进行服务发现与负载均衡。
- **判题沙箱 (C++20)**: 基于 Linux 原生特性的高性能执行引擎，低开销、低延迟。
- **数据层**: **PostgreSQL** 负责持久化存储，**Redis** 负责缓存热点数据与消息队列。

### 2. 多重安全沙箱机制
- **Seccomp (安全计算模式)**:基于白名单的系统调用过滤，从内核层面拦截 execve, socket 等危险调用。
- **Cgroups V2**: 对 CPU、内存、PID 进行严格的资源隔离，有效防御 Fork Bomb 与 OOM 攻击。
- **RLimit**: 传统的资源限制机制，作为第二道防线确保系统稳定。

### 3. 高以可靠性与可观测性
- **Prometheus & Grafana**: 实时监控 QPS、P99 延迟、队列堆积量 (Queue Depth) 及 Worker 负载。
- **优雅停机 (Graceful Shutdown)**: 确保服务重启期间无任务丢失。
- **慢路径恢复 (Slow Path)**: 后台守护协程自动检测并恢复因节点故障而超时的“僵尸任务”。

### 4. 企业级工程实践
- **OAuth 2.0**: 集成 GitHub 第三方登录。
- **结构化日志**: 全链路 Trace ID 追踪。
- **Protobuf**: 内部服务间采用高效的二进制序列化协议。

## 系统架构

```mermaid
graph TD
    User[用户/客户端] -->|HTTP/REST| API[API Gateway (Go)]
    API -->|Auth/RateLimit| API
    API -->|Write Job| Redis[(Redis Queue)]
    API -->|Meta Data| DB[(PostgreSQL)]
    
    subgraph Scheduler Layer
        Sched[Scheduler (Go)] -->|Watch| Etcd[(Etcd Registry)]
        Sched -->|BRPopLPush| Redis
    end
    
    subgraph Worker Nodes
        Worker1[Worker 1 (Go + C++)]
        Worker2[Worker 2 (Go + C++)]
    end
    
    Sched -->|gRPC/Protobuf| Worker1
    Sched -->|gRPC/Protobuf| Worker2
    
    Worker1 -->|Report| Redis
    Worker1 -->|Sandbox| Kernel[Linux Kernel]
    
    Prometheus[Prometheus] -->|Scrape| API
    Prometheus -->|Scrape| Sched
    Prometheus -->|Scrape| Worker1
```

## API 文档

| 方法 | 路径 | 描述 |
|------|------|------|
| POST | `/api/v1/auth/login` | 用户登录 (JWT) |
| POST | `/api/v1/auth/register` | 用户注册 |
| POST | `/api/v1/problems` | 上传题目包 (Admin) |
| POST | `/api/v1/submit` | 提交代码 |
| GET  | `/api/v1/status/:job_id` | 查询判题状态 |
| GET  | `/metrics` | Prometheus 监控指标 |

| 组件 | 技术选型 | 作用 |
|------|----------|------|
| **API Server** | Go 1.21, Gin | 接入层，鉴权，限流 |
| **Scheduler** | Go 1.21, Go-Redis | 任务分发，Worker 管理 |
| **Worker** | C++20, CMake | 沙箱执行引擎 (核心) |
| **Discovery** | Etcd | 服务注册与发现 |
| **Data Plane** | Redis, PostgreSQL | 消息队列，缓存，持久化 |
| **Monitor** | Prometheus | 指标采集 |

## 快速开始
 
 ### 环境依赖
 - Linux (推荐 Ubuntu 22.04+)
 - Docker & Docker Configure
 - Make
 
 ### 安装部署
 
 1. **克隆仓库**
    ```bash
    git clone https://github.com/d1guo/Deep_OJ.git
    cd Deep_OJ
    ```
 
 2. **启动服务**
    ```bash
    make docker-up
    ```
    *首次启动会自动构建镜像，可能需要几分钟。*
 
 3. **运行测试**
    ```bash
    make test
    # 或者运行压力测试
    make bench
    ```
 
 ## 验证
 预期集成测试输出：
 ```text
 [SUCCESS] Problem uploaded, ID: 1
 [SUCCESS] Submitted, Job ID: ...
 [SUCCESS] Test Passed! Result: Accepted
 ```

## 许可证
MIT License