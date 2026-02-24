#!/bin/bash
set -e

WORKER_USER=${SUDO_USER:-$(whoami)}
WORKER_GROUP=${SUDO_USER:-$(whoami)}

if [ "$EUID" -ne 0 ]; then
  echo "请使用 root（sudo）运行。"
  exit 1
fi

echo "正在为 Deep-OJ 配置 Cgroups v2..."

# 1. 解析 Cgroups v2 挂载点（优先 unified）
detect_cgroup_root() {
    if [ -f "/sys/fs/cgroup/unified/cgroup.controllers" ]; then
        echo "/sys/fs/cgroup/unified"
        return 0
    fi
    if [ -f "/sys/fs/cgroup/cgroup.controllers" ]; then
        echo "/sys/fs/cgroup"
        return 0
    fi
    return 1
}

CGROUP_ROOT="$(detect_cgroup_root || true)"
if [ -z "$CGROUP_ROOT" ]; then
    echo "未检测到可用的 Cgroups v2 挂载点（/sys/fs/cgroup/unified 或 /sys/fs/cgroup）。"
    exit 1
fi
echo "检测到 Cgroups v2 挂载点：$CGROUP_ROOT"

CJ_PATH="${CGROUP_ROOT}/deep_oj"

# 2. 创建 deep_oj cgroup
if [ ! -d "$CJ_PATH" ]; then
    mkdir -p "$CJ_PATH"
    echo "已创建 $CJ_PATH"
fi

# 3. 在根目录启用控制器（若尚未启用）
# 当前假设 deep_oj 位于 root 下，需启用 memory 与 pids。
for controller in memory pids; do
    if grep -qw "$controller" "$CGROUP_ROOT/cgroup.controllers"; then
        if ! grep -qw "$controller" "$CGROUP_ROOT/cgroup.subtree_control"; then
            echo "+$controller" > "$CGROUP_ROOT/cgroup.subtree_control"
            echo "已在根目录启用 $controller"
        fi
    else
        echo "内核中不可用控制器：$controller"
    fi
done

# 4. 委派给 worker 用户（允许创建子 cgroup）
chown -R "$WORKER_USER:$WORKER_GROUP" "$CJ_PATH"
chmod 755 "$CJ_PATH"

# 5. 在 deep_oj 目录启用控制器（让子 cgroup 可用）
# 严格委派下应由目录所有者写入 subtree_control，这里先由 root 预置。
echo "+memory +pids" > "$CJ_PATH/cgroup.subtree_control" 2>/dev/null || true

echo "已完成 Cgroups 委派，用户：$WORKER_USER"
echo "路径：$CJ_PATH"
