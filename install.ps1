[CmdletBinding()]
param(
    [string]$Version = $(if ($env:N0DING_VERSION) { $env:N0DING_VERSION } else { "latest" }),
    [string]$InstallDir = $(if ($env:N0DING_INSTALL_DIR) { $env:N0DING_INSTALL_DIR } else { Join-Path $HOME ".n0ding" })
)

$ErrorActionPreference = "Stop"
$Repository = "HN-Tran/n0ding"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "Docker is required: https://docs.docker.com/get-docker/"
}
& docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
    throw "Docker Compose v2 is required"
}

if ($Version -eq "latest") {
    $Release = Invoke-RestMethod "https://api.github.com/repos/$Repository/releases/latest"
    $Version = $Release.tag_name
    if (-not $Version) { throw "No published release found" }
}

$ImageVersion = $Version.TrimStart([char]'v')
$AssetBase = if ($env:N0DING_RELEASE_BASE_URL) {
    $env:N0DING_RELEASE_BASE_URL
} else {
    "https://github.com/$Repository/releases/download/$Version"
}
$HealthUrl = if ($env:N0DING_HEALTH_URL) { $env:N0DING_HEALTH_URL } else { "http://localhost:8080/healthz" }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

function Save-ReleaseAsset([string]$Name) {
    $Destination = Join-Path $InstallDir $Name
    $Temporary = "$Destination.tmp"
    Invoke-WebRequest "$AssetBase/$Name" -OutFile $Temporary
    Move-Item -Force $Temporary $Destination
}

Save-ReleaseAsset "compose.yaml"
Save-ReleaseAsset "n0ding.toml"
Save-ReleaseAsset "SHA256SUMS"

$Checksums = Get-Content (Join-Path $InstallDir "SHA256SUMS")
foreach ($Name in @("compose.yaml", "n0ding.toml")) {
    $Expected = (($Checksums | Where-Object { $_ -match "  $([regex]::Escape($Name))$" }) -split "\s+")[0]
    $Actual = (Get-FileHash -Algorithm SHA256 (Join-Path $InstallDir $Name)).Hash.ToLowerInvariant()
    if (-not $Expected -or $Actual -ne $Expected.ToLowerInvariant()) {
        throw "$Name checksum mismatch"
    }
}

@"
N0DING_IMAGE=ghcr.io/hn-tran/n0ding:$ImageVersion
N0DING_BIND_ADDRESS=127.0.0.1
N0DING_PORT=8080
N0DING_PUBLIC_URL=http://localhost:8080
"@ | Set-Content -Encoding utf8 (Join-Path $InstallDir ".env")

& docker compose --project-directory $InstallDir pull
if ($LASTEXITCODE -ne 0) { throw "Could not pull the n0ding image" }
& docker compose --project-directory $InstallDir up -d
if ($LASTEXITCODE -ne 0) { throw "Could not start n0ding" }

$Healthy = $false
for ($Attempt = 0; $Attempt -lt 30; $Attempt++) {
    try {
        Invoke-RestMethod $HealthUrl | Out-Null
        $Healthy = $true
        break
    } catch {
        Start-Sleep -Seconds 2
    }
}
if (-not $Healthy) {
    throw "Service did not become healthy. Run: docker compose --project-directory `"$InstallDir`" logs"
}

Write-Host ""
Write-Host "n0ding $Version is running at http://localhost:8080"
Write-Host "Install directory: $InstallDir"
Write-Host "Status: curl.exe http://localhost:8080/api/v1/status"
Write-Host "Logs:   docker compose --project-directory `"$InstallDir`" logs -f"
Write-Host ""
Write-Host "Client setup is opt-in. See: https://github.com/$Repository#client-setup"
