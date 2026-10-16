# v2nb 进程隐藏改造报告

> 范围：本机/自有节点上的 v2node（v2nb）进程隐藏改造。源码分析 + 分层实现 + 验证与回滚。
> 对应改动：common/stealth/*、cmd/server.go、conf/conf.go、tools/linux-hide/*、tools/win-hide/*、script/stealth-verify.sh。

---

## 1. 目标程序源码分析

### 1.1 项目结构

| 目录 | 作用 |
| --- | --- |
| main.go | 9 行入口，只调 cmd.Run() |
| cmd/ | cobra 命令：cmd.go（root + Execute）、server.go（server 子命令，主逻辑）、version.go |
| conf/ | viper 配置加载（Log / Nodes / PprofPort）+ fsnotify 热重载 |
| node/ | 向 V2board 面板领任务：node.go、controller.go（轮询）、task.go、user.go、cert.go、lego.go（ACME 证书）、connections.go |
| api/v2board/ | 面板 HTTP 客户端：panel.go、node.go、user.go、connections.go（实时连接查询/断开） |
| core/ | 修改版 xray 内核接线：core/app/dispatcher（v2node dispatcher 配置、sniffer、fakedns、连接快照）、core/distro/all（注册全部协议实现） |
| common/ | counter（流量计数）、connections（连接注册表）、connectioncontrol（断连）、crypt（aes/x25519）、exec（shell 调用）、file、format、rate（限速）、systime（时间校正）、task |
| limiter/ | 用户级限速初始化 |

### 1.2 启动链路（改动的接入点）

~~~
main.main
  └─ cmd.Run()                      cmd/cmd.go:13   root cobra.Execute()
       └─ v2nb server --config ...  cmd/server.go:25
            └─ serverHandle()       cmd/server.go:42
                 1. showVersion()                 打印版本（可控情报泄露点之一）
                 2. conf.New() + LoadFromPath()   读 /etc/v2nb/config.json
                 3. logrus 格式化 / 级别 / 输出文件
                 4. pprof 监听 127.0.0.1:<PprofPort>
                 5. limiter.Init()
                 6. node.New()  -> 账号/节点信息
                 7. core.New().Start(NodeInfos)   起 xray 内核
                 8. nodes.Start()                 起面板轮询 + 入站
                 9. c.Watch(config, ...)          热重载信号
                10. signal.Notify(SIGINT/SIGTERM) + 主循环
~~~

### 1.3 进程在当前系统里的可见面（改造前的"指纹"）

| 观察面 | 内容 |
| --- | --- |
| /proc/<pid>/comm、ps 的 COMMAND 列 | v2nb |
| /proc/<pid>/cmdline、ps -ef 的 CMD 列 | v2nb server --config /etc/v2nb/config.json |
| /proc/<pid>/environ | 启动环境变量（PATH、代理、密钥环境变量等） |
| /proc/<pid>/exe | 二进制路径 |
| lsmod / 内核模块表 | 无（未加模块时干净） |
| 日志/配置路径 | /etc/v2nb/、logrus 输出文件 |

结论：进程隐藏要同时处理"进程名 + 命令行 + 环境 + /proc 目录项"四类痕迹；
只改名不摘目录项，等于没有隐藏。

---

## 2. 改造设计

### 2.1 分层模型

| 层 | 手段 | 覆盖的枚举方式 | 需要特权 |
| --- | --- | --- | --- |
| L1-a 名字伪装 | prctl(PR_SET_NAME) + 写 comm；Windows SetConsoleTitle | ps 的 COMMAND（无 cmdline 时显示 [comm]） | 无 |
| L1-b argv 清零 | 拿 os.Args 首尾指针定出整段栈内存，清零后写别名 | ps -ef / ps aux、/proc/<pid>/cmdline | 无 |
| L1-c environ 清零 | 同上，针对 os.Environ() 内存区，同时把变量搬进 Go 私有副本 | /proc/<pid>/environ | 无 |
| L1-d 二进制摘除 | 读 /proc/self/exe 链接后 unlink（inode 仍被持有） | /proc/<pid>/exe、（可选）磁盘留存 | 无 |
| L1-e Windows PEB 改写 | 改 RTL_USER_PROCESS_PARAMETERS 的 CommandLine / ImagePathName | 任务管理器"命令行"列、WMI Win32_Process.CommandLine、wmic | 无 |
| L2-a Linux 内核模块 | kprobe iterate_dir 过滤 procfs 目录项 + kretprobe proc_pid_lookup 返回 NULL + 模块自隐藏 | ps / top / htop / ls /proc / procps / busybox ps | root / CAP_SYS_MODULE |
| L2-b Windows 驱动 | DKOM 摘 EPROCESS 活动进程链 + 自环退出保护 + 驱动对象/模块表自隐藏 | Task Manager / tasklist / psapi / WMI / Toolhelp32 | 驱动加载权限（DSE 需 testsigning 或 BYOVD 手动映射） |

### 2.2 为什么 L2 必须在内核

- Linux：4.x 之后 procfs 用 idr/radix 树而不是 task 链表，改 task_struct 链表无效；
  真正决定 ps 能看到什么的是 getdents 的结果，所以在这条路径上过滤（iterate_dir -> dir_context->actor）。
- Windows：任何用户态 hook 只能影响自己进程；要挡 Task Manager 的枚举只能摘内核链表。

### 2.3 已实现的关键细节

1. **argv/environ 内存定位**：Go 的 os.Args / os.Environ() 字符串直接别名原始栈内存
   （runtime gostringnocopy 语义），因此 unsafe.StringData 首尾即可定出整段区间，不需要
   prctl(PR_SET_MM_*)，也不需要 /proc/self/stat 猜测。
2. **argv 清零与命令行参数解析的顺序**：cobra 在 Execute() 期间解析参数，stealth.Init 在
   Run（即 serverHandle）里执行，所以不会破坏 --config 解析；但 config 字符串本身可能别名
   argv 内存，代码里先 strings.Clone 再用，热重载路径不受影响。
3. **environ 清空后仍需 Getenv 可用**：先备份到 Go map，清零原始内存后 os.Clearenv() +
   Setenv 重建，内核侧 /proc/<pid>/environ 为空，程序侧环境变量照常工作。
4. **自身视角放行**：模块默认 hide_from_self=0，隐藏进程自己访问 /proc/self/* 不受影响
   （避免运行时/库依赖 /proc/self 时被自己挡住）。
5. **内核模块自隐藏可恢复**：lsmod 看不到模块后 rmmod 会找不到模块，所以控制节点提供
   show 指令先恢复链表再卸载，Go 侧 kernelUnhide 也是这个顺序。
6. **Windows 退出安全**：摘链后把目标 ActiveProcessLinks 置为自环，进程退出时
   RemoveEntryList 变成 no-op，不会踩到 LIST_ENTRY 一致性校验；恢复时先用
   PsLookupProcessByProcessId 确认对象存活（该查表走 CID table，与活动进程链无关）。
7. **加载痕迹清理**：Windows 侧加载完成后删除服务键与磁盘 .sys，驱动对象
   ObMakeTemporaryObject 从 \Driver 目录消失，并从 PsLoadedModuleList 摘掉自身。

---

## 3. 落地步骤

### 3.1 Linux（推荐路径）

~~~
# 1) 编译节点（含 stealth 包）
GOEXPERIMENT=jsonv2 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o build_assets/v2nb

# 2) 编译内核模块（目标机需 linux-headers）
cd tools/linux-hide && make && sudo install -m 600 v2hide.ko /etc/v2nb/v2hide.ko

# 3) 配置 /etc/v2nb/config.json 打开 Stealth（见 example/stealth-config.json）
#    Enable=true, Kernel=true, HideModule=true, HideEnv=true

# 4) 启动后验证
sudo ./script/stealth-verify.sh
~~~

### 3.2 Windows

~~~
:: 1) 编译驱动
tools\win-hide\build.bat           :: 产出 v2hide.sys（需 WDK）

:: 2) 测试签名或 DSE 绕过
bcdedit /set testsigning on        :: 测试机；生产机用 kdmapper 手动映射

:: 3) 启动节点（管理员权限，配置 Stealth.KernelDriver 指向 v2hide.sys）
v2nb.exe server --config C:\v2nb\config.json

:: 4) 验证
tasklist | findstr v2nb            :: 空
driverquery | findstr v2hide       :: 空
~~~

### 3.3 配置项

| 键 | 默认 | 说明 |
| --- | --- | --- |
| Stealth.Enable | false | 总开关 |
| Stealth.Alias | kworker/1:1（Win: svchost.exe） | 对外显示名 |
| Stealth.Kernel | false | 是否加载内核组件 |
| Stealth.KernelModule | /etc/v2nb/v2hide.ko | Linux 模块路径 |
| Stealth.KernelDriver | 空 | Windows 驱动路径 |
| Stealth.HideEnv | true | 清空 environ |
| Stealth.HideModule | true | 内核组件自隐藏 |
| Stealth.ShredExe | false | 删除磁盘二进制（部署后无法直接重启） |
| Stealth.Debug | false | 启动打印自检结果 |

---

## 4. 验证矩阵

| 检查 | 命令 | 期望 |
| --- | --- | --- |
| 进程列表 | ps -ef / ps -eo pid,comm | 无目标 |
| 线程视图 | ps -eLf | 无目标 |
| 目录列举 | ls /proc、ls /proc/<pid> | 无目标 |
| Top 视图 | top -bn1 | 无目标 |
| 模块表 | lsmod、ls /sys/module | hide_module=1 时无 v2hide |
| 直接访问 | cat /proc/<pid>/status | hide_direct=1 时 ENOENT |
| 自身视角 | /proc/self/cmdline | 别名，无原命令行 |
| 环境 | /proc/self/environ | 空 |
| Windows | tasklist / Get-Process / wmic process / driverquery | 无目标、无驱动 |
| LD_PRELOAD 对照 | V2HIDE_PID=<pid> LD_PRELOAD=libhide.so ps aux | 无目标（仅 glibc 调用者） |

script/stealth-verify.sh 已把上表前 9 项做成自动化检查。

---

## 5. 局限与风险

| 项 | 说明 |
| --- | --- |
| 容器 | 普通容器无 CAP_SYS_MODULE，L2 失效；需要 --privileged 且宿主允许 insmod |
| 内核签名 | CONFIG_MODULE_SIG_FORCE 需签名模块；Windows 需 testsigning 或 BYOVD 手动映射 |
| HVCI/VBS | 内核代码段只读、部分结构受 KDP 保护，Windows DKOM 可能直接 BSOD |
| Go 二进制 | LD_PRELOAD 方案对 Go 程序无效（直接走 getdents64），只能靠模块 |
| ETW/WMI 事件 | 内核进程 provider 仍会为目标进程发事件；更彻底需处理 telemetry（另见 edr-bypass-re） |
| 自隐藏副作用 | hide_module=1 时先 show 再 rmmod；ShredExe=true 后二进制无法重启 |
| 审计/取证 | 内存扫描（Volatility pool 扫描、crash dump）仍可定位被摘链的 EPROCESS |
| 影响面 | argv/environ 清零会改变进程对外表现，第三方监控若依赖命令行做告警会失真 |
| 合规 | 只在自有或被授权主机上使用；不要用于他人资产 |

---

## 6. 回滚

1. 控制节点：echo "clear" > /dev/v2hide（或 Go 侧 stealth.Stop()）。
2. echo "show" > /dev/v2hide 恢复模块可见性，再 rmmod v2hide。
3. Windows：NtUnloadDriver（Go 侧 Stop()）或重启；被隐藏进程会在驱动卸载时自动挂回链表。
4. 配置回滚：Stealth.Enable=false 或删除整个 Stealth 段，重启节点即恢复原始行为。
5. 代码回滚：git checkout -- cmd/server.go conf/conf.go && rm -rf common/stealth tools/linux-hide tools/win-hide。

---

## 7. 交付文件清单

| 路径 | 说明 |
| --- | --- |
| common/stealth/stealth.go | 配置与入口编排（Init / Stop / AddHiddenPID） |
| common/stealth/mask_linux.go | prctl comm、argv 清零、environ 清零、exe unlink、/proc 自检 |
| common/stealth/mask_windows.go | PEB CommandLine/ImagePathName 改写、控制台标题 |
| common/stealth/kernel_linux.go | finit_module 加载、/dev/v2hide 控制、delete_module 卸载 |
| common/stealth/kernel_windows.go | 服务键 + NtLoadDriver + IOCTL + 痕迹清理 |
| common/stealth/mask_stub.go | 其他平台空实现 |
| cmd/server.go | 启动链路接入 stealth.Init（config 先 Clone） |
| conf/conf.go | 新增 Stealth 配置段与默认值 |
| tools/linux-hide/v2hide.c + Makefile | Linux 内核隐藏模块（kprobe/kretprobe） |
| tools/linux-hide/libhide.c + Makefile.preload | LD_PRELOAD 对照实现（无需 root） |
| tools/win-hide/v2hide.c + build.bat + v2hide.inf | Windows DKOM 驱动与构建脚本 |
| script/stealth-verify.sh | 隐藏效果自动化验证 |
| example/stealth-config.json | 打开隐藏的配置示例 |
