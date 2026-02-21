# Deep-OJ 成品优先任务清单（TASK.md）

> 目标：先做出“可运行、可恢复、不乱结果”的成品，再补齐 agent/安全/CI 等增强项。  
> 执行规则：每次只做一个 [ ]，做完必须提供“验收证据”，再改为 [x]。

---

## 0) 执行规则（强制）
- 每次只做 **一个** 未完成 [ ]。
- 完成后必须：
  1) 更新本文件：该项 [ ] -> [x]
  2) 在该项下补充“验收证据”（命令/关键输出/涉及文件）
- 涉及 DB / Redis 结构变更：必须提供 **迁移步骤 + 回滚步骤**（写进 docs/ 或本文件证据里给出链接/段落）
- Prometheus metrics **禁止**使用 `job_id` 作为 label（高基数）；日志可带 `job_id + attempt_id + trace_id` 但要采样/限流
- 任何 kill/清理逻辑必须 **幂等**（重复执行不报错）
- 不允许“只写方案不改代码”。必须给出可合入的 diff。

---

## 1) 全局硬约束（不变量，系统定律）
> 下面不变量必须写入 `DESIGN.md`，并在代码中被严格执行。

- [x] INV-1 完成定义：**Job 完成 = DB status=FINISHED 且 attempt_id 匹配**；队列仅驱动执行  
  - 证据：`rg -n "INV-1|DB 为唯一状态机|attempt_id" DESIGN.md`
- [x] INV-2 ACK 顺序：**DB 落盘成功后才 XACK**；禁止先 XACK 后写 DB  
  - 证据：`rg -n "INV-2|DB.*XACK|XACK.*DB" DESIGN.md src/go/internal/worker/stream_consumer.go`
- [x] INV-3 PEL 回收入口：**claim -> DB CAS(RUNNING) 成功** 才允许执行；禁止“重入队再执行”  
  - 证据：`rg -n "INV-3|CAS\\(RUNNING\\)|ReclaimSubmissionForRun" DESIGN.md src/go/internal/repository/postgres.go src/go/internal/worker/stream_consumer.go`
- [x] INV-4 CAS 失败分流：  
  - 业务性失败（已 FINISHED / attempt 落后 / lease 被占）=> 立即 XACK（可先记录原因）  
  - 系统性失败（DB 超时/连接故障/可重试冲突）=> 不 XACK，退避重试（限速）  
  - 证据：`rg -n "INV-4|business.*XACK|system.*no.*XACK|reason" DESIGN.md src/go/internal/worker/stream_consumer.go`
- [x] INV-5 FINISHED 幂等 SQL 固化：  
  `UPDATE ... WHERE job_id=? AND attempt_id=? AND status IN ('RUNNING')`  
  0 行更新 => 视为过期/重复/被抢占，记录 reason，禁止覆盖新 attempt  
  - 证据：`rg -n "INV-5|0 行|stale|attempt" DESIGN.md src/go/internal/repository/postgres.go`
- [x] INV-6 可观测性：metrics 禁止 job_id label；Logs/Tracing 使用 `job_id+trace_id` 并采样/限流  
  - 证据：`rg -n "INV-6|job_id label|trace_id" DESIGN.md src/go/internal/*/metrics.go`
- [x] INV-7 可回滚：关键路径必须有 feature flag；如 agent 失败可降级 exec CLI  
  - 证据：`rg -n "INV-7|feature flag|降级|fallback" DESIGN.md RUNBOOK.md`

---

## 2) 成品定义（MVP Done Definition）
> 当下面 4 条全部完成，视为“可交付成品”。

- [x] MVP-1 一键启动：提供 `docker-compose up`（或等价脚本）能启动 API + Worker + Redis + Postgres（可选 MinIO/Etcd），并在 README 写明  
  - 验收证据（2026-02-17）：
    - `docker compose up -d --build`
    - `docker compose ps`
    - `docker logs oj-api --tail 200 2>&1 | rg -n "API Server starting"`
    - 端口冲突（18080）已修复：`Bind ... port is already allocated` 不再出现
