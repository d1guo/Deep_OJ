#!/bin/bash
set -e

BIN_PATH="build/judge_engine"

if [ ! -f "$BIN_PATH" ]; then
    echo "未找到二进制文件：$BIN_PATH"
    echo "请先执行 'make build'。"
    exit 1
fi

if [ "$EUID" -ne 0 ]; then
  echo "请使用 root（sudo）运行。"
  exit 1
fi

echo "正在为 $BIN_PATH 设置 Linux Capabilities..."

# CAP_SYS_ADMIN: mount/umount/pivot_root/clone(namespaces) 需要
# CAP_SYS_CHROOT: pivot_root/chroot 需要
# CAP_SETUID/GID: 沙箱内降权需要
# CAP_CHOWN/FOWNER: 临时目录权限处理需要
# CAP_SYS_RESOURCE: setrlimit 与 cgroup 限制需要
# CAP_KILL: 终止子进程需要
# CAP_DAC_OVERRIDE: 可选，尽量通过正确属主规避

CAPS="cap_sys_admin,cap_setuid,cap_setgid,cap_chown,cap_fowner,cap_kill,cap_sys_resource+ep"

setcap "$CAPS" "$BIN_PATH"

# 校验
getcap "$BIN_PATH"
echo "能力集设置完成。"
