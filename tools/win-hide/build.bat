@echo off
rem v2hide.sys 构建脚本（需要 WDK 10/11 + VS Build Tools）
rem 用法：在 "x64 Native Tools Command Prompt for VS" 里执行 build.bat
setlocal

if "%WDKVER%"=="" set WDKVER=10.0.26100.0
if "%WDKROOT%"=="" set WDKROOT=C:\Program Files (x86)\Windows Kits\10
set KM=%WDKROOT%\Include\%WDKVER%\km
set KMLIB=%WDKROOT%\Lib\%WDKVER%\km\x64

if not exist "%KM%\ntifs.h" (
  echo [x] 找不到 WDK 头文件 %KM%\ntifs.h，请设置 WDKVER / WDKROOT
  exit /b 1
)

cl /nologo /c /kernel /GS- /Gz /W4 /WX- /O1 /D_AMD64_ /DAMD64 /DWIN32 /D_WIN64 ^
   /I"%KM%" /I"%KM%\crt" v2hide.c
if errorlevel 1 exit /b 1

link /nologo /DRIVER:WDM /SUBSYSTEM:NATIVE /ENTRY:DriverEntry /MACHINE:X64 ^
     /OPT:REF /OPT:ICF /INCREMENTAL:NO ^
     /LIBPATH:"%KMLIB%" ^
     /OUT:v2hide.sys v2hide.obj ntoskrnl.lib hal.lib ntstrsafe.lib BufferOverflowK.lib
if errorlevel 1 exit /b 1

echo [v] 生成 v2hide.sys
endlocal