- [x] MVP-2 端到端：提交 -> 入队 -> 执行 -> 结果落库 -> 查询结果，全链路可跑通（提供命令/接口示例）  
  - 验收证据（2026-02-18）：
    - 一键：`bash scripts/verify_mvp2_e2e.sh`
    - 关键输出示例：
      - `extracted_job_id=1e539f1b-c1f3-428c-b7c5-3302591ca130`
      - `api_status_check: status=Finished ... verdict=Accepted attempt_id=1`
      - Postgres：`SELECT ... FROM submissions WHERE job_id='...'` 返回 1 行且终态 `Accepted`
    - 涉及文件：`scripts/verify_mvp2_e2e.sh`、`RUNBOOK.md`、`TASK.md`
- [x] MVP-3 崩溃恢复：Worker 在关键点崩溃（未 XACK）后，重启可 reclaim 并最终完成且不乱结果（集成测试或可复现步骤）  
  - 验收证据（2026-02-18）：
    - 一键：`bash scripts/verify_mvp3_crash_recover.sh`
    - 关键输出判据（示例）：
      - `EVIDENCE_PRE_CRASH: status=running attempt_id=1`
      - `EVIDENCE_CRASH: signal=SIGKILL ... worker_restarted=1`
      - `EVIDENCE_PEL: ... pending_after>0`
      - `EVIDENCE_RECLAIM: ... attempt_after_api=2`
      - `EVIDENCE_FENCING: ... stale_write_rows=0`
      - `MVP-3 verify passed`
    - 涉及文件：`scripts/verify_mvp3_crash_recover.sh`、`RUNBOOK.md`、`TASK.md`
- [x] MVP-4 可观测：至少有基本 metrics + structured logs，能定位单个 job（logs）+ 看系统吞吐/延迟（metrics）  
  - 验收证据（2026-02-18）：
    - 一键：`bash scripts/verify_mvp4_observability.sh`
    - 关键输出示例：
      - `metrics_reachable_components=api worker scheduler`
      - `metrics_required_names=ok`
      - `metrics_job_id_label=absent`
      - `EVIDENCE_LOG_CHAIN: trace_id=<...> job_id=<...>`
      - `MVP-4 verify passed`
    - 涉及文件：`scripts/verify_mvp4_observability.sh`、`RUNBOOK.md`、`TASK.md`

---

## 3) 任务队列（严格按顺序做）

### A. 仓库侦察与文档骨架（先把“可解释性”打底）
- [x] A1 仓库侦察：输出当前队列实现/DB schema/Worker 执行链路/可观测现状（写入 `docs/REPO_SURVEY.md`）  
  - 验收证据（2026-02-14）：
    - `cd src/go && go test ./internal/survey -v` => `PASS`
    - `bash scripts/repo_survey_probe.sh --help` => 打印 Usage
- [x] A2 写入 `DESIGN.md` 骨架：架构图 + 状态机 + 关键时序图（先占位也行）  
  - 验收证据（2026-02-14）：
    - `awk 'BEGIN{n=0} /^```mermaid$/{n++} END{print n}' DESIGN.md` => `3`
    - `rg -n "INV-2|INV-5|INV-3|INV-4|状态机|时序" DESIGN.md`
- [x] A3 写入 `RUNBOOK.md`：本地启动、常见故障排查、指标/日志位置  
  - 验收证据（2026-02-14）：
    - `rg -n "^## 0\\. 快速开始|docker compose up -d --build" RUNBOOK.md`
    - `rg -n "^## 3\\. 端到端验收|/api/v1/submit|/api/v1/status/" RUNBOOK.md`

### B. 结构化结果协议 + Worker 执行稳定性（先让执行链路“稳”）
- [x] B1 C++ Judge 输出结构化结果（优先 JSON），字段：job_id/attempt_id/verdict/time_ms/mem_kb/exit_signal/sandbox_error  
  - 证据：
    - 构建：`cmake -S . -B build && cmake --build build -j`
    - 自测：`./build/judge_engine --self_test --job_id demo_job --attempt_id 0`
- [x] B2 Worker 严格校验 job_id/attempt_id；不匹配拒收并记录 reason  
  - 测试：`cd src/go && go test ./internal/worker -run TestJudgeProtocol -v`
- [x] B3 stdout/stderr 非阻塞 drain + 限流/截断，避免 Wait 卡死  
  - 测试：`cd src/go && go test ./internal/worker -run TestDrainLarge -v`
