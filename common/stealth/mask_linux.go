//go:build linux

package stealth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// comm 长度上限：TASK_COMM_LEN(16) - 1
const commMax = 15

// 包初始化阶段就改主线程名：此刻仍在 m0（进程主线程）上，
// 而 ps 读的是线程组 leader 的 comm，只有 leader 自己写 comm 才生效。
func init() {
	n := defaultAlias()
	if len(n) > commMax {
		n = n[:commMax]
	}
	if p, err := unix.BytePtrFromString(n); err == nil {
		_ = unix.Prctl(unix.PR_SET_NAME, uintptr(unsafe.Pointer(p)), 0, 0, 0)
	}
}

func maskUser(c Config) error {
	var errs []string
	if err := setComm(c.Alias); err != nil {
		errs = append(errs, "comm:"+err.Error())
	}
	if err := maskArgs(c.Alias); err != nil {
		errs = append(errs, "argv:"+err.Error())
	}
	if c.HideEnv {
		// 放在 argv 伪装之前：re-exec 成功后新进程会带着 alias 作为 argv[0]
		// 重新走一遍本函数，此时 environ 已由内核置空，不必再处理。
		if err := maskEnviron(c.Alias); err != nil {
			errs = append(errs, "environ:"+err.Error())
		}
	}
	if c.ShredExe {
		if err := shredSelfExe(); err != nil {
			errs = append(errs, "exe:"+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func setComm(alias string) error {
	n := alias
	if len(n) > commMax {
		n = n[:commMax]
	}
	p, err := unix.BytePtrFromString(n)
	if err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NAME, uintptr(unsafe.Pointer(p)), 0, 0, 0); err != nil {
		return err
	}
	// 同一线程再写一次 comm
	_ = os.WriteFile("/proc/thread-self/comm", []byte(n), 0)
	return nil
}

// maskArgs 把 argv 所在栈内存清零后写入别名。Go 的 os.Args 字符串直接别名原始
// argv 内存，因此拿到第一个和最后一个字符串的数据指针即可定出整个区间。
func maskArgs(alias string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recover: %v", r)
		}
	}()
	if len(os.Args) == 0 {
		return errors.New("os.Args 为空")
	}
	first := unsafe.StringData(os.Args[0])
	if first == nil {
		return errors.New("argv 内存不可寻址")
	}
	// re-exec 之后 os.Args 只有一个元素（argv[0] 已是 alias）：
	// 只清它自己，避免区间判断把 end<=start 当成异常报出来
	if len(os.Args) == 1 {
		buf := unsafe.Slice(first, len(os.Args[0]))
		for i := range buf {
			buf[i] = 0
		}
		copy(buf, alias)
		os.Args = []string{alias}
		return nil
	}
	last := unsafe.StringData(os.Args[len(os.Args)-1])
	if last == nil {
		return errors.New("argv 内存不可寻址")
	}
	start := uintptr(unsafe.Pointer(first))
	end := uintptr(unsafe.Pointer(last))
	tail := uintptr(len(os.Args[len(os.Args)-1])) + 1
	if end <= start || end+tail-start > 1<<20 {
		return errors.New("argv 区间异常")
	}
	region := unsafe.Slice(first, int(end+tail-start))
	for i := range region {
		region[i] = 0
	}
	copy(region, alias)
	os.Args = []string{alias}
	return nil
}

// maskEnviron 让 /proc/<pid>/environ 为空。
//
// 不能靠直接清零内存来实现：Go 1.27 把环境变量放在 env map 里，os.Environ()
// 返回的 "k=v" 是遍历 map 时新建的字符串——顺序随机、堆上分散。拿首尾两个指针
// 做区间清零，中间必然夹着无关的堆对象（env map 的桶、header 等），把它们一并
// 抹掉之后，任何 os.Getenv 都会踩进被破坏的 map：
//
//	os.Getenv → syscall.Getenv → runtime.mapaccess2_faststr → SIGSEGV
//	unexpected fault address 0x...   (pc 落在 runtime.(*itabTableType).find)
//	→ exit status=2，systemd 表现为「启动失败」并无限重启
//
// 正确做法是让内核在 exec 时用空的环境变量表重建进程镜像：exec 自身一次，
// 新进程的 environ 天然为空，/proc/<pid>/environ 读出来就是 0 字节，且没有
// 任何内存被破坏。成功时本函数不返回。
func maskEnviron(alias string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recover: %v", r)
		}
	}()
	// 已 re-exec 过：argv[0] 由本次 exec 换成了 alias。
	// 同时看 environ：正常情况内核给的就是空环境。
	// 两个判据一起用，避免某个包的 init 在 exec 后又 Setenv 造成反复 re-exec。
	if len(os.Args) > 0 && os.Args[0] == alias {
		return nil
	}
	if len(os.Environ()) == 0 {
		return nil
	}
	if len(os.Args) == 0 {
		return errors.New("os.Args 为空")
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		return fmt.Errorf("定位自身可执行文件失败: %v", err)
	}
	argv := make([]string, 0, len(os.Args))
	argv = append(argv, alias)
	argv = append(argv, os.Args[1:]...)
	// envv 传 nil：内核以空环境启动新镜像。
	// exec 成功则不返回；失败保留当前进程，只放弃环境隐藏，不影响启动。
	if err := unix.Exec(self, argv, nil); err != nil {
		return fmt.Errorf("re-exec 失败: %w", err)
	}
	return nil
}

// shredSelfExe 把真实可执行文件从磁盘删除（inode 仍被运行中的进程持有）。
// 部署脚本请先把二进制 link 成目标名再启动，否则无法再重启。
func shredSelfExe() error {
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return err
	}
	if strings.HasSuffix(exe, " (deleted)") {
		return nil
	}
	if err := os.Remove(exe); err != nil {
		return fmt.Errorf("remove %s: %w", exe, err)
	}
	return nil
}

// VerifyPlatform 读取 /proc 自检，供 Debug 日志与验证脚本使用。
func VerifyPlatform() []string {
	out := []string{}
	pid := os.Getpid()
	for _, p := range []string{"/proc/self/comm", "/proc/self/cmdline", "/proc/self/environ", "/proc/self/exe"} {
		b, err := os.ReadFile(p)
		if err != nil {
			out = append(out, fmt.Sprintf("%s -> 读取失败: %v", p, err))
			continue
		}
		out = append(out, fmt.Sprintf("%s -> %q", p, strings.Join(strings.Split(string(b), string(rune(0))), " ")))
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(b)
		if i := strings.IndexByte(s, 0x29); i > 0 && i+2 < len(s) {
			out = append(out, fmt.Sprintf("/proc/%d/stat comm -> %s", pid, strings.TrimSpace(s[i+2:])))
		}
	}
	found := false
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			if e.Name() == fmt.Sprint(pid) {
				found = true
				break
			}
		}
	}
	out = append(out, fmt.Sprintf("/proc 目录项里能否看到自身 pid(模块默认对自身可见, 对其他进程不可见): %v", found))
	return out
}
