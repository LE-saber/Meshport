@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Open-Meshport.ps1" -Stop %*
if errorlevel 1 pause
