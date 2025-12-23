<#
.SYNOPSIS
    llm-mux Windows Installer
    Installs llm-mux binary and sets up a background service.

.DESCRIPTION
    1. Downloads the specified or latest release from GitHub.
    2. Verifies checksum integrity.
    3. Installs to $env:LOCALAPPDATA\Programs\llm-mux.
    4. Adds the install directory to the User PATH.
    5. Initializes default configuration.
    6. Creates a Scheduled Task to run llm-mux at logon with auto-restart.

.PARAMETER Version
    Specific version to install (e.g., "v1.0.0"). Default: latest.

.PARAMETER InstallPath
    Custom installation directory. Default: $env:LOCALAPPDATA\Programs\llm-mux

.PARAMETER NoService
    Skip scheduled task setup (install binary only).

.PARAMETER NoVerify
    Skip checksum verification.

.PARAMETER Force
    Force reinstall even if same version exists.

.EXAMPLE
    # Default install (latest version)
    irm https://raw.githubusercontent.com/nghyane/llm-mux/main/install.ps1 | iex

    # Install specific version
    & { $Version = "v1.0.0"; irm .../install.ps1 | iex }

    # Binary only, no service
    & { $NoService = $true; irm .../install.ps1 | iex }
#>

param(
    [string]$Version = "",
    [string]$InstallPath = "",
    [switch]$NoService = $false,
    [switch]$NoVerify = $false,
    [switch]$Force = $false
)

# --- Configuration ---
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Faster downloads

$Repo = "nghyane/llm-mux"
$AppName = "llm-mux"
$TaskName = "llm-mux Background Service"

# Set TLS 1.2 for older PowerShell versions
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# --- Helper Functions ---

function Write-Log {
    param([string]$Message, [string]$Color = "White")
    Write-Host "==> " -ForegroundColor Cyan -NoNewline
    Write-Host $Message -ForegroundColor $Color
}

function Write-Info {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor Gray
}

function Write-Warn {
    param([string]$Message)
    Write-Host "warning: " -ForegroundColor Yellow -NoNewline
    Write-Host $Message
}

function Write-Err {
    param([string]$Message)
    Write-Host "error: " -ForegroundColor Red -NoNewline
    Write-Host $Message
    exit 1
}

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# --- Architecture Detection ---

function Get-Architecture {
    Write-Log "Detecting system architecture..."

    $arch = $env:PROCESSOR_ARCHITECTURE
    switch ($arch) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        "x86"   { return "386" }
        default { Write-Err "Unsupported architecture: $arch. Supported: AMD64, ARM64, x86" }
    }
}

# --- Version Management ---

function Get-LatestVersion {
    if ($script:Version) {
        Write-Info "Using specified version: $script:Version"
        return $script:Version
    }

    Write-Log "Fetching latest release info..."

    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -TimeoutSec 30
        $tagName = $release.tag_name

        if (-not $tagName) {
            Write-Err "Failed to parse version from GitHub API response."
        }

        Write-Info "Latest version: $tagName"
        return $tagName
    }
    catch {
        Write-Err "Failed to fetch latest release: $($_.Exception.Message)`nCheck your internet connection or specify -Version parameter."
    }
}

# --- Checksum Verification ---

function Get-FileHash256 {
    param([string]$FilePath)

    $hash = Get-FileHash -Path $FilePath -Algorithm SHA256
    return $hash.Hash.ToLower()
}

function Test-Checksum {
    param(
        [string]$FilePath,
        [string]$ChecksumsPath,
        [string]$FileName
    )

    if ($script:NoVerify) {
        Write-Warn "Checksum verification skipped (-NoVerify)"
        return $true
    }

    if (-not (Test-Path $ChecksumsPath)) {
        Write-Warn "Checksums file not available. Skipping verification."
        return $true
    }

    $checksumContent = Get-Content $ChecksumsPath -Raw
    $pattern = "([a-fA-F0-9]{64})\s+\*?$([regex]::Escape($FileName))"

    if ($checksumContent -match $pattern) {
        $expected = $matches[1].ToLower()
        $actual = Get-FileHash256 -FilePath $FilePath

        if ($expected -ne $actual) {
            Write-Host ""
            Write-Err "Checksum verification FAILED!`n  Expected: $expected`n  Actual:   $actual`n`nThe downloaded file may be corrupted or tampered with."
        }

        Write-Info "Checksum verified"
        return $true
    }
    else {
        Write-Warn "No checksum found for $FileName in checksums file."
        return $true
    }
}

