# Kin single-command installer for Windows (PowerShell)
# Sourced from: https://github.com/sm-o3/Kin

$ErrorActionPreference = "Stop"

Write-Host "==============================================" -ForegroundColor Blue
Write-Host "          Kin Installer for Windows           " -ForegroundColor Blue
Write-Host "==============================================" -ForegroundColor Blue

$installDir = Join-Path $HOME "AppData\Local\Programs\Kin"

# --- Helper Functions ---

function Download-File {
    param(
        [string]$Url,
        [string]$OutputPath
    )
    
    # Try WebClient first as it is fast and reliable
    try {
        $webClient = New-Object System.Net.WebClient
        # Set a modern user-agent so GitHub doesn't block the request
        $webClient.Headers.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
        $webClient.DownloadFile($Url, $OutputPath)
        if (Test-Path $OutputPath) {
            $fileSize = (Get-Item $OutputPath).Length
            if ($fileSize -gt 1MB) {
                return $true
            }
        }
    } catch {
        Write-Host "[!] WebClient download method failed. Trying alternative..." -ForegroundColor Yellow
    }
    
    # Try Invoke-WebRequest with progress disabled (to avoid truncation/slowness)
    try {
        $oldProgress = $ProgressPreference
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri $Url -OutFile $OutputPath -UseBasicParsing -UserAgent "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36" -Verbose:$false -ErrorAction Stop
        $ProgressPreference = $oldProgress
        if (Test-Path $OutputPath) {
            return $true
        }
    } catch {
        Write-Host "[!] Invoke-WebRequest download method failed: $_" -ForegroundColor Yellow
        $ProgressPreference = $oldProgress
    }
    
    return $false
}

function Install-Tor {
    param(
        [string]$InstallDir,
        [string]$TempDir
    )
    
    if (!(Get-Command tor -ErrorAction SilentlyContinue) -and !(Test-Path (Join-Path $InstallDir "tor.exe"))) {
        Write-Host "[*] tor.exe not found in PATH or local directory. Downloading Tor Expert Bundle..." -ForegroundColor Yellow
        $torUrl = "https://archive.torproject.org/tor-package-archive/torbrowser/13.5.1/tor-expert-bundle-windows-x86_64-13.5.1.tar.gz"
        $torArchive = Join-Path $TempDir "tor.tar.gz"
        
        $downloaded = Download-File -Url $torUrl -OutputPath $torArchive
        if ($downloaded) {
            try {
                # Extract using native tar tool on Windows
                Write-Host "[*] Extracting Tor binary..."
                Start-Process tar -ArgumentList "-xzf `"$torArchive`" -C `"$TempDir`"" -NoNewWindow -Wait
                
                # Find tor.exe and copy all files in its directory (including DLLs and geoip data) to InstallDir
                $extractedTor = Get-ChildItem -Path $TempDir -Filter "tor.exe" -Recurse | Select-Object -First 1
                if ($extractedTor) {
                    $torFolder = $extractedTor.Directory.FullName
                    Write-Host "[*] Copying Tor files and libraries..."
                    Copy-Item (Join-Path $torFolder "*") $InstallDir -Force -Recurse
                    Write-Host "[+] Installed Tor binaries and dependencies into Kin program directory." -ForegroundColor Green
                } else {
                    Write-Host "[!] Warning: tor.exe not found in extracted archive. You may need to install Tor manually." -ForegroundColor Red
                }
            } catch {
                Write-Host "[!] Warning: Failed to extract Tor: $_" -ForegroundColor Red
                Write-Host "You may need to install Tor manually and add it to your PATH." -ForegroundColor Red
            }
        } else {
            Write-Host "[!] Warning: Failed to download Tor. You may need to install Tor manually." -ForegroundColor Red
        }
    }
}

function Add-ToPath {
    param(
        [string]$InstallDir
    )
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$InstallDir*") {
        $newUserPath = $userPath
        if (!$newUserPath.EndsWith(";")) {
            $newUserPath += ";"
        }
        $newUserPath += $InstallDir
        [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
        Write-Host "[+] Registered Kin installation directory in User PATH environment variable." -ForegroundColor Green
    }
}

# --- Precompiled Release Download Attempt ---
$downloadSuccess = $false
$isAmd64 = ($env:PROCESSOR_ARCHITECTURE -eq "AMD64") -or ($env:PROCESSOR_ARCHITEW6432 -eq "AMD64")

