@echo off
setlocal
cd /d "%~dp0"

set "GOEXE=go"

if "%~1"=="" goto all
if /I "%~1"=="unit" goto unit
if /I "%~1"=="e2e" goto e2e
echo Usage: run_tests.cmd [unit^|e2e]
exit /b 2

:all
call :unit || exit /b 1
call :e2e || exit /b 1
exit /b 0

:unit
"%GOEXE%" test ./...
exit /b %ERRORLEVEL%

:e2e
set "TESTBIN=%TEMP%\bitbang-e2e-%RANDOM%-%RANDOM%.exe"
"%GOEXE%" build -o "%TESTBIN%" .\cmd\bitbang || exit /b 1
set "BITBANG_BIN=%TESTBIN%"
python -m pytest tests\e2e -v --timeout=60 --timeout-method=thread
set "TEST_RESULT=%ERRORLEVEL%"
del /q "%TESTBIN%" >nul 2>&1
exit /b %TEST_RESULT%
