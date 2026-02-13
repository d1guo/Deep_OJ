#!/bin/bash
set -e

# Configuration
CGROUP_ROOT="/sys/fs/cgroup"
CJ_PATH="${CGROUP_ROOT}/deep_oj"
WORKER_USER=${SUDO_USER:-$(whoami)}
WORKER_GROUP=${SUDO_USER:-$(whoami)}

if [ "$EUID" -ne 0 ]; then
  echo "❌ Please run as root (sudo)"
  exit 1
fi

echo "🔧 Setting up Cgroups v2 for Deep-OJ..."

# 1. Check Cgroups v2
if [ $(stat -f -c %T "$CGROUP_ROOT") != "cgroup2s" ] && [ $(stat -f -c %T "$CGROUP_ROOT") != "tmpfs" ]; then
    # Some systems mount cgroup2 as tmpfs or cgroup2fs. The magic number check in C++ is more robust.
    # Here we just assume if it's not traditional cgroup v1, it might be v2.
    # Actually, standard check:
    if mount | grep "cgroup2" > /dev/null; then
        echo "✅ Cgroups v2 detected."
    else
        echo "⚠️ Cgroups v2 not detected or mounted. This script might fail."
    fi
fi

# 2. Create deep_oj cgroup
if [ ! -d "$CJ_PATH" ]; then
    mkdir -p "$CJ_PATH"
    echo "✅ Created $CJ_PATH"
fi

# 3. Enable controllers in root (if not already enabled)
# Depending on kernel, we might need to enable them in the parent of deep_oj if it's not root
# Assuming deep_oj is under root for now.
# We need 'memory' and 'pids'
for controller in memory pids; do
    if grep -q "$controller" "$CGROUP_ROOT/cgroup.controllers"; then
        if ! grep -q "$controller" "$CGROUP_ROOT/cgroup.subtree_control"; then
            echo "+$controller" > "$CGROUP_ROOT/cgroup.subtree_control"
            echo "✅ Enabled $controller in root"
        fi
    else
        echo "⚠️ Controller $controller not available in kernel"
    fi
done

# 4. Delegate to worker user
# Allow worker user to create sub-cgroups
chown -R "$WORKER_USER:$WORKER_GROUP" "$CJ_PATH"
chmod 755 "$CJ_PATH"

# 5. Enable controllers in deep_oj (so children can use them)
# Note: The worker process (running as WORKER_USER) needs to write to cgroup.subtree_control
# But we process it here as root to ensure it's set up initially.
# Actually, strictly following delegation, the delegatee should write to subtree_control of the directory they own.
# But writing here ensures it's ready.
echo "+memory +pids" > "$CJ_PATH/cgroup.subtree_control" 2>/dev/null || true

echo "✅ Cgroups delegation complete for user: $WORKER_USER"
echo "   Path: $CJ_PATH"
