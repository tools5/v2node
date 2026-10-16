#!/usr/bin/env bash
# stealth-verify.sh - 进程隐藏效果验证（Linux）
#
# 用法：
#   sudo ./script/stealth-verify.sh              # 自动找 v2nb 进程
#   PID=1234 sudo ./script/stealth-verify.sh     # 指定 pid
#
# 判定标准：除"隐藏进程自身视角"外，所有枚举方式都应看不到目标进程。
# （故意不启用 set -u，PID 未设置时按空处理）

target_pid="$PID"
if [ -z "$target_pid" ]; then
  target_pid=$(pgrep -f 'v2nb server' | head -n1)
fi
if [ -z "$target_pid" ]; then
  echo "[x] 没找到目标进程，用 PID=<pid> 指定"
  exit 1
fi
echo "[*] 目标 pid = $target_pid"
echo

pass=0
fail=0

check() { # $1 描述  $2 命令
  local out
  out=$(eval "$2" 2>/dev/null)
  if [ -z "$out" ]; then
    echo "[v] $1 -> 不可见"
    pass=$((pass+1))
  else
    echo "[x] $1 -> 仍可见: $out"
    fail=$((fail+1))
  fi
}

check "ps -ef"                "ps -ef | awk -v p=$target_pid '\$2==p'"
check "ps -eo pid,comm"       "ps -eo pid,comm | awk -v p=$target_pid '\$1==p'"
check "ps -eLf (线程视角)"    "ps -eLf | awk -v p=$target_pid '\$2==p'"
check "ls /proc"              "ls /proc | grep -x $target_pid"
check "busybox ps"            "busybox ps 2>/dev/null | awk -v p=$target_pid '\$1==p'"
check "top -bn1"              "top -bn1 2>/dev/null | awk -v p=$target_pid '\$1==p'"
check "cat /proc/PID/status"  "cat /proc/$target_pid/status 2>/dev/null | head -n1"
check "ls /proc/PID"          "ls /proc/$target_pid 2>/dev/null"
check "lsmod 里的 v2hide"     "lsmod | awk '\$1==\"v2hide\"'"
check "ls /sys/module"        "ls /sys/module | grep -x v2hide"

echo
echo "[*] 自检（隐藏进程自身视角，默认应当仍然可见）"
if [ -r "/proc/$target_pid/cmdline" ]; then
  echo "    cmdline = $(tr '\0' ' ' < /proc/$target_pid/cmdline)"
  echo "    comm    = $(cat /proc/$target_pid/comm 2>/dev/null)"
  echo "    environ = $(tr '\0' ' ' < /proc/$target_pid/environ 2>/dev/null | head -c 120)"
fi
echo
echo "[=] 通过 $pass 项，失败 $fail 项"
if [ "$fail" -eq 0 ]; then
  echo "[=] 隐藏生效"
else
  echo "[!] 有枚举方式仍能看到目标，先确认模块是否加载成功（dmesg | tail）"
fi