if ($isAmd64) {
    $tag = "v1.0.0"
    try {
        $apiResponse = Invoke-RestMethod -Uri "https://api.github.com/repos/sm-o3/Kin/releases/latest" -UseBasicParsing -ErrorAction Stop
        if ($apiResponse -and $apiResponse.tag_name) {
            $tag = $apiResponse.tag_name
        }
    } catch {
        # Fallback to hardcoded tag if offline or API fails
    }
    $assetName = "kin-windows-amd64.zip"
    $url = "https://github.com/sm-o3/Kin/releases/download/$tag/$assetName"
    
    $tempDir = Join-Path $env:TEMP ("kin-download-" + (New-Guid).ToString().Substring(0,8))
    New-Item -ItemType Directory -Path $tempDir -Force | Out-Null
    $zipPath = Join-Path $tempDir $assetName
    
    Write-Host "[*] Attempting to download precompiled binary: $assetName..." -ForegroundColor Green
    $downloadSuccess = Download-File -Url $url -OutputPath $zipPath
    
    if ($downloadSuccess) {
        Write-Host "[+] Download successful! Extracting binary..." -ForegroundColor Green
        try {
            Expand-Archive -Path $zipPath -DestinationPath $tempDir -Force
            
            # Find the binary inside the extracted files
            $extractedExe = Get-ChildItem -Path $tempDir -Filter "kin*.exe" -Recurse | Select-Object -First 1
            if ($extractedExe) {
                Write-Host "[*] Installing binary to: $installDir"
                New-Item -ItemType Directory -Path $installDir -Force | Out-Null
                Copy-Item $extractedExe.FullName (Join-Path $installDir "kin.exe") -Force
                
                # Check / install Tor
                Install-Tor -InstallDir $installDir -TempDir $tempDir
                
                # Add to User PATH
                Add-ToPath -InstallDir $installDir
                
                # Cleanup
                Remove-Item -Recurse -Force $tempDir -ErrorAction SilentlyContinue
                
                Write-Host ""
                Write-Host "==============================================" -ForegroundColor Green
                Write-Host "  🎉 Kin installed successfully!" -ForegroundColor Green
                Write-Host "==============================================" -ForegroundColor Green
                Write-Host "[+] Binary path: $installDir\kin.exe"
                Write-Host ""
                Write-Host "NOTE: Please open a NEW terminal window/tab to start using 'kin'."
                Write-Host "Run 'kin' to start the application and access the Web UI at http://127.0.0.1:8080"
                Write-Host ""
                return
            } else {
                Write-Host "[!] Error: kin.exe not found in downloaded archive. Falling back to build from source." -ForegroundColor Yellow
            }
        } catch {
            Write-Host "[!] Extraction or installation of precompiled binary failed: $_" -ForegroundColor Yellow
            Write-Host "Falling back to build from source." -ForegroundColor Yellow
        }
        Remove-Item -Recurse -Force $tempDir -ErrorAction SilentlyContinue
    } else {
        Write-Host "[!] Pre-compiled binary download failed. Falling back to build from source." -ForegroundColor Yellow
        Remove-Item -Recurse -Force $tempDir -ErrorAction SilentlyContinue
    }
} else {
    Write-Host "[*] Unsupported architecture for precompiled binary. Falling back to build from source." -ForegroundColor Yellow
}

# --- Source Build Fallback (Requires Go and Git) ---
Write-Host "[*] Checking source build dependencies..."

$needsRestart = $false

# Check Git
if (!(Get-Command git -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Git not found. Installing via winget..." -ForegroundColor Yellow
    try {
        Start-Process winget -ArgumentList "install --id Git.Git -e --source winget --silent" -NoNewWindow -Wait
        $needsRestart = $true
    } catch {
        Write-Host "[!] Winget Git installation encountered an issue: $_" -ForegroundColor Yellow
    }
}

# Check Go
if (!(Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Go (Golang) not found. Installing via winget..." -ForegroundColor Yellow
    try {
        Start-Process winget -ArgumentList "install --id GoLang.Go -e --source winget --silent" -NoNewWindow -Wait
        $needsRestart = $true
    } catch {
        Write-Host "[!] Winget Go installation encountered an issue: $_" -ForegroundColor Yellow
    }
}

if ($needsRestart) {
    Write-Host "[!] Windows package installations completed." -ForegroundColor Green
    Write-Host "[!] IMPORTANT: Please close this PowerShell window and open a NEW one to run this script again so that the newly installed Git/Go compiler tools are active." -ForegroundColor Red
    return
}

# Double check dependencies are working
if (!(Get-Command git -ErrorAction SilentlyContinue) -or !(Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "[!] Error: Dependencies (Git/Go) could not be resolved automatically. Please install Git and Go manually retrying." -ForegroundColor Red
    return
}

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
    return
}

Write-Host "[*] Installing binary to: $installDir"
New-Item -ItemType Directory -Path $installDir -Force | Out-Null
Copy-Item "kin.exe" (Join-Path $installDir "kin.exe") -Force

# Check / install Tor
Install-Tor -InstallDir $installDir -TempDir $tempDir

# Add to User PATH
Add-ToPath -InstallDir $installDir

# Cleanup
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
