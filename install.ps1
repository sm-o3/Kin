# Kin single-command installer for Windows (PowerShell)
# Sourced from: https://github.com/sm-o3/Kin

$ErrorActionPreference = "Stop"

Write-Host "==============================================" -ForegroundColor Blue
Write-Host "          Kin Installer for Windows           " -ForegroundColor Blue
Write-Host "==============================================" -ForegroundColor Blue

# 1. Dependency Checks & Setup
Write-Host "[*] Checking dependencies..."

$needsRestart = $false

# Check Git
if (!(Get-Command git -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Git not found. Installing via winget..." -ForegroundColor Yellow
    Start-Process winget -ArgumentList "install --id Git.Git -e --source winget --silent" -NoNewWindow -Wait
    $needsRestart = $true
}

# Check Go
if (!(Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Go (Golang) not found. Installing via winget..." -ForegroundColor Yellow
    Start-Process winget -ArgumentList "install --id GoLang.Go -e --source winget --silent" -NoNewWindow -Wait
    $needsRestart = $true
}

if ($needsRestart) {
    Write-Host "[!] Windows package installations completed." -ForegroundColor Green
    Write-Host "[!] IMPORTANT: Please close this PowerShell window and open a NEW one to run this script again so that the newly installed Git/Go compiler tools are active." -ForegroundColor Red
    exit
}

# Double check dependencies are working
if (!(Get-Command git -ErrorAction SilentlyContinue) -or !(Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[!] Error: Dependencies (Git/Go) could not be resolved automatically. Please install Git and Go manually before retrying." -ForegroundColor Red
    exit
}

# 2. Clone and Build
$tempDir = Join-Path $env:TEMP ("kin-build-" + (New-Guid).ToString().Substring(0,8))
Write-Host "[*] Creating temporary build directory: $tempDir"
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

Write-Host "[*] Cloning Kin repository..."
Start-Process git -ArgumentList "clone https://github.com/sm-o3/Kin.git `"$tempDir`"" -NoNewWindow -Wait

$originalLocation = Get-Location
Set-Location $tempDir

Write-Host "[*] Compiling Kin from source..."
Start-Process go -ArgumentList "build -mod=vendor -o kin.exe ." -NoNewWindow -Wait

if (!(Test-Path "kin.exe")) {
    Write-Host "[!] Error: Compilation failed." -ForegroundColor Red
    Set-Location $originalLocation
    Remove-Item -Recurse -Force $tempDir
    exit
}

# 3. Install Binary
$installDir = Join-Path $HOME "AppData\Local\Programs\Kin"
Write-Host "[*] Installing binary to: $installDir"
New-Item -ItemType Directory -Path $installDir -Force | Out-Null
Copy-Item "kin.exe" (Join-Path $installDir "kin.exe") -Force

# 3.1 Install Tor on Windows
if (!(Get-Command tor -ErrorAction SilentlyContinue) -and !(Test-Path (Join-Path $installDir "tor.exe"))) {
    Write-Host "[*] tor.exe not found in PATH or local directory. Downloading Tor Expert Bundle..." -ForegroundColor Yellow
    $torUrl = "https://archive.torproject.org/tor-package-archive/torbrowser/13.5.1/tor-expert-bundle-windows-x86_64-13.5.1.tar.gz"
    $torArchive = Join-Path $tempDir "tor.tar.gz"
    
    # Download
    Invoke-WebRequest -Uri $torUrl -OutFile $torArchive -Verbose:$false
    
    # Extract using native tar tool on Windows
    Write-Host "[*] Extracting Tor binary..."
    Start-Process tar -ArgumentList "-xzf `"$torArchive`" -C `"$tempDir`"" -NoNewWindow -Wait
    
    # Find tor.exe and copy all files in its directory (including DLLs and geoip data) to installDir
    $extractedTor = Get-ChildItem -Path $tempDir -Filter "tor.exe" -Recurse | Select-Object -First 1
    if ($extractedTor) {
        $torFolder = $extractedTor.Directory.FullName
        Write-Host "[*] Copying Tor files and libraries..."
        Copy-Item (Join-Path $torFolder "*") $installDir -Force -Recurse
        Write-Host "[+] Installed Tor binaries and dependencies into Kin program directory." -ForegroundColor Green
    } else {
        Write-Host "[!] Warning: tor.exe not found in extracted archive. You may need to install Tor manually." -ForegroundColor Red
    }
}

# Add to User PATH
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    $newUserPath = $userPath
    if (!$newUserPath.EndsWith(";")) {
        $newUserPath += ";"
    }
    $newUserPath += $installDir
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    Write-Host "[+] Registered Kin installation directory in User PATH environment variable." -ForegroundColor Green
}

# 4. Cleanup
Set-Location $originalLocation
Write-Host "[*] Cleaning up temporary files..."
Remove-Item -Recurse -Force $tempDir

Write-Host ""
Write-Host "==============================================" -ForegroundColor Green
Write-Host "  🎉 Kin installed successfully!" -ForegroundColor Green
Write-Host "==============================================" -ForegroundColor Green
Write-Host "[+] Binary path: $installDir\kin.exe"
Write-Host ""
Write-Host "NOTE: Please open a NEW terminal window/tab to start using 'kin'."
Write-Host "Run 'kin' to start the application and access the Web UI at http://127.0.0.1:8080"
Write-Host ""
