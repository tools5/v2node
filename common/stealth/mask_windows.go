//go:build windows

package stealth

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntdll = windows.NewLazySystemDLL("ntdll.dll")
var kernel32 = windows.NewLazySystemDLL("kernel32.dll")

// RTL_USER_PROCESS_PARAMETERS (x64) 关键字段偏移
const (
	pebProcessParameters = 0x20 // PEB->ProcessParameters
	uppImagePathName     = 0x60 // UNICODE_STRING ImagePathName
	uppCommandLine       = 0x70 // UNICODE_STRING CommandLine
	processBasicInfo     = 0
)

type processBasicInformation struct {
	ExitStatus                   uintptr
	PebBaseAddress               unsafe.Pointer
	AffinityMask                 uintptr
	BasePriority                 int32
	reserved                     uint32
	UniqueProcessID              uintptr
	InheritedFromUniqueProcessID uintptr
}

func maskUser(c Config) error {
	var errs []string
	if err := maskPeb(c.Alias); err != nil {
		errs = append(errs, "peb:"+err.Error())
	}
	if err := maskConsoleTitle(c.Alias); err != nil {
		errs = append(errs, "title:"+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// maskPeb 改写本进程 PEB 里的 CommandLine 与 ImagePathName。
// 任务管理器“命令行”列、WMI Win32_Process.CommandLine、wmic process 都读这里。
func maskPeb(alias string) error {
	handle := windows.CurrentProcess()
	info := processBasicInformation{}
	var ret uint32
	r, _, e := ntdll.NewProc("NtQueryInformationProcess").Call(uintptr(handle), processBasicInfo,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&ret)))
	if r != 0 {
		return fmt.Errorf("NtQueryInformationProcess=0x%x %v", r, e)
	}
	if info.PebBaseAddress == nil {
		return fmt.Errorf("PEB 为空")
	}
	params := readPtr(unsafe.Add(info.PebBaseAddress, pebProcessParameters))
	if params == nil {
		return fmt.Errorf("ProcessParameters 为空")
	}

	fake := strings.TrimSuffix(alias, ".exe") + ".exe"
	if fake == ".exe" {
		fake = "svchost.exe"
	}
	fakePath := "C:\\Windows\\System32\\" + fake
	if err := rewriteUnicodeString(unsafe.Add(params, uppCommandLine), fakePath); err != nil {
		return fmt.Errorf("CommandLine: %w", err)
	}
	if err := rewriteUnicodeString(unsafe.Add(params, uppImagePathName), fakePath); err != nil {
		return fmt.Errorf("ImagePathName: %w", err)
	}
	return nil
}

func maskConsoleTitle(alias string) error {
	p, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		return err
	}
	_, _, _ = kernel32.NewProc("SetConsoleTitleW").Call(uintptr(unsafe.Pointer(p)))
	return nil
}

// rewriteUnicodeString 用不超长的假值覆盖 UNICODE_STRING 的 Buffer，剩余部分补空格。
func rewriteUnicodeString(dst unsafe.Pointer, val string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recover: %v", r)
		}
	}()
	maxLen := readU16(unsafe.Add(dst, 2))
	buf := readPtr(unsafe.Add(dst, 8))
	if buf == nil || maxLen < 2 {
		return fmt.Errorf("Buffer 无效")
	}
	newBuf, err := windows.UTF16FromString(val)
	if err != nil {
		return err
	}
	capacity := int(maxLen / 2)
	if len(newBuf) > capacity {
		newBuf = newBuf[:capacity]
		newBuf[capacity-1] = 0
	}
	dstSlice := unsafe.Slice((*uint16)(buf), capacity)
	for i := range dstSlice {
		dstSlice[i] = 0x20 // 空格填充，抹掉原命令行残留
	}
	copy(dstSlice, newBuf)
	*(*uint16)(dst) = uint16(len(newBuf) * 2)
	return nil
}

func readPtr(p unsafe.Pointer) unsafe.Pointer { return *(*unsafe.Pointer)(p) }
func readU16(p unsafe.Pointer) uint16         { return *(*uint16)(p) }

// VerifyPlatform 在 Windows 上回读 PEB，确认命令行已被替换。
func VerifyPlatform() []string {
	return []string{
		fmt.Sprintf("pid -> %d", os.Getpid()),
		fmt.Sprintf("PEB CommandLine -> %q", selfCommandLine()),
	}
}

func selfCommandLine() string {
	handle := windows.CurrentProcess()
	info := processBasicInformation{}
	var ret uint32
	r, _, _ := ntdll.NewProc("NtQueryInformationProcess").Call(uintptr(handle), processBasicInfo,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&ret)))
	if r != 0 || info.PebBaseAddress == nil {
		return ""
	}
	params := readPtr(unsafe.Add(info.PebBaseAddress, pebProcessParameters))
	if params == nil {
		return ""
	}
	l := int(readU16(unsafe.Add(params, uppCommandLine)))
	buf := readPtr(unsafe.Add(params, uppCommandLine+8))
	if buf == nil || l <= 0 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(buf), l/2))
}
