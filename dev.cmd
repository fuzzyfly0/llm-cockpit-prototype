@echo off
rem Startet dev.ps1 ohne die PowerShell-Ausfuehrungsrichtlinie zu aendern.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0dev.ps1"