- [x] B4 基本 metrics（禁止 job_id label）+ 结构化日志字段（job_id/attempt_id/trace_id）  
  - 测试：`cd src/go && go test ./internal/worker -run 'TestTraceIDPropagation|TestMetrics' -v`

### C. Redis Streams + Consumer Group 主线（成品核心）
- [x] C1 API 入队改为 Streams：XADD 主 stream（建议 deepoj:jobs），消息字段含 job_id/enqueue_ts/payload_ref/priority  
  - 验收：`docker exec -it oj-redis redis-cli -a "$REDIS_PASSWORD" XRANGE deepoj:jobs - + COUNT 1`
- [x] C2 Worker 消费改为 XREADGROUP（consumer group），并实现 INV-2（DB 后 XACK）  
  - 测试：`cd src/go && go test ./internal/worker -run 'TestStreamConsumer_ClaimHeartbeatFinalizeAndXAck|TestStreamConsumer_DBErrorNoXAck' -v`
- [x] C3 引入 attempt_id/lease 元数据（DB 侧），并实现 `claim -> DB CAS(RUNNING)` 门禁（INV-3）
- [x] C4 PEL reclaim：XAUTOCLAIM/XCLAIM，idle=job_timeout+grace；reclaim 后 attempt_id++；按 INV-4 分流
- [x] C5 FINISHED 幂等入库（INV-5）：0 行更新必须记录 reason，不允许覆盖新 attempt
- [x] C6 Streams 保留与清理：XTRIM MAXLEN~ 策略 + 文档化保留窗口

### D. Outbox + 背压（成品增强但强烈建议做）
- [x] D1 Outbox Pattern：API 同事务写 jobs + outbox_events；dispatcher 异步投递 streams，幂等可重试
- [x] D2 Streams 压力口径：backlog/lag（XINFO GROUPS + XLEN）与 oldest_age（enqueue_ts），写入 bench_report.md
- [x] D3 API 背压：按 backlog/oldest_age 分级限流；重压返回 429 + Retry-After  
  - 验收：`bash scripts/verify_d3_backpressure.sh`（出现 429 + Retry-After，恢复 worker 后 2xx）
- [x] D3.1 升级 go-redis 到 v9 以兼容 Redis 7+/8 的 XAUTOCLAIM 返回结构，修复 reclaim loop 解析错误  
  - 验收证据（2026-02-19）：
    - `cd src/go && go list -m github.com/redis/go-redis/v9` => `github.com/redis/go-redis/v9 v9.18.0`
    - `docker compose up -d --build worker`
    - `docker logs oj-worker --tail 200 | rg -n "got 3, wanted 2|xautoclaim_error"` => 无匹配（退出码 1）

### E. 架构收敛（避免双系统语义打架）
- [x] E1 固化 Streams 与 Scheduler/gRPC 决策（已选 A：Streams-only 数据面）  
  - 验收：`bash scripts/verify_e1_streams_only.sh`（scheduler control-plane only；worker 执行 consume/ack/reclaim）
- [x] E2 B3 控制面收敛：scheduler 仅保留 repair/gc 循环，不再承担任何数据面职责  
  - 验收证据（2026-02-21）：
    - `bash scripts/verify_b3_control_plane.sh`
    - 启动日志包含：`dispatch_mode=streams_only`、`control_plane_only=true`、`legacy_loops_started=0`、`control_plane_loops_started=repair,gc`
    - 默认日志包含：`event=repair_disabled`、`event=gc_disabled`
    - 开启 repair 后日志包含：`event=repair_xadd` 且 Redis `XRANGE/XREVRANGE deepoj:jobs` 可见对应 `job_id`

### F. judge-agent + UDS（可选但希望最终做完）
- [ ] F1 judge-agent 常驻 + UDS RPC；请求必须携带 job_id+attempt_id
- [ ] F2 UDS 幂等：重复请求返回 RUNNING/FINISHED，不得重复执行
- [ ] F3 agent 并发模型与限流：每机最大并发 N，超额排队/拒绝；资源限制按 job 生效
- [ ] F4 agent 启动恢复：SIGCHLD reaper + 扫描清理遗留 cgroup/进程组（幂等）
- [ ] F5 feature flag：agent 优先，UDS 异常自动降级 exec CLI（可回滚）

