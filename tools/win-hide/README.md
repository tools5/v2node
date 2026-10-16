# v2hide.sys — Windows x64 进程隐藏驱动

把目标进程从 **PsActiveProcessHead 活动进程链** 摘掉（DKOM）。因为 Task Manager、
tasklist、psapi、WMI(Win32_Process)、CreateToolhelp32Snapshot 都枚举这条链，
摘掉之后它们在进程列表里都看不到该进程。

## 为什么用户态做不到

| 手段 | 效果 |
| --- | --- |
| 改 PEB 里的 CommandLine / ImagePathName | 命令行列变假（任务管理器命令行列、WMI 都读 PEB），但进程仍出现在列表 |
| 改进程名 / 伪装成 svchost | 只能迷惑人眼，PID/句柄/服务链一览仍在 |
| 想 hook NtQuerySystemInformation | 只能 hook 自己进程；要挡别人的枚举必须注入每个监控进程，等于和 EDR 正面对抗 |
| 内核驱动 DKOM | 进程真的不在链上，所有基于该链的枚举全部失效（本方案） |

## 构建

~~~
:: 在 "x64 Native Tools Command Prompt for VS" 里：
build.bat
:: 需要 WDK（Windows Driver Kit 10/11）。产出 v2hide.sys
~~~

## 加载

驱动加载后立即读服务键 Parameters 下的参数：DevName / HidePid / HideDriver。
Go 侧（common/stealth/kernel_windows.go）已经自动做完下面所有步骤：

~~~
1. 复制 v2hide.sys 到 C:\Windows\System32\drivers\<随机名>.sys
2. 建服务键 HKLM\SYSTEM\CurrentControlSet\Services\<随机名>
     ImagePath=\??\<路径>  Type=1  Start=3
     Parameters\DevName=<随机设备名>  Parameters\HidePid=<自身 pid>  Parameters\HideDriver=1
3. 提权 SeLoadDriverPrivilege -> NtLoadDriver(\Registry\Machine\System\CurrentControlSet\Services\<随机名>)
4. 打开 \\.\<设备名>，IOCTL_V2HIDE_ADD 下发要隐藏的 pid
5. 删除服务键 + 删除磁盘 .sys（驱动已驻留，注册表和文件都不留痕）
~~~

手动验证：

~~~
sc create v2hide type= kernel binPath= C:\path\v2hide.sys
:: 或使用 IOCTL_V2HIDE_ADD(0x222004) / DEL(0x222008) / STATUS(0x22200C) 控制
~~~

## 签名与加载限制

| 环境 | 做法 |
| --- | --- |
| 测试机 + 测试签名 | bcdedit /set testsigning on 重启，然后用自签证书签 v2hide.sys |
| 生产机（DSE 强制） | 用 kdmapper 之类的"手动映射"（BYOVD）把驱动直接映射进内核：不建服务、不落盘、不进 PsLoadedModuleList，隐蔽性最好 |
| HVCI / VBS 开启 | 内核 .text 不可写、部分内核结构受 KDP 保护，DKOM 可能直接 BSOD；先关 HVCI 或改用支持 Hyper-V 的隐蔽方案 |

手动映射（kdmapper 思路）要点：
- 驱动里不要留 DriverEntry 之外的导入依赖，映射器负责修导入表；
- 不调用 IoCreateDevice 也可以：纯 DKOM，用 PsLookupProcessByProcessId 定位 pid，
  再由映射器自带 CLI 下发，全程没有设备对象和符号链接；
- 映射后驱动不在已加载模块列表里，DriverUnload 不会被调用，恢复只能重启。

## 验证

~~~
tasklist | findstr v2nb        :: 空
powershell "Get-Process | Where-Object {$_.Id -eq <PID>}"   :: 查不到
wmic process where processid=<PID> get name                 :: 查不到
driverquery | findstr v2hide   :: 空（HideDriver=1）
~~~

## 注意

- 摘链后进程仍然正常工作：句柄表、CID table（PsLookupProcessByProcessId）不受影响，
  所以自身 IO 与子进程创建照常。
- 退出安全：摘链后把 ActiveProcessLinks 置成自环，进程退出时 RemoveEntryList 是 no-op，
  不会触发 LIST_ENTRY 校验崩溃。
- ETW 内核 provider（Microsoft-Windows-Kernel-Process）仍会为目标进程发事件；
  要连 ETW 一起消掉属于更高一层的对抗（见 edr-bypass-re 的 telemetry-blinding）。
- 只在自己拥有或被授权的主机上使用。
