#!/bin/bash
set -e

BIN_PATH="build/judge_engine"

if [ ! -f "$BIN_PATH" ]; then
    echo "❌ Binary not found: $BIN_PATH"
    echo "   Please run 'make build' first."
    exit 1
fi

if [ "$EUID" -ne 0 ]; then
  echo "❌ Please run as root (sudo)"
  exit 1
fi

echo "🔧 Setting Linux Capabilities for $BIN_PATH..."

# CAP_SYS_ADMIN: Required for mount, umount, pivot_root, clone(namespaces)
# CAP_SYS_CHROOT: Required for pivot_root/chroot (sometimes covered by SYS_ADMIN but good to be explicit)
# CAP_SETUID/GID: Required to drop privileges inside the sandbox
# CAP_CHOWN/FOWNER: Required to set ownership of temp directories
# CAP_SYS_RESOURCE: Required for setrlimit (if limits > hard limit) and cgroup limits
# CAP_KILL: Required to kill child processes
# CAP_DAC_OVERRIDE: (Optional) We try to avoid this by using correct ownership

CAPS="cap_sys_admin,cap_setuid,cap_setgid,cap_chown,cap_fowner,cap_kill,cap_sys_resource+ep"

setcap "$CAPS" "$BIN_PATH"

# Verify
getcap "$BIN_PATH"
echo "✅ Capabilities set successfully."
