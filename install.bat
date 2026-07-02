@echo off
REM install.bat — Windows CMD 一键安装入口
chcp 65001 >nul

cd /d "%~dp0"

REM 探测架构
if "%PROCESSOR_ARCHITECTURE%"=="ARM64" (
    set "ARCH=arm64"
) else (
    set "ARCH=amd64"
)

set "BINARY=llm-gw-installer-windows-%ARCH%.exe"

if not exist "%BINARY%" (
    set "BINARY=llm-gw-installer.exe"
)

if not exist "%BINARY%" (
    echo ❌ 未找到 installer 二进制（期望 %BINARY%）
    echo 请重新下载 release 包
    exit /b 1
)

"%BINARY%" %*
