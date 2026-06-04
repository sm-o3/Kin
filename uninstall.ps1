# Kin uninstaller for Windows (PowerShell)
# Sourced from: https://github.com/sm-o3/Kin

$ErrorActionPreference = "Stop"

Write-Host "==============================================" -ForegroundColor Red
Write-Host "          Kin Uninstaller for Windows         " -ForegroundColor Red
Write-Host "==============================================" -ForegroundColor Red

$installDir = Join-Path $HOME "AppData\Local\Programs\Kin"

if (Test-Path $installDir) {
    Write-Host "[*] Removing Kin directory: $installDir..." -ForegroundColor Yellow
    # Make sure tor or kin are not running in background
    Stop-Process -Name "kin" -ErrorAction SilentlyContinue
    Stop-Process -Name "tor" -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $installDir
    Write-Host "[+] Removed Kin installation files." -ForegroundColor Green
} else {
    Write-Host "[!] Kin installation not found in $installDir." -ForegroundColor Yellow
}

# Remove PATH
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -like "*$installDir*") {
    Write-Host "[*] Removing Kin from User PATH..." -ForegroundColor Yellow
    # Filter out Kin path
    $paths = $userPath -split ";" | Where-Object { $_ -ne $installDir -and $_ -ne "" }
    $newUserPath = $paths -join ";"
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    Write-Host "[+] Removed Kin from PATH environment variable." -ForegroundColor Green
}

# Clean data directory
$confirm = Read-Host "Do you want to delete all Kin data (database, private keys, media in ~/.kin)? [y/N]"
if ($confirm -eq "y" -or $confirm -eq "Y") {
    $dataDir = Join-Path $HOME ".kin"
    if (Test-Path $dataDir) {
        Write-Host "[*] Removing Kin data directory: $dataDir..." -ForegroundColor Yellow
        Remove-Item -Recurse -Force $dataDir
        Write-Host "[+] Kin data deleted." -ForegroundColor Green
    }
} else {
    Write-Host "[i] Kin data directory kept at $HOME\.kin."
}

Write-Host ""
Write-Host "==============================================" -ForegroundColor Green
Write-Host "  🎉 Kin has been successfully uninstalled!" -ForegroundColor Green
Write-Host "==============================================" -ForegroundColor Green