# --- Download & Install ---

function Install-Binary {
    param(
        [string]$TagName,
        [string]$Arch,
        [string]$TargetDir
    )

    $versionNum = $TagName.TrimStart('v')
    $zipName = "llm-mux_${versionNum}_windows_${Arch}.zip"
    $downloadUrl = "https://github.com/$Repo/releases/download/$TagName/$zipName"
    $checksumsUrl = "https://github.com/$Repo/releases/download/$TagName/checksums.txt"

    $tempDir = Join-Path $env:TEMP "llm-mux-install-$(Get-Random)"
    $tempZip = Join-Path $tempDir $zipName
    $tempChecksums = Join-Path $tempDir "checksums.txt"

    try {
        # Create temp directory
        New-Item -ItemType Directory -Force -Path $tempDir | Out-Null

        # Download archive
        Write-Log "Downloading $zipName..."
        Write-Info "URL: $downloadUrl"

        try {
            Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -TimeoutSec 120
        }
        catch {
            Write-Err "Failed to download: $($_.Exception.Message)`nThe version or platform may not be available."
        }

        # Download checksums (optional)
        Write-Log "Downloading checksums..."
        try {
            Invoke-WebRequest -Uri $checksumsUrl -OutFile $tempChecksums -TimeoutSec 30
        }
        catch {
            Write-Warn "Checksums file not available for this release."
        }

        # Verify checksum
        Test-Checksum -FilePath $tempZip -ChecksumsPath $tempChecksums -FileName $zipName

        # Create install directory
        if (-not (Test-Path $TargetDir)) {
            Write-Log "Creating install directory..."
            New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
        }

        # Backup existing binary
        $existingBinary = Join-Path $TargetDir "$AppName.exe"
        if (Test-Path $existingBinary) {
            Write-Info "Backing up existing binary..."
            $backupPath = "$existingBinary.bak"
            Move-Item -Path $existingBinary -Destination $backupPath -Force -ErrorAction SilentlyContinue
        }

        # Extract archive
        Write-Log "Extracting to $TargetDir..."
        $extractDir = Join-Path $tempDir "extracted"
        Expand-Archive -Path $tempZip -DestinationPath $extractDir -Force

        # Find the binary (handle nested directories)
        $exeFile = Get-ChildItem -Path $extractDir -Filter "$AppName.exe" -Recurse | Select-Object -First 1

        if (-not $exeFile) {
            Write-Err "Could not find $AppName.exe in the downloaded archive."
        }

        # Move binary to install directory
        Move-Item -Path $exeFile.FullName -Destination $TargetDir -Force

        # Also copy any additional files (README, LICENSE, etc.)
        $additionalFiles = Get-ChildItem -Path $extractDir -File -Recurse | Where-Object { $_.Name -ne "$AppName.exe" }
        foreach ($file in $additionalFiles) {
            Copy-Item -Path $file.FullName -Destination $TargetDir -Force -ErrorAction SilentlyContinue
        }

        Write-Info "Binary installed: $(Join-Path $TargetDir "$AppName.exe")"
    }
    finally {
        # Cleanup temp directory
        if (Test-Path $tempDir) {
            Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# --- PATH Management ---

function Add-ToPath {
    param([string]$Directory)

    Write-Log "Checking PATH..."

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")

    # Check if already in PATH
    $pathEntries = $userPath -split ';' | Where-Object { $_ -ne '' }
    if ($pathEntries -contains $Directory) {
        Write-Info "PATH already configured."
        return
    }

    # Add to PATH
    Write-Info "Adding $Directory to User PATH..."

    # Clean up PATH (remove empty entries, duplicates)
    $cleanPath = ($pathEntries | Select-Object -Unique) -join ';'
    $newPath = "$cleanPath;$Directory"

    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")

    # Update current session
    $env:Path = "$env:Path;$Directory"

    Write-Info "PATH updated. Restart your terminal to apply."
}

# --- Configuration ---

function Initialize-Config {
    param([string]$BinaryPath)

    Write-Log "Initializing config and credentials..."

    # --init handles both config creation and management key generation
    # It outputs the actual paths used (platform-specific)
    # This avoids hardcoding paths that differ between Windows/Linux
    try {
        $pinfo = New-Object System.Diagnostics.ProcessStartInfo
        $pinfo.FileName = $BinaryPath
        $pinfo.Arguments = "--init"
        $pinfo.RedirectStandardOutput = $true
        $pinfo.RedirectStandardError = $true
        $pinfo.UseShellExecute = $false
        $pinfo.CreateNoWindow = $true

        $process = New-Object System.Diagnostics.Process
        $process.StartInfo = $pinfo
        $process.Start() | Out-Null
        $stdout = $process.StandardOutput.ReadToEnd()
        $process.WaitForExit()

        if ($process.ExitCode -eq 0) {
            # Parse output from binary for actual paths
            $lines = $stdout -split "`n"

            foreach ($line in $lines) {
                $trimmed = $line.Trim()
                if ($trimmed -match "^Created:\s*(.+)$") {
                    Write-Info "Config created: $($matches[1])"
                }
                elseif ($trimmed -match "^Management key:\s*(.+)$") {
                    Write-Info "Management key: $($matches[1])"
                }
                elseif ($trimmed -match "^Generated management key:" -or $trimmed -match "^Regenerated management key:") {
                    # Next non-empty line contains the key
                    continue
                }
                elseif ($trimmed -match "^\s*([a-f0-9]{32})$") {
                    Write-Info "Management key: $($matches[1])"
                }
                elseif ($trimmed -match "^Location:\s*(.+)$") {
                    Write-Info "Credentials: $($matches[1])"
                }
            }
        }
        else {
            Write-Warn "Init returned non-zero exit code."
        }
    }
    catch {
        Write-Warn "Failed to initialize: $($_.Exception.Message)"
        Write-Warn "Run '$AppName --init' manually later."
    }
}

# --- Service Management (Scheduled Task) ---

function Install-ScheduledTask {
    param([string]$BinaryPath)

    Write-Log "Setting up scheduled task..."

    # Remove existing task if present
    $existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($existingTask) {
        Write-Info "Removing existing task..."
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    }

    # Create action
    $action = New-ScheduledTaskAction -Execute $BinaryPath -WorkingDirectory $env:USERPROFILE

    # Create triggers: at logon + on task registration (start immediately)
    $triggerLogon = New-ScheduledTaskTrigger -AtLogon

    # Create settings with restart on failure
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -StartWhenAvailable `
        -RunOnlyIfNetworkAvailable

    # Register the task
    try {
        Register-ScheduledTask `
            -TaskName $TaskName `
            -Action $action `
            -Trigger $triggerLogon `
            -Settings $settings `
            -Description "Runs llm-mux proxy server in the background. Restarts automatically on failure." `
            | Out-Null

        Write-Info "Scheduled task created: $TaskName"

        # Start the task immediately
        Write-Info "Starting service..."
        Start-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue

        return $true
    }
    catch {
        Write-Warn "Failed to create scheduled task: $($_.Exception.Message)"
        Write-Warn "You can run llm-mux manually: $BinaryPath"
        return $false
    }
}

function Get-ServiceStatus {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if (-not $task) {
        return "not installed"
    }

    $taskInfo = Get-ScheduledTaskInfo -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($taskInfo.LastTaskResult -eq 267009) {
        # Task is running
        return "running"
    }

    # Check if process is actually running
    $process = Get-Process -Name $AppName -ErrorAction SilentlyContinue
    if ($process) {
        return "running"
    }

    return "stopped"
}

# --- Version Check ---

function Test-ExistingInstall {
    param(
        [string]$BinaryPath,
        [string]$TargetVersion
    )

    if (-not (Test-Path $BinaryPath)) {
        return $true  # No existing install, proceed
    }

    if ($script:Force) {
        Write-Info "Force reinstall requested."
        return $true
    }

    try {
        $existingVersion = & $BinaryPath --version 2>&1 | Select-Object -First 1

        if ($existingVersion -match $TargetVersion.TrimStart('v')) {
            Write-Info "$AppName $TargetVersion is already installed."
            Write-Info "Use -Force to reinstall."
            return $false
        }
    }
    catch {
        # Can't determine version, proceed with install
    }

    return $true
}

# --- Main ---

function Show-Success {
    param(
        [string]$TagName,
        [string]$InstallDir,
        [bool]$ServiceInstalled
    )

    $status = if ($ServiceInstalled) { Get-ServiceStatus } else { "not installed" }

    # Use XDG-compliant path (with fallback for Windows)
    $configDir = if ($env:XDG_CONFIG_HOME) {
        Join-Path $env:XDG_CONFIG_HOME "llm-mux"
    } else {
        Join-Path $env:USERPROFILE ".config\llm-mux"
    }

    Write-Host ""
    Write-Host "========================================================" -ForegroundColor Green
    Write-Host " $AppName $TagName installed successfully!" -ForegroundColor Green
    Write-Host "========================================================" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Binary:  $InstallDir\$AppName.exe"
    Write-Host "  Config:  $configDir\config.yaml"
    if (-not $script:NoService) {
        Write-Host "  Service: $status"
    }
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  1. Login to a provider:"
    Write-Host "     $AppName --login              # Gemini"
    Write-Host "     $AppName --claude-login       # Claude"
    Write-Host "     $AppName --copilot-login      # GitHub Copilot"
    Write-Host "     $AppName --codex-login        # OpenAI Codex"
    Write-Host ""

    if ($script:NoService) {
        Write-Host "  2. Start the server:"
        Write-Host "     $AppName"
    }
    else {
        Write-Host "  2. Service commands:"
        Write-Host "     Start-ScheduledTask -TaskName '$TaskName'   # Start"
        Write-Host "     Stop-ScheduledTask -TaskName '$TaskName'    # Stop"
        Write-Host "     Get-ScheduledTaskInfo -TaskName '$TaskName' # Status"
    }

    Write-Host ""
    Write-Host "  3. Test the API:"
    Write-Host "     curl http://localhost:8317/v1/models"
    Write-Host "     # or with PowerShell:"
    Write-Host "     Invoke-RestMethod http://localhost:8317/v1/models"
    Write-Host ""

    if ($status -eq "running") {
        Write-Host "  The service is now running!" -ForegroundColor Green
    }
    elseif (-not $script:NoService) {
        Write-Host "  Note: Restart your terminal, then start the service with:" -ForegroundColor Yellow
        Write-Host "     Start-ScheduledTask -TaskName '$TaskName'"
    }

    Write-Host ""
}

function Main {
    Write-Host ""
    Write-Log "llm-mux Windows Installer"
    Write-Host ""

    # Check if running as admin (warn but don't block)
    if (Test-Administrator) {
        Write-Warn "Running as Administrator. The service will be installed for the current user only."
    }

    # Detect architecture
    $arch = Get-Architecture
    Write-Info "Architecture: $arch"

    # Determine install directory
    if (-not $script:InstallPath) {
        $installDir = Join-Path $env:LOCALAPPDATA "Programs\$AppName"
    }
    else {
        $installDir = $script:InstallPath
    }
    Write-Info "Install directory: $installDir"

    # Get version
    $tagName = Get-LatestVersion

    # Check existing installation
    $binaryPath = Join-Path $installDir "$AppName.exe"
    if (-not (Test-ExistingInstall -BinaryPath $binaryPath -TargetVersion $tagName)) {
        Write-Host ""
        exit 0
    }

    # Install binary
    Install-Binary -TagName $tagName -Arch $arch -TargetDir $installDir

    # Update PATH
    Add-ToPath -Directory $installDir

    # Initialize config
    Initialize-Config -BinaryPath $binaryPath

    # Setup service
    $serviceInstalled = $false
    if (-not $script:NoService) {
        $serviceInstalled = Install-ScheduledTask -BinaryPath $binaryPath
    }

    # Show success message
    Show-Success -TagName $tagName -InstallDir $installDir -ServiceInstalled $serviceInstalled
}

# Run main
Main
