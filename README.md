# 🏛️ Deep-OJ: Distributed Online Judge System

![C++](https://img.shields.io/badge/C++-20-blue.svg?style=flat&logo=c%2B%2B)
![gRPC](https://img.shields.io/badge/RPC-gRPC-green.svg?style=flat&logo=google)
![Redis](https://img.shields.io/badge/Data-Redis-red.svg?style=flat&logo=redis)
![License](https://img.shields.io/badge/License-MIT-yellow.svg)
![Build](https://img.shields.io/badge/Build-Passing-brightgreen.svg)

> **高性能、高可靠、事件驱动** 的分布式在线判题系统。

---

## 📖 项目简介 (Introduction)

**Deep-OJ** 是一个基于 **C++20 标准** 开发的分布式判题系统。它专为解决传统 OJ 在高并发场景下的吞吐瓶颈与状态一致性问题而设计。通过 **API / Scheduler / Worker** 三层解耦架构与 **Redis** 数据总线，实现了在受限资源环境下的极致吞吐与容灾能力。

### 🎯 设计目标

* **轻量级高性能**：在 **受限资源环境（2 核 / 4G）** 下稳定运行。
* **极致吞吐**：通过 **极致的异步解耦** 提升系统并发处理能力。
* **高可用**：在高并发场景下仍具备 **容灾与自恢复能力**。

### ✨ 核心亮点

> **事件驱动快路径（Fast Path） + 状态回查兜底（Slow Path）** 的双模确认机制。

在保证高性能的同时，完美解决了分布式系统中 **状态一致性 vs 性能** 的经典矛盾。

---

## 🏗️ 系统架构 (System Topology)

Deep-OJ 采用 **三层解耦架构**，Redis 作为数据与状态的中枢，各组件通过 gRPC 强契约通信，构成稳定闭环。

### 🧩 1. API Server（门面）
对外提供 HTTP API，作为流量入口。
* **接收提交**：处理用户代码提交请求。
* **任务生产**：生成 `JobId` 并将任务入队。
* **结果查询**：提供判题状态的查询接口与持久化存储。

### 🧩 2. Redis（中枢）
承担系统的数据总线角色：
* **`queue:pending` (List)**: 待分配任务队列。
* **`queue:processing` (ZSet)**: 正在处理的任务集合（Score 为时间戳，用于超时检测）。
* **`Pub/Sub`**: 实时事件通知通道（用于 Fast Path）。

### 🧩 3. Scheduler（大脑）
核心调度器，负责任务分发与生命周期管理。
* **负载均衡**：根据 Worker 负载情况通过 gRPC 分发任务。
* **可靠拉取**：从 Redis 获取任务。
* **生命周期管理**：负责 ACK 确认、失败重试及超时处理。

### 🧩 4. Worker（肌肉）
判题执行节点，构建基于内核隔离的安全沙箱。
* **安全沙箱**：基于 **Linux cgroups + seccomp** 限制资源与系统调用。
* **判题流程**：编译 -> 运行 -> 结果汇总。
* **主动上报**：通过 gRPC 主动向 API Server 汇报结果。

---

## 🔄 数据流转与核心机制 (Data Flow)

Deep-OJ 采用了创新的 **快慢路径设计** 来平衡性能与数据一致性。

### 🚀 阶段一：提交与调度
1.  **User → API**：用户提交代码，API 生成 `JobId`，`LPUSH` 到 `queue:pending`，并立即返回 `JobId`。
2.  **Scheduler**：使用 `BRPOPLPUSH` 原子指令将任务从待处理队列移入 `queue:processing`（确保任务永不丢失），并通过 gRPC `ExecuteTask` 分发给 Worker。

### 🚀 阶段二：执行与上报
1.  **Worker**：写入临时文件，启动沙箱（cgroups 限制资源），编译并运行代码。
2.  **Worker → API**：判题完成后，Worker 通过 gRPC `UpdateStatus` 主动“交卷”。

### 🔄 阶段三：确认与清理（核心亮点）

#### ✅ 路径 A：快路径（Fast Path，99% 正常情况）
* **流程**：API 写库/缓存 -> `PUBLISH job_done` -> Scheduler 收到通知 -> `ZREM queue:processing`。
* **优势**：全程 `O(1)` Redis 操作，无数据库轮询，毫秒级确认。

#### 🛟 路径 B：慢路径（Slow Path，1% 异常兜底）
* **场景**：Pub/Sub 消息丢失或 Scheduler 重启。
* **流程**：Scheduler 定期扫描 `queue:processing` 中超时（>30s）的任务 -> 发起 **Check-Back**（查 Redis/DB）。

---

## ⚖️ 工程决策与权衡 (Trade-offs)

1.  **一致性模型：At-Least-Once**
    * 允许极低概率的任务重复执行（判题逻辑天然幂等）。
    * 数据库利用 Primary Key 保证 **Exactly-Once 存储**。
    * *决策理由：相比 2PC，这是一种用计算换复杂度的高性价比方案。*

2.  **超时阈值设定：30秒**
    * 公式：`T_timeout ≈ (T_compile + T_run) × 1.5 + T_jitter`。
    * *决策理由：覆盖 P99 判题耗时，避免过早重试导致资源浪费。*

3.  **高可用设计 (HA)**
    * Scheduler 设计为 **无状态节点**。
    * 采用 **Active-Standby** 架构，通过 Redis 分布式锁选主。

---

## 🛠️ 技术栈 (Tech Stack)

* **Core**: C++20 (Concepts / jthread / Modules)
* **Communication**: gRPC + Protobuf (Schema-First)
* **Middleware**: Redis (AOF On / Lua Scripting)
* **Kernel Isolation**:
    * **Linux cgroups**: 资源配额（CPU/Memory）
    * **seccomp**: 系统调用白名单过滤

---

## 🚀 快速开始 (Getting Started)

### 环境要求
* Linux Kernel 5.4+ (支持 cgroups v2)
* GCC 11+ / Clang 14+ (支持 C++20)
* Redis 6.0+
* Protobuf & gRPC

### 构建项目

```bash
# 克隆项目
git clone [https://github.com/your-username/Deep-OJ.git](https://github.com/your-username/Deep-OJ.git)
cd Deep-OJ

# 创建构建目录
mkdir build && cd build

# 编译
cmake ..
make -j4

```

### 运行服务

```bash
# 1. 启动 Redis
redis-server

# 2. 启动 API Server
./bin/api_server

# 3. 启动 Scheduler
./bin/scheduler

# 4. 启动 Worker (需 root 权限以操作 cgroups)
sudo ./bin/worker

```