### G. 沙箱可信度与展示材料（最后做“护城河”）
- [x] G1 “杀全家”语义：进程组 + kill(-pgid, SIGKILL)，防子孙泄漏（含测试）  
  - 验收证据（2026-02-20）：
    - 前置检查（workspace/seccomp/权限）：`bash scripts/verify_g1_prereq.sh`
    - 默认 seccomp 编译：
      ```bash
      mkdir -p "$(pwd)/data/workspace"
      cat >/tmp/g1_accept.cpp <<'CPP'
      #include <iostream>
      int main() { std::cout << "ok\n"; return 0; }
      CPP
      docker run --rm \
        -v "$(pwd)/data/workspace:/data/workspace" \
        -v "/tmp/g1_accept.cpp:/tmp/g1_accept.cpp:ro" \
        --cap-add SYS_ADMIN \
        --cap-add SYS_RESOURCE \
        --cap-add SYS_CHROOT \
        --cap-add SETUID \
        --cap-add SETGID \
        deep-oj:v3 \
        /app/judge_engine --compile -s /tmp/g1_accept.cpp -r "g1_default_$(date +%s)" -C /app/config.yaml
      ```
      在 2026-02-20 的验收环境中返回 `编译沙箱系统错误 (退出码: 194, sandbox_error: pivot_root_failed)`，并含 `step=pivot_root errno=1(Operation not permitted)` 上下文日志（若宿主机 Docker 默认 seccomp 已是 unconfined，则此条可能直接 `status=Compiled`）。
    - 显式放开 seccomp 编译：
      ```bash
      docker run --rm \
        -v "$(pwd)/data/workspace:/data/workspace" \
        -v "/tmp/g1_accept.cpp:/tmp/g1_accept.cpp:ro" \
        --security-opt seccomp=unconfined \
        --cap-add SYS_ADMIN \
        --cap-add SYS_RESOURCE \
        --cap-add SYS_CHROOT \
        --cap-add SETUID \
        --cap-add SETGID \
        deep-oj:v3 \
        /app/judge_engine --compile -s /tmp/g1_accept.cpp -r "g1_unconfined_$(date +%s)" -C /app/config.yaml
      ```
      返回 `status=Compiled`。
    - `bash scripts/verify_g1_kill_all.sh`  
      输出 `EVIDENCE_G1_PARENT_KILL ... all_gone=1`、`EVIDENCE_G1_KILL_BEFORE_PS`、`EVIDENCE_G1_KILL_AFTER_PS`、`EVIDENCE_G1_KILL_BEFORE_CGROUP`、`EVIDENCE_G1_KILL_AFTER_CGROUP`，最终 `G1 verify passed`。
- [ ] G2 清理幂等：cgroup/namespace 清理可重复执行（含测试/复现）
- [ ] G3 6 类对抗测试：fork/线程/内存/FD/磁盘/逃逸 syscall（用例 + 结果）
- [ ] G4 FS/路径逃逸测试：/etc、/proc、符号链接穿越、超大文件写入（用例 + 结果）
- [ ] G5 seccomp allowlist 演进策略：审计模式收集缺失 syscall；未知默认拒绝（文档 + 日志样例）
- [ ] G6 部署前提与降级策略：userns 禁用如何跑、最小 capabilities、cgroup v2 委派/挂载前提（文档）

### H. 质量护栏（强烈建议：配合“先 AI 写完后学项目”）
- [ ] H1 GitHub Actions CI：go test/vet/staticcheck/shellcheck +（可选）race
- [ ] H2 本地一键质量检查：`bash scripts/verify_ci.sh` 与 CI 对齐
- [ ] H3 “不变量回归测试”最小集：把 INV-2/INV-5/INV-3 的关键行为做成自动断言（单测/集成）

---

## 4) “成品完成后”的超级进化文档（占位）
> 注意：这部分等成品稳定后再做。

- [ ] DOC-1 自动生成《从 ACM 到 Deep-OJ 工程深挖》：按源码模块/时序/不变量/故障演练讲解
- [ ] DOC-2 面试版 1 页护符：10 条不变量 + 3 个崩溃时序 + 5 个常见追问答法